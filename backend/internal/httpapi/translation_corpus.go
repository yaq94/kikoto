package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/yexca/kikoto/backend/internal/translation"
)

// translationPrefillFailureStop is the persisted attempt count after which an
// item is reported failed. A failed item stays in the failure table and keeps
// the corpus incomplete; it is never treated as skipped or covered.
const translationPrefillFailureStop = 3

// translationModelLanguages are the languages the prefill pass generates.
// zh-tw is a display projection over zh-Hans and native zh-Hant, so it is not
// a separate queue and does not require a full second copy.
var translationModelLanguages = []string{
	translation.SourceEnglish,
	translation.SourceHans,
	translation.SourceJapanese,
	translation.SourceKorean,
}

// translationPrefillSlots are the effective entity fields in prefill order.
// source_catalog_entry has no description column and OpenList listings do not
// provide one, so an entry contributes only its title.
var translationPrefillSlots = []struct {
	Kind  string
	Field string
}{
	{Kind: "entry", Field: "title"},
	{Kind: "series", Field: "title"},
	{Kind: "work", Field: "title"},
	{Kind: "work", Field: "description"},
}

type translationPrefillCursor struct {
	Language string
	Kind     string
	Field    string
	ID       int64
}

// translationCorpusEntrySQL is the effective entry projection. Admission is
// the same predicate used by the publisher and browse API, so a hidden card
// never enters coverage or prefill.
func translationCorpusEntrySQL() string {
	return `SELECT 'entry' AS entity_kind, e.id AS entity_id, 'title' AS field,
			e.title AS source_text
		FROM source_catalog_entry e
		JOIN file_source src ON src.id=e.file_source_id
		WHERE e.relocated_to_id IS NULL
		  AND src.enabled=1 AND src.source_type='openlist'
		  AND trim(e.title)<>''
		  AND ` + catalogEntryAdmittedSQL("e")
}

func translationCorpusSeriesSQL() string {
	return `SELECT 'series' AS entity_kind, s.id AS entity_id, 'title' AS field, s.title AS source_text
		FROM catalog_series s
		WHERE trim(s.title)<>''
		  AND (SELECT COUNT(*) FROM catalog_series_member m WHERE m.series_id=s.id)>=2
		  AND EXISTS(
			SELECT 1 FROM catalog_series_member m
			JOIN source_catalog_entry e ON e.id=m.catalog_entry_id
			JOIN file_source src ON src.id=e.file_source_id
			WHERE m.series_id=s.id AND e.relocated_to_id IS NULL
			  AND src.enabled=1 AND src.source_type='openlist'
			  AND ` + catalogEntryAdmittedSQL("e") + `)`
}

func translationCorpusWorkSQL(field string) string {
	column := "w.title"
	if field == "description" {
		column = "w.description"
	}
	return `SELECT 'work' AS entity_kind, w.id AS entity_id, '` + field + `' AS field,
			` + column + ` AS source_text
		FROM work w
		WHERE trim(` + column + `)<>''
		  AND NOT EXISTS(SELECT 1 FROM catalog_series s WHERE s.work_id=w.id)`
}

// translationCorpusSQL is the complete effective searchable corpus. Tag and
// directory rows are display-only and never appear here.
func translationCorpusSQL() string {
	return strings.Join([]string{
		translationCorpusEntrySQL(),
		translationCorpusSeriesSQL(),
		translationCorpusWorkSQL("title"),
		translationCorpusWorkSQL("description"),
	}, " UNION ALL ")
}

// translationCorpusSlotSQL returns the direct keyset query for one slot. The
// boolean is false for a kind/field pair the effective corpus does not define,
// such as an entry description left over from an older row.
func translationCorpusSlotSQL(kind, field string) (string, bool) {
	switch kind {
	case "entry":
		if field != "title" {
			return "", false
		}
		return translationCorpusEntrySQL(), true
	case "series":
		if field != "title" {
			return "", false
		}
		return translationCorpusSeriesSQL(), true
	default:
		if field != "title" && field != "description" {
			return "", false
		}
		return translationCorpusWorkSQL(field), true
	}
}

