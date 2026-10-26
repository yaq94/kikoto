package httpapi

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"testing"

	"github.com/yexca/kikoto/backend/internal/catalogadmission"
	"github.com/yexca/kikoto/backend/internal/library"
	"github.com/yexca/kikoto/backend/internal/translation"
)

// shortQueryIDs runs the shared generator and returns the matched translation
// row ids in scan order, so a paging test can assert no row is skipped or
// repeated.
func shortQueryIDs(t *testing.T, db *sql.DB, arm library.ShortSearchArm, needle string, limit int) []int64 {
	t.Helper()
	query, args := library.ShortTranslationSearchSQL(
		[]library.DisplaySearchTarget{{Language: translation.SourceHans, Query: needle}},
		[]library.ShortSearchArm{arm}, limit)
	if query == "" {
		return nil
	}
	rows, err := db.Query(query, args...)
	if err != nil {
		t.Fatalf("short query: %v", err)
	}
	defer func() { _ = rows.Close() }()
	ids := []int64{}
	for rows.Next() {
		var entityID, id int64
		var language, kind, field string
		var exact int
		if err = rows.Scan(&entityID, &language, &kind, &field, &id, &exact); err != nil {
			t.Fatalf("scan short row: %v", err)
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		t.Fatalf("short query rows: %v", err)
	}
	return ids
}

func shortQueryScope(t *testing.T, db *sql.DB, kind string, id int64) (sql.NullInt64, int) {
	t.Helper()
	var sourceID sql.NullInt64
	var browsable int
	if err := db.QueryRow(`SELECT source_id,browsable FROM content_translation
		WHERE entity_kind=? AND entity_id=? AND field='title' AND language=?`,
		kind, id, translation.SourceHans).Scan(&sourceID, &browsable); err != nil {
		t.Fatalf("read scope %s/%d: %v", kind, id, err)
	}
	return sourceID, browsable
}

func seedScopeSource(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO file_source(id,code,display_name,source_type,enabled)
		VALUES(91,'example_remote_a','Example Remote A','openlist',1)`); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	return 91
}

func seedScopeEntry(t *testing.T, db *sql.DB, sourceID, id int64, title string, audio int) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO source_catalog_entry(
		id,file_source_id,root_path,primary_code,title,availability_status,last_seen_generation)
		VALUES(?,?,?,?,?,'available',1)`, id, sourceID, "/entry/"+strconv.FormatInt(id, 10), "", title); err != nil {
		t.Fatalf("seed entry %d: %v", id, err)
	}
	for index := 0; index < audio; index++ {
		if _, err := db.Exec(`INSERT INTO source_catalog_file(
			catalog_entry_id,relative_path,title,kind,size_bytes,observed_generation)
			VALUES(?,?,?,'audio',1024,1)`, id, "track-"+strconv.Itoa(index)+".mp3", "track"); err != nil {
			t.Fatalf("seed file %d/%d: %v", id, index, err)
		}
	}
}

