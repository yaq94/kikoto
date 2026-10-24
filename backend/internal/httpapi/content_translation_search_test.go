package httpapi

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/yexca/kikoto/backend/internal/config"
	"github.com/yexca/kikoto/backend/internal/translation"
)

func insertTranslatedContent(t *testing.T, db *sql.DB, kind string, id int64, field, language, sourceText, text, origin string) {
	t.Helper()
	key := fmt.Sprintf("%s|%d|%s|%s|%s|%s", kind, id, field, language, origin, text)
	model, template := "", ""
	if origin == "model" {
		model = translation.DefaultModel
		template = translation.TemplateVersion
	}
	if _, err := db.Exec(`INSERT INTO content_translation(
		cache_key, entity_kind, entity_id, field, language, source_text, translated_text, origin, model, template_version)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, key, kind, id, field, language, sourceText, text, origin, model, template); err != nil {
		t.Fatalf("insert translation %s/%d/%s: %v", kind, id, language, err)
	}
}

func requestRemoteWorks(t *testing.T, db *sql.DB, query string) remoteWorksResponse {
	t.Helper()
	server := NewServer(db, config.Config{})
	request := httptest.NewRequest(http.MethodGet, "/api/remote-sources/7/works?"+query, nil)
	recorder := httptest.NewRecorder()
	server.listOpenListWorks(recorder, request, remoteSourceForUse{ID: 7, Enabled: true})
	if recorder.Code != http.StatusOK {
		t.Fatalf("remote works status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response remoteWorksResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func remoteWorkIDs(response remoteWorksResponse) []int64 {
	ids := make([]int64, 0, len(response.Works))
	for _, work := range response.Works {
		if work.SeriesID > 0 {
			ids = append(ids, -work.SeriesID)
			continue
		}
		ids = append(ids, work.CatalogEntryID)
	}
	return ids
}

// A display language searches only rows in that language. Original-only text is
// not a substitute for a missing translation.
func TestContentTranslationSearchRemoteStrictTargetLanguage(t *testing.T) {
	db := openMigratedTestDB(t)
	seedCatalogPlanSource(t, db)
	insertCatalogPlanEntry(t, db, 1, "Yoru no Monogatari", "")
	insertCatalogPlanAudio(t, db, 1, "01.mp3", "rain")
	insertCatalogPlanEntry(t, db, 2, "雨夜回想", "")
	insertCatalogPlanAudio(t, db, 2, "01.mp3", "rain")
	insertCatalogPlanEntry(t, db, 3, "雨夜の物語", "RJ00000003")
	insertCatalogPlanAudio(t, db, 3, "01.mp3", "rain")
	insertCatalogPlanEntry(t, db, 4, "雨夜戀歌", "")
	insertCatalogPlanAudio(t, db, 4, "01.mp3", "rain")
	insertCatalogPlanEntry(t, db, 5, "Hare no Hi", "")
	insertCatalogPlanAudio(t, db, 5, "01.mp3", "sun")

	insertTranslatedContent(t, db, "entry", 1, "title", "zh-Hans", "Yoru no Monogatari", "雨夜的故事", "model")
	insertTranslatedContent(t, db, "entry", 2, "title", "zh-Hans", "雨夜回想", "雨夜回想", "native")
	insertTranslatedContent(t, db, "entry", 3, "title", "ja", "雨夜の物語", "雨夜の物語", "native")
	insertTranslatedContent(t, db, "entry", 4, "title", "zh-Hant", "雨夜戀歌", "雨夜戀歌", "native")
	insertTranslatedContent(t, db, "entry", 5, "title", "zh-Hans", "Hare no Hi", "晴天", "model")

	simplified := requestRemoteWorks(t, db, "q="+url.QueryEscape("雨夜")+"&lang=zh-cn&pageSize=24")
	if simplified.Total != 2 {
		t.Fatalf("zh-cn total = %d, want 2: %#v", simplified.Total, remoteWorkIDs(simplified))
	}
	got := remoteWorkIDs(simplified)
	if len(got) != 2 || got[0] != 2 || got[1] != 1 {
		t.Fatalf("zh-cn ids = %v, want [2 1] (updated desc, id desc)", got)
	}

	fts := requestRemoteWorks(t, db, "q="+url.QueryEscape("雨夜的故事")+"&lang=zh-cn&pageSize=24")
	if fts.Total != 1 || len(fts.Works) != 1 || fts.Works[0].CatalogEntryID != 1 {
		t.Fatalf("zh-cn fts = %d %v, want entry 1: %#v", fts.Total, remoteWorkIDs(fts), fts)
	}
}

// origin keeps the original-title search path, including entries that have no
// target-language row.
func TestContentTranslationSearchRemoteOriginKeepsOriginalBehavior(t *testing.T) {
	db := openMigratedTestDB(t)
	seedCatalogPlanSource(t, db)
	insertCatalogPlanEntry(t, db, 1, "Yoru no Monogatari", "")
	insertCatalogPlanAudio(t, db, 1, "01.mp3", "rain")
	insertCatalogPlanEntry(t, db, 2, "雨夜回想", "")
	insertCatalogPlanAudio(t, db, 2, "01.mp3", "rain")
	insertCatalogPlanEntry(t, db, 3, "雨夜の物語", "")
	insertCatalogPlanAudio(t, db, 3, "01.mp3", "rain")
	insertCatalogPlanEntry(t, db, 4, "雨夜戀歌", "")
	insertCatalogPlanAudio(t, db, 4, "01.mp3", "rain")
	insertTranslatedContent(t, db, "entry", 1, "title", "zh-Hans", "Yoru no Monogatari", "雨夜的故事", "model")
	insertTranslatedContent(t, db, "entry", 2, "title", "zh-Hans", "雨夜回想", "雨夜回想", "native")
	insertTranslatedContent(t, db, "entry", 3, "title", "ja", "雨夜の物語", "雨夜の物語", "native")
	insertTranslatedContent(t, db, "entry", 4, "title", "zh-Hant", "雨夜戀歌", "雨夜戀歌", "native")

	response := requestRemoteWorks(t, db, "q="+url.QueryEscape("雨夜")+"&lang=origin&pageSize=24")
	got := remoteWorkIDs(response)
	if response.Total != 3 || len(got) != 3 || got[0] != 4 || got[1] != 3 || got[2] != 2 {
		t.Fatalf("origin result = total %d ids %v, want 3 and [4 3 2]", response.Total, got)
	}
}

// Search uses the same effective translation definition as coverage: the
// current source text and the configured model/template. Native rows only need
// the current source text.
func TestContentTranslationSearchRemoteIgnoresHistoricalRows(t *testing.T) {
	db := openMigratedTestDB(t)
	seedCatalogPlanSource(t, db)
	insertCatalogPlanEntry(t, db, 1, "Current title", "")
	insertCatalogPlanAudio(t, db, 1, "01.mp3", "rain")
	insertTranslatedContent(t, db, "entry", 1, "title", "zh-Hans", "Current title", "当前词", "model")
	if _, err := db.Exec(`INSERT INTO content_translation(
		cache_key,entity_kind,entity_id,field,language,source_text,translated_text,origin,model,template_version)
		VALUES('old-model','entry',1,'title','zh-Hans','Current title','旧模型词','model','old-model',?),
		      ('old-source','entry',1,'title','zh-Hans','Old title','旧原文词','model',?,?)`,
		translation.TemplateVersion, translation.DefaultModel, translation.TemplateVersion); err != nil {
		t.Fatal(err)
	}
	current := requestRemoteWorks(t, db, "q="+url.QueryEscape("当前词")+"&lang=zh-cn&pageSize=24")
	if current.Total != 1 || current.Works[0].CatalogEntryID != 1 {
		t.Fatalf("current search = %d %v", current.Total, remoteWorkIDs(current))
	}
	for _, query := range []string{"旧模型词", "旧原文词"} {
		response := requestRemoteWorks(t, db, "q="+url.QueryEscape(query)+"&lang=zh-cn&pageSize=24")
		if response.Total != 0 {
			t.Fatalf("%q returned historical rows: %v", query, remoteWorkIDs(response))
		}
	}
}

// Traditional queries must reach Simplified translations through t2s and
// native Traditional rows through the text as typed.
func TestContentTranslationSearchRemoteTraditionalMergesLanguages(t *testing.T) {
	db := openMigratedTestDB(t)
	seedCatalogPlanSource(t, db)
	insertCatalogPlanEntry(t, db, 1, "Renai Monogatari", "")
	insertCatalogPlanAudio(t, db, 1, "01.mp3", "rain")
	insertCatalogPlanEntry(t, db, 2, "戀愛小品", "")
	insertCatalogPlanAudio(t, db, 2, "01.mp3", "rain")
	insertCatalogPlanEntry(t, db, 3, "Untranslated", "")
	insertCatalogPlanAudio(t, db, 3, "01.mp3", "rain")

	insertTranslatedContent(t, db, "entry", 1, "title", "zh-Hans", "Renai Monogatari", "恋爱故事", "model")
	insertTranslatedContent(t, db, "entry", 2, "title", "zh-Hant", "戀愛小品", "戀愛小品", "native")

	response := requestRemoteWorks(t, db, "q="+url.QueryEscape("戀愛")+"&lang=zh-tw&pageSize=24")
	if response.Total != 2 {
		t.Fatalf("zh-tw total = %d, want 2: %v", response.Total, remoteWorkIDs(response))
	}
	seen := map[int64]bool{}
	for _, id := range remoteWorkIDs(response) {
		seen[id] = true
	}
	if !seen[1] || !seen[2] || seen[3] {
		t.Fatalf("zh-tw ids = %v, want entries 1 and 2 only", remoteWorkIDs(response))
	}
}

// total must agree with the cards actually returned across pages; the card
// pipeline and the count share one materialised CTE.
func TestContentTranslationSearchRemoteTotalMatchesPages(t *testing.T) {
	db := openMigratedTestDB(t)
	seedCatalogPlanSource(t, db)
	for index := int64(1); index <= 5; index++ {
		insertCatalogPlanEntry(t, db, index, fmt.Sprintf("Original %d", index), "")
		insertCatalogPlanAudio(t, db, index, "01.mp3", "rain")
		source := fmt.Sprintf("Original %d", index)
		insertTranslatedContent(t, db, "entry", index, "title", "zh-Hans", source, fmt.Sprintf("作品%d", index), "model")
	}
	seen := map[int64]bool{}
	for page := 1; page <= 3; page++ {
		response := requestRemoteWorks(t, db, fmt.Sprintf("q=%s&lang=zh-cn&pageSize=2&page=%d", url.QueryEscape("作品"), page))
		if response.Total != 5 {
			t.Fatalf("page %d total = %d, want 5", page, response.Total)
		}
		for _, id := range remoteWorkIDs(response) {
			if seen[id] {
				t.Fatalf("card %d appeared on two pages", id)
			}
			seen[id] = true
		}
	}
	if len(seen) != 5 {
		t.Fatalf("distinct cards = %d, want 5", len(seen))
	}
	out := requestRemoteWorks(t, db, fmt.Sprintf("q=%s&lang=zh-cn&pageSize=2&page=9", url.QueryEscape("作品")))
	if out.Total != 5 || len(out.Works) != 0 {
		t.Fatalf("out-of-range page = total %d rows %d, want 5/0", out.Total, len(out.Works))
	}
}

func requestLibraryWorks(t *testing.T, db *sql.DB, query string) struct {
	Works []libraryWorkSummary `json:"works"`
	Total int                  `json:"total"`
} {
	t.Helper()
	server := NewServer(db, config.Config{})
	request := httptest.NewRequest(http.MethodGet, "/api/works?"+query, nil)
	recorder := httptest.NewRecorder()
	server.listWorks(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("library works status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Works []libraryWorkSummary `json:"works"`
		Total int                  `json:"total"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func TestContentTranslationSearchLibraryLanguageFilter(t *testing.T) {
	db := openMigratedTestDB(t)
	workIDs := []int64{}
	for index, title := range []string{"Original Alpha", "Original 中文 Beta", "Original Gamma"} {
		result, err := db.Exec("INSERT INTO work (primary_code, title) VALUES (?, ?)", fmt.Sprintf("RJ000000%02d", index+1), title)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := result.LastInsertId()
		workIDs = append(workIDs, id)
	}
	insertTranslatedContent(t, db, "work", workIDs[0], "title", "zh-Hans", "Original Alpha", "中文译文", "model")
	insertTranslatedContent(t, db, "work", workIDs[1], "title", "zh-Hans", "Original 中文 Beta", "原生中文", "native")
	insertTranslatedContent(t, db, "work", workIDs[2], "title", "en", "Original Gamma", "English Only", "native")

	display := requestLibraryWorks(t, db, "q="+url.QueryEscape("中文")+"&lang=zh-cn")
	if display.Total != 2 {
		t.Fatalf("zh-cn total = %d, want 2: %#v", display.Total, display.Works)
	}
	for _, work := range display.Works {
		if work.ID == workIDs[2] {
			t.Fatalf("untranslated work leaked into zh-cn results")
		}
	}

	origin := requestLibraryWorks(t, db, "q="+url.QueryEscape("中文")+"&lang=origin")
	if origin.Total != 1 || len(origin.Works) != 1 || origin.Works[0].ID != workIDs[1] {
		t.Fatalf("origin total = %d rows %d, want the one source title match", origin.Total, len(origin.Works))
	}

	empty := requestLibraryWorks(t, db, "lang=zh-cn")
	if empty.Total != 3 {
		t.Fatalf("empty query total = %d, want 3", empty.Total)
	}
}

func TestContentTranslationSearchLibraryIgnoresHistoricalRows(t *testing.T) {
	db := openMigratedTestDB(t)
	result, err := db.Exec("INSERT INTO work (primary_code, title) VALUES ('RJ00000050','Current Work')")
	if err != nil {
		t.Fatal(err)
	}
	workID, _ := result.LastInsertId()
	insertTranslatedContent(t, db, "work", workID, "title", "zh-Hans", "Current Work", "当前词", "model")
	if _, err := db.Exec(`INSERT INTO content_translation(
		cache_key,entity_kind,entity_id,field,language,source_text,translated_text,origin,model,template_version)
		VALUES('work-old-model','work',?,'title','zh-Hans','Current Work','旧模型词','model','old-model',?),
		      ('work-old-source','work',?,'title','zh-Hans','Old Work','旧原文词','model',?,?)`,
		workID, translation.TemplateVersion, workID, translation.DefaultModel, translation.TemplateVersion); err != nil {
		t.Fatal(err)
	}
	current := requestLibraryWorks(t, db, "q="+url.QueryEscape("当前词")+"&lang=zh-cn")
	if current.Total != 1 || len(current.Works) != 1 || current.Works[0].ID != workID {
		t.Fatalf("current library search = %d %#v", current.Total, current.Works)
	}
	for _, query := range []string{"旧模型词", "旧原文词"} {
		response := requestLibraryWorks(t, db, "q="+url.QueryEscape(query)+"&lang=zh-cn")
		if response.Total != 0 {
			t.Fatalf("%q returned historical library rows: %#v", query, response.Works)
		}
	}
}

// An exact translated title outranks a substring hit even when the tie-break
// order (id DESC) would place it second.
func TestContentTranslationSearchRemoteExactFirst(t *testing.T) {
	db := openMigratedTestDB(t)
	seedCatalogPlanSource(t, db)
	insertCatalogPlanEntry(t, db, 1, "Exact", "")
	insertCatalogPlanAudio(t, db, 1, "01.mp3", "rain")
	insertCatalogPlanEntry(t, db, 2, "Substring", "")
	insertCatalogPlanAudio(t, db, 2, "01.mp3", "rain")
	insertTranslatedContent(t, db, "entry", 1, "title", "zh-Hans", "Exact", "雨夜", "model")
	insertTranslatedContent(t, db, "entry", 2, "title", "zh-Hans", "Substring", "雨夜物语", "model")

	response := requestRemoteWorks(t, db, "q="+url.QueryEscape("雨夜")+"&lang=zh-cn&pageSize=24")
	got := remoteWorkIDs(response)
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("exact-first order = %v, want [1 2]: %#v", got, response)
	}
}

// Exact ranking is part of display-language search and must use the same
// effective-row definition as inclusion: current source text and current
// model/template, with native rows exempt from the identity check.
func TestContentTranslationSearchRemoteExactRankUsesEffectiveRows(t *testing.T) {
	db := openMigratedTestDB(t)
	seedCatalogPlanSource(t, db)
	titles := map[int64]string{
		1: "Exact current",
		2: "Substring two",
		3: "Substring three",
		4: "Substring four",
	}
	for id, title := range titles {
		insertCatalogPlanEntry(t, db, id, title, "")
		insertCatalogPlanAudio(t, db, id, "01.mp3", "rain")
	}
	insertTranslatedContent(t, db, "entry", 1, "title", "zh-Hans", "Exact current", "夜雨", "model")
	for _, id := range []int64{2, 3, 4} {
		insertTranslatedContent(t, db, "entry", id, "title", "zh-Hans", titles[id], "夜雨物语", "model")
	}
	if _, err := db.Exec(`INSERT INTO content_translation(
		cache_key,entity_kind,entity_id,field,language,source_text,translated_text,origin,model,template_version)
		VALUES('rank-old-model','entry',3,'title','zh-Hans','Substring three','夜雨','model','old-model',?),
		      ('rank-old-source','entry',4,'title','zh-Hans','Old source','夜雨','model',?,?)`,
		translation.TemplateVersion, translation.DefaultModel, translation.TemplateVersion); err != nil {
		t.Fatal(err)
	}

	response := requestRemoteWorks(t, db, "q="+url.QueryEscape("夜雨")+"&lang=zh-cn&pageSize=24")
	got := remoteWorkIDs(response)
	want := []int64{1, 4, 3, 2}
	if len(got) != len(want) {
		t.Fatalf("exact-rank result = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("exact-rank result = %v, want %v", got, want)
		}
	}
}

func TestContentTranslationSearchRemoteDoesNotTruncateLargeMatchSet(t *testing.T) {
	db := openMigratedTestDB(t)
	seedCatalogPlanSource(t, db)
	if _, err := db.Exec(`
		WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<5001)
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,availability_status)
		SELECT i,7,'/bulk/'||i,'Original '||i,'available' FROM n;
		WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<5001)
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes)
		SELECT i,'01.mp3','01.mp3','audio',1 FROM n;
		WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<5001)
		INSERT INTO content_translation(cache_key,entity_kind,entity_id,field,language,source_text,translated_text,origin)
		SELECT 'limit-'||i,'entry',i,'title','zh-Hans','Original '||i,'作品'||i,'model' FROM n`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE content_translation SET model=?, template_version=? WHERE origin='model'`,
		translation.DefaultModel, translation.TemplateVersion); err != nil {
		t.Fatal(err)
	}
	response := requestRemoteWorks(t, db, "q="+url.QueryEscape("作品")+"&lang=zh-cn&pageSize=100&page=51")
	if response.Total != 5001 || len(response.Works) != 1 {
		t.Fatalf("bounded search = total %d rows %d, want 5001/1", response.Total, len(response.Works))
	}
}

// A one- or two-character query cannot use the trigram index. It must still
// reach every row of the effective language corpus instead of stopping at a
// newest-50,000 window. The marker row is the oldest row in the table.
func TestContentTranslationSearchRemoteShortQueryCoversRowsBeyondFiftyThousand(t *testing.T) {
	db := openMigratedTestDB(t)
	seedCatalogPlanSource(t, db)
	insertCatalogPlanEntry(t, db, 1, "甲乙丙", "")
	insertCatalogPlanAudio(t, db, 1, "01.mp3", "rain")
	if _, err := db.Exec(`
		WITH RECURSIVE n(i) AS (SELECT 2 UNION ALL SELECT i+1 FROM n WHERE i<50001)
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,availability_status)
		SELECT i,7,'/bulk/'||i,'Original '||i,'available' FROM n;
		WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<50001)
		INSERT INTO content_translation(cache_key,entity_kind,entity_id,field,language,source_text,translated_text,origin)
		SELECT 'short-marker','entry',1,'title','zh-Hans','甲乙丙','甲乙丙','native';
		WITH RECURSIVE n(i) AS (SELECT 2 UNION ALL SELECT i+1 FROM n WHERE i<50001)
		INSERT INTO content_translation(cache_key,entity_kind,entity_id,field,language,source_text,translated_text,origin)
		SELECT 'short-filler-'||i,'entry',i,'title','zh-Hans','Original '||i,'作品'||i,'model' FROM n`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE content_translation SET model=?, template_version=? WHERE origin='model'`,
		translation.DefaultModel, translation.TemplateVersion); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"甲", "甲乙"} {
		response := requestRemoteWorks(t, db, "q="+url.QueryEscape(query)+"&lang=zh-cn&pageSize=24")
		if response.Total != 1 || len(response.Works) != 1 || response.Works[0].CatalogEntryID != 1 {
			t.Fatalf("%q total = %d ids = %v, want entry 1", query, response.Total, remoteWorkIDs(response))
		}
	}
}