// translationCorpusPage reads the next page after the cursor. The order is
// (kind, field, id, language), so one entity is offered to every model
// language before the scan moves to the next id. The cursor names the last
// emitted item; resuming starts strictly after it. When the page ends exactly
// on a batch boundary, the next call continues with the following item
// instead of repeating the model call.
func (s *Server) translationCorpusPage(ctx context.Context, cursor translationPrefillCursor, limit int) ([]translationCorpusItem, translationPrefillCursor, error) {
	if limit <= 0 {
		return nil, cursor, nil
	}
	startSlot := translationPrefillSlotIndex(cursor.Kind, cursor.Field)
	if startSlot < 0 {
		startSlot = 0
		cursor = translationPrefillCursor{}
	}
	startLanguage := -1
	if cursor.Language != "" {
		startLanguage = indexOf(translationModelLanguages, cursor.Language)
	}
	items := make([]translationCorpusItem, 0, limit)
	next := cursor
	for slotIndex := startSlot; slotIndex < len(translationPrefillSlots); slotIndex++ {
		slot := translationPrefillSlots[slotIndex]
		query, ok := translationCorpusSlotSQL(slot.Kind, slot.Field)
		if !ok {
			continue
		}
		// minID is inclusive. Only the cursor's own slot may start in the
		// middle of an entity; every later slot starts at its first id.
		minID := int64(0)
		skipLanguage := -1
		if slotIndex == startSlot {
			minID = cursor.ID
			skipLanguage = startLanguage
		}
		for {
			remaining := limit - len(items)
			if remaining <= 0 {
				return items, next, nil
			}
			rows, err := s.db.QueryContext(ctx, `SELECT entity_id,source_text FROM (`+
				query+`) WHERE entity_id>=? ORDER BY entity_id LIMIT ?`, minID, remaining)
			if err != nil {
				return nil, next, fmt.Errorf("read translation corpus %s/%s: %w", slot.Kind, slot.Field, err)
			}
			type corpusRow struct {
				id   int64
				text string
			}
			found := make([]corpusRow, 0, remaining)
			for rows.Next() {
				var row corpusRow
				if err := rows.Scan(&row.id, &row.text); err != nil {
					rows.Close()
					return nil, next, err
				}
				found = append(found, row)
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return nil, next, err
			}
			rows.Close()
			if len(found) == 0 {
				break
			}
			for _, row := range found {
				languageStart := 0
				if row.id == minID && skipLanguage >= 0 {
					// The cursor already emitted this entity up to and
					// including skipLanguage.
					languageStart = skipLanguage + 1
				}
				for index := languageStart; index < len(translationModelLanguages); index++ {
					if len(items) >= limit {
						return items, next, nil
					}
					items = append(items, translationCorpusItem{
						Language:   translationModelLanguages[index],
						EntityKind: slot.Kind,
						EntityID:   row.id,
						Field:      slot.Field,
						SourceText: row.text,
					})
					next = translationPrefillCursor{
						Language: translationModelLanguages[index],
						Kind:     slot.Kind, Field: slot.Field, ID: row.id,
					}
				}
				minID = row.id + 1
				skipLanguage = -1
				next = translationPrefillCursor{Kind: slot.Kind, Field: slot.Field, ID: minID}
			}
			if len(found) < remaining {
				break
			}
		}
	}
	// The scan is past every effective row. Keeping the end position makes a
	// later batch cheap; a newly published id above it is still discovered.
	return items, next, nil
}

func indexOf(values []string, target string) int {
	for index, value := range values {
		if value == target {
			return index
		}
	}
	return -1
}

func translationPrefillSlotIndex(kind, field string) int {
	for index, slot := range translationPrefillSlots {
		if slot.Kind == kind && slot.Field == field {
			return index
		}
	}
	return -1
}

type translationCorpusItem struct {
	Language   string
	EntityKind string
	EntityID   int64
	Field      string
	SourceText string
}

type translationCorpusLanguageCoverage struct {
	Language string
	Rows     int64
	// Native and Model count current rows by origin. An item normally has one
	// current row, but a native row can coexist with an earlier model row while
	// the corpus transitions, so Native+Model may exceed Rows.
	Native  int64
	Model   int64
	Missing int64
	Stale   int64
	Failed  int64
	Retry   int64
}

