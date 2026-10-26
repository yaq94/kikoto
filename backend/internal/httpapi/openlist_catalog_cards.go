package httpapi

import (
	"database/sql"
	"strings"
	"unicode/utf8"

	"github.com/yexca/kikoto/backend/internal/library"
	"github.com/yexca/kikoto/backend/internal/translation"
)

// openListCatalogCardsQuery builds the deduplicated card source once. Empty
// search omits both the FTS CTE and the per-file instr predicates, so browsing
// without a query never touches file titles.
func openListCatalogCardsQuery(sourceID int64, query string) (string, string, []any) {
	return openListCatalogCardsQueryFiltered(sourceID, query, nil, nil, nil, "", 0)
}

// openListCatalogCardsQueryLocalized restricts text search to the reader's
// display-language corpus. It never falls back to the original title.
//
// shortLimit bounds how many cards a 1-2 character query may return. The scan
// still walks the complete effective range in index order, so the budget covers
// the oldest rows instead of a newest window; zero keeps every match, which the
// trigram path and the diagnostic readers use.
func openListCatalogCardsQueryLocalized(sourceID int64, query string, targets []library.DisplaySearchTarget, configKey string, shortLimit int) (string, string, []any) {
	return openListCatalogCardsQueryFiltered(sourceID, query, nil, nil, targets, configKey, shortLimit)
}

func openListCatalogCardsQueryForEntries(sourceID int64, entryIDs []int64) (string, string, []any) {
	return openListCatalogCardsQueryFiltered(sourceID, "", entryIDs, nil, nil, "", 0)
}

