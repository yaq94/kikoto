package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	// D-16 admission requires at least one audio file. Without it the card is
	// not part of the effective corpus, so prefill would correctly do nothing.
	if _, err := s.db.Exec(`INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes)
		VALUES(?,'01.mp3','01.mp3','audio',1)`, id); err != nil {
		t.Fatal(err)
	}
}

func readPrefillState(t *testing.T, s *Server) (int64, string, string) {
	t.Helper()
	var cursor int64
	var status, lastError string
	if err := s.db.QueryRow(`SELECT cursor_id,status,last_error FROM translation_prefill_state WHERE id=1`).Scan(&cursor, &status, &lastError); err != nil {
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
	if status != "complete" || lastError != "" {
		t.Fatalf("resumed state = %q/%q, want complete with no error", status, lastError)
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
	// A failure is persisted before the cursor moves on. Holding the scan at
	// the failed entity would block every later entity behind one bad call.
	if cursor == 0 || status != "running" {
		t.Fatalf("after failure state = cursor %d status %q, want an advanced cursor and running", cursor, status)
	}
	if !strings.Contains(lastError, "no translation") {
		t.Fatalf("last_error = %q, want the model failure reason", lastError)
	}
	var failedRows int
	if err := s.db.QueryRow(`SELECT count(*) FROM translation_prefill_failure`).Scan(&failedRows); err != nil {
		t.Fatal(err)
	}
	if failedRows != 3 {
		t.Fatalf("persisted failures = %d, want one per model language", failedRows)
	}
	// The persisted retry has a backoff. Make it due without changing the
	// attempt count; a restart must retry the same three language rows.
	if _, err := s.db.Exec(`UPDATE translation_prefill_failure SET next_retry_at=datetime('now','-1 minute')`); err != nil {
		t.Fatal(err)
	}

	if _, err := s.runTranslationPrefillBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, status, lastError = readPrefillState(t, s)
	if status != "running" || lastError != "" {
		t.Fatalf("after retry status = %q last_error %q, want running/empty", status, lastError)
	}
	if calls.Load() != 6 {
		t.Fatalf("model calls = %d, want three failed then three retried", calls.Load())
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM translation_prefill_failure`).Scan(&failedRows); err != nil {
		t.Fatal(err)
	}
	if failedRows != 0 {
		t.Fatalf("retry left %d failure rows after success", failedRows)
	}
}

func TestTranslationPrefillKeepsPersistentFailureAfterThreeAttempts(t *testing.T) {
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":""},"finish_reason":"stop"}]}`)
	}))
	defer model.Close()
	s, host, admin, _ := translationHTTPFixture(t)
	seedPrefillCard(t, s, 1, "Example title")
	configurePrefillModel(t, host, admin, model.URL)

	for attempt := 1; attempt <= 3; attempt++ {
		if _, err := s.runTranslationPrefillBatch(context.Background()); err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		_, status, lastError := readPrefillState(t, s)
		if status != "running" {
			t.Fatalf("attempt %d status = %q, want running", attempt, status)
		}
		if !strings.Contains(lastError, "no translation") {
			t.Fatalf("attempt %d last_error = %q, want the upstream failure", attempt, lastError)
		}
		if len(lastError) > 300 {
			t.Fatalf("last_error length = %d, want at most 300", len(lastError))
		}
		if attempt < 3 {
			// The third attempt exhausts the budget and must stay unscheduled.
			if _, err := s.db.Exec(`UPDATE translation_prefill_failure SET next_retry_at=datetime('now','-1 minute')`); err != nil {
				t.Fatal(err)
			}
		}
	}
	if calls.Load() != 9 {
		t.Fatalf("model calls = %d, want three languages on each of three batches", calls.Load())
	}

	// A terminal failure stays in the table and is never treated as skipped or
	// covered. Reaching the end of the scan cannot make the corpus complete.
	if _, err := s.runTranslationPrefillBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, status, lastError := readPrefillState(t, s)
	if status == "complete" || lastError == "" {
		t.Fatalf("after scan end = status %q last_error %q, want running with a failure", status, lastError)
	}
	var failed int
	if err := s.db.QueryRow(`SELECT count(*) FROM translation_prefill_failure WHERE attempts>=3`).Scan(&failed); err != nil {
		t.Fatal(err)
	}
	if failed != 3 {
		t.Fatalf("terminal failures = %d, want three language rows", failed)
	}
	// A terminal failure is not scheduled. NULL is the contract for "no retry
	// planned", and the due queue must not pick it up again.
	var unscheduled int
	if err := s.db.QueryRow(`SELECT count(*) FROM translation_prefill_failure
		WHERE attempts>=3 AND next_retry_at IS NULL`).Scan(&unscheduled); err != nil {
		t.Fatal(err)
	}
	if unscheduled != 3 {
		t.Fatalf("terminal failures with NULL next_retry_at = %d, want 3", unscheduled)
	}
	due, err := s.dueTranslationPrefillFailures(context.Background(), mustTranslationConfig(t, s), 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Fatalf("terminal failures were scheduled again: %#v", due)
	}
}

