package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/yexca/kikoto/backend/internal/config"
	"github.com/yexca/kikoto/backend/internal/translation"
)

func configurePrefillModel(t *testing.T, host *httptest.Server, admin *http.Cookie, baseURL string) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"baseUrl": baseURL, "minFreeBytes": 0})
	if err != nil {
		t.Fatal(err)
	}
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", string(payload), http.StatusOK)
}

// seedPrefillCard seeds one admitted OpenList card. The prefill pass only walks
// entries behind an enabled OpenList source, so the fixture owns that source.
func seedPrefillCard(t *testing.T, s *Server, id int64, title string) {
	t.Helper()
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO file_source(id,code,display_name,source_type,enabled)
		VALUES(1,'prefill-fixture','Example Source','openlist',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,availability_status) VALUES(?,1,?,?,'available')`, id, "/"+title, title); err != nil {
		t.Fatal(err)
	}
}

func readPrefillState(t *testing.T, s *Server) (int64, string, string) {
	t.Helper()
	var cursor int64
	var status, lastError string
	if err := s.db.QueryRow(`SELECT cursor_entry_id,status,last_error FROM translation_prefill_state WHERE id=1`).Scan(&cursor, &status, &lastError); err != nil {
		t.Fatal(err)
	}
	return cursor, status, lastError
}

func successfulPrefillModel(t *testing.T, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"示例译文"},"finish_reason":"stop"}]}`)
	}))
}

func TestTranslationPrefillNativeTitleWritesNativeRowWithoutModelCall(t *testing.T) {
	var calls atomic.Int32
	var mu sync.Mutex
	prompts := []string{}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		prompts = append(prompts, string(body))
		mu.Unlock()
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"示例译文"},"finish_reason":"stop"}]}`)
	}))
	defer model.Close()
	s, host, admin, _ := translationHTTPFixture(t)
	seedPrefillCard(t, s, 1, "Example title")
	configurePrefillModel(t, host, admin, model.URL)

	if _, err := s.runTranslationPrefillBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	var origin, translated, language string
	if err := s.db.QueryRow(`SELECT origin,translated_text,language FROM content_translation
		WHERE entity_kind='entry' AND entity_id=1 AND field='title' AND language='en'`).Scan(&origin, &translated, &language); err != nil {
		t.Fatal(err)
	}
	if origin != "native" || translated != "Example title" || language != "en" {
		t.Fatalf("native row = origin %q text %q language %q", origin, translated, language)
	}
	if calls.Load() != 3 {
		t.Fatalf("model calls = %d, want one for each non-native language", calls.Load())
	}
	for _, prompt := range prompts {
		if strings.Contains(prompt, "翻译为英语") {
			t.Fatalf("native English title was sent to the model: %s", prompt)
		}
	}
}

func TestTranslationPrefillPausesOnLowDiskAndResumesWhenSpaceReturns(t *testing.T) {
	var calls atomic.Int32
	model := successfulPrefillModel(t, &calls)
	defer model.Close()
	s, host, admin, _ := translationHTTPFixture(t)
	seedPrefillCard(t, s, 1, "Example title")
	configurePrefillModel(t, host, admin, model.URL)

	s.catalogDiskSpace = func(string) (diskSpaceSnapshot, error) {
		return diskSpaceSnapshot{Available: 0}, nil
	}
	if _, err := s.runTranslationPrefillBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, status, lastError := readPrefillState(t, s)
	if status != "paused" || lastError != "paused_low_disk" {
		t.Fatalf("low disk state = %q/%q, want paused/paused_low_disk", status, lastError)
	}
	if calls.Load() != 0 {
		t.Fatalf("model calls while paused = %d, want 0", calls.Load())
	}

	s.catalogDiskSpace = func(string) (diskSpaceSnapshot, error) {
		return diskSpaceSnapshot{Available: 4 << 30}, nil
	}
	if _, err := s.runTranslationPrefillBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, status, lastError = readPrefillState(t, s)
	if status != "running" || lastError != "" {
		t.Fatalf("resumed state = %q/%q, want running with no error", status, lastError)
	}
	if calls.Load() == 0 {
		t.Fatal("translation did not resume after free space returned")
	}
}

func TestTranslationPrefillRetriesFailedEntryBeforeAdvancingCursor(t *testing.T) {
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 3 {
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":""},"finish_reason":"stop"}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"示例译文"},"finish_reason":"stop"}]}`)
	}))
	defer model.Close()
	s, host, admin, _ := translationHTTPFixture(t)
	seedPrefillCard(t, s, 1, "Example title")
	configurePrefillModel(t, host, admin, model.URL)

	if _, err := s.runTranslationPrefillBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	cursor, status, lastError := readPrefillState(t, s)
	if cursor != 0 || status != "running" {
		t.Fatalf("after failure state = cursor %d status %q, want 0/running", cursor, status)
	}
	if !strings.Contains(lastError, "no translation") {
		t.Fatalf("last_error = %q, want the model failure reason", lastError)
	}

	if _, err := s.runTranslationPrefillBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	cursor, status, lastError = readPrefillState(t, s)
	if cursor != 1 || status != "running" || lastError != "" {
		t.Fatalf("after retry state = cursor %d status %q last_error %q, want 1/running/empty", cursor, status, lastError)
	}
	if calls.Load() != 6 {
		t.Fatalf("model calls = %d, want three failed then three retried", calls.Load())
	}
}