func seedScopeTranslation(t *testing.T, db *sql.DB, kind string, id int64, sourceText, translated string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO content_translation(
		cache_key,entity_kind,entity_id,field,language,source_text,translated_text,origin)
		VALUES(?,?,?,'title',?,?,?,'native')`,
		kind+"-"+strconv.FormatInt(id, 10)+"-scope", kind, id, translation.SourceHans, sourceText, translated); err != nil {
		t.Fatalf("seed translation %s/%d: %v", kind, id, err)
	}
}

func refreshScopeAdmission(t *testing.T, db *sql.DB, sourceID int64) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin admission refresh: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err = refreshSourceCatalogEntryAdmissionsTx(context.Background(), tx, sourceID); err != nil {
		t.Fatalf("refresh admissions: %v", err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatalf("commit admission refresh: %v", err)
	}
}

func TestContentTranslationScopeFollowsAdmissionAndTitle(t *testing.T) {
	db := openMigratedTestDB(t)
	sourceID := seedScopeSource(t, db)
	seedScopeEntry(t, db, sourceID, 1, "Rain", 1)
	seedScopeTranslation(t, db, "entry", 1, "Rain", "雨")
	refreshScopeAdmission(t, db, sourceID)

	if _, browsable := shortQueryScope(t, db, "entry", 1); browsable != 1 {
		t.Fatalf("admitted card browsable=%d, want 1", browsable)
	}
	arm := library.ShortSearchArm{EntityKind: "entry", Fields: []string{"title"}, SourceID: sourceID}
	if ids := shortQueryIDs(t, db, arm, "雨", 10); len(ids) != 1 {
		t.Fatalf("admitted card hits=%v, want one row", ids)
	}

	// A title edit hides the old translation; restoring the title makes the
	// same row current again.
	if _, err := db.Exec(`UPDATE source_catalog_entry SET title='Sun' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if ids := shortQueryIDs(t, db, arm, "雨", 10); len(ids) != 0 {
		t.Fatalf("stale title hits=%v, want none", ids)
	}
	if _, err := db.Exec(`UPDATE source_catalog_entry SET title='Rain' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if ids := shortQueryIDs(t, db, arm, "雨", 10); len(ids) != 1 {
		t.Fatalf("restored title hits=%v, want one row", ids)
	}

	// An oversized card is refused by admission, so its translation leaves the
	// searchable set even though the title did not change.
	if _, err := db.Exec(`UPDATE source_catalog_entry_admission SET files=5001,audio=6,non_audio=4995
		WHERE catalog_entry_id=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE source_catalog_entry SET title='Rain' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if ids := shortQueryIDs(t, db, arm, "雨", 10); len(ids) != 0 {
		t.Fatalf("refused card hits=%v, want none", ids)
	}

	// Adding the audio that makes the card playable publishes it again.
	if _, err := db.Exec(`DELETE FROM source_catalog_file WHERE catalog_entry_id=1`); err != nil {
		t.Fatal(err)
	}
	refreshScopeAdmission(t, db, sourceID)
	if ids := shortQueryIDs(t, db, arm, "雨", 10); len(ids) != 0 {
		t.Fatalf("fileless card hits=%v, want none", ids)
	}
	if _, err := db.Exec(`INSERT INTO source_catalog_file(
		catalog_entry_id,relative_path,title,kind,size_bytes,observed_generation)
		VALUES(1,'01.mp3','01.mp3','audio',1024,1)`); err != nil {
		t.Fatal(err)
	}
	refreshScopeAdmission(t, db, sourceID)
	if ids := shortQueryIDs(t, db, arm, "雨", 10); len(ids) != 1 {
		t.Fatalf("restored audio hits=%v, want one row", ids)
	}
}

func TestContentTranslationScopeSeriesNeedsAnAdmittedMember(t *testing.T) {
	db := openMigratedTestDB(t)
	sourceID := seedScopeSource(t, db)
	seedScopeEntry(t, db, sourceID, 1, "Episode One", 1)
	seedScopeEntry(t, db, sourceID, 2, "Episode Two", 1)
	if _, err := db.Exec(`INSERT INTO catalog_series(id,title,source_id) VALUES(1,'Rain Series',?)`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence)
		VALUES(1,1,1,'manual')`); err != nil {
		t.Fatal(err)
	}
	seedScopeTranslation(t, db, "series", 1, "Rain Series", "雨系列")
	refreshScopeAdmission(t, db, sourceID)

	arm := library.ShortSearchArm{EntityKind: "series", Fields: []string{"title"}, SourceID: sourceID}
	// One member is below the two-episode minimum, so the series is not a card.
	if ids := shortQueryIDs(t, db, arm, "雨", 10); len(ids) != 0 {
		t.Fatalf("single-member series hits=%v, want none", ids)
	}
	if _, err := db.Exec(`INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence)
		VALUES(1,2,2,'manual')`); err != nil {
		t.Fatal(err)
	}
	if ids := shortQueryIDs(t, db, arm, "雨", 10); len(ids) != 1 {
		t.Fatalf("two-member series hits=%v, want one row", ids)
	}

	// A member that cannot play does not count; with both refused the series
	// leaves the searchable set.
	if _, err := db.Exec(`UPDATE source_catalog_entry_admission SET files=5001,audio=6,non_audio=4995
		WHERE catalog_entry_id IN (1,2)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE catalog_series SET title='Rain Series' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if ids := shortQueryIDs(t, db, arm, "雨", 10); len(ids) != 0 {
		t.Fatalf("series with refused members hits=%v, want none", ids)
	}
}