func mustTranslationConfig(t *testing.T, s *Server) translation.Config {
	t.Helper()
	cfg, err := translation.NewStore(s.db).Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// The cursor is (kind, field, id, language). One entity must receive every
// model language before the scan advances, and a batch boundary must not repeat
// an item or skip the next one.
func TestTranslationPrefillCursorCoversEverySlotAndLanguage(t *testing.T) {
	var calls atomic.Int32
	model := successfulPrefillModel(t, &calls)
	defer model.Close()
	s, host, admin, _ := translationHTTPFixture(t)
	configurePrefillModel(t, host, admin, model.URL)
	const entries = 14
	for id := int64(1); id <= entries; id++ {
		seedPrefillCard(t, s, id, fmt.Sprintf("Example title %d", id))
	}
	// 14 entries x 4 languages = 56 items, so the first 50-item batch stops in
	// the middle of the entry slot and the second resumes there.
	batches := 0
	for batches < 6 {
		more, err := s.runTranslationPrefillBatch(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		batches++
		if !more {
			break
		}
	}
	_, status, lastError := readPrefillState(t, s)
	if status != "complete" || lastError != "" {
		t.Fatalf("prefill did not complete: status %q last_error %q", status, lastError)
	}
	var rows int
	if err := s.db.QueryRow(`SELECT count(*) FROM content_translation
		WHERE entity_kind='entry' AND field='title'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != entries*4 {
		t.Fatalf("entry title rows = %d, want %d", rows, entries*4)
	}
	// English is native, so only three model calls per entry.
	if calls.Load() != entries*3 {
		t.Fatalf("model calls = %d, want %d", calls.Load(), entries*3)
	}
	var duplicates int
	if err := s.db.QueryRow(`SELECT count(*) FROM (
		SELECT entity_kind,entity_id,field,language,source_text,count(*) AS n
		FROM content_translation GROUP BY entity_kind,entity_id,field,language,source_text HAVING n>1)`).Scan(&duplicates); err != nil {
		t.Fatal(err)
	}
	if duplicates != 0 {
		t.Fatalf("cursor repeated %d corpus items", duplicates)
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
	insertTranslatedContent(t, s.db, "entry", 1, "title", "zh-Hant", "当前标题", "當前標題", "native")
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
	// Keep the model name but move the rows to a different config key. The
	// current key covers base URL and reasoning too, so a model-only edit is not
	// the only way a row becomes stale.
	if _, err := s.db.Exec(`UPDATE content_translation SET config_key='legacy'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE translation_prefill_state SET status='complete' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", `{"disableThinking":false}`, 200)

	view := translationRequest(t, host, admin, "GET", "/api/settings/translation/prefill", "", 200)
	if view["status"] == "complete" {
		t.Fatalf("model change left complete coverage: %#v", view)
	}
	languages := prefillLanguageRows(t, view)
	if item := languages["zh-Hans"]; item == nil || item["rows"] != float64(0) || item["stale"] != float64(1) {
		t.Fatalf("zh-Hans after model change = %#v, want 0 rows and one stale", item)
	}
}

func TestTranslationPrefillPublishedEntryRollsCursorBack(t *testing.T) {
	db := openMigratedTestDB(t)
	if _, err := db.Exec(`UPDATE translation_prefill_state SET cursor_id=10,status='complete' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
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
	if _, err := db.Exec(`DELETE FROM translation_prefill_failure WHERE entity_kind='entry' AND entity_id IN (20,7)`); err != nil {
		t.Fatal(err)
	}
	var cursor int64
	var status string
	if err := db.QueryRow(`SELECT cursor_id,status FROM translation_prefill_state WHERE id=1`).Scan(&cursor, &status); err != nil {
		t.Fatal(err)
	}
	if cursor != 6 || status != "running" {
		t.Fatalf("published rollback = cursor %d status %q, want 6/running", cursor, status)
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

// A changed source text starts a fresh retry budget. The old failure row is
// keyed by the old hash and must not suppress the new item.
func TestTranslationPrefillSourceChangeClearsOldFailureHash(t *testing.T) {
	s, host, admin, _ := translationHTTPFixture(t)
	seedPrefillCard(t, s, 1, "First title")
	configurePrefillModel(t, host, admin, "http://127.0.0.1:1")
	cfg, err := translation.NewStore(s.db).Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	item := translationCorpusItem{Language: "zh-Hans", EntityKind: "entry", EntityID: 1, Field: "title", SourceText: "First title"}
	if _, err := s.recordTranslationPrefillFailure(context.Background(), item, translation.ConfigKey(cfg), errors.New("upstream unavailable")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE source_catalog_entry SET title='Second title' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE translation_prefill_state SET status='running' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	// The trigger deletes failures by entity/field, but a stale hash can also
	// survive an interrupted trigger or a restored backup. Verify the due query
	// itself rejects a hash that no longer matches the effective text.
	item.SourceText = "Second title"
	if _, err := s.recordTranslationPrefillFailure(context.Background(), item, translation.ConfigKey(cfg), errors.New("second failure")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO translation_prefill_failure(
		entity_kind,entity_id,field,language,config_key,source_hash,attempts,next_retry_at,last_error)
		VALUES('entry',1,'title','zh-Hans',?,?,1,datetime('now','-1 minute'),'old text')`,
		translation.ConfigKey(cfg), translationSourceHash("First title")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE translation_prefill_failure SET next_retry_at=datetime('now','-1 minute')`); err != nil {
		t.Fatal(err)
	}
	due, err := s.dueTranslationPrefillFailures(context.Background(), cfg, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range due {
		if candidate.SourceText != "Second title" {
			t.Fatalf("due failure used stale source text: %#v", candidate)
		}
	}
	var stale int
	if err := s.db.QueryRow(`SELECT count(*) FROM translation_prefill_failure WHERE source_hash=?`,
		translationSourceHash("First title")).Scan(&stale); err != nil {
		t.Fatal(err)
	}
	if stale != 0 {
		t.Fatalf("stale hash row survived: %d", stale)
	}
}
