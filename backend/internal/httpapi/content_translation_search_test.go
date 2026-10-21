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
)

func insertTranslatedContent(t *testing.T, db *sql.DB, kind string, id int64, field, language, text, origin string) {
	t.Helper()
	key := fmt.Sprintf("%s|%d|%s|%s|%s|%s", kind, id, field, language, origin, text)
	if _, err := db.Exec(`INSERT INTO content_translation(
		cache_key, entity_kind, entity_id, field, language, source_text, translated_text, origin)
		VALUES(?,?,?,?,?,?,?,?)`, key, kind, id, field, language, text, text, origin); err != nil {
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

// A display language returns only rows translated into it, or authored in it.
// An untranslated Japanese entry must stay invisible even though its source
// title contains the query text.
func TestContentTranslationSearchRemoteLanguageStrict(t *testing.T) {
	db := openMigratedTestDB(t)
	seedCatalogPlanSource(t, db)
	insertCatalogPlanEntry(t, db, 1, "Yoru no Monogatari", "")
	insertCatalogPlanAudio(t, db, 1, "01.mp3", "rain")
	insertCatalogPlanEntry(t, db, 2, "Yoru no Omoide", "")
	insertCatalogPlanAudio(t, db, 2, "01.mp3", "rain")
	insertCatalogPlanEntry(t, db, 3, "雨夜の物語", "RJ90000001")
	insertCatalogPlanAudio(t, db, 3, "01.mp3", "rain")
	insertCatalogPlanEntry(t, db, 4, "Yoru Koi Uta", "")
	insertCatalogPlanAudio(t, db, 4, "01.mp3", "rain")
	insertCatalogPlanEntry(t, db, 5, "Hare no Hi", "")
	insertCatalogPlanAudio(t, db, 5, "01.mp3", "sun")

	insertTranslatedContent(t, db, "entry", 1, "title", "zh-Hans", "雨夜的故事", "model")
	insertTranslatedContent(t, db, "entry", 2, "title", "zh-Hans", "雨夜回想", "native")
	insertTranslatedContent(t, db, "entry", 3, "title", "ja", "雨夜の物語", "native")
	insertTranslatedContent(t, db, "entry", 4, "title", "zh-Hant", "雨夜戀歌", "native")
	insertTranslatedContent(t, db, "entry", 5, "title", "zh-Hans", "晴天", "model")

	simplified := requestRemoteWorks(t, db, "q="+url.QueryEscape("雨夜")+"&lang=zh-cn&pageSize=24")
	if simplified.Total != 2 {
		t.Fatalf("zh-cn total = %d, want 2: %#v", simplified.Total, remoteWorkIDs(simplified))
	}
	got := remoteWorkIDs(simplified)
	if len(got) != 2 || got[0] != 2 || got[1] != 1 {
		t.Fatalf("zh-cn ids = %v, want [2 1] (updated desc, id desc)", got)
	}
	for _, id := range got {
		if id == 3 || id == 4 || id == 5 {
			t.Fatalf("zh-cn leaked untranslated or other-language entry %d", id)
		}
	}

	fts := requestRemoteWorks(t, db, "q="+url.QueryEscape("雨夜的故事")+"&lang=zh-cn&pageSize=24")
	if fts.Total != 1 || len(fts.Works) != 1 || fts.Works[0].CatalogEntryID != 1 {
		t.Fatalf("zh-cn fts = %d %v, want entry 1: %#v", fts.Total, remoteWorkIDs(fts), fts)
	}
}

// Traditional queries must reach Simplified translations through t2s and
// native Traditional rows through the text as typed.
func TestContentTranslationSearchRemoteTraditionalMergesLanguages(t *testing.T) {
	db := openMigratedTestDB(t)
	seedCatalogPlanSource(t, db)
	insertCatalogPlanEntry(t, db, 1, "Renai Monogatari", "")
	insertCatalogPlanAudio(t, db, 1, "01.mp3", "rain")
	insertCatalogPlanEntry(t, db, 2, "Renai Shouhin", "")
	insertCatalogPlanAudio(t, db, 2, "01.mp3", "rain")
	insertCatalogPlanEntry(t, db, 3, "Untranslated", "")
	insertCatalogPlanAudio(t, db, 3, "01.mp3", "rain")

	insertTranslatedContent(t, db, "entry", 1, "title", "zh-Hans", "恋爱故事", "model")
	insertTranslatedContent(t, db, "entry", 2, "title", "zh-Hant", "戀愛小品", "native")

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
		insertTranslatedContent(t, db, "entry", index, "title", "zh-Hans", fmt.Sprintf("作品%d", index), "model")
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
		result, err := db.Exec("INSERT INTO work (primary_code, title) VALUES (?, ?)", fmt.Sprintf("RJ1000000%d", index+1), title)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := result.LastInsertId()
		workIDs = append(workIDs, id)
	}
	insertTranslatedContent(t, db, "work", workIDs[0], "title", "zh-Hans", "中文译文", "model")
	insertTranslatedContent(t, db, "work", workIDs[1], "title", "zh-Hans", "原生中文", "native")
	insertTranslatedContent(t, db, "work", workIDs[2], "title", "en", "English Only", "native")

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

// An exact translated title outranks a substring hit even when the tie-break
// order (id DESC) would place it second.
func TestContentTranslationSearchRemoteExactFirst(t *testing.T) {
	db := openMigratedTestDB(t)
	seedCatalogPlanSource(t, db)
	insertCatalogPlanEntry(t, db, 1, "Exact", "")
	insertCatalogPlanAudio(t, db, 1, "01.mp3", "rain")
	insertCatalogPlanEntry(t, db, 2, "Substring", "")
	insertCatalogPlanAudio(t, db, 2, "01.mp3", "rain")
	insertTranslatedContent(t, db, "entry", 1, "title", "zh-Hans", "雨夜", "model")
	insertTranslatedContent(t, db, "entry", 2, "title", "zh-Hans", "雨夜物语", "model")

	response := requestRemoteWorks(t, db, "q="+url.QueryEscape("雨夜")+"&lang=zh-cn&pageSize=24")
	got := remoteWorkIDs(response)
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("exact-first order = %v, want [1 2]: %#v", got, response)
	}
}