func TestContentTranslationScopeWorkLeavesWhenASeriesClaimsIt(t *testing.T) {
	db := openMigratedTestDB(t)
	if _, err := db.Exec(`INSERT INTO work(id,primary_code,title,description)
		VALUES(1,'RJ00000002','Rain Work','A rainy night')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO content_translation(
		cache_key,entity_kind,entity_id,field,language,source_text,translated_text,origin)
		VALUES('work-title','work',1,'title',?,'Rain Work','雨作品','native')`, translation.SourceHans); err != nil {
		t.Fatal(err)
	}
	arm := library.ShortSearchArm{EntityKind: "work", Fields: []string{"title", "description"}}
	if ids := shortQueryIDs(t, db, arm, "雨", 10); len(ids) != 1 {
		t.Fatalf("local work hits=%v, want one row", ids)
	}
	sourceID := seedScopeSource(t, db)
	if _, err := db.Exec(`INSERT INTO catalog_series(id,title,source_id,work_id) VALUES(1,'Rain Series',?,1)`, sourceID); err != nil {
		t.Fatal(err)
	}
	if ids := shortQueryIDs(t, db, arm, "雨", 10); len(ids) != 0 {
		t.Fatalf("work claimed by a series hits=%v, want none", ids)
	}
}

func TestContentTranslationScopeAfterPagesEveryRowOnce(t *testing.T) {
	db := openMigratedTestDB(t)
	sourceID := seedScopeSource(t, db)
	const total = 120
	seedScopeEntry(t, db, sourceID, 1, "Paged", 1)
	for index := 1; index <= total; index++ {
		if _, err := db.Exec(`INSERT INTO content_translation(
			cache_key,entity_kind,entity_id,field,language,source_text,translated_text,origin)
			VALUES(?, 'entry', 1, 'title', ?, 'Paged', ?, 'native')`,
			"paged-"+strconv.Itoa(index), translation.SourceHans, "雨"+strconv.Itoa(index)); err != nil {
			t.Fatalf("seed paged row %d: %v", index, err)
		}
	}
	refreshScopeAdmission(t, db, sourceID)

	arm := library.ShortSearchArm{EntityKind: "entry", Fields: []string{"title"}, SourceID: sourceID}
	seen := map[int64]bool{}
	var after int64
	for page := 0; page < 10; page++ {
		arm.After = after
		ids := shortQueryIDs(t, db, arm, "雨", 50)
		if len(ids) == 0 {
			break
		}
		for _, id := range ids {
			if seen[id] {
				t.Fatalf("row %d returned twice", id)
			}
			seen[id] = true
		}
		after = ids[len(ids)-1]
	}
	if len(seen) != total {
		t.Fatalf("paged rows=%d, want %d", len(seen), total)
	}
}