func openListCatalogCardsQueryFiltered(sourceID int64, query string, entryIDs, seriesIDs []int64, localized []library.DisplaySearchTarget, configKey string, shortLimit int) (string, string, []any) {
	query = strings.TrimSpace(query)
	prefix := ""
	args := []any{}
	entrySearch := ""
	entryArgs := []any{}
	seriesSearch := ""
	seriesArgs := []any{}
	entrySource := "source_catalog_entry e INDEXED BY source_catalog_entry_live_root"
	seriesScope := ""
	scope := []string{}
	searched := entryIDs != nil || seriesIDs != nil
	// A short translated search only returns rows whose stored `browsable` flag
	// already means "admitted and playable". Recomputing admission and the audio
	// probe per candidate would repeat that work 1000 times for no new answer.
	trustedScope := false
	if searched {
		// A display-language search already knows the matching identities.
		// Drive the card pipeline from that bounded set with primary-key
		// lookups instead of scanning every entry of the source. A sentinel id
		// keeps the entry arm empty when only a series title matched.
		entryValues := "(?,0)"
		if len(entryIDs) > 0 {
			entryValues = strings.TrimSuffix(strings.Repeat("(?,0),", len(entryIDs)), ",")
			for _, id := range entryIDs {
				args = append(args, id)
			}
		} else {
			args = append(args, int64(-1))
		}
		prefix = "WITH requested_entry(id,exact) AS (VALUES " + entryValues + "), "
		entrySource = "requested_entry AS requested CROSS JOIN source_catalog_entry AS e ON e.id = requested.id"
		scope = append(scope, `s.id IN (
			SELECT scope_member.series_id FROM catalog_series_member AS scope_member
			JOIN requested_entry AS scope_requested ON scope_requested.id = scope_member.catalog_entry_id)`)
		// The series arm always reads requested_series, so the name must exist
		// even when the caller supplied no series ids. A sentinel keeps it
		// empty.
		values := "(?,0)"
		if len(seriesIDs) > 0 {
			values = strings.TrimSuffix(strings.Repeat("(?,0),", len(seriesIDs)), ",")
			for _, id := range seriesIDs {
				args = append(args, id)
			}
		} else {
			args = append(args, int64(-1))
		}
		prefix += "requested_series(id,exact) AS (VALUES " + values + "), "
		scope = append(scope, `s.id IN (SELECT id FROM requested_series)`)
		seriesScope = " AND (" + strings.Join(scope, " OR ") + ")"
	}
	if query != "" {
		if len(localized) > 0 {
			// A display language searches only its own corpus. Original-title
			// matches are not a fallback for a missing translation.
			prefix, args = localizedCatalogSearchSQL(localized, sourceID, utf8.RuneCountInString(query) < 3, configKey, shortLimit)
			// The hit set already names the cards. Driving the pipeline from it
			// keeps the work proportional to the matches instead of scanning
			// every entry of the source and filtering afterwards. A 1-2
			// character query can match the whole source, so its candidate set
			// is capped; the reported total is capped with it.
			short := utf8.RuneCountInString(query) < 3
			// The cap lives inside each arm, so SQLite stops scanning at the
			// bound instead of materializing a full-source match first.
			// `exact` rides along so the page can order exact titles first
			// without a second probe per card.
			prefix += `, requested_entry(id,exact) AS (SELECT catalog_entry_id,max(exact) FROM search_hits ` +
				`WHERE catalog_entry_id<>0 GROUP BY catalog_entry_id), ` +
				`requested_series(id,exact) AS (SELECT series_id,max(exact) FROM search_hits ` +
				`WHERE series_id<>0 GROUP BY series_id) `
			entrySource = "requested_entry AS requested CROSS JOIN source_catalog_entry AS e ON e.id = requested.id"
			scope = append(scope, `s.id IN (
				SELECT scope_member.series_id FROM catalog_series_member AS scope_member
				JOIN requested_entry AS scope_requested ON scope_requested.id = scope_member.catalog_entry_id)`)
			scope = append(scope, `s.id IN (SELECT id FROM requested_series)`)
			seriesScope = " AND (" + strings.Join(scope, " OR ") + ")"
			searched = true
			trustedScope = short
		} else if utf8.RuneCountInString(query) >= 3 {
			prefix = `WITH search_hits AS MATERIALIZED (
	SELECT d.catalog_entry_id, d.series_id
	FROM source_catalog_search s JOIN source_catalog_search_doc d ON d.id=s.rowid
	WHERE source_catalog_search MATCH ? AND (d.entity_kind='series' OR d.file_source_id=?)
	`
			args = append(args, ftsPhrase(query), sourceID)
			// The trigram path has no exact flag; its ordering stays by
			// recency, and 0 keeps the shared page projection valid.
			prefix += `), requested_entry(id,exact) AS (SELECT catalog_entry_id,0 FROM search_hits WHERE catalog_entry_id<>0), ` +
				`requested_series(id,exact) AS (SELECT series_id,0 FROM search_hits WHERE series_id<>0) `
			entrySource = "requested_entry AS requested CROSS JOIN source_catalog_entry AS e ON e.id = requested.id"
			scope = append(scope, `s.id IN (
				SELECT scope_member.series_id FROM catalog_series_member AS scope_member
				JOIN requested_entry AS scope_requested ON scope_requested.id = scope_member.catalog_entry_id)`)
			scope = append(scope, `s.id IN (SELECT id FROM requested_series)`)
			seriesScope = " AND (" + strings.Join(scope, " OR ") + ")"
			searched = true
		} else {
			// A trigram needs three characters. One- and two-character queries
			// keep the old substring behaviour rather than silently matching
			// nothing.
			entrySearch = ` AND (instr(lower(e.title),lower(?))>0 OR instr(lower(e.primary_code),lower(?))>0
	OR EXISTS(SELECT 1 FROM source_catalog_file f WHERE f.catalog_entry_id=e.id AND instr(lower(f.title),lower(?))>0))`
			entryArgs = []any{query, query, query}
			seriesSearch = ` AND (instr(lower(s.title),lower(?))>0 OR EXISTS(
	SELECT 1 FROM catalog_series_member m JOIN source_catalog_entry se ON se.id=m.catalog_entry_id
	WHERE m.series_id=s.id AND (instr(lower(se.title),lower(?))>0 OR instr(lower(se.primary_code),lower(?))>0
	OR EXISTS(SELECT 1 FROM source_catalog_file sf WHERE sf.catalog_entry_id=se.id AND instr(lower(sf.title),lower(?))>0))))`
			seriesArgs = []any{query, query, query, query}
		}
	}
	// Browsing a whole source and reading a bounded search need different dedup
	// shapes. Only a card that belongs to a work can collide with another card,
	// and only through a shared identity; every other card's own id already
	// makes it unique. Ranking that small set keeps the dedup off the tens of
	// thousands of unregistered cards. A search has already narrowed the
	// candidate set, so it keeps the single-pass rank that probes matched ids by
	// rowid instead of re-entering the live-root index.
	entryDedup := ""
	rankSuffix := ""
	if !searched {
		workRank := `work_rank AS (
			SELECT ranked.id,ROW_NUMBER() OVER (PARTITION BY COALESCE(identity.identity_work_id,ranked.work_id) ORDER BY ranked.id) AS rank
			FROM (SELECT id,work_id FROM source_catalog_entry
				WHERE file_source_id=? AND relocated_to_id IS NULL AND work_id IS NOT NULL) AS ranked
			LEFT JOIN shared_work_identity identity ON identity.work_id=ranked.work_id
		) `
		if strings.HasPrefix(prefix, "WITH ") {
			prefix += ", " + workRank
		} else {
			prefix = "WITH " + workRank
		}
		args = append(args, sourceID)
		entryDedup = ` AND (e.work_id IS NULL OR e.id IN (SELECT id FROM work_rank WHERE rank=1))`
	}
	entryHasAudio := catalogEntryHasAudioSQL("e")
	entryAdmitted := catalogEntryAdmittedSQL("e")
	seriesMemberAdmitted := entryAdmitted
	// `exact` only exists on a search hit set. A browse has no needle, so every
	// card is equally inexact.
	entryExact := "0 AS exact"
	seriesExact := "0 AS exact"
	if searched {
		entryExact = "requested.exact AS exact"
		// A series is an exact hit when its own title matches, or when a
		// member that matched exactly belongs to it. The member set is the
		// bounded hit set, so this stays one lookup per candidate.
		seriesExact = `COALESCE((SELECT exact FROM requested_series WHERE id=s.id),
			EXISTS(SELECT 1 FROM catalog_series_member exact_member
				JOIN requested_entry exact_entry ON exact_entry.id=exact_member.catalog_entry_id
				WHERE exact_member.series_id=s.id AND exact_entry.exact=1),0) AS exact`
	}
	seriesPlayable := `CASE WHEN EXISTS(
			SELECT 1 FROM catalog_series_member mm JOIN source_catalog_entry ee ON ee.id=mm.catalog_entry_id
			JOIN source_catalog_file ff INDEXED BY source_catalog_file_parent ON ff.catalog_entry_id=ee.id AND ff.kind='audio'
			WHERE mm.series_id=s.id AND ee.relocated_to_id IS NULL AND ee.availability_status='available' AND ` + catalogEntryAdmittedSQL("ee") + `
		) THEN 'available' ELSE 'missing' END`
	seriesHasAudio := `EXISTS(
			SELECT 1 FROM catalog_series_member mm JOIN source_catalog_entry ee ON ee.id=mm.catalog_entry_id
			JOIN source_catalog_file ff INDEXED BY source_catalog_file_parent ON ff.catalog_entry_id=ee.id AND ff.kind='audio'
			WHERE mm.series_id=s.id AND ee.relocated_to_id IS NULL AND ` + catalogEntryAdmittedSQL("ee") + `
		)`
	if trustedScope {
		// A stored `browsable` row already passed admission and has audio. The
		// series arm still needs a member's availability, but its admission and
		// audio probes are known to succeed.
		entryHasAudio = "1"
		entryAdmitted = "1=1"
		seriesMemberAdmitted = "1=1"
		seriesPlayable = `CASE WHEN EXISTS(
			SELECT 1 FROM catalog_series_member mm JOIN source_catalog_entry ee ON ee.id=mm.catalog_entry_id
			JOIN source_catalog_file ff INDEXED BY source_catalog_file_parent ON ff.catalog_entry_id=ee.id AND ff.kind='audio'
			WHERE mm.series_id=s.id AND ee.relocated_to_id IS NULL AND ee.availability_status='available'
		) THEN 'available' ELSE 'missing' END`
		seriesHasAudio = `EXISTS(
			SELECT 1 FROM catalog_series_member mm JOIN source_catalog_entry ee ON ee.id=mm.catalog_entry_id
			JOIN source_catalog_file ff INDEXED BY source_catalog_file_parent ON ff.catalog_entry_id=ee.id AND ff.kind='audio'
			WHERE mm.series_id=s.id AND ee.relocated_to_id IS NULL
		)`
	}
	arms := `(
		SELECT e.id,0 AS series_id,e.primary_code,e.title,e.work_id,e.updated_at,e.availability_status,` + entryExact + `,
			` + entryHasAudio + ` AS has_audio FROM ` + entrySource + `
		WHERE e.file_source_id=? AND e.relocated_to_id IS NULL AND NOT EXISTS(SELECT 1 FROM catalog_series_member m WHERE m.catalog_entry_id=e.id AND (SELECT COUNT(*) FROM catalog_series_member sibling WHERE sibling.series_id=m.series_id)>=2)
			AND ` + entryAdmitted + entrySearch + `
			` + entryDedup + `
		UNION ALL
		SELECT 0,s.id,'',s.title,s.work_id,s.created_at,` + seriesPlayable + `,` + seriesExact + `,
			` + seriesHasAudio + ` AS has_audio FROM catalog_series s
		WHERE (SELECT COUNT(*) FROM catalog_series_member all_members WHERE all_members.series_id=s.id)>=2` + seriesScope + `
		AND EXISTS(SELECT 1 FROM catalog_series_member m CROSS JOIN source_catalog_entry e ON e.rowid=m.catalog_entry_id
		WHERE m.series_id=s.id AND e.file_source_id=? AND e.relocated_to_id IS NULL AND ` +
		seriesMemberAdmitted + seriesSearch + `)
	)`
	if searched {
		rankSuffix = `FROM (SELECT candidates.*,ROW_NUMBER() OVER (PARTITION BY CASE WHEN work_id IS NOT NULL THEN 'work:' || (SELECT identity_work_id FROM shared_work_identity WHERE work_id=candidates.work_id) WHEN series_id>0 THEN 'series:' || series_id ELSE 'catalog:' || id END ORDER BY series_id,id) AS identity_rank FROM ` + arms + ` AS candidates) AS cards WHERE identity_rank=1`
	} else {
		rankSuffix = `FROM ` + arms + ` AS cards`
	}
	from := rankSuffix
	args = append(args, sourceID)
	args = append(args, entryArgs...)
	args = append(args, sourceID)
	args = append(args, seriesArgs...)
	return prefix, from, args
}

