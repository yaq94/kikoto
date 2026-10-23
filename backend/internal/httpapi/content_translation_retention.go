package httpapi

import (
	"context"

	"github.com/yexca/kikoto/backend/internal/translation"
)

// contentTranslationRetentionBatch bounds one maintenance pass so the writer
// is never held while the whole corpus is trimmed. A pass runs after the
// prefill worker's own batch and only removes rows the model can rebuild.
const contentTranslationRetentionBatch = 500

// pruneContentTranslations enforces the two administrator limits on the
// durable corpus: a maximum number of rows and an approximate byte budget.
// Only model rows are candidates. A native row costs no model call and carries
// the authored text, so it is never eviction material.
//
// Superseded rows (an older cache key for the same entity, field and language)
// are removed first: the newer row already answers every lookup, and the old
// one cannot be reached by the current version. Both limits are soft budgets;
// the pass stops after one batch and resumes on the next tick.
func (s *Server) pruneContentTranslations(ctx context.Context, cfg translation.Config) error {
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
	if err := s.db.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(length(source_text)+length(translated_text)),0)
		FROM content_translation`).Scan(&rows, &bytes); err != nil {
		return err
	}
	var evictable int64
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM content_translation WHERE origin='model'`).Scan(&evictable); err != nil {
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
		// The byte budget wins when it is tighter: estimate the average row and
		// keep only what fits. Both numbers are budgets, not exact accounting.
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
	if remove > evictable {
		remove = evictable
	}
	if remove > contentTranslationRetentionBatch {
		remove = contentTranslationRetentionBatch
	}
	// Oldest first: a recently used translation is the most likely to be asked
	// for again, and created_at keeps the order stable across restarts.
	_, err := s.db.ExecContext(ctx, `DELETE FROM content_translation WHERE id IN (
		SELECT id FROM content_translation WHERE origin='model'
		ORDER BY updated_at,id LIMIT ?)`, remove)
	return err
}
