package httpapi

import (
	"context"
	"database/sql"
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
	// translationPrefillFailureLimit bounds the retry table independently of
	// the display-cache budget. Old hashes and retired entities are removed
	// first; beyond this many rows the oldest are dropped so a stream of
	// changed titles cannot grow the table without limit.
	translationPrefillFailureLimit = 10000
	// Below this budget SQLite's page granularity dominates the measurement (a
	// single page is already larger than the configured value), so tests and
	// deliberately tiny limits keep the historical text-byte meaning. Real
	// cache budgets, including the 256 MiB default, use measured pages.
	contentTranslationDiskBudgetFloor = 1 << 20
)

// One maintenance pass at a time keeps concurrent model completions from
// issuing overlapping delete statements against the single SQLite writer.
var contentTranslationRetentionMu sync.Mutex

// pruneTranslationCacheAfterWrite applies the same retention pass after a
// durable write. It is best effort: the translated text is already usable, and
// the periodic worker will retry a failed maintenance query. Single-row
// completions are checked in small batches so the predicate does not scan the
// whole corpus on every translation; a large batch runs a pass immediately.
func (s *Server) pruneTranslationCacheAfterWrite(ctx context.Context, cfg translation.Config, written int) {
	if cfg.MaxEntries <= 0 && cfg.CacheBytes <= 0 {
		return
	}
	if written < 16 {
		if s.translationRetentionWrites.Add(int64(max(written, 1))) < 32 {
			return
		}
	}
	s.translationRetentionWrites.Store(0)
	if err := s.pruneTranslationCache(ctx, cfg, false); err != nil {
		slog.Warn("translation cache retention failed after write", "error", err)
	}
}

// pruneContentTranslations runs the periodic cleanup and applies the two
// administrator limits to rebuildable model output. Current corpus rows — a
// live catalog entry with its current title and the configured model/template
// — are never eviction material. Historical rows and the source-field display
// cache are. Native rows carry authored text and are never counted or evicted.
// Source identities and personal records live in other tables and are never
// touched.
func (s *Server) pruneContentTranslations(ctx context.Context, cfg translation.Config) error {
	return s.pruneTranslationCache(ctx, cfg, true)
}

// translationBudgetReport compares the rebuildable cache against the soft
// budget and also reports when persistent title/description corpus alone
// exceeds it. Persistent rows are never deleted, so exceeding the budget is a
// signal rather than permission to sacrifice search.
func (s *Server) translationBudgetReport(ctx context.Context, cfg translation.Config) (bool, int64, error) {
	rows, logical, err := s.evictableTranslationStats(ctx, cfg)
	if err != nil {
		return false, 0, err
	}
	bytes := logical
	if measured, ok := s.translationCacheDiskBytesFor(ctx, cfg); ok {
		bytes = measured
	}
	persistent, err := s.persistentTranslationTextBytes(ctx, cfg)
	if err != nil {
		return false, 0, err
	}
	overRows := cfg.MaxEntries > 0 && rows > int64(cfg.MaxEntries)
	overBytes := cfg.CacheBytes > 0 && (bytes+persistent > cfg.CacheBytes)
	return overRows || overBytes, bytes, nil
}

