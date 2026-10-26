package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yexca/kikoto/backend/internal/config"
	"github.com/yexca/kikoto/backend/internal/library"
	"github.com/yexca/kikoto/backend/internal/testfixture"
)

func explainCatalogPlan(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	rows, err := db.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatalf("explain: %v\n%s", err, query)
	}
	defer func() { _ = rows.Close() }()
	var plan strings.Builder
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err = rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(detail)
		plan.WriteString("\n")
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return plan.String()
}

func seedCatalogPlanSource(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO file_source(id,code,display_name,source_type) VALUES(7,'plan_source','Plan Source','openlist');
		INSERT INTO file_source_endpoint(file_source_id,api_url,base_url) VALUES(7,'https://source.example.invalid','https://source.example.invalid');`)
	if err != nil {
		t.Fatal(err)
	}
}

func insertCatalogPlanEntry(t *testing.T, db *sql.DB, id int64, title, code string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,primary_code)
		VALUES(?,7,?,?,?)`, id, fmt.Sprintf("/work/%d", id), title, code)
	if err != nil {
		t.Fatal(err)
	}
}

func insertCatalogPlanAudio(t *testing.T, db *sql.DB, entryID int64, relative, title string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes)
		VALUES(?,?,?,'audio',128)`, entryID, relative, title)
	if err != nil {
		t.Fatal(err)
	}
}

func TestOpenListCatalogBrowseSinglePassPlan(t *testing.T) {
	db := openMigratedTestDB(t)
	seedCatalogPlanSource(t, db)
	for index := 1; index <= 6; index++ {
		id := int64(index)
		insertCatalogPlanEntry(t, db, id, fmt.Sprintf("Work %d", index), testfixture.WorkCode(testfixture.PrefixRJ, index))
		insertCatalogPlanAudio(t, db, id, "track.mp3", fmt.Sprintf("Track %d", index))
	}
	if _, err := db.Exec(`UPDATE source_catalog_entry SET updated_at=? WHERE id=?`, "2026-01-01 00:00:00", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ANALYZE`); err != nil {
		t.Fatal(err)
	}

	prefix, from, args := openListCatalogCardsQuery(7, "")
	pageSQL := catalogCardPageSQL(prefix, from, false)
	plan := explainCatalogPlan(t, db, pageSQL, append(args, 2, 0)...)
	t.Logf("empty browse plan:\n%s", plan)
	assertNoCatalogFileTableScan(t, plan)
	if strings.Count(plan, "SEARCH e USING INDEX source_catalog_entry_live_root") != 1 {
		t.Fatalf("browse does not read the entry source exactly once: %s", plan)
	}

	type card struct {
		id    int64
		total int
	}
	readPage := func(page, size int) []card {
		rows, err := db.Query(pageSQL, append(args, size, (page-1)*size)...)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		cards := []card{}
		for rows.Next() {
			var item card
			var seriesID int64
			var code, title, updated, availability string
			var workID sql.NullInt64
			var exact int
			var hasAudio bool
			if err = rows.Scan(&item.id, &seriesID, &code, &title, &workID, &updated, &availability, &exact, &hasAudio, &item.total); err != nil {
				t.Fatal(err)
			}
			cards = append(cards, item)
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		return cards
	}
	first := readPage(1, 2)
	second := readPage(2, 2)
	third := readPage(3, 2)
	if len(first) != 2 || len(second) != 2 || len(third) != 2 {
		t.Fatalf("page sizes = %d/%d/%d", len(first), len(second), len(third))
	}
	seen := map[int64]bool{}
	for _, page := range [][]card{first, second, third} {
		for _, item := range page {
			if item.total != 6 {
				t.Fatalf("page total = %d, want 6", item.total)
			}
			if seen[item.id] {
				t.Fatalf("card %d appeared on two pages", item.id)
			}
			seen[item.id] = true
		}
	}
	if len(seen) != 6 {
		t.Fatalf("distinct cards = %d, want 6", len(seen))
	}
}