func ftsPhrase(query string) string {
	return `"` + strings.ReplaceAll(query, `"`, `""`) + `"`
}

// catalogCardPageColumns is the card projection every reader shares. `exact`
// follows the availability so a search can put exact titles first; a browse
// reports 0.
const catalogCardPageColumns = `id,series_id,primary_code,title,work_id,updated_at,availability_status,exact,has_audio`

// catalogCardPageSQL builds the page query for one candidate set. A capped
// search omits the window total and reports its count separately; every other
// shape carries the total on the page so a non-empty page needs one statement.
func catalogCardPageSQL(prefix, from string, capped bool) string {
	total := `,COUNT(*) OVER () AS total`
	if capped {
		total = ""
	}
	return catalogStatementPrefix(prefix) + `SELECT ` + catalogCardPageColumns + total + ` ` + from +
		` ORDER BY exact DESC,updated_at DESC,series_id DESC,id DESC LIMIT ? OFFSET ?`
}

// catalogStatementPrefix closes a CTE list that ends with a separator, so a
// statement can follow it. A prefix that already ends in a complete definition
// is left alone.
func catalogStatementPrefix(prefix string) string {
	trimmed := strings.TrimSpace(prefix)
	if strings.HasSuffix(trimmed, ",") {
		return strings.TrimSuffix(trimmed, ",") + " "
	}
	if trimmed == "" {
		return ""
	}
	return trimmed + " "
}