func TestTranslationPrefillSkipsEntryAfterThreeConsecutiveFailures(t *testing.T) {
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":""},"finish_reason":"stop"}]}`)
	}))
	defer model.Close()
	s, host, admin, _ := translationHTTPFixture(t)
	seedPrefillCard(t, s, 1, "Example title")
	configurePrefillModel(t, host, admin, model.URL)

	var cursor int64
	var status, lastError string
	for attempt := 1; attempt <= 3; attempt++ {
		if _, err := s.runTranslationPrefillBatch(context.Background()); err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		cursor, status, lastError = readPrefillState(t, s)
		if status != "running" {
			t.Fatalf("attempt %d status = %q, want running", attempt, status)
		}
		if attempt < 3 {
			if cursor != 0 {
				t.Fatalf("attempt %d cursor = %d, want the failing entry retried", attempt, cursor)
			}
			continue
		}
		if cursor != 1 {
			t.Fatalf("third failure cursor = %d, want entry skipped", cursor)
		}
		if !strings.Contains(lastError, "skipped 1 entries after 3 failures") || !strings.Contains(lastError, "1") {
			t.Fatalf("last_error = %q, want skipped count and id", lastError)
		}
		if len(lastError) > 300 {
			t.Fatalf("last_error length = %d, want at most 300", len(lastError))
		}
	}
	if calls.Load() != 9 {
		t.Fatalf("model calls = %d, want three languages on each of three batches", calls.Load())
	}

	// Reaching the end of the scan must not turn skipped entries into a
	// complete corpus. The cursor returns to the first missing entry.
	if _, err := s.runTranslationPrefillBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	cursor, status, lastError = readPrefillState(t, s)
	if status == "complete" || cursor != 0 || !strings.Contains(lastError, "coverage incomplete") {
		t.Fatalf("after scan end = cursor %d status %q last_error %q, want 0/running/incomplete", cursor, status, lastError)
	}
}

func prefillLanguageRows(t *testing.T, view map[string]any) map[string]map[string]any {
	t.Helper()
	raw, ok := view["languages"].([]any)
	if !ok {
		t.Fatalf("prefill languages = %#v", view["languages"])
	}
	rows := map[string]map[string]any{}
	for _, item := range raw {
		language := item.(map[string]any)
		rows[language["language"].(string)] = language
	}
	return rows
}

