package httpapi

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/yexca/kikoto/backend/internal/translation"
)

const (
	// contentTranslationRetentionBatch bounds one delete statement so the
	// writer is never held while the whole corpus is trimmed.
	contentTranslationRetentionBatch = 500
	// A maintenance pass may need several batches after a large prefill burst.
	// The cap still bounds one tick; the next tick continues if the budget is
	// not reached.
	contentTranslationRetentionBatches = 20
)

// One maintenance pass at a time keeps concurrent model completions from
// issuing overlapping delete statements against the single SQLite writer.
var contentTranslationRetentionMu sync.Mutex

// pruneTranslationCacheAfterWrite applies the same retention pass after a
// durable write. It is best effort: the translated text is already usable, and
// the periodic worker will retry a failed maintenance query. Without this call
// a large interactive burst could grow past the configured budget until the
// next scheduled pass.
func (s *Server) pruneTranslationCacheAfterWrite(ctx context.Context, cfg translation.Config) {
	if cfg.MaxEntries <= 0 && cfg.CacheBytes <= 0 {
		return
	}
	if err := s.pruneTranslationCache(ctx, cfg, false); err != nil {
		slog.Warn("translation cache retention failed after write", "error", err)
	}
}

// pruneContentTranslations enforces the two administrator limits on the
// durable translation cache: a maximum number of model rows and an approximate
// byte budget for model text. It covers both the searchable content corpus and
// the source-field display cache. Native rows cost no model call and carry
// authored text, so they are neither counted nor eviction material. Source
// identities and personal records live in other tables and are never touched.
//
// Superseded rows (an older cache key for the same entity or source field) are
// removed first: the newer row already answers every lookup, and the old one
// cannot be reached by the current version. A pass may delete several bounded
// batches; the next tick continues if a burst still exceeds a budget.
func (s *Server) pruneContentTranslations(ctx context.Context, cfg translation.Config) error {
	return s.pruneTranslationCache(ctx, cfg, true)
}

// pruneTranslationCache is the shared implementation. A write-path pass only
// enforces the budgets and leaves superseded rows for the low-frequency pass, so
// an existing row stays searchable until the next scheduled retention tick.
func (s *Server) pruneTranslationCache(ctx context.Context, cfg translation.Config, removeSuperseded bool) error {
	contentTranslationRetentionMu.Lock()
	defer contentTranslationRetentionMu.Unlock()

	for range contentTranslationRetentionBatches {
		if removeSuperseded {
			// A superseded row is unreachable by the current lookup key. It is
			// cheaper to remove than to count against either budget.
			if _, err := s.db.ExecContext(ctx, `DELETE FROM content_translation WHERE id IN (
					SELECT id FROM content_translation old
					WHERE old.origin='model' AND EXISTS(
						SELECT 1 FROM content_translation cur
						WHERE cur.origin='model'
							AND cur.entity_kind=old.entity_kind AND cur.entity_id=old.entity_id
							AND cur.field=old.field AND cur.language=old.language
							AND cur.id>old.id)
					LIMIT ?)`, contentTranslationRetentionBatch); err != nil {
				return err
			}
			if _, err := s.db.ExecContext(ctx, `DELETE FROM source_field_translation WHERE id IN (
					SELECT id FROM source_field_translation old
					WHERE EXISTS(
						SELECT 1 FROM source_field_translation cur
						WHERE cur.source_id=old.source_id AND cur.work_code=old.work_code
							AND cur.field=old.field AND cur.path=old.path
							AND cur.language=old.language AND cur.id>old.id)
					LIMIT ?)`, contentTranslationRetentionBatch); err != nil {
				return err
			}
		}
		if cfg.MaxEntries <= 0 && cfg.CacheBytes <= 0 {
			return nil
		}
		var rows, bytes int64
		if err := s.db.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(
					byte_count),0)
				FROM (
					SELECT length(CAST(source_text AS BLOB))+length(CAST(translated_text AS BLOB)) AS byte_count
					FROM content_translation WHERE origin='model'
					UNION ALL
					SELECT length(CAST(source_text AS BLOB))+length(CAST(translated_text AS BLOB)) AS byte_count
					FROM source_field_translation
				)`).Scan(&rows, &bytes); err != nil {
			return err
		}
		overRows := cfg.MaxEntries > 0 && rows > int64(cfg.MaxEntries)
		overBytes := cfg.CacheBytes > 0 && bytes > cfg.CacheBytes
		if !overRows && !overBytes {
			return nil
		}
		keep := rows
		if overRows {
			keep = int64(cfg.MaxEntries)
		}
		if overBytes {
			// The byte budget wins when it is tighter: estimate the average row
			// and keep only what fits. Page and index overhead are not included;
			// this bounds the stored text itself.
			average := bytes / max(rows, 1)
			if average < 1 {
				average = 1
			}
			if fits := cfg.CacheBytes / average; fits < keep {
				keep = fits
			}
		}
		remove := rows - keep
		if remove <= 0 {
			return nil
		}
		if remove > contentTranslationRetentionBatch {
			remove = contentTranslationRetentionBatch
		}
		// Oldest first: a recently used translation is the most likely to be
		// asked for again, and updated_at keeps the order stable across restarts.
		affected, err := s.deleteOldestTranslationRows(ctx, remove)
		if err != nil {
			return err
		}
		if affected < int64(remove) {
			return nil
		}
	}
	return nil
}

// deleteOldestTranslationRows removes the oldest model rows across both cache
// tables. The caller has already decided how many rows the budget permits.
func (s *Server) deleteOldestTranslationRows(ctx context.Context, limit int64) (int64, error) {
	if limit <= 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `
		SELECT kind,id FROM (
			SELECT 'content' AS kind,id,updated_at
			FROM content_translation WHERE origin='model'
			UNION ALL
			SELECT 'source' AS kind,id,updated_at
			FROM source_field_translation
		)
		ORDER BY updated_at,kind,id
		LIMIT ?`, limit)
	if err != nil {
		return 0, fmt.Errorf("select translation cache rows: %w", err)
	}
	var contentIDs, sourceIDs []int64
	for rows.Next() {
		var kind string
		var id int64
		if err := rows.Scan(&kind, &id); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan translation cache row: %w", err)
		}
		if kind == "source" {
			sourceIDs = append(sourceIDs, id)
		} else {
			contentIDs = append(contentIDs, id)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("select translation cache rows: %w", err)
	}
	rows.Close()
	var affected int64
	deleteRows := func(table string, ids []int64) error {
		if len(ids) == 0 {
			return nil
		}
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
		args := make([]any, 0, len(ids))
		for _, id := range ids {
			args = append(args, id)
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE id IN (`+placeholders+`)`, args...)
		if err != nil {
			return err
		}
		deleted, err := result.RowsAffected()
		if err != nil {
			return err
		}
		affected += deleted
		return nil
	}
	if err := deleteRows("content_translation", contentIDs); err != nil {
		return 0, err
	}
	if err := deleteRows("source_field_translation", sourceIDs); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return affected, nil
}