type translationCorpusCoverage struct {
	Total     int64
	Languages map[string]translationCorpusLanguageCoverage
	// Native and Model aggregate every reported language, including the
	// native-only zh-Hant projection.
	Native  int64
	Model   int64
	Missing int64
	Stale   int64
	Failed  int64
	Retry   int64
}

func (c translationCorpusCoverage) complete() bool {
	// An empty corpus is not evidence that translation finished. Without this
	// guard a fresh installation would report complete before any card exists.
	return c.Total > 0 && c.Missing == 0 && c.Stale == 0 && c.Failed == 0 && c.Retry == 0
}

// translationCurrentRowSQL accepts a native row or a model row produced by the
// current output-shaping configuration. zh-Hant is native-only.
func translationCurrentRowSQL(alias, language string) string {
	if language == translation.SourceHant {
		return alias + `.origin='native'`
	}
	return `(` + alias + `.origin='native' OR (` + alias + `.origin='model' AND ` + alias + `.config_key=?))`
}

// translationStaleConditionSQL marks an effective item that has no current row
// but has either a changed-source row or a model row from another config. It is
// one disjoint bucket; failure counts exclude it so rows+stale+failed+missing
// always adds up to the corpus total.
func translationStaleConditionSQL(language, configKey string) (string, []any) {
	current := translationCurrentRowSQL("ct", language)
	args := []any{language}
	if language != translation.SourceHant {
		args = append(args, configKey)
	}
	args = append(args, language, language)
	if language != translation.SourceHant {
		args = append(args, configKey)
	}
	return `NOT EXISTS(
			SELECT 1 FROM content_translation ct
			WHERE ct.entity_kind=c.entity_kind AND ct.entity_id=c.entity_id
			  AND ct.field=c.field AND ct.language=? AND ct.source_text=c.source_text
			  AND ` + current + `)
		AND (
			EXISTS(
				SELECT 1 FROM content_translation ct
				WHERE ct.entity_kind=c.entity_kind AND ct.entity_id=c.entity_id
				  AND ct.field=c.field AND ct.language=? AND ct.source_text<>c.source_text)
			OR EXISTS(
				SELECT 1 FROM content_translation ct
				WHERE ct.entity_kind=c.entity_kind AND ct.entity_id=c.entity_id
				  AND ct.field=c.field AND ct.language=? AND ct.source_text=c.source_text
				  AND ct.origin='model' AND ct.config_key<>?))`, args
}

