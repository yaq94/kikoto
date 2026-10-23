package httpapi

import (
	"context"

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

// pruneContentTranslations enforces the two administrator limits on the
// durable corpus: a maximum number of model rows and an approximate byte
// budget for model text. A native row costs no model call and carries authored
// text, so it is neither counted nor eviction material.
//
// Superseded rows (an older cache key for the same entity, field and language)
// are removed first: the newer row already answers every lookup, and the old
// one cannot be reached by the current version. A pass may delete several
// bounded batches; the next tick continues if a burst still exceeds a budget.
func (s *Server) pruneContentTranslations(ctx context.Context, cfg translation.Config) error {
	for range contentTranslationRetentionBatches {
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
		if cfg.MaxEntries <= 0 && cfg.CacheBytes <= 0 {
			return nil
		}
		var rows, bytes int64
		if err := s.db.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(
				length(CAST(source_text AS BLOB))+length(CAST(translated_text AS BLOB))),0)
				FROM content_translation WHERE origin='model'`).Scan(&rows, &bytes); err != nil {
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
		result, err := s.db.ExecContext(ctx, `DELETE FROM content_translation WHERE id IN (
				SELECT id FROM content_translation WHERE origin='model'
				ORDER BY updated_at,id LIMIT ?)`, remove)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected < int64(remove) {
			return nil
		}
	}
	return nil
}