// catalogCardScan reads one card row. capped selects the shape without the
// window total, which a capped search reports separately.
func catalogCardScan(rows *sql.Rows, item *remoteWorkSummary, availability *string, exact *int, hasAudio *bool, total *int, capped bool) error {
	if capped {
		return rows.Scan(&item.CatalogEntryID, &item.SeriesID, &item.PrimaryCode, &item.Title, &item.WorkID,
			&item.UpdatedAt, availability, exact, hasAudio)
	}
	return rows.Scan(&item.CatalogEntryID, &item.SeriesID, &item.PrimaryCode, &item.Title, &item.WorkID,
		&item.UpdatedAt, availability, exact, hasAudio, total)
}

// localizedCatalogSearchSQL builds the translated hit set for a display
// language. Entry and series titles are searchable; source_catalog_entry has
// no description column. Three or more characters use the trigram index;
// one- and two-character needles use the shared keyset generator so the
// remote, local and benchmark paths cannot diverge again.
// catalogShortSearchResultLimit is how many cards a 1-2 character search may
// return. The scan still covers the complete effective set in index order, but
// both the page and the reported total stop at this budget, and the response
// marks the list truncated when the overflow probe finds another match.
// Building a card row for every match would otherwise make one keystroke cost
// the whole source.
const catalogShortSearchResultLimit = 1000