// translationCorpusCoverageFor computes the operator view and the prefill
// completion decision from one definition. A current row must match the
// effective entity, the current field text, the requested language and either
// a native origin or the current config key.
func (s *Server) translationCorpusCoverageFor(ctx context.Context, cfg translation.Config) (translationCorpusCoverage, error) {
	coverage := translationCorpusCoverage{Languages: map[string]translationCorpusLanguageCoverage{}}
	configKey := translation.ConfigKey(cfg)
	corpus := `WITH corpus AS MATERIALIZED (` + translationCorpusSQL() + `)`
	if err := s.db.QueryRowContext(ctx, corpus+` SELECT count(*) FROM corpus`).Scan(&coverage.Total); err != nil {
		return coverage, fmt.Errorf("count effective translation corpus: %w", err)
	}
	for _, language := range translationModelLanguages {
		item := translationCorpusLanguageCoverage{Language: language}
		current := translationCurrentRowSQL("ct", language)
		// One scan returns the covered item count and the current native/model
		// row counts. Counting them in separate statements would scan the same
		// corpus three times. Rows counts items; a native and a model row can
		// coexist for one item, so Native+Model may exceed Rows.
		rowsQuery := corpus + `, current AS MATERIALIZED (
				SELECT c.entity_kind,c.entity_id,c.field,ct.origin
				FROM corpus c
				JOIN content_translation ct
				  ON ct.entity_kind=c.entity_kind AND ct.entity_id=c.entity_id
				 AND ct.field=c.field AND ct.language=? AND ct.source_text=c.source_text
				 AND ` + current + `
			),
			items AS MATERIALIZED (
				SELECT DISTINCT entity_kind,entity_id,field FROM current
			)
			SELECT
				(SELECT count(*) FROM items),
				(SELECT count(*) FROM current WHERE origin='native'),
				(SELECT count(*) FROM current WHERE origin='model')`
		rowsArgs := []any{language}
		if language != translation.SourceHant {
			rowsArgs = append(rowsArgs, configKey)
		}
		if err := s.db.QueryRowContext(ctx, rowsQuery, rowsArgs...).Scan(&item.Rows, &item.Native, &item.Model); err != nil {
			return coverage, fmt.Errorf("count %s coverage: %w", language, err)
		}
		staleCondition, staleArgs := translationStaleConditionSQL(language, configKey)
		staleQuery := corpus + ` SELECT count(*) FROM corpus c WHERE ` + staleCondition
		if err := s.db.QueryRowContext(ctx, staleQuery, staleArgs...).Scan(&item.Stale); err != nil {
			return coverage, fmt.Errorf("count %s stale rows: %w", language, err)
		}
		failureQuery := corpus + ` SELECT
			COALESCE(sum(CASE WHEN f.attempts>=? THEN 1 ELSE 0 END),0),
			COALESCE(sum(CASE WHEN f.attempts<? THEN 1 ELSE 0 END),0)
			FROM corpus c
			JOIN translation_prefill_failure f
			  ON f.entity_kind=c.entity_kind AND f.entity_id=c.entity_id
			 AND f.field=c.field AND f.language=?
			 AND f.config_key=? AND f.source_hash=` + translationSourceHashFunction + `(c.source_text)
			WHERE NOT EXISTS(
				SELECT 1 FROM content_translation ct
				WHERE ct.entity_kind=c.entity_kind AND ct.entity_id=c.entity_id
				  AND ct.field=c.field AND ct.language=? AND ct.source_text=c.source_text
				  AND ` + current + `)
			  AND NOT (` + staleCondition + `)`
		failureArgs := []any{translationPrefillFailureStop, translationPrefillFailureStop,
			language, configKey, language, configKey}
		failureArgs = append(failureArgs, staleArgs...)
		if err := s.db.QueryRowContext(ctx, failureQuery, failureArgs...).Scan(&item.Failed, &item.Retry); err != nil {
			return coverage, fmt.Errorf("count %s failures: %w", language, err)
		}
		// A retry is still missing from coverage; only a terminal failure is
		// counted as failed. Retry is reported separately but included in
		// missing, so rows+stale+failed+missing adds up to the total.
		item.Missing = max64(coverage.Total-item.Rows-item.Stale-item.Failed, 0)
		coverage.Languages[language] = item
		coverage.Native += item.Native
		coverage.Model += item.Model
		coverage.Missing += item.Missing
		coverage.Stale += item.Stale
		coverage.Failed += item.Failed
		coverage.Retry += item.Retry
	}
	// zh-Hant is a native-only display projection. A missing native Traditional
	// title is visible in this language's Missing/Rows counts but is not added to
	// the global Missing total, so it does not block completion: zh-tw reads the
	// current zh-Hans row and converts it locally.
	hant := translationCorpusLanguageCoverage{Language: translation.SourceHant}
	current := translationCurrentRowSQL("ct", translation.SourceHant)
	hantQuery := corpus + `, current AS MATERIALIZED (
			SELECT c.entity_kind,c.entity_id,c.field,ct.origin
			FROM corpus c
			JOIN content_translation ct
			  ON ct.entity_kind=c.entity_kind AND ct.entity_id=c.entity_id
			 AND ct.field=c.field AND ct.language=? AND ct.source_text=c.source_text
			 AND ` + current + `
		),
		items AS MATERIALIZED (
			SELECT DISTINCT entity_kind,entity_id,field FROM current
		)
		SELECT
			(SELECT count(*) FROM items),
			(SELECT count(*) FROM current WHERE origin='native'),
			(SELECT count(*) FROM current WHERE origin='model')`
	if err := s.db.QueryRowContext(ctx, hantQuery, translation.SourceHant).Scan(&hant.Rows, &hant.Native, &hant.Model); err != nil {
		return coverage, fmt.Errorf("count native Traditional coverage: %w", err)
	}
	hant.Missing = max64(coverage.Total-hant.Rows, 0)
	coverage.Languages[translation.SourceHant] = hant
	coverage.Native += hant.Native
	coverage.Model += hant.Model
	return coverage, nil
}

