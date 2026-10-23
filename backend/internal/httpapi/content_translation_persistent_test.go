package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// A title already written in the target language becomes a native row. The
// model must not be called, and the row keeps empty model metadata so it is
// never counted as a generation.
func TestContentTranslationNativeRowDoesNotCallModel(t *testing.T) {
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"unexpected"},"finish_reason":"stop"}]}`)
	}))
	defer model.Close()
	s, host, admin, listener := translationHTTPFixture(t)
	const title = "深海少女的告白"
	if _, err := s.db.Exec(`INSERT INTO work(id,primary_code,title) VALUES(1,'RJ00000000',?)`, title); err != nil {
		t.Fatal(err)
	}
	cfg, _ := json.Marshal(map[string]any{"baseUrl": model.URL})
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", string(cfg), 200)

	body, _ := json.Marshal(map[string]any{"workId": 1, "field": "title", "text": title, "language": "zh-cn"})
	result := translationRequest(t, host, listener, "POST", "/api/content-translations", string(body), 200)
	if result["status"] != "native" || result["text"] != title || result["original"] != title || result["cacheStatus"] != "stored" {
		t.Fatalf("native result: %#v", result)
	}
	if calls.Load() != 0 {
		t.Fatalf("native title reached the model %d times", calls.Load())
	}
	var origin, modelName, template, translated string
	if err := s.db.QueryRow(`SELECT origin,model,template_version,translated_text FROM content_translation
		WHERE entity_kind='work' AND entity_id=1 AND field='title' AND language='zh-Hans'`).Scan(&origin, &modelName, &template, &translated); err != nil {
		t.Fatal(err)
	}
	if origin != "native" || modelName != "" || template != "" || translated != title {
		t.Fatalf("native row = (%s,%q,%q,%q)", origin, modelName, template, translated)
	}

	// Traditional display reuses the same Simplified source without a model.
	traditional, _ := json.Marshal(map[string]any{"workId": 1, "field": "title", "text": title, "language": "zh-tw"})
	result = translationRequest(t, host, listener, "POST", "/api/content-translations", string(traditional), 200)
	if result["status"] != "native" || result["text"] != title || result["language"] != "zh-tw" {
		t.Fatalf("native Traditional result: %#v", result)
	}
	if calls.Load() != 0 {
		t.Fatalf("native Traditional title reached the model %d times", calls.Load())
	}
}

func TestContentTranslationBatchAcceptsTwoHundredItems(t *testing.T) {
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"unexpected"},"finish_reason":"stop"}]}`)
	}))
	defer model.Close()
	s, host, admin, listener := translationHTTPFixture(t)
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	type wireItem struct {
		Key    string `json:"key"`
		WorkID int64  `json:"workId"`
		Field  string `json:"field"`
		Text   string `json:"text"`
	}
	// The client omits zero-valued fields, so a full page stays well inside the
	// request-body budget while still carrying 200 real work identities.
	const title = "例"
	items := make([]wireItem, 0, contentTranslationBatchLimit)
	for i := 0; i < contentTranslationBatchLimit; i++ {
		if _, err := tx.Exec(`INSERT INTO work(id,primary_code,title) VALUES(?,?,?)`, i+1, fmt.Sprintf("RJ%08d", i), title); err != nil {
			t.Fatal(err)
		}
		items = append(items, wireItem{Key: fmt.Sprint(i), WorkID: int64(i + 1), Field: "title", Text: title})
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	cfg, _ := json.Marshal(map[string]any{"baseUrl": model.URL})
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", string(cfg), 200)
	body, _ := json.Marshal(struct {
		Language string     `json:"language"`
		Items    []wireItem `json:"items"`
	}{"zh-cn", items})
	res := translationRequest(t, host, listener, "POST", "/api/content-translations/batch", string(body), 200)
	results := res["items"].([]any)
	if len(results) != contentTranslationBatchLimit {
		t.Fatalf("batch returned %d items", len(results))
	}
	for _, raw := range results {
		item := raw.(map[string]any)
		if item["status"] != "native" || item["language"] != "zh-cn" || item["code"] != nil {
			t.Fatalf("batch item: %#v", item)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("native batch reached the model %d times", calls.Load())
	}
}

// A batch item may identify its work by code, exactly like the single
// endpoint. The code is resolved inside the batch, not silently ignored.
func TestContentTranslationBatchResolvesWorkCodes(t *testing.T) {
	s, host, _, listener := translationHTTPFixture(t)
	if _, err := s.db.Exec(`INSERT INTO work(id,primary_code,title) VALUES(1,'RJ00000042','Example title')`); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{
		"language": "origin",
		"items": []map[string]any{{
			"key": "by-code", "code": "RJ00000042", "field": "title", "text": "Example title",
		}},
	})
	res := translationRequest(t, host, listener, "POST", "/api/content-translations/batch", string(body), 200)
	item := res["items"].([]any)[0].(map[string]any)
	if item["status"] != "original" || item["text"] != "Example title" {
		t.Fatalf("code batch item = %#v", item)
	}
}