func TestContentTranslationScopeThresholdsMatchAdmission(t *testing.T) {
	db := openMigratedTestDB(t)
	sourceID := seedScopeSource(t, db)
	// The view stores the probe bounds as literals because SQLite has no
	// constants. Exercise the boundary of each rule instead of searching for
	// the digits, so a changed comparison or a changed Go threshold fails.
	cases := []struct {
		name     string
		id       int64
		audio    int
		nonAudio int
		want     bool
	}{
		{name: "no_audio", id: 1, audio: 0, nonAudio: 0, want: false},
		{name: "one_audio", id: 2, audio: 1, nonAudio: 0, want: true},
		{name: "files_at_limit", id: 3, audio: catalogadmission.ProbeAudio, nonAudio: catalogadmission.ProbeFiles - catalogadmission.ProbeAudio, want: true},
		{name: "files_over_limit", id: 4, audio: catalogadmission.ProbeAudio, nonAudio: catalogadmission.ProbeFiles - catalogadmission.ProbeAudio + 1, want: false},
		{name: "non_audio_at_limit", id: 5, audio: catalogadmission.ProbeAudio, nonAudio: catalogadmission.ProbeNonAudio, want: true},
		{name: "non_audio_over_limit", id: 6, audio: catalogadmission.ProbeAudio - 1, nonAudio: catalogadmission.ProbeNonAudio + 1, want: false},
		{name: "non_audio_with_enough_audio", id: 7, audio: catalogadmission.ProbeAudio, nonAudio: catalogadmission.ProbeNonAudio + 1, want: true},
	}
	for _, item := range cases {
		seedScopeEntry(t, db, sourceID, item.id, "Card "+strconv.FormatInt(item.id, 10), item.audio)
		if item.nonAudio > 0 {
			if _, err := db.Exec(`INSERT INTO source_catalog_file(
				catalog_entry_id,relative_path,title,kind,size_bytes,observed_generation)
				SELECT ?, 'non-audio-' || value, 'cover', 'other', 1, 1
				FROM (WITH RECURSIVE n(value) AS (SELECT 1 UNION ALL SELECT value+1 FROM n WHERE value < ?)
					SELECT value FROM n)`, item.id, item.nonAudio); err != nil {
				t.Fatalf("%s: seed non-audio files: %v", item.name, err)
			}
		}
	}
	refreshScopeAdmission(t, db, sourceID)
	for _, item := range cases {
		var searchable int
		if err := db.QueryRow(`SELECT searchable FROM catalog_entry_searchable WHERE catalog_entry_id=?`, item.id).Scan(&searchable); err != nil {
			t.Fatalf("%s: read view: %v", item.name, err)
		}
		if got := searchable == 1; got != item.want {
			t.Fatalf("%s: searchable=%v, want %v", item.name, got, item.want)
		}
	}
}

// A short query must stay inside one covering index range: no per-candidate
// join to source_catalog_entry, and no table lookup for the returned columns.
func TestContentTranslationScopeShortQueryUsesCoveringIndex(t *testing.T) {
	db := openMigratedTestDB(t)
	sourceID := seedScopeSource(t, db)
	seedScopeEntry(t, db, sourceID, 1, "Rain", 1)
	seedScopeTranslation(t, db, "entry", 1, "Rain", "雨")
	refreshScopeAdmission(t, db, sourceID)
	if _, err := db.Exec(`ANALYZE`); err != nil {
		t.Fatal(err)
	}

	arm := library.ShortSearchArm{EntityKind: "entry", Fields: []string{"title"}, SourceID: sourceID}
	query, args := library.ShortTranslationSearchSQL(
		[]library.DisplaySearchTarget{{Language: translation.SourceHans, Query: "雨"}},
		[]library.ShortSearchArm{arm}, 0)
	if query == "" {
		t.Fatal("short query is empty")
	}
	plan := shortQueryPlan(t, db, query, args)
	joined := strings.Join(plan, "\n")
	if !strings.Contains(joined, "COVERING INDEX idx_content_translation_short_scan") {
		t.Fatalf("short query does not use the covering scan index:\n%s", joined)
	}
	if strings.Contains(joined, "source_catalog_entry") {
		t.Fatalf("short query joins the entity row:\n%s", joined)
	}
	if strings.Contains(joined, "USE TEMP B-TREE") {
		t.Fatalf("short query sorts its whole range:\n%s", joined)
	}
}