// Coverage is an effective-corpus statistic: only a live available catalog
// entry, its current title, and the configured model/template count.
func TestTranslationPrefillCoverageExcludesRetiredAndHistoricalRows(t *testing.T) {
	s, host, admin, _ := translationHTTPFixture(t)
	seedPrefillCard(t, s, 1, "当前标题")
	if _, err := s.db.Exec(`INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,availability_status)
		VALUES(2,1,'/retired','退役标题','missing'),
		      (3,1,'/excluded','排除标题','excluded'),
		      (4,1,'/pending','待确认标题','unconfirmed_empty')`); err != nil {
		t.Fatal(err)
	}
	cfg, err := translation.NewStore(s.db).Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, language := range []string{"en", "zh-Hans", "ja", "ko"} {
		insertTranslatedContent(t, s.db, "entry", 1, "title", language, "当前标题", "Current title", "model")
	}
	// Historical source text for a live entry.
	insertTranslatedContent(t, s.db, "entry", 1, "title", "zh-Hans", "旧标题", "旧译文", "model")
	// Retired entry still has a row.
	insertTranslatedContent(t, s.db, "entry", 2, "title", "zh-Hans", "退役标题", "退役译文", "model")
	insertTranslatedContent(t, s.db, "entry", 3, "title", "zh-Hans", "排除标题", "排除译文", "model")
	insertTranslatedContent(t, s.db, "entry", 4, "title", "zh-Hans", "待确认标题", "待确认译文", "model")
	// Ensure the rows carry the current model identity.
	if _, err := s.db.Exec(`UPDATE content_translation SET model=?, template_version=?`,
		cfg.Model, translation.TemplateVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE translation_prefill_state SET status='complete' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	view := translationRequest(t, host, admin, "GET", "/api/settings/translation/prefill", "", 200)
	if view["total"] != float64(1) || view["status"] != "complete" {
		t.Fatalf("coverage view = total %v status %v, want 1/complete", view["total"], view["status"])
	}
	languages := prefillLanguageRows(t, view)
	for _, language := range []string{"en", "zh-Hans", "ja", "ko", "zh-Hant"} {
		item := languages[language]
		if item == nil || item["rows"] != float64(1) || item["missing"] != float64(0) {
			t.Fatalf("%s coverage = %#v, want one row and no missing", language, item)
		}
	}
}

// Changing the model makes every old row historical. Coverage must drop and
// the status must not stay complete until the new model fills the corpus.
func TestTranslationPrefillCoverageDropsOnModelChange(t *testing.T) {
	s, host, admin, _ := translationHTTPFixture(t)
	seedPrefillCard(t, s, 1, "当前标题")
	for _, language := range []string{"en", "zh-Hans", "ja", "ko"} {
		insertTranslatedContent(t, s.db, "entry", 1, "title", language, "当前标题", "Old translation", "model")
	}
	if _, err := s.db.Exec(`UPDATE content_translation SET model='old-model', template_version=?`, translation.TemplateVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE translation_prefill_state SET status='complete' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", `{"model":"new-model"}`, 200)

	view := translationRequest(t, host, admin, "GET", "/api/settings/translation/prefill", "", 200)
	if view["status"] == "complete" {
		t.Fatalf("model change left complete coverage: %#v", view)
	}
	languages := prefillLanguageRows(t, view)
	if item := languages["zh-Hans"]; item == nil || item["rows"] != float64(0) || item["missing"] != float64(1) {
		t.Fatalf("zh-Hans after model change = %#v, want 0 rows and one missing", item)
	}
}

func TestTranslationPrefillPublishedEntryRollsCursorBack(t *testing.T) {
	db := openMigratedTestDB(t)
	if _, err := db.Exec(`UPDATE translation_prefill_state SET cursor_entry_id=10,status='complete' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	s := &Server{db: db, translationPrefillFailures: map[int64]int{7: 3, 20: 2, 99: 1}}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = noteCatalogEntriesPublishedTx(context.Background(), tx, []int64{20, 7}); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	s.clearTranslationPrefillFailures([]int64{20, 7})
	var cursor int64
	var status string
	if err := db.QueryRow(`SELECT cursor_entry_id,status FROM translation_prefill_state WHERE id=1`).Scan(&cursor, &status); err != nil {
		t.Fatal(err)
	}
	if cursor != 6 || status != "running" {
		t.Fatalf("published rollback = cursor %d status %q, want 6/running", cursor, status)
	}
	if s.translationPrefillFailureCount(7) != 0 || s.translationPrefillFailureCount(20) != 0 || s.translationPrefillFailureCount(99) != 1 {
		t.Fatal("published entries did not get a fresh retry budget")
	}
}

func TestTranslationPrefillBatchAvoidsFullCorpusStatistics(t *testing.T) {
	db, counter := openCatalogCountingTestDB(t)
	s := NewServer(db, config.Config{})
	seedPrefillCard(t, s, 1, "Example title")
	model := successfulPrefillModel(t, &atomic.Int32{})
	defer model.Close()
	baseURL := model.URL
	minFree := int64(0)
	if _, err := translation.NewStore(db).Update(context.Background(), translation.Patch{BaseURL: &baseURL, MinFreeBytes: &minFree}); err != nil {
		t.Fatal(err)
	}
	counter.reset()
	if _, err := s.runTranslationPrefillBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	statements := counter.snapshot()
	foundStateRead := false
	for _, statement := range statements {
		lower := strings.ToLower(statement)
		if strings.Contains(lower, "group by language") {
			t.Fatalf("worker ran the full language statistics query: %s", statement)
		}
		if strings.Contains(lower, "count(*) from source_catalog_entry") {
			t.Fatalf("worker ran the full corpus count query: %s", statement)
		}
		if strings.Contains(lower, "from translation_prefill_state") {
			foundStateRead = true
		}
	}
	if !foundStateRead {
		t.Fatal("worker did not read the single prefill state row")
	}
}

// The background pass has no viewer to scope it. It must not translate cards
// behind a disabled source or a source that is no longer an OpenList catalog.
func TestTranslationPrefillSkipsDisabledAndForeignSources(t *testing.T) {
	var calls atomic.Int32
	model := successfulPrefillModel(t, &calls)
	defer model.Close()
	s, host, admin, _ := translationHTTPFixture(t)
	configurePrefillModel(t, host, admin, model.URL)
	if _, err := s.db.Exec(`INSERT INTO file_source(id,code,display_name,source_type,enabled)
		VALUES(1,'prefill-disabled','Disabled Source','openlist',0),(2,'prefill-other','Other Source','localfs',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,availability_status)
		VALUES(1,1,'/Disabled','Disabled title','available'),(2,2,'/Foreign','Foreign title','available')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.runTranslationPrefillBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatalf("model calls for out-of-scope sources = %d, want 0", calls.Load())
	}
	var rows int
	if err := s.db.QueryRow(`SELECT count(*) FROM content_translation`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("wrote %d rows for out-of-scope sources", rows)
	}
}

func TestTranslationPrefillLoadsProtectedNamesOncePerEntry(t *testing.T) {
	db, counter := openCatalogCountingTestDB(t)
	s := NewServer(db, config.Config{})
	seedPrefillCard(t, s, 1, "Example title")
	model := successfulPrefillModel(t, &atomic.Int32{})
	defer model.Close()
	baseURL := model.URL
	minFree := int64(0)
	if _, err := translation.NewStore(db).Update(context.Background(), translation.Patch{BaseURL: &baseURL, MinFreeBytes: &minFree}); err != nil {
		t.Fatal(err)
	}
	counter.reset()
	if _, err := s.runTranslationPrefillBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The sidecar query is the protected-name read for an unregistered card.
	// It must run once for the entry, not once per target language.
	loads := 0
	for _, statement := range counter.snapshot() {
		if strings.Contains(statement, "SELECT sidecar_json FROM source_catalog_entry WHERE id=?") {
			loads++
		}
	}
	if loads != 1 {
		t.Fatalf("protected-name loads = %d, want once for all languages of one entry", loads)
	}
}