func TestOpenListCatalogSearchFTSCoverage(t *testing.T) {
	db := openMigratedTestDB(t)
	seedCatalogPlanSource(t, db)
	insertCatalogPlanEntry(t, db, 1, "Alpha Numberless", "")
	insertCatalogPlanAudio(t, db, 1, "01 opening.mp3", "Opening Track")
	insertCatalogPlanEntry(t, db, 2, "Second Work", "RJ00000001")
	insertCatalogPlanAudio(t, db, 2, "hidden.mp3", "Hidden File Name")
	insertCatalogPlanEntry(t, db, 3, "Episode One", "")
	insertCatalogPlanAudio(t, db, 3, "e1.mp3", "Episode One Track")
	insertCatalogPlanEntry(t, db, 4, "Episode Two", "")
	insertCatalogPlanAudio(t, db, 4, "e2.mp3", "Episode Two Track")
	if _, err := db.Exec(`INSERT INTO catalog_series(id,title,source_id) VALUES(1,'Beta Collection',7);
		INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence) VALUES(1,3,1,'manual'),(1,4,2,'manual');`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ANALYZE`); err != nil {
		t.Fatal(err)
	}

	prefix, from, args := openListCatalogCardsQuery(7, "Hidden")
	searchSQL := catalogCardPageSQL(prefix, from, false)
	plan := explainCatalogPlan(t, db, searchSQL, append(append([]any{}, args...), 24, 0)...)
	t.Logf("search plan:\n%s", plan)
	assertNoCatalogFileTableScan(t, plan)
	if !strings.Contains(plan, "VIRTUAL TABLE") {
		t.Fatalf("search does not use the FTS table: %s", plan)
	}

	readIDs := func(query string) []string {
		prefix, from, args := openListCatalogCardsQuery(7, query)
		rows, err := db.Query(catalogStatementPrefix(prefix)+`SELECT id,series_id,COUNT(*) OVER () AS total `+from+` ORDER BY updated_at DESC,series_id DESC,id DESC LIMIT 24`, args...)
		if err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		defer func() { _ = rows.Close() }()
		ids := []string{}
		for rows.Next() {
			var id, seriesID int64
			var total int
			if err = rows.Scan(&id, &seriesID, &total); err != nil {
				t.Fatal(err)
			}
			if seriesID > 0 {
				ids = append(ids, fmt.Sprintf("series:%d", seriesID))
			} else {
				ids = append(ids, fmt.Sprintf("%d", id))
			}
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		return ids
	}
	cases := []struct {
		query string
		want  string
	}{
		{"Alpha", "1"},
		{"RJ00000001", "2"},
		{"Hidden", "2"},
		{"Beta", "series:1"},
	}
	for _, testCase := range cases {
		got := readIDs(testCase.query)
		if len(got) != 1 || got[0] != testCase.want {
			t.Fatalf("search %q = %v, want [%s]", testCase.query, got, testCase.want)
		}
	}
}

func TestOpenListCatalogTracksDepthLimitAndSummary(t *testing.T) {
	db := openMigratedTestDB(t)
	seedCatalogPlanSource(t, db)
	insertCatalogPlanEntry(t, db, 60, "Tree Work", "")
	files := []struct{ path, title, kind string }{
		{"01.mp3", "01", "audio"},
		{"A", "A", "folder"},
		{"A/02.mp3", "02", "audio"},
		{"A/B", "B", "folder"},
		{"A/B/03.mp3", "03", "audio"},
	}
	for _, file := range files {
		if _, err := db.Exec(`INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES(60,?,?,?,10)`, file.path, file.title, file.kind); err != nil {
			t.Fatal(err)
		}
	}
	server := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	defer func() { _ = server.Shutdown(context.Background()) }()

	tracks := func(id int64, target string) ([]map[string]any, map[string]any) {
		request := httptest.NewRequest("GET", target, nil)
		request.SetPathValue("id", fmt.Sprintf("%d", id))
		response := httptest.NewRecorder()
		server.getSourceCatalogTracks(response, request)
		if response.Code != 200 {
			t.Fatalf("%s: %d %s", target, response.Code, response.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		raw, _ := payload["tracks"].([]any)
		nodes := make([]map[string]any, 0, len(raw))
		for _, item := range raw {
			nodes = append(nodes, item.(map[string]any))
		}
		return nodes, payload
	}

	all, _ := tracks(60, "/api/source-catalog/60/tracks")
	if len(all) != 2 {
		t.Fatalf("full tree roots = %d, want 2: %#v", len(all), all)
	}
	level, _ := tracks(60, "/api/source-catalog/60/tracks?depth=1")
	if len(level) != 2 {
		t.Fatalf("depth=1 roots = %d, want 2", len(level))
	}
	folder := level[1]
	if folder["title"] != "A" || len(folder["children"].([]any)) != 0 {
		t.Fatalf("depth=1 folder leaked children: %#v", folder)
	}
	scoped, payload := tracks(60, "/api/source-catalog/60/tracks?path=A&depth=2")
	if payload["total"] != float64(2) {
		t.Fatalf("scoped total = %v, want 2", payload["total"])
	}
	if len(scoped) != 2 {
		t.Fatalf("scoped roots = %d, want 2", len(scoped))
	}
	if scoped[1]["title"] != "B" || len(scoped[1]["children"].([]any)) != 1 {
		t.Fatalf("depth=2 folder = %#v", scoped[1])
	}
	if _, err := db.Exec(`INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,primary_code) VALUES(61,7,'/work/61','Limit Work','')`); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 10; index++ {
		insertCatalogPlanAudio(t, db, 61, fmt.Sprintf("track-%02d.mp3", index), fmt.Sprintf("Track %d", index))
	}
	limited, limitedPayload := tracks(61, "/api/source-catalog/61/tracks?depth=1&limit=3")
	if len(limited) != 3 || limitedPayload["total"] != float64(10) || limitedPayload["truncated"] != true {
		t.Fatalf("limited page = %#v", limitedPayload)
	}
}

func TestOpenListCatalogTracksLargeCardSummary(t *testing.T) {
	db := openMigratedTestDB(t)
	seedCatalogPlanSource(t, db)
	insertCatalogPlanEntry(t, db, 62, "Large Work", "")
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	statement, err := tx.Prepare(`INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES(62,?,?,'audio',10)`)
	if err != nil {
		t.Fatal(err)
	}
	// Admission caps a card at 5,000 files, so the summary has to be proven on a
	// card that can actually exist.
	for index := 0; index < 4900; index++ {
		if _, err = statement.Exec(fmt.Sprintf("track-%05d.mp3", index), fmt.Sprintf("Track %d", index)); err != nil {
			t.Fatal(err)
		}
	}
	if err = statement.Close(); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	server := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	defer func() { _ = server.Shutdown(context.Background()) }()
	request := httptest.NewRequest("GET", "/api/source-catalog/62/tracks?depth=1", nil)
	request.SetPathValue("id", "62")
	response := httptest.NewRecorder()
	server.getSourceCatalogTracks(response, request)
	if response.Code != 200 {
		t.Fatalf("large card: %d %s", response.Code, response.Body.String())
	}
	if response.Body.Len() >= 200<<10 {
		t.Fatalf("large card response = %d bytes, want < 200 KiB", response.Body.Len())
	}
	var payload map[string]any
	if err = json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["total"] != float64(4900) || payload["summary"] != true || payload["truncated"] != true {
		t.Fatalf("large card summary = %#v", payload)
	}
	if len(payload["tracks"].([]any)) != catalogTrackSummaryPreview {
		t.Fatalf("preview tracks = %d, want %d", len(payload["tracks"].([]any)), catalogTrackSummaryPreview)
	}
	plan := explainCatalogPlan(t, db, `SELECT relative_path,title,kind,size_bytes,modified_at,observed_generation
		FROM source_catalog_file WHERE catalog_entry_id=? AND parent_path=? ORDER BY relative_path LIMIT ?`, 62, "", 201)
	t.Logf("track level plan:\n%s", plan)
	assertNoCatalogFileTableScan(t, plan)
}

// A display-language page joins the card source to a translated hit set. The
// hit set already names the entries, so every entry read must be a primary-key
// probe: the live-root index must not be entered at all, and file rows must not
// be scanned.
func TestOpenListCatalogLanguageSearchPlan(t *testing.T) {
	db := openMigratedTestDB(t)
	seedCatalogPlanSource(t, db)
	ids := []int64{}
	for index := 1; index <= 6; index++ {
		id := int64(index)
		ids = append(ids, id)
		insertCatalogPlanEntry(t, db, id, fmt.Sprintf("Original %d", index), "")
		insertCatalogPlanAudio(t, db, id, "track.mp3", fmt.Sprintf("Track %d", index))
		insertTranslatedContent(t, db, "entry", id, "title", "zh-Hans", fmt.Sprintf("Original %d", index), fmt.Sprintf("作品%d", index), "model")
	}
	if _, err := db.Exec(`ANALYZE`); err != nil {
		t.Fatal(err)
	}
	prefix, from, args := openListCatalogCardsQueryForEntries(7, ids)
	pageSQL := catalogCardPageSQL(prefix, from, false)
	plan := explainCatalogPlan(t, db, pageSQL, append(append([]any{}, args...), 2, 0)...)
	t.Logf("language search plan:\n%s", plan)
	assertNoCatalogFileTableScan(t, plan)
	if strings.Contains(plan, "source_catalog_entry_live_root") {
		t.Fatalf("language search re-enters the live-root index instead of probing matched ids: %s", plan)
	}
	if !strings.Contains(plan, "SEARCH e USING INTEGER PRIMARY KEY (rowid=?)") {
		t.Fatalf("language search does not probe entries by rowid: %s", plan)
	}
	var total, rows int
	if err := db.QueryRow(pageSQL, append(append([]any{}, args...), 2, 0)...).Scan(new(int64), new(int64), new(string), new(string), new(sql.NullInt64), new(string), new(string), new(int64), new(bool), &total); err != nil {
		t.Fatalf("run language page: %v", err)
	}
	rows = 1
	if total != 6 || rows != 1 {
		t.Fatalf("language page total=%d rows=%d, want 6/1", total, rows)
	}
}

// The 1-2 character translated fallback walks bounded id ranges on the
// language index. It must not scan the translation table in one statement.
func TestOpenListCatalogShortTranslationSearchPlan(t *testing.T) {
	db := openMigratedTestDB(t)
	seedCatalogPlanSource(t, db)
	for index := 1; index <= 6; index++ {
		id := int64(index)
		title := fmt.Sprintf("Original %d", index)
		insertCatalogPlanEntry(t, db, id, title, "")
		insertCatalogPlanAudio(t, db, id, "track.mp3", fmt.Sprintf("Track %d", index))
		insertTranslatedContent(t, db, "entry", id, "title", "zh-Hans", title, fmt.Sprintf("作品%d", index), "model")
	}
	targets, ok := library.DisplaySearchTargets("zh-cn", "夜")
	if !ok {
		t.Fatal("zh-cn is not a display language")
	}
	prefix, from, args := openListCatalogCardsQueryLocalized(7, "夜", targets, "config-key")
	plan := explainCatalogPlan(t, db, prefix+`SELECT count(*) `+from, args...)
	t.Logf("short translated search plan:\n%s", plan)
	if strings.Contains(plan, "SCAN content_translation\n") {
		t.Fatalf("short translated search scans the whole table: %s", plan)
	}
	if !strings.Contains(plan, "idx_content_translation_short_scan") {
		t.Fatalf("short translated search does not use the short covering index: %s", plan)
	}
}

// A keyword search must keep the same "entry source read once" property as an
// empty browse: the series membership probe looks entries up by rowid, so the
// live-root index is not re-entered once per series.
func TestOpenListCatalogSearchSingleEntryScan(t *testing.T) {
	db := openMigratedTestDB(t)
	seedCatalogPlanSource(t, db)
	for index := 1; index <= 6; index++ {
		insertCatalogPlanEntry(t, db, int64(index), fmt.Sprintf("Episode %d", index), "")
		insertCatalogPlanAudio(t, db, int64(index), "track.mp3", fmt.Sprintf("Track %d", index))
	}
	if _, err := db.Exec(`INSERT INTO catalog_series(id,title,source_id) VALUES(1,'Beta Collection',7);
		INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence) VALUES(1,1,1,'manual'),(1,2,2,'manual');`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ANALYZE`); err != nil {
		t.Fatal(err)
	}

	for _, query := range []string{"Episode 1", "Ep"} {
		t.Run(query, func(t *testing.T) {
			prefix, from, args := openListCatalogCardsQuery(7, query)
			searchSQL := catalogCardPageSQL(prefix, from, false)
			plan := explainCatalogPlan(t, db, searchSQL, append(append([]any{}, args...), 24, 0)...)
			t.Logf("%q plan:\n%s", query, plan)
			// A keyword search drives the pipeline from its hit set, so the
			// source is never scanned; each candidate is a rowid probe. The
			// empty browse above keeps the single live-root read.
			if strings.Contains(plan, "SCAN e USING INDEX source_catalog_entry_live_root") {
				t.Fatalf("query %q scans the entry source: %s", query, plan)
			}
			if query == "Episode 1" {
				if count := strings.Count(plan, "source_catalog_entry_live_root"); count != 0 {
					t.Fatalf("query %q reads the live-root index %d times, want 0: %s", query, count, plan)
				}
			}
			if !strings.Contains(plan, "SEARCH e USING INTEGER PRIMARY KEY (rowid=?)") {
				t.Fatalf("query %q does not probe entries by rowid: %s", query, plan)
			}
		})
	}
}