// persistentTranslationTextBytes is the logical text footprint of current
// title/description rows. It is the protected share that a cache measurement
// cannot free; the operator still sees it in the soft-budget decision.
func (s *Server) persistentTranslationTextBytes(ctx context.Context, cfg translation.Config) (int64, error) {
	var bytes int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(sum(
			length(CAST(ct.source_text AS BLOB))+length(CAST(ct.translated_text AS BLOB))),0)
		FROM content_translation ct
		WHERE ct.origin='model' AND `+translationPersistentCorpusSQL("ct"),
		translation.ConfigKey(cfg), translation.ConfigKey(cfg), translation.ConfigKey(cfg)).Scan(&bytes)
	if err != nil {
		return 0, fmt.Errorf("measure persistent translation corpus: %w", err)
	}
	return bytes, nil
}

// pruneTranslationCache is the shared implementation. A write-path pass only
// enforces the budgets; the periodic pass also removes historical rows even
// while the budgets are not exceeded.
func (s *Server) pruneTranslationCache(ctx context.Context, cfg translation.Config, removeSuperseded bool) error {
	contentTranslationRetentionMu.Lock()
	defer contentTranslationRetentionMu.Unlock()

	// The periodic pass first removes rows that can no longer answer a current
	// lookup, even when the size budgets are not exceeded. This is bounded like
	// every other writer.
	if removeSuperseded {
		for range contentTranslationRetentionBatches {
			historical, err := s.deleteHistoricalContentTranslations(ctx, cfg, contentTranslationRetentionBatch)
			if err != nil {
				return err
			}
			superseded, err := s.deleteSupersededSourceTranslations(ctx, contentTranslationRetentionBatch)
			if err != nil {
				return err
			}
			failures, err := s.deleteStaleTranslationPrefillFailures(ctx, cfg, contentTranslationRetentionBatch)
			if err != nil {
				return err
			}
			if historical+superseded+failures == 0 {
				break
			}
		}
	}

	batches := contentTranslationRetentionBatches
	if !removeSuperseded {
		batches = 1
	}
	if cfg.MaxEntries <= 0 && cfg.CacheBytes <= 0 {
		return nil
	}
	for range batches {
		rows, logicalBytes, err := s.evictableTranslationStats(ctx, cfg)
		if err != nil {
			return err
		}
		// Current corpus alone may exceed the soft budget. It is never deleted;
		// the pass simply has no rebuildable rows left to free.
		if rows == 0 {
			return nil
		}
		// dbstat walks every page in the database. On the write path skip that
		// measurement until the logical cache text is at least one third of the
		// configured disk budget; row-count trimming does not need page data.
		// A periodic pass always measures so page overhead is never ignored.
		measureDisk := cfg.CacheBytes >= contentTranslationDiskBudgetFloor
		if !removeSuperseded && cfg.CacheBytes > 0 && logicalBytes*3 < cfg.CacheBytes {
			measureDisk = false
		}
		bytes := logicalBytes
		measuredDisk := false
		if measureDisk {
			if measured, ok := s.translationCacheDiskBytesFor(ctx, cfg); ok {
				bytes = measured
				measuredDisk = true
			}
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
			// and keep only what fits. For real budgets `bytes` is the measured
			// page footprint of both cache tables and their indexes/FTS shadow
			// tables, so the configured disk limit includes page overhead.
			average := bytes / max(rows, 1)
			if average < 1 {
				average = 1
			}
			fits := cfg.CacheBytes * rows / max(bytes, 1)
			if fits < keep {
				keep = fits
			}
		}
		remove := rows - keep
		if remove <= 0 {
			return nil
		}
		if overBytes && measuredDisk {
			// A measured page budget can be one page over while the row-count
			// estimate says nothing must go. Free at least a page's worth of
			// rows so dbstat can observe the reduction and the loop converges.
			average := bytes / max(rows, 1)
			pageSize := int64(4096)
			_ = s.db.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&pageSize)
			pageRows := pageSize*2/max(average, 1) + 1
			if remove < pageRows {
				remove = min(pageRows, rows)
			}
		}
		if remove > contentTranslationRetentionBatch {
			remove = contentTranslationRetentionBatch
		}
		affected, err := s.deleteOldestTranslationRows(ctx, cfg, remove)
		if err != nil {
			return err
		}
		if affected < int64(remove) {
			return nil
		}
	}
	return nil
}

// deleteStaleTranslationPrefillFailures removes retry rows that can no longer
// become a current corpus row: another config key, a retired entity, changed
// source text, or an id beyond the bounded retry table. It is called from the
// low-frequency maintenance pass, never from a request path.
func (s *Server) deleteStaleTranslationPrefillFailures(ctx context.Context, cfg translation.Config, limit int64) (int64, error) {
	if limit <= 0 {
		return 0, nil
	}
	configKey := translation.ConfigKey(cfg)
	corpus := translationCorpusSQL()
	result, err := s.db.ExecContext(ctx, `DELETE FROM translation_prefill_failure WHERE rowid IN (
		SELECT f.rowid FROM translation_prefill_failure f
		WHERE f.config_key<>?
		   OR NOT EXISTS (
			SELECT 1 FROM (`+corpus+`) c
			WHERE c.entity_kind=f.entity_kind AND c.entity_id=f.entity_id
			  AND c.field=f.field AND c.source_text IS NOT NULL
			  AND `+translationSourceHashFunction+`(c.source_text)=f.source_hash)
		LIMIT ?)`, configKey, limit)
	if err != nil {
		return 0, fmt.Errorf("delete stale prefill failures: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if deleted > 0 {
		return deleted, nil
	}
	// The table is bounded by rowid so a burst of changed titles cannot grow it
	// without limit. This runs only after the correctness cleanup is exhausted.
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM translation_prefill_failure WHERE config_key=?`, configKey).Scan(&total); err != nil {
		return 0, err
	}
	if total <= translationPrefillFailureLimit {
		return 0, nil
	}
	remove := total - translationPrefillFailureLimit
	if remove > limit {
		remove = limit
	}
	result, err = s.db.ExecContext(ctx, `DELETE FROM translation_prefill_failure WHERE rowid IN (
		SELECT rowid FROM translation_prefill_failure
		WHERE config_key=?
		ORDER BY rowid
		LIMIT ?)`, configKey, remove)
	if err != nil {
		return 0, fmt.Errorf("trim prefill failures: %w", err)
	}
	return result.RowsAffected()
}