func localizedCatalogSearchSQL(targets []library.DisplaySearchTarget, sourceID int64, short bool, configKey string, shortLimit int) (string, []any) {
	if !short {
		prefix := `WITH search_hits AS MATERIALIZED (`
		args := []any{}
		for index, target := range targets {
			if index > 0 {
				prefix += ` UNION ALL `
			}
			phrase := ftsPhrase(target.Query)
			prefix += `SELECT ct.entity_id AS catalog_entry_id,0 AS series_id,(ct.translated_text=?) AS exact FROM content_translation_search
				JOIN content_translation ct ON ct.id=content_translation_search.rowid
				JOIN source_catalog_entry e ON e.id=ct.entity_id
				WHERE content_translation_search MATCH ? AND ct.entity_kind='entry'
				  AND ct.field='title' AND ct.language=?
				  AND e.file_source_id=? AND e.relocated_to_id IS NULL
				  AND ct.source_text=e.title
				  AND ct.browsable=1
				  AND ` + catalogEntryAdmittedSQL("e") + `
				  AND ` + translationCurrentRowSQL("ct", target.Language) + `
				UNION ALL
				SELECT 0 AS catalog_entry_id,ct.entity_id AS series_id,(ct.translated_text=?) AS exact FROM content_translation_search
				JOIN content_translation ct ON ct.id=content_translation_search.rowid
				JOIN catalog_series s ON s.id=ct.entity_id
				WHERE content_translation_search MATCH ? AND ct.entity_kind='series' AND ct.field='title' AND ct.language=?
				  AND s.source_id=? AND ct.source_text=s.title
				  AND ct.browsable=1
				  AND (SELECT COUNT(*) FROM catalog_series_member sm WHERE sm.series_id=s.id)>=2
				  AND EXISTS(SELECT 1 FROM catalog_series_member sm JOIN source_catalog_entry se ON se.id=sm.catalog_entry_id
					WHERE sm.series_id=s.id AND se.relocated_to_id IS NULL AND ` + catalogEntryAdmittedSQL("se") + `)
				  AND ` + translationCurrentRowSQL("ct", target.Language)
			args = append(args, target.Query, phrase, target.Language, sourceID)
			if target.Language != translation.SourceHant {
				args = append(args, configKey)
			}
			args = append(args, target.Query, phrase, target.Language, sourceID)
			if target.Language != translation.SourceHant {
				args = append(args, configKey)
			}
		}
		return prefix + `) `, args
	}

	// The row itself carries the conclusion: `browsable` says the stored text
	// still matches a live, playable card of an enabled source. Keeping it in
	// the scan index means a translated title for a card that cannot be shown
	// neither consumes the result budget nor costs a join per candidate.
	entrySQL, entryArgs := library.ShortTranslationSearchSQL(targets, []library.ShortSearchArm{
		{EntityKind: "entry", Fields: []string{"title"}, SourceID: sourceID, ConfigKey: configKey},
	}, shortLimit)
	seriesSQL, seriesArgs := library.ShortTranslationSearchSQL(targets, []library.ShortSearchArm{
		{EntityKind: "series", Fields: []string{"title"}, SourceID: sourceID, ConfigKey: configKey},
	}, shortLimit)
	if entrySQL == "" && seriesSQL == "" {
		return `WITH search_hits AS MATERIALIZED (SELECT -1 AS catalog_entry_id, -1 AS series_id, 0 AS exact WHERE 0) `, nil
	}
	// Project each arm straight into the two card columns plus the exact-match
	// flag the scan already computed. Routing both through one intermediate row
	// set would materialize every match twice, which at the size of a full
	// source costs more than the scan itself.
	branches := []string{}
	args := []any{}
	if entrySQL != "" {
		branches = append(branches, `SELECT entity_id AS catalog_entry_id,0 AS series_id,exact FROM (`+entrySQL+`)`)
		args = append(args, entryArgs...)
	}
	if seriesSQL != "" {
		branches = append(branches, `SELECT 0 AS catalog_entry_id,entity_id AS series_id,exact FROM (`+seriesSQL+`)`)
		args = append(args, seriesArgs...)
	}
	prefix := `WITH search_hits AS MATERIALIZED (` + strings.Join(branches, " UNION ALL ") + `) `
	return prefix, args
}

// shortSearchOverflowQuery reports whether any branch of a 1-2 character search
// has more effective rows than the card budget. It is a bounded probe: the
// result list and its total describe the effective set, and this flag says
// whether the list stopped early.
func shortSearchOverflowQuery(targets []library.DisplaySearchTarget, sourceID int64, configKey string) (string, []any) {
	return library.ShortTranslationSearchOverflowSQL(targets, []library.ShortSearchArm{
		{EntityKind: "entry", Fields: []string{"title"}, SourceID: sourceID, ConfigKey: configKey},
		{EntityKind: "series", Fields: []string{"title"}, SourceID: sourceID, ConfigKey: configKey},
	}, catalogShortSearchResultLimit)
}