// The local library searches works, and a work row has no source. Naming that
// constant lets the same covering index answer the order without sorting the
// whole language range.
func TestContentTranslationScopeWorkShortQueryUsesCoveringIndex(t *testing.T) {
	db := openMigratedTestDB(t)
	if _, err := db.Exec(`INSERT INTO work(id,primary_code,title,description) VALUES(1,'RJ00000001','Rain','A quiet night')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO content_translation(
		cache_key,entity_kind,entity_id,field,language,source_text,translated_text,origin)
		VALUES('work-title','work',1,'title',?,'Rain','雨','native'),
		      ('work-desc','work',1,'description',?,'A quiet night','安静的夜','native')`,
		translation.SourceHans, translation.SourceHans); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE content_translation SET browsable=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ANALYZE`); err != nil {
		t.Fatal(err)
	}
	arm := library.ShortSearchArm{EntityKind: "work", Fields: []string{"title", "description"}}
	query, args := library.ShortTranslationSearchSQL(
		[]library.DisplaySearchTarget{{Language: translation.SourceHans, Query: "雨"}},
		[]library.ShortSearchArm{arm}, 1000)
	if query == "" {
		t.Fatal("short query is empty")
	}
	plan := strings.Join(shortQueryPlan(t, db, query, args), "\n")
	if !strings.Contains(plan, "COVERING INDEX idx_content_translation_short_scan") {
		t.Fatalf("work short query does not use the covering scan index:\n%s", plan)
	}
	if strings.Contains(plan, "USE TEMP B-TREE") {
		t.Fatalf("work short query sorts its whole range:\n%s", plan)
	}
}

// A staged split hides its members until the parent publishes. The stored
// scope must follow that gate, or a 1-2 character search would return a card
// that every other read refuses.
func TestContentTranslationScopeFollowsEpisodeStaging(t *testing.T) {
	db := openMigratedTestDB(t)
	sourceID := seedScopeSource(t, db)
	seedScopeEntry(t, db, sourceID, 1, "Rain", 1)
	seedScopeTranslation(t, db, "entry", 1, "Rain", "雨")
	refreshScopeAdmission(t, db, sourceID)
	if _, browsable := shortQueryScope(t, db, "entry", 1); browsable != 1 {
		t.Fatalf("published card browsable=%d, want 1", browsable)
	}
	if _, err := db.Exec(`INSERT INTO source_catalog_episode_pending(
		source_id,parent_root,catalog_entry_root,catalog_entry_id)
		VALUES(?,'/Creator','/Creator/Rain',1)`, sourceID); err != nil {
		t.Fatalf("stage entry: %v", err)
	}
	if _, browsable := shortQueryScope(t, db, "entry", 1); browsable != 0 {
		t.Fatalf("staged card browsable=%d, want 0", browsable)
	}
	if _, err := db.Exec(`INSERT INTO source_catalog_episode_publication(
		source_id,parent_root,state) VALUES(?,'/Creator','published')`, sourceID); err != nil {
		t.Fatalf("publish split: %v", err)
	}
	if _, browsable := shortQueryScope(t, db, "entry", 1); browsable != 1 {
		t.Fatalf("published split browsable=%d, want 1", browsable)
	}
	if _, err := db.Exec(`DELETE FROM source_catalog_episode_pending
		WHERE source_id=? AND parent_root='/Creator'`, sourceID); err != nil {
		t.Fatalf("clear staging: %v", err)
	}
	if _, browsable := shortQueryScope(t, db, "entry", 1); browsable != 1 {
		t.Fatalf("unstaged card browsable=%d, want 1", browsable)
	}
}

// A series answers a search through its members, so a member file change must
// clear the series row too; otherwise a series with no playable member would
// keep matching.
func TestContentTranslationScopeSeriesFollowsMemberFiles(t *testing.T) {
	db := openMigratedTestDB(t)
	sourceID := seedScopeSource(t, db)
	seedScopeEntry(t, db, sourceID, 1, "Rain", 1)
	seedScopeEntry(t, db, sourceID, 2, "Rain Two", 1)
	if _, err := db.Exec(`INSERT INTO catalog_series(id,title,source_id) VALUES(1,'Rain Series',?)`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence)
		VALUES(1,1,1,'directory'),(1,2,2,'directory')`); err != nil {
		t.Fatal(err)
	}
	seedScopeTranslation(t, db, "series", 1, "Rain Series", "雨系列")
	refreshScopeAdmission(t, db, sourceID)
	if _, browsable := shortQueryScope(t, db, "series", 1); browsable != 1 {
		t.Fatalf("series with two admitted members browsable=%d, want 1", browsable)
	}
	// Removing the last playable member clears the series row in the same
	// transaction as the file delete.
	if _, err := db.Exec(`DELETE FROM source_catalog_file WHERE catalog_entry_id IN (1,2)`); err != nil {
		t.Fatal(err)
	}
	if _, browsable := shortQueryScope(t, db, "series", 1); browsable != 0 {
		t.Fatalf("series with no playable member browsable=%d, want 0", browsable)
	}
	// Publishing a file again restores both the entry and the series.
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.Exec(`INSERT INTO source_catalog_file(
		catalog_entry_id,relative_path,title,kind,size_bytes,observed_generation)
		VALUES(1,'01.mp3','01.mp3','audio',1,1)`); err != nil {
		t.Fatal(err)
	}
	if err = refreshContentTranslationBrowsableTx(context.Background(), tx, []int64{1}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, browsable := shortQueryScope(t, db, "series", 1); browsable != 1 {
		t.Fatalf("restored series browsable=%d, want 1", browsable)
	}
}

