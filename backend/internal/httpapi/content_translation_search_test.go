package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/yexca/kikoto/backend/internal/config"
	"github.com/yexca/kikoto/backend/internal/testfixture"
	"github.com/yexca/kikoto/backend/internal/translation"
)

func insertTranslatedContent(t *testing.T, db *sql.DB, kind string, id int64, field, language, sourceText, text, origin string) {
	t.Helper()
	key := fmt.Sprintf("%s|%d|%s|%s|%s|%s", kind, id, field, language, origin, text)
	model, template, configKey := "", "", ""
	if origin == "model" {
		model = translation.DefaultModel
		template = translation.TemplateVersion
		configKey = translation.ConfigKey(translation.Config{Model: translation.DefaultModel, DisableThinking: true})
	}
	if _, err := db.Exec(`INSERT INTO content_translation(
		cache_key, entity_kind, entity_id, field, language, source_text, translated_text, origin, config_key, model, template_version)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`, key, kind, id, field, language, sourceText, text, origin, configKey, model, template); err != nil {
		t.Fatalf("insert translation %s/%d/%s: %v", kind, id, language, err)
	}
	refreshTestTranslationScope(t, db)
}

// refreshTestTranslationScope applies the production publish and scope steps.
// A test seeds catalog rows directly, so without this the stored scope would
// stay at its insert default and every search would look empty.
func refreshTestTranslationScope(t *testing.T, db *sql.DB) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin scope refresh: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.Query(`SELECT id FROM file_source`)
	if err != nil {
		t.Fatalf("list sources: %v", err)
	}
	sources := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			t.Fatalf("scan source: %v", err)
		}
		sources = append(sources, id)
	}
	if err = rows.Err(); err != nil {
		t.Fatalf("list sources: %v", err)
	}
	_ = rows.Close()
	for _, id := range sources {
		if err = refreshSourceCatalogEntryAdmissionsTx(context.Background(), tx, id); err != nil {
			t.Fatalf("refresh source %d admissions: %v", id, err)
		}
	}
	if _, err = tx.Exec(`UPDATE content_translation SET (source_id,browsable)=(
		SELECT scope.source_id,scope.browsable FROM content_translation_scope scope
		WHERE scope.translation_id=content_translation.id)`); err != nil {
		t.Fatalf("refresh translation scope: %v", err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatalf("commit scope refresh: %v", err)
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

// A display language must not fall back to the original title, and a release
// code stays searchable even when the card has no target-language row.
func TestContentTranslationSearchRemoteStrictTextAndLanguageIndependentCode(t *testing.T) {
	db := openMigratedTestDB(t)
	seedCatalogPlanSource(t, db)
	insertCatalogPlanEntry(t, db, 1, "Original Only Title", "RJ00000001")
	insertCatalogPlanAudio(t, db, 1, "01.mp3", "rain")
	insertCatalogPlanEntry(t, db, 2, "Translated Title", "")
	insertCatalogPlanAudio(t, db, 2, "01.mp3", "rain")
	insertTranslatedContent(t, db, "entry", 2, "title", "zh-Hans", "Translated Title", "中文标题", "model")

	for _, query := range []string{"Original Only", "Original"} {
		response := requestRemoteWorks(t, db, "q="+url.QueryEscape(query)+"&lang=zh-cn&pageSize=24")
		if response.Total != 0 {
			t.Fatalf("%q leaked the original title into zh-cn: %v", query, remoteWorkIDs(response))
		}
	}
	code := requestRemoteWorks(t, db, "q=RJ00000001&lang=zh-cn&pageSize=24")
	if code.Total != 1 || len(code.Works) != 1 || code.Works[0].CatalogEntryID != 1 {
		t.Fatalf("code search in zh-cn = %d %v, want entry 1", code.Total, remoteWorkIDs(code))
	}
}

// An entry has no description column. A model row for one cannot make a card
// match, and a description request against a catalog entry is invalid input.
func TestContentTranslationSearchRemoteEntryHasNoDescription(t *testing.T) {
	db := openMigratedTestDB(t)
	seedCatalogPlanSource(t, db)
	insertCatalogPlanEntry(t, db, 1, "Original Title", "")
	insertCatalogPlanAudio(t, db, 1, "01.mp3", "rain")
	insertTranslatedContent(t, db, "entry", 1, "description", "zh-Hans", "A quiet night", "安静的夜晚", "model")

	response := requestRemoteWorks(t, db, "q="+url.QueryEscape("安静")+"&lang=zh-cn&pageSize=24")
	if response.Total != 0 {
		t.Fatalf("entry description row leaked into search: %v", remoteWorkIDs(response))
	}
	s, host, listener, _ := translationHTTPFixture(t)
	seedPrefillCard(t, s, 1, "Original Title")
	result := translationRequest(t, host, listener, "POST", "/api/content-translations",
		`{"catalogEntryId":1,"field":"description","text":"A quiet night","language":"zh-cn"}`, 400)
	if result["code"] != "translation_invalid_input" {
		t.Fatalf("entry description request = %#v, want invalid input", result)
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
		result, err := db.Exec("INSERT INTO work (primary_code, title) VALUES (?, ?)", testfixture.WorkCode(testfixture.PrefixRJ, index), title)
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

// Local Library search follows the same contract as the remote catalog:
// description is searchable, origin never reads translations, and a release
// code remains language-independent.
func TestContentTranslationSearchLibraryStrictTextDescriptionAndCode(t *testing.T) {
	db := openMigratedTestDB(t)
	original, err := db.Exec(`INSERT INTO work (primary_code,title,description)
		VALUES ('RJ00000061','Original Only Title','A quiet night')`)
	if err != nil {
		t.Fatal(err)
	}
	originalID, _ := original.LastInsertId()
	translated, err := db.Exec(`INSERT INTO work (primary_code,title,description)
		VALUES ('RJ00000062','Translated Title','Translated description')`)
	if err != nil {
		t.Fatal(err)
	}
	translatedID, _ := translated.LastInsertId()
	insertTranslatedContent(t, db, "work", translatedID, "title", "zh-Hans", "Translated Title", "中文标题", "model")
	insertTranslatedContent(t, db, "work", translatedID, "description", "zh-Hans", "Translated description", "中文简介", "model")

	if leaked := requestLibraryWorks(t, db, "q="+url.QueryEscape("Original Only")+"&lang=zh-cn"); leaked.Total != 0 {
		t.Fatalf("original title leaked into zh-cn: %#v", leaked.Works)
	}
	description := requestLibraryWorks(t, db, "q="+url.QueryEscape("中文简介")+"&lang=zh-cn")
	if description.Total != 1 || len(description.Works) != 1 || description.Works[0].ID != translatedID {
		t.Fatalf("translated description search = %d %#v", description.Total, description.Works)
	}
	code := requestLibraryWorks(t, db, "q=RJ00000061&lang=zh-cn")
	if code.Total != 1 || len(code.Works) != 1 || code.Works[0].ID != originalID {
		t.Fatalf("code search in zh-cn = %d %#v", code.Total, code.Works)
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
		cache_key,entity_kind,entity_id,field,language,source_text,translated_text,origin,config_key,model,template_version)
		VALUES('rank-old-model','entry',3,'title','zh-Hans','Substring three','夜雨','model','old-key','old-model',?),
		      ('rank-old-source','entry',4,'title','zh-Hans','Old source','夜雨','model','old-key',?,?)`,
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

// A 1-2 character query can match the whole source, so its card pipeline is
// bounded. The bound is explicit and reported: the total is the capped count,
// and a page past the cap is empty rather than a silent slice of a longer set.
func TestContentTranslationSearchRemoteCapsShortMatchSet(t *testing.T) {
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
		INSERT INTO content_translation(cache_key,entity_kind,entity_id,field,language,source_text,translated_text,origin,config_key,model,template_version)
		SELECT 'limit-'||i,'entry',i,'title','zh-Hans','Original '||i,'作品'||i,'model',?,?,? FROM n`,
		translation.ConfigKey(translation.Config{Model: translation.DefaultModel, DisableThinking: true}),
		translation.DefaultModel, translation.TemplateVersion); err != nil {
		t.Fatal(err)
	}
	refreshTestTranslationScope(t, db)
	response := requestRemoteWorks(t, db, "q="+url.QueryEscape("作品")+"&lang=zh-cn&pageSize=100&page=10")
	if response.Total != catalogShortSearchResultLimit || len(response.Works) != 100 {
		t.Fatalf("bounded search = total %d rows %d, want %d/100",
			response.Total, len(response.Works), catalogShortSearchResultLimit)
	}
	// Three characters use the trigram path, which reports the complete set.
	fts := requestRemoteWorks(t, db, "q="+url.QueryEscape("作品1")+"&lang=zh-cn&pageSize=100&page=12")
	if fts.Total != 1111 || len(fts.Works) != 11 {
		t.Fatalf("fts search = total %d rows %d, want 1111/11", fts.Total, len(fts.Works))
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
		INSERT INTO content_translation(cache_key,entity_kind,entity_id,field,language,source_text,translated_text,origin,config_key,model,template_version)
		SELECT 'short-filler-'||i,'entry',i,'title','zh-Hans','Original '||i,'作品'||i,'model',?,?,? FROM n`,
		translation.ConfigKey(translation.Config{Model: translation.DefaultModel, DisableThinking: true}),
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