// translationCacheDiskBytes measures the pages owned by the rebuildable
// translation cache. Persistent title/description rows are corpus, not cache:
// their pages are attributed to the protected share and reported as
// over_budget instead of being evicted.
func (s *Server) translationCacheDiskBytes(ctx context.Context) (int64, bool) {
	cfg, err := translation.NewStore(s.db).Load(ctx)
	if err != nil {
		return 0, false
	}
	return s.translationCacheDiskBytesFor(ctx, cfg)
}

func (s *Server) translationCacheDiskBytesFor(ctx context.Context, cfg translation.Config) (int64, bool) {
	var contentPages, sourcePages sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT
		COALESCE(sum(CASE WHEN name='content_translation'
			OR name LIKE 'content_translation_search%'
			OR name LIKE 'idx_content_translation%'
			OR name LIKE 'sqlite_autoindex_content_translation%'
			THEN pgsize END),0),
		COALESCE(sum(CASE WHEN name='source_field_translation'
			OR name LIKE 'idx_source_field_translation%'
			OR name LIKE 'sqlite_autoindex_source_field_translation%'
			THEN pgsize END),0)
		FROM dbstat`).Scan(&contentPages, &sourcePages)
	if err != nil {
		return 0, false
	}
	// Only rebuildable rows are cache. A native row, or a current corpus row,
	// contributes its page share to the protected side instead.
	var cacheText, totalText int64
	if err = s.db.QueryRowContext(ctx, `SELECT
		COALESCE(sum(CASE WHEN origin='model' AND NOT `+translationPersistentCorpusSQL("ct")+`
			THEN length(CAST(ct.source_text AS BLOB))+length(CAST(ct.translated_text AS BLOB)) ELSE 0 END),0),
		COALESCE(sum(length(CAST(ct.source_text AS BLOB))+length(CAST(ct.translated_text AS BLOB))),0)
		FROM content_translation ct`, translation.ConfigKey(cfg), translation.ConfigKey(cfg), translation.ConfigKey(cfg)).Scan(&cacheText, &totalText); err != nil {
		return 0, false
	}
	modelContentPages := int64(0)
	if totalText > 0 {
		modelContentPages = contentPages.Int64 * cacheText / totalText
	}
	return modelContentPages + sourcePages.Int64, true
}

// translationDisplayCacheCurrentSQL matches display-only rows that still
// answer a browse: a tag belongs to a live entity and a directory still has
// the same real path and title. Title/description rows are protected by the
// corpus predicate instead.
func translationDisplayCacheCurrentSQL(alias string) string {
	return `(
		(` + alias + `.field='tag' AND (
			(` + alias + `.entity_kind='entry' AND EXISTS(
				SELECT 1 FROM source_catalog_entry e WHERE e.id=` + alias + `.entity_id AND e.relocated_to_id IS NULL))
			OR (` + alias + `.entity_kind='series' AND EXISTS(
				SELECT 1 FROM catalog_series s WHERE s.id=` + alias + `.entity_id))
			OR (` + alias + `.entity_kind='work' AND EXISTS(
				SELECT 1 FROM work w WHERE w.id=` + alias + `.entity_id))))
		OR (` + alias + `.field='directory' AND EXISTS(
			SELECT 1 FROM source_catalog_entry e
			JOIN source_catalog_file f ON f.catalog_entry_id=e.id
			WHERE e.id=` + alias + `.entity_id AND e.relocated_to_id IS NULL
			  AND f.relative_path=` + alias + `.entity_key AND f.title=` + alias + `.source_text))
	)`
}

// deleteHistoricalContentTranslations removes superseded and retired model
// rows: changed text, a stale config key, a retired entity or a directory
// that no longer exists. Current title/description corpus rows and current
// display cache are never selected.
func (s *Server) deleteHistoricalContentTranslations(ctx context.Context, cfg translation.Config, limit int64) (int64, error) {
	if limit <= 0 {
		return 0, nil
	}
	configKey := translation.ConfigKey(cfg)
	result, err := s.db.ExecContext(ctx, `DELETE FROM content_translation WHERE id IN (
		SELECT ct.id FROM content_translation ct
		WHERE ct.origin='model'
		  AND NOT `+translationPersistentCorpusSQL("ct")+`
		  AND NOT `+translationDisplayCacheCurrentSQL("ct")+`
		LIMIT ?)`, configKey, configKey, configKey, limit)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// deleteSupersededSourceTranslations removes older cache keys for the same
// source field. The newest row already answers the lookup.
func (s *Server) deleteSupersededSourceTranslations(ctx context.Context, limit int64) (int64, error) {
	if limit <= 0 {
		return 0, nil
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM source_field_translation WHERE id IN (
		SELECT id FROM source_field_translation old
		WHERE EXISTS(
			SELECT 1 FROM source_field_translation cur
			WHERE cur.source_id=old.source_id AND cur.work_code=old.work_code
				AND cur.field=old.field AND cur.path=old.path
				AND cur.language=old.language AND cur.id>old.id)
		LIMIT ?)`, limit)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// evictableTranslationStats counts only rows the budget may remove: display