func (s *Server) translationCorpusItemCurrent(ctx context.Context, item translationCorpusItem, configKey string) (bool, error) {
	current := translationCurrentRowSQL("ct", item.Language)
	args := []any{item.EntityKind, item.EntityID, item.Field, item.Language, item.SourceText}
	if item.Language != translation.SourceHant {
		args = append(args, configKey)
	}
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM content_translation ct
		WHERE ct.entity_kind=? AND ct.entity_id=? AND ct.field=? AND ct.language=?
		  AND ct.source_text=? AND `+current+`)`, args...).Scan(&exists)
	return exists != 0, err
}

// translationPrefillItemExhausted reports a persisted failure that already
// spent its attempt budget for this exact source text. Such an item stays
// failed and incomplete, but the scan must not spend a model call on it again.
func (s *Server) translationPrefillItemExhausted(ctx context.Context, item translationCorpusItem, configKey string) (bool, error) {
	var exhausted int
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM translation_prefill_failure
		WHERE entity_kind=? AND entity_id=? AND field=? AND language=? AND config_key=?
		  AND source_hash=? AND attempts>=?)`,
		item.EntityKind, item.EntityID, item.Field, item.Language, configKey,
		translationSourceHash(item.SourceText), translationPrefillFailureStop).Scan(&exhausted)
	return exhausted != 0, err
}

// translationCorpusItemText reloads one effective field. It returns
// sql.ErrNoRows when the entity retired, the field became empty, or an old row
// names a field this corpus no longer defines.
func (s *Server) translationCorpusItemText(ctx context.Context, kind string, id int64, field string) (string, error) {
	query, ok := translationCorpusSlotSQL(kind, field)
	if !ok {
		return "", sql.ErrNoRows
	}
	var text string
	err := s.db.QueryRowContext(ctx, `SELECT source_text FROM (`+query+`) WHERE entity_id=?`, id).Scan(&text)
	return text, err
}

// translationPersistentCorpusSQL protects current title rows for entries and
// series, and current title/description rows for works. It is the single
// definition used by retention and the budget report. A stale config key or
// changed source text is not protected.
func translationPersistentCorpusSQL(alias string) string {
	return `(
		(` + alias + `.entity_kind='entry' AND ` + alias + `.field='title' AND EXISTS(
			SELECT 1 FROM source_catalog_entry e JOIN file_source src ON src.id=e.file_source_id
			WHERE e.id=` + alias + `.entity_id AND e.relocated_to_id IS NULL
			  AND src.enabled=1 AND src.source_type='openlist'
			  AND ` + catalogEntryAdmittedSQL("e") + `
			  AND ` + alias + `.source_text=e.title
			  AND (` + alias + `.origin='native' OR (` + alias + `.origin='model' AND ` + alias + `.config_key=?))))
		OR (` + alias + `.entity_kind='series' AND ` + alias + `.field='title' AND EXISTS(
			SELECT 1 FROM catalog_series s
			WHERE s.id=` + alias + `.entity_id AND ` + alias + `.source_text=s.title
			  AND (SELECT COUNT(*) FROM catalog_series_member m WHERE m.series_id=s.id)>=2
			  AND EXISTS(SELECT 1 FROM catalog_series_member m
				JOIN source_catalog_entry e ON e.id=m.catalog_entry_id
				JOIN file_source src ON src.id=e.file_source_id
				WHERE m.series_id=s.id AND e.relocated_to_id IS NULL
				  AND src.enabled=1 AND src.source_type='openlist' AND ` + catalogEntryAdmittedSQL("e") + `)
			  AND (` + alias + `.origin='native' OR (` + alias + `.origin='model' AND ` + alias + `.config_key=?))))
		OR (` + alias + `.entity_kind='work' AND ` + alias + `.field IN ('title','description') AND EXISTS(
			SELECT 1 FROM work w
			WHERE w.id=` + alias + `.entity_id
			  AND NOT EXISTS(SELECT 1 FROM catalog_series s WHERE s.work_id=w.id)
			  AND ((` + alias + `.field='title' AND ` + alias + `.source_text=w.title)
			    OR (` + alias + `.field='description' AND ` + alias + `.source_text=w.description))
			  AND (` + alias + `.origin='native' OR (` + alias + `.origin='model' AND ` + alias + `.config_key=?))))
	)`
}

