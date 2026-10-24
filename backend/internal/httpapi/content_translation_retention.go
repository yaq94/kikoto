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
			if historical+superseded == 0 {
				break
			}
		}
	}

	// With no row-count budget, the measured page footprint decides whether the
	// rebuildable cache needs a trim. Staying under the soft byte budget avoids
	// scanning the whole corpus with the current-row predicate on every tick.
	if cfg.MaxEntries <= 0 && cfg.CacheBytes > 0 {
		if measured, ok := s.translationCacheDiskBytes(ctx); ok && measured <= cfg.CacheBytes {
			return nil
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
			if measured, ok := s.translationCacheDiskBytes(ctx); ok {
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

// translationCacheDiskBytes measures the pages owned by the durable translation
// cache. A small text row still occupies a B-tree page slot and index entries,
// so counting only text bytes can grow the database well past the configured
// limit. dbstat is optional in some SQLite builds; when it is unavailable the
// caller falls back to the text budget rather than failing translation.
func (s *Server) translationCacheDiskBytes(ctx context.Context) (int64, bool) {
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
	// The content_translation table also stores authored native rows, which are
	// not cache and are never evicted. Attribute its pages by model text share so
	// a large native corpus cannot empty the model cache.
	var totalText, modelText sql.NullInt64
	if err = s.db.QueryRowContext(ctx, `SELECT
		COALESCE(sum(length(CAST(source_text AS BLOB))+length(CAST(translated_text AS BLOB))),0),
		COALESCE(sum(CASE WHEN origin='model'
			THEN length(CAST(source_text AS BLOB))+length(CAST(translated_text AS BLOB)) ELSE 0 END),0)
		FROM content_translation`).Scan(&totalText, &modelText); err != nil {
		return 0, false
	}
	modelContentPages := int64(0)
	if totalText.Valid && totalText.Int64 > 0 {
		modelContentPages = contentPages.Int64 * modelText.Int64 / totalText.Int64
	} else if contentPages.Int64 > 0 {
		var totalRows, modelRows int64
		if err = s.db.QueryRowContext(ctx, `SELECT count(*),
			COALESCE(sum(CASE WHEN origin='model' THEN 1 ELSE 0 END),0)
			FROM content_translation`).Scan(&totalRows, &modelRows); err != nil {
			return 0, false
		}
		if totalRows > 0 {
			modelContentPages = contentPages.Int64 * modelRows / totalRows
		}
	}
	return modelContentPages + sourcePages.Int64, true
}

// currentContentTranslationSQL matches rows that still answer a current lookup:
// a live work or series, a live catalog title with its current text, or a live
// entry's current directory. Only title rows carry the search corpus, but the
// other fields are protected with their own current source so a browse does
// not lose them.
func currentContentTranslationSQL(alias string) string {
	return `(
		(` + alias + `.entity_kind='work' AND EXISTS(
			SELECT 1 FROM work current_work
			WHERE current_work.id=` + alias + `.entity_id
			  AND (` + alias + `.field<>'title' OR ` + alias + `.source_text=current_work.title)))
		OR (` + alias + `.entity_kind='series' AND EXISTS(
			SELECT 1 FROM catalog_series current_series
			WHERE current_series.id=` + alias + `.entity_id
			  AND (` + alias + `.field<>'title' OR ` + alias + `.source_text=current_series.title)))
		OR (` + alias + `.entity_kind='entry' AND ` + alias + `.field='title' AND EXISTS(
			SELECT 1 FROM source_catalog_entry current_entry
			JOIN file_source current_source ON current_source.id=current_entry.file_source_id
			WHERE current_entry.id=` + alias + `.entity_id
			  AND current_entry.relocated_to_id IS NULL
			  AND current_entry.availability_status='available'
			  AND trim(current_entry.title)<>''
			  AND current_source.enabled=1 AND current_source.source_type='openlist'
			  AND ` + alias + `.source_text=current_entry.title
			  AND (` + alias + `.origin='native'
			    OR (` + alias + `.origin='model' AND ` + alias + `.model=? AND ` + alias + `.template_version=?))))
		OR (` + alias + `.entity_kind='entry' AND ` + alias + `.field='directory' AND EXISTS(
			SELECT 1 FROM source_catalog_entry current_entry
			JOIN source_catalog_file current_file ON current_file.catalog_entry_id=current_entry.id
			WHERE current_entry.id=` + alias + `.entity_id
			  AND current_entry.relocated_to_id IS NULL
			  AND current_file.relative_path=` + alias + `.entity_key
			  AND current_file.title=` + alias + `.source_text))
		OR (` + alias + `.entity_kind='entry' AND ` + alias + `.field NOT IN ('title','directory') AND EXISTS(
			SELECT 1 FROM source_catalog_entry current_entry
			WHERE current_entry.id=` + alias + `.entity_id AND current_entry.relocated_to_id IS NULL))
	)`
}

// deleteHistoricalContentTranslations removes title model rows for retired
// entities, changed source text, or an old model/template. One join pass covers
// the three entity kinds; non-title fields are left to the budget pass because
// their current source is not a single entity title.
func (s *Server) deleteHistoricalContentTranslations(ctx context.Context, cfg translation.Config, limit int64) (int64, error) {
	if limit <= 0 {
		return 0, nil
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM content_translation WHERE id IN (
		SELECT ct.id FROM content_translation ct
		LEFT JOIN source_catalog_entry e ON ct.entity_kind='entry' AND e.id=ct.entity_id
		LEFT JOIN work w ON ct.entity_kind='work' AND w.id=ct.entity_id
		LEFT JOIN catalog_series s ON ct.entity_kind='series' AND s.id=ct.entity_id
		LEFT JOIN file_source src ON src.id=e.file_source_id
		WHERE ct.origin='model' AND ct.field='title'
		  AND (
			(ct.entity_kind='entry' AND (
				e.id IS NULL OR e.relocated_to_id IS NOT NULL OR e.availability_status<>'available'
				OR src.id IS NULL OR src.enabled<>1 OR src.source_type<>'openlist'
				OR ct.source_text<>e.title OR ct.model<>? OR ct.template_version<>?
			))
			OR (ct.entity_kind='work' AND (w.id IS NULL OR ct.source_text<>w.title OR ct.model<>? OR ct.template_version<>?))
			OR (ct.entity_kind='series' AND (s.id IS NULL OR ct.source_text<>s.title OR ct.model<>? OR ct.template_version<>?))
		  )
		LIMIT ?)`, cfg.Model, translation.TemplateVersion, cfg.Model, translation.TemplateVersion,
		cfg.Model, translation.TemplateVersion, limit)
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

// evictableTranslationStats counts only rows the budget may remove.
func (s *Server) evictableTranslationStats(ctx context.Context, cfg translation.Config) (int64, int64, error) {
	var rows, logicalBytes int64
	err := s.db.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(byte_count),0)
		FROM (
			SELECT length(CAST(ct.source_text AS BLOB))+length(CAST(ct.translated_text AS BLOB)) AS byte_count
			FROM content_translation ct
			WHERE ct.origin='model' AND NOT `+currentContentTranslationSQL("ct")+`
			UNION ALL
			SELECT length(CAST(source_text AS BLOB))+length(CAST(translated_text AS BLOB)) AS byte_count
			FROM source_field_translation
		)`, cfg.Model, translation.TemplateVersion).Scan(&rows, &logicalBytes)
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
			WHERE ct.origin='model' AND NOT `+currentContentTranslationSQL("ct")+`
			UNION ALL
			SELECT 'source' AS kind,id,updated_at
			FROM source_field_translation
		)
		ORDER BY updated_at,kind,id
		LIMIT ?`, cfg.Model, translation.TemplateVersion, limit)
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