// Folding two series moves members between them. Both the series a member
// leaves and the one it joins change their answer, so a move must recompute
// both instead of leaving the emptied series searchable.
func TestContentTranslationScopeSeriesFollowsMemberMove(t *testing.T) {
	db := openMigratedTestDB(t)
	sourceID := seedScopeSource(t, db)
	seedScopeEntry(t, db, sourceID, 1, "Episode One", 1)
	seedScopeEntry(t, db, sourceID, 2, "Episode Two", 1)
	seedScopeEntry(t, db, sourceID, 3, "Episode Three", 1)
	for _, series := range []struct {
		id    int64
		title string
	}{{1, "Rain Series"}, {2, "Rain Series Two"}} {
		if _, err := db.Exec(`INSERT INTO catalog_series(id,title,source_id) VALUES(?,?,?)`, series.id, series.title, sourceID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence)
		VALUES(1,1,1,'directory'),(1,2,2,'directory'),(2,3,1,'directory')`); err != nil {
		t.Fatal(err)
	}
	seedScopeTranslation(t, db, "series", 1, "Rain Series", "雨系列")
	seedScopeTranslation(t, db, "series", 2, "Rain Series Two", "雨系列二")
	refreshScopeAdmission(t, db, sourceID)
	if _, browsable := shortQueryScope(t, db, "series", 1); browsable != 1 {
		t.Fatalf("series one browsable=%d, want 1", browsable)
	}
	if _, browsable := shortQueryScope(t, db, "series", 2); browsable != 0 {
		t.Fatalf("single-member series browsable=%d, want 0", browsable)
	}
	// Moving one member out leaves series one with a single episode, so it
	// stops being a card; series two reaches the two-episode minimum.
	if _, err := db.Exec(`UPDATE catalog_series_member SET series_id=2,position=2 WHERE catalog_entry_id=2`); err != nil {
		t.Fatal(err)
	}
	if _, browsable := shortQueryScope(t, db, "series", 1); browsable != 0 {
		t.Fatalf("emptied series browsable=%d, want 0", browsable)
	}
	if _, browsable := shortQueryScope(t, db, "series", 2); browsable != 1 {
		t.Fatalf("grown series browsable=%d, want 1", browsable)
	}
}