func TestContentTranslationBatchRejectsTwoHundredAndOneItems(t *testing.T) {
	_, host, _, listener := translationHTTPFixture(t)
	items := make([]map[string]any, 0, contentTranslationBatchLimit+1)
	for i := 0; i <= contentTranslationBatchLimit; i++ {
		items = append(items, map[string]any{"key": fmt.Sprint(i), "workId": 1, "field": "title", "text": "x"})
	}
	body, _ := json.Marshal(map[string]any{"language": "zh-cn", "items": items})
	result := translationRequest(t, host, listener, "POST", "/api/content-translations/batch", string(body), 400)
	if result["code"] != "translation_invalid_input" {
		t.Fatalf("oversized batch: %#v", result)
	}
}

// One refused item must not cancel its neighbours.
func TestContentTranslationBatchIsolatesItemFailures(t *testing.T) {
	s, host, admin, listener := translationHTTPFixture(t)
	if _, err := s.db.Exec(`INSERT INTO work(id,primary_code,title) VALUES(1,'RJ00000000','深海少女的告白'),(2,'RJ00000001','浅海少年的告白')`); err != nil {
		t.Fatal(err)
	}
	cfg, _ := json.Marshal(map[string]any{"baseUrl": "https://model.example.invalid"})
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", string(cfg), 200)
	items := []contentTranslationBatchItem{
		{Key: "ok", contentTranslationRequest: contentTranslationRequest{WorkID: 1, Field: "title", Text: "深海少女的告白"}},
		{Key: "stale", contentTranslationRequest: contentTranslationRequest{WorkID: 2, Field: "title", Text: "wrong text"}},
		{Key: "missing", contentTranslationRequest: contentTranslationRequest{WorkID: 999, Field: "title", Text: "no such work"}},
		{Key: "bad-field", contentTranslationRequest: contentTranslationRequest{WorkID: 1, Field: "note", Text: "深海少女的告白"}},
	}
	body, _ := json.Marshal(contentTranslationBatchRequest{Language: "zh-cn", Items: items})
	res := translationRequest(t, host, listener, "POST", "/api/content-translations/batch", string(body), 200)
	byKey := map[string]map[string]any{}
	for _, raw := range res["items"].([]any) {
		item := raw.(map[string]any)
		byKey[item["key"].(string)] = item
	}
	if got := byKey["ok"]; got["status"] != "native" || got["text"] != "深海少女的告白" {
		t.Fatalf("healthy item: %#v", got)
	}
	if got := byKey["stale"]; got["status"] != "failed" || got["code"] != "translation_content_changed" || got["text"] != "" {
		t.Fatalf("stale item: %#v", got)
	}
	if got := byKey["missing"]; got["status"] != "failed" || got["code"] != "translation_content_unavailable" {
		t.Fatalf("missing item: %#v", got)
	}
	if got := byKey["bad-field"]; got["status"] != "failed" || got["code"] != "translation_invalid_input" {
		t.Fatalf("invalid field item: %#v", got)
	}
	translationRequest(t, host, nil, "POST", "/api/content-translations/batch", string(body), 401)
}

// A production page carries titles near the 20.3-character average, not the
// single character used by the compact bound test. The shared request decoder
// caps every translation body at 16 KiB, which a full page exceeds.
func TestContentTranslationBatchFitsProductionTitles(t *testing.T) {
	s, host, admin, listener := translationHTTPFixture(t)
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	type wireItem struct {
		Key    string `json:"key"`
		WorkID int64  `json:"workId"`
		Field  string `json:"field"`
		Text   string `json:"text"`
	}
	const title = "深海少女的告白与秘密的森林之中"
	items := make([]wireItem, 0, contentTranslationBatchLimit)
	for i := 0; i < contentTranslationBatchLimit; i++ {
		if _, err := tx.Exec(`INSERT INTO work(id,primary_code,title) VALUES(?,?,?)`, i+1, fmt.Sprintf("RJ%08d", i), title); err != nil {
			t.Fatal(err)
		}
		items = append(items, wireItem{Key: fmt.Sprint(i), WorkID: int64(i + 1), Field: "title", Text: title})
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	cfg, _ := json.Marshal(map[string]any{"baseUrl": "https://model.example.invalid"})
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", string(cfg), 200)
	body, _ := json.Marshal(map[string]any{"language": "zh-cn", "items": items})
	t.Logf("batch body = %d bytes", len(body))
	res := translationRequest(t, host, listener, "POST", "/api/content-translations/batch", string(body), 200)
	results := res["items"].([]any)
	if len(results) != contentTranslationBatchLimit {
		t.Fatalf("batch returned %d items", len(results))
	}
}

// A batch item that asks for a field the resource does not publish uses the
// same invalid-input code as the single-item endpoint. A different code would
// tell the client to retry a request that can never succeed.
func TestContentTranslationBatchReportsMissingFieldAsInvalidInput(t *testing.T) {
	_, host, admin, _ := translationHTTPFixture(t)
	result := translationRequest(t, host, admin, "POST", "/api/content-translations/batch", `{"language":"zh-cn","items":[{"key":"missing","sourceId":7,"code":"RJ00000000","field":"description","text":"Example"}]}`, 200)
	items, _ := result["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("batch response: %#v", result)
	}
	item := items[0].(map[string]any)
	if item["status"] != "failed" || item["code"] != "translation_invalid_input" {
		t.Fatalf("missing field response: %#v", item)
	}
}