func (s *Server) clearTranslationPrefillFailures(ctx context.Context, item translationCorpusItem, configKey string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM translation_prefill_failure
		WHERE entity_kind=? AND entity_id=? AND field=? AND language=? AND config_key=?`,
		item.EntityKind, item.EntityID, item.Field, item.Language, configKey)
	return err
}

func (s *Server) recordTranslationPrefillFailure(ctx context.Context, item translationCorpusItem, configKey string, cause error) (int64, error) {
	var attempts int64
	err := s.db.QueryRowContext(ctx, `INSERT INTO translation_prefill_failure(
		entity_kind,entity_id,field,language,config_key,source_hash,attempts,next_retry_at,last_error)
		VALUES(?,?,?,?,?,?,1,datetime('now','+1 minute'),?)
		ON CONFLICT(entity_kind,entity_id,field,language,config_key,source_hash) DO UPDATE SET
			attempts=translation_prefill_failure.attempts+1,
			next_retry_at=CASE translation_prefill_failure.attempts+1
				WHEN 1 THEN datetime('now','+1 minute')
				WHEN 2 THEN datetime('now','+5 minutes')
				ELSE NULL END,
			last_error=excluded.last_error
		RETURNING attempts`,
		item.EntityKind, item.EntityID, item.Field, item.Language, configKey,
		translationSourceHash(item.SourceText), errorText(cause)).Scan(&attempts)
	return attempts, err
}

// dueTranslationPrefillFailures returns persisted failures whose backoff
// expired and whose source hash still matches the effective text.
func (s *Server) dueTranslationPrefillFailures(ctx context.Context, cfg translation.Config, limit int) ([]translationCorpusItem, error) {
	if limit <= 0 {
		return nil, nil
	}
	configKey := translation.ConfigKey(cfg)
	rows, err := s.db.QueryContext(ctx, `SELECT entity_kind,entity_id,field,language,source_hash
		FROM translation_prefill_failure
		WHERE config_key=? AND attempts<?
		  AND (next_retry_at IS NULL OR next_retry_at<=CURRENT_TIMESTAMP)
		ORDER BY next_retry_at,entity_kind,entity_id,field,language LIMIT ?`,
		configKey, translationPrefillFailureStop, limit)
	if err != nil {
		return nil, fmt.Errorf("read due prefill failures: %w", err)
	}
	defer rows.Close()
	type failure struct {
		item       translationCorpusItem
		sourceHash string
	}
	failures := []failure{}
	for rows.Next() {
		var item translationCorpusItem
		var sourceHash string
		if err := rows.Scan(&item.EntityKind, &item.EntityID, &item.Field, &item.Language, &sourceHash); err != nil {
			return nil, err
		}
		item.Language = normalizePrefillLanguage(item.Language)
		if item.Language == "" {
			continue
		}
		failures = append(failures, failure{item: item, sourceHash: sourceHash})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	items := []translationCorpusItem{}
	for _, failure := range failures {
		text, err := s.translationCorpusItemText(ctx, failure.item.EntityKind, failure.item.EntityID, failure.item.Field)
		if errors.Is(err, sql.ErrNoRows) {
			_, _ = s.db.ExecContext(ctx, `DELETE FROM translation_prefill_failure
				WHERE entity_kind=? AND entity_id=? AND field=? AND language=? AND config_key=?`,
				failure.item.EntityKind, failure.item.EntityID, failure.item.Field, failure.item.Language, configKey)
			continue
		}
		if err != nil {
			return nil, err
		}
		if translationSourceHash(text) != failure.sourceHash {
			// Changed text starts a fresh retry budget. The old row is not
			// allowed to suppress the new source hash.
			_, _ = s.db.ExecContext(ctx, `DELETE FROM translation_prefill_failure
				WHERE entity_kind=? AND entity_id=? AND field=? AND language=? AND config_key=? AND source_hash=?`,
				failure.item.EntityKind, failure.item.EntityID, failure.item.Field, failure.item.Language, configKey, failure.sourceHash)
			continue
		}
		failure.item.SourceText = text
		items = append(items, failure.item)
	}
	return items, nil
}

func normalizePrefillLanguage(language string) string {
	for _, candidate := range translationModelLanguages {
		if language == candidate {
			return language
		}
	}
	return ""
}
