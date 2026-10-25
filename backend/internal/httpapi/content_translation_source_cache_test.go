package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yexca/kikoto/backend/internal/testfixture"
	"github.com/yexca/kikoto/backend/internal/translation"
)

// remoteSourceFieldFixture serves one remote work with two directory paths that
// carry the same display name. The model always returns the same Chinese text,
// so a second script can prove that Traditional display reuses the Simplified
// row instead of generating again.
func remoteSourceFieldFixture(t *testing.T, title *atomic.Value, modelCalls *atomic.Int32) (*Server, *httptest.Server, *http.Cookie, *http.Cookie) {
	t.Helper()
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modelCalls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message":       map[string]string{"content": "听见音乐"},
				"finish_reason": "stop",
			}},
		})
	}))
	t.Cleanup(model.Close)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/workInfo/RJ00000001":
			current, _ := title.Load().(string)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":        1,
				"title":     current,
				"circle":    map[string]any{"id": 1, "name": "Example Circle"},
				"vas":       []any{map[string]any{"id": "1", "name": "Example Voice"}},
				"tags":      []any{},
				"source_id": "RJ00000001",
			})
		case "/api/tracks/1":
			_, _ = io.WriteString(w, `[{"type":"folder","title":"Folder A","children":[{"type":"audio","title":"Shared.wav","mediaStreamUrl":"https://media.example.invalid/a.wav"}]},{"type":"folder","title":"Folder B","children":[{"type":"audio","title":"Shared.wav","mediaStreamUrl":"https://media.example.invalid/b.wav"}]}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(source.Close)

	s, host, admin, listener := translationHTTPFixture(t)
	if _, err := s.db.Exec(`INSERT INTO file_source(id,code,display_name,source_type) VALUES(1,'example_remote','Example Remote','kikoeru_compatible'); INSERT INTO file_source_endpoint(file_source_id,base_url,api_url) VALUES(1,?,?)`, source.URL, source.URL); err != nil {
		t.Fatal(err)
	}
	cfg, _ := json.Marshal(map[string]any{"baseUrl": model.URL, "minFreeBytes": 0})
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", string(cfg), 200)
	return s, host, admin, listener
}

func sourceFieldRequest(t *testing.T, host *httptest.Server, listener *http.Cookie, field, text, path, language string) map[string]any {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"sourceId": 1, "code": "RJ00000001", "field": field,
		"text": text, "path": path, "language": language,
	})
	return translationRequest(t, host, listener, "POST", "/api/content-translations", string(body), 200)
}

func TestContentTranslationSourceFieldCacheReusesMatchingRowsAndInvalidatesChanges(t *testing.T) {
	var calls atomic.Int32
	var title atomic.Value
	title.Store("Example title")
	s, host, admin, listener := remoteSourceFieldFixture(t, &title, &calls)

	simplified := sourceFieldRequest(t, host, listener, "title", "Example title", "", "zh-cn")
	if simplified["text"] != "听见音乐" || simplified["status"] != "translated" || simplified["cacheStatus"] != "stored" {
		t.Fatalf("first source title = %#v", simplified)
	}
	repeated := sourceFieldRequest(t, host, listener, "title", "Example title", "", "zh-cn")
	if repeated["text"] != "听见音乐" || repeated["cacheStatus"] != "hit" {
		t.Fatalf("repeated source title = %#v", repeated)
	}
	traditional := sourceFieldRequest(t, host, listener, "title", "Example title", "", "zh-tw")
	if traditional["text"] != "聽見音樂" || traditional["cacheStatus"] != "hit" {
		t.Fatalf("Traditional source title = %#v", traditional)
	}
	if calls.Load() != 1 {
		t.Fatalf("title repeated browsing generated %d times, want 1", calls.Load())
	}

	firstPath := sourceFieldRequest(t, host, listener, "directory", "Shared.wav", "Folder A/Shared.wav", "zh-cn")
	if firstPath["cacheStatus"] != "stored" {
		t.Fatalf("first directory = %#v", firstPath)
	}
	firstPathRepeated := sourceFieldRequest(t, host, listener, "directory", "Shared.wav", "Folder A/Shared.wav", "zh-cn")
	if firstPathRepeated["cacheStatus"] != "hit" {
		t.Fatalf("repeated directory = %#v", firstPathRepeated)
	}
	// The same visible file name in another directory is a different field.
	secondPath := sourceFieldRequest(t, host, listener, "directory", "Shared.wav", "Folder B/Shared.wav", "zh-cn")
	if secondPath["cacheStatus"] != "stored" || calls.Load() != 3 {
		t.Fatalf("second directory = %#v; calls=%d", secondPath, calls.Load())
	}

	title.Store("Changed title")
	s.invalidateRemoteWorkCache(1)
	changed := sourceFieldRequest(t, host, listener, "title", "Changed title", "", "zh-cn")
	if changed["cacheStatus"] != "stored" || calls.Load() != 4 {
		t.Fatalf("changed source text = %#v; calls=%d", changed, calls.Load())
	}
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", `{"model":"Example Model 2"}`, 200)
	remodeled := sourceFieldRequest(t, host, listener, "title", "Changed title", "", "zh-cn")
	if remodeled["cacheStatus"] != "stored" || calls.Load() != 5 {
		t.Fatalf("model change reused stale source row: %#v; calls=%d", remodeled, calls.Load())
	}

	// A source that became unavailable cannot be read through the cache.
	if _, err := s.db.Exec(`UPDATE file_source SET enabled=0 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	translationRequest(t, host, listener, "POST", "/api/content-translations",
		`{"sourceId":1,"code":"RJ00000001","field":"title","text":"Changed title","language":"zh-cn"}`, 503)
	if calls.Load() != 5 {
		t.Fatalf("disabled source reached the model or cache: calls=%d", calls.Load())
	}

	if err := s.pruneContentTranslations(context.Background(), translation.Config{Limits: translation.DefaultLimits()}); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := s.db.QueryRow(`SELECT count(*) FROM source_field_translation`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	// The old title rows are superseded by the newer model result; the two
	// directory rows keep their own paths.
	if rows != 3 {
		t.Fatalf("source field rows = %d, want 3", rows)
	}
}

func TestSourceFieldTranslationCacheKeySeparatesIdentity(t *testing.T) {
	base := sourceFieldTranslationCacheKey(1, "RJ00000001", "title", "", "zh-Hans", "Example title", "model-a", "https://model.example/v1", "template-a", true)
	variants := map[string]string{
		"source":   sourceFieldTranslationCacheKey(2, "RJ00000001", "title", "", "zh-Hans", "Example title", "model-a", "https://model.example/v1", "template-a", true),
		"work":     sourceFieldTranslationCacheKey(1, "RJ00000002", "title", "", "zh-Hans", "Example title", "model-a", "https://model.example/v1", "template-a", true),
		"field":    sourceFieldTranslationCacheKey(1, "RJ00000001", "tag", "", "zh-Hans", "Example title", "model-a", "https://model.example/v1", "template-a", true),
		"path":     sourceFieldTranslationCacheKey(1, "RJ00000001", "directory", "Folder B/track.wav", "zh-Hans", "track.wav", "model-a", "https://model.example/v1", "template-a", true),
		"language": sourceFieldTranslationCacheKey(1, "RJ00000001", "title", "", "ja", "Example title", "model-a", "https://model.example/v1", "template-a", true),
		"text":     sourceFieldTranslationCacheKey(1, "RJ00000001", "title", "", "zh-Hans", "Changed title", "model-a", "https://model.example/v1", "template-a", true),
		"model":    sourceFieldTranslationCacheKey(1, "RJ00000001", "title", "", "zh-Hans", "Example title", "model-b", "https://model.example/v1", "template-a", true),
		"endpoint": sourceFieldTranslationCacheKey(1, "RJ00000001", "title", "", "zh-Hans", "Example title", "model-a", "https://other.example/v1", "template-a", true),
		"template": sourceFieldTranslationCacheKey(1, "RJ00000001", "title", "", "zh-Hans", "Example title", "model-a", "https://model.example/v1", "template-b", true),
		"thinking": sourceFieldTranslationCacheKey(1, "RJ00000001", "title", "", "zh-Hans", "Example title", "model-a", "https://model.example/v1", "template-a", false),
	}
	for name, key := range variants {
		if key == base || key == "" {
			t.Fatalf("%s change reused cache identity", name)
		}
	}
	if got := sourceFieldTranslationCacheKey(1, " rj00000001 ", "title", "", "zh-Hans", "Example title", "model-a", "https://model.example/v1", "template-a", true); got != base {
		t.Fatal("work code normalization changed the cache identity")
	}
}

func TestContentTranslationSourceFieldCacheDoesNotStoreFailedOutput(t *testing.T) {
	var calls atomic.Int32
	var mode atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch mode.Load() {
		case 0:
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":""},"finish_reason":"stop"}]}`)
		case 1:
			http.Error(w, "upstream failed", http.StatusInternalServerError)
		default:
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"听见音乐"},"finish_reason":"stop"}]}`)
		}
	}))
	defer model.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/workInfo/RJ00000001":
			_, _ = io.WriteString(w, `{"id":1,"source_id":"RJ00000001","title":"Example title","circle":{"id":1,"name":"Example Circle"},"vas":[],"tags":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer source.Close()
	s, host, admin, listener := translationHTTPFixture(t)
	if _, err := s.db.Exec(`INSERT INTO file_source(id,code,display_name,source_type) VALUES(1,'example_remote','Example Remote','kikoeru_compatible'); INSERT INTO file_source_endpoint(file_source_id,base_url,api_url) VALUES(1,?,?)`, source.URL, source.URL); err != nil {
		t.Fatal(err)
	}
	cfg, _ := json.Marshal(map[string]any{"baseUrl": model.URL, "minFreeBytes": 0})
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", string(cfg), 200)
	const body = `{"sourceId":1,"code":"RJ00000001","field":"title","text":"Example title","language":"zh-cn"}`

	empty := translationRequest(t, host, listener, "POST", "/api/content-translations", body, 200)
	if empty["status"] != "failed" || empty["code"] != "translation_empty_output" {
		t.Fatalf("empty output = %#v", empty)
	}
	var rows int
	if err := s.db.QueryRow(`SELECT count(*) FROM source_field_translation`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("empty output wrote %d rows: %v", rows, err)
	}

	mode.Store(1)
	failed := translationRequest(t, host, listener, "POST", "/api/content-translations", body, 200)
	if failed["status"] != "failed" || failed["code"] == "" {
		t.Fatalf("upstream failure = %#v", failed)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM source_field_translation`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("failed output wrote %d rows: %v", rows, err)
	}

	mode.Store(2)
	stored := translationRequest(t, host, listener, "POST", "/api/content-translations", body, 200)
	if stored["status"] != "translated" || stored["cacheStatus"] != "stored" {
		t.Fatalf("successful retry = %#v", stored)
	}
	hit := translationRequest(t, host, listener, "POST", "/api/content-translations", body, 200)
	if hit["cacheStatus"] != "hit" || calls.Load() != 3 {
		t.Fatalf("retry cache = %#v; calls=%d", hit, calls.Load())
	}
}

func TestTranslationRetentionCountsSourceFieldCacheAndKeepsNativeRows(t *testing.T) {
	server := openContentTranslationTestServer(t)
	ctx := context.Background()
	cfg := translation.Config{Limits: translation.Limits{CacheBytes: 80}, Model: "m"}
	configKey := translation.ConfigKey(cfg)
	nativeText := strings.Repeat("作", 40)
	seedCurrentTranslationEntry(t, server.db, 2, "content source text 01")
	if _, err := server.saveContentTranslations(ctx, []contentTranslationRow{
		{CacheKey: "native-1", EntityKind: "entry", EntityID: 1, Field: "title", Language: "ja", SourceText: nativeText, TranslatedText: nativeText, Origin: "native"},
		{CacheKey: "content-model", EntityKind: "entry", EntityID: 2, Field: "title", Language: "zh-Hans", SourceText: "content source text 01", TranslatedText: "content target text 01", Origin: "model", ConfigKey: configKey, Model: "m", TemplateVersion: translation.TemplateVersion},
	}); err != nil {
		t.Fatal(err)
	}
	sourceRows := []sourceFieldTranslationRow{
		{CacheKey: "source-a", SourceID: 1, WorkCode: "RJ00000001", Field: "title", Language: "zh-Hans", SourceText: "source text a 01", TranslatedText: "target text a 01", Model: "m", TemplateVersion: translation.TemplateVersion},
		{CacheKey: "source-b", SourceID: 1, WorkCode: "RJ00000002", Field: "title", Language: "zh-Hans", SourceText: "source text b 01", TranslatedText: "target text b 01", Model: "m", TemplateVersion: translation.TemplateVersion},
		{CacheKey: "source-c", SourceID: 1, WorkCode: "RJ00000003", Field: "title", Language: "zh-Hans", SourceText: "source text c 01", TranslatedText: "target text c 01", Model: "m", TemplateVersion: translation.TemplateVersion},
	}
	if _, err := server.saveSourceFieldTranslations(ctx, sourceRows); err != nil {
		t.Fatal(err)
	}
	if _, err := server.db.Exec(`UPDATE content_translation SET updated_at='2026-01-01 00:00:01' WHERE cache_key='content-model';
		UPDATE source_field_translation SET updated_at='2026-01-01 00:00:02' WHERE cache_key='source-a';
		UPDATE source_field_translation SET updated_at='2026-01-01 00:00:03' WHERE cache_key='source-b';
		UPDATE source_field_translation SET updated_at='2026-01-01 00:00:04' WHERE cache_key='source-c';`); err != nil {
		t.Fatal(err)
	}
	// The live content row is corpus. Only the three rebuildable source rows
	// compete for this 80-byte budget, which keeps two of them.
	if err := server.pruneContentTranslations(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	var modelRows, nativeRows, sourceBytes int64
	if err := server.db.QueryRow(`SELECT
		(SELECT count(*) FROM content_translation WHERE origin='model')+(SELECT count(*) FROM source_field_translation),
		(SELECT count(*) FROM content_translation WHERE origin='native'),
		(SELECT COALESCE(sum(length(CAST(source_text AS BLOB))+length(CAST(translated_text AS BLOB))),0) FROM source_field_translation)
		`).Scan(&modelRows, &nativeRows, &sourceBytes); err != nil {
		t.Fatal(err)
	}
	if modelRows != 3 || nativeRows != 1 || sourceBytes > 80 {
		t.Fatalf("retention rows=%d native=%d sourceBytes=%d, want 3/1 and <=80", modelRows, nativeRows, sourceBytes)
	}
	var contentKept int
	if err := server.db.QueryRow(`SELECT count(*) FROM content_translation WHERE cache_key='content-model'`).Scan(&contentKept); err != nil || contentKept != 1 {
		t.Fatalf("current content row kept=%d err=%v", contentKept, err)
	}
	var sourceKept int
	if err := server.db.QueryRow(`SELECT count(*) FROM source_field_translation WHERE cache_key IN ('source-b','source-c')`).Scan(&sourceKept); err != nil || sourceKept != 2 {
		t.Fatalf("newest source rows kept=%d err=%v", sourceKept, err)
	}
	var identity int
	if err := server.db.QueryRow(`SELECT count(*) FROM file_source WHERE id=1`).Scan(&identity); err != nil || identity != 1 {
		t.Fatalf("source identity removed: %d %v", identity, err)
	}
}

// A model write must enforce the budget through the shared retention pass; the
// test does not call the retention function itself.
func TestTranslationCacheBudgetIsEnforcedByWrites(t *testing.T) {
	server, host, admin, _ := translationHTTPFixture(t)
	if _, err := server.db.Exec(`INSERT INTO file_source(id,code,display_name,source_type)
		VALUES(1,'cache_budget','Cache budget','openlist')`); err != nil {
		t.Fatal(err)
	}
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", `{"cacheBytes":4096,"maxEntries":0,"minFreeBytes":0}`, 200)
	rows := make([]sourceFieldTranslationRow, 0, 120)
	for index := range 120 {
		rows = append(rows, sourceFieldTranslationRow{
			CacheKey: fmt.Sprintf("source-write-%03d", index), SourceID: 1,
			WorkCode: testfixture.WorkCodeAt(index), Field: "title", Language: "zh-Hans",
			SourceText: strings.Repeat("a", 40), TranslatedText: strings.Repeat("b", 40),
			Model: "m", TemplateVersion: translation.TemplateVersion,
		})
	}
	if status, err := server.saveSourceFieldTranslations(context.Background(), rows); err != nil || status != contentTranslationStored {
		t.Fatalf("save source rows status=%q err=%v", status, err)
	}
	var count, bytes int64
	if err := server.db.QueryRow(`SELECT count(*),COALESCE(sum(
			length(CAST(source_text AS BLOB))+length(CAST(translated_text AS BLOB))),0)
		FROM source_field_translation`).Scan(&count, &bytes); err != nil {
		t.Fatal(err)
	}
	if count == 0 || count >= 120 || bytes > 4096 {
		t.Fatalf("write-path retention rows=%d bytes=%d, want some rows and <=4096", count, bytes)
	}
}

func TestSourceFieldTranslationCacheStaysOutOfSearchCorpus(t *testing.T) {
	server := openContentTranslationTestServer(t)
	ctx := context.Background()
	if _, err := server.db.Exec(`INSERT INTO file_source(id,code,display_name,source_type)
		VALUES(1,'source_cache','Source cache','openlist')`); err != nil {
		t.Fatal(err)
	}
	if _, err := server.saveSourceFieldTranslations(ctx, []sourceFieldTranslationRow{{
		CacheKey: "source-only", SourceID: 1, WorkCode: "RJ00000001", Field: "tag",
		Language: "zh-Hans", SourceText: "Source tag", TranslatedText: "SourceOnlyNeedle",
		Model: "m", TemplateVersion: translation.TemplateVersion,
	}}); err != nil {
		t.Fatal(err)
	}
	ids, err := server.translatedEntityIDs(ctx, "zh-Hans", "entry", "SourceOnlyNeedle", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Fatalf("source-level cache entered search corpus: %v", ids)
	}
	var corpus, search int
	if err := server.db.QueryRow(`SELECT (SELECT count(*) FROM content_translation),(SELECT count(*) FROM content_translation_search)`).Scan(&corpus, &search); err != nil {
		t.Fatal(err)
	}
	if corpus != 0 || search != 0 {
		t.Fatalf("source cache changed search corpus: content=%d search=%d", corpus, search)
	}
	if _, err := server.saveContentTranslations(ctx, []contentTranslationRow{{
		CacheKey: "content-only", EntityKind: "entry", EntityID: 7, Field: "title",
		Language: "zh-Hans", SourceText: "内容原文", TranslatedText: "CorpusNeedle", Origin: "model",
		Model: "m", TemplateVersion: translation.TemplateVersion,
	}}); err != nil {
		t.Fatal(err)
	}
	ids, err = server.translatedEntityIDs(ctx, "zh-Hans", "entry", "CorpusNeedle", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 7 {
		t.Fatalf("content corpus search lost its row: %v", ids)
	}
}