// cache, stale rows and retired rows. Current title/description corpus rows
// are excluded, so a tight budget cannot delete search correctness.
func (s *Server) evictableTranslationStats(ctx context.Context, cfg translation.Config) (int64, int64, error) {
	var rows, logicalBytes int64
	err := s.db.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(byte_count),0)
		FROM (
			SELECT length(CAST(ct.source_text AS BLOB))+length(CAST(ct.translated_text AS BLOB)) AS byte_count
			FROM content_translation ct
			WHERE ct.origin='model' AND NOT `+translationPersistentCorpusSQL("ct")+`
			UNION ALL
			SELECT length(CAST(source_text AS BLOB))+length(CAST(translated_text AS BLOB)) AS byte_count
			FROM source_field_translation
		)`, translation.ConfigKey(cfg), translation.ConfigKey(cfg), translation.ConfigKey(cfg)).Scan(&rows, &logicalBytes)
	if err != nil {
		return 0, 0, err
	}
	return rows, logicalBytes, nil
}

// deleteOldestTranslationRows removes the oldest evictable model rows across
// both cache tables. Current corpus rows are excluded from the selection.
func (s *Server) deleteOldestTranslationRows(ctx context.Context, cfg translation.Config, limit int64) (int64, error) {
	if limit <= 0 {
		return 0, nil
	}
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return 0, err
	}
	defer release()
	rows, err := tx.QueryContext(ctx, `
		SELECT kind,id FROM (
			SELECT 'content' AS kind,ct.id,ct.updated_at
			FROM content_translation ct
			WHERE ct.origin='model' AND NOT `+translationPersistentCorpusSQL("ct")+`
			UNION ALL
			SELECT 'source' AS kind,id,updated_at
			FROM source_field_translation
		)
		ORDER BY updated_at,kind,id
		LIMIT ?`, translation.ConfigKey(cfg), translation.ConfigKey(cfg), translation.ConfigKey(cfg), limit)
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
