package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestContentTranslationReadsOnlyPublicWorkFields(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "Example title") || strings.Contains(string(body), "Private note") {
			t.Error("unexpected model input")
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"示例标题"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()
	s, host, admin, listener := translationHTTPFixture(t)
	if _, err := s.db.Exec(`INSERT INTO work (id, primary_code, title, description) VALUES (1, 'RJ00000000', 'Example title', 'Example description')`); err != nil {
		t.Fatal(err)
	}
	cfg, _ := json.Marshal(map[string]any{"baseUrl": upstream.URL, "model": "Example Model"})
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", string(cfg), 200)
	const path = "/api/content-translations"
	result := translationRequest(t, host, listener, "POST", path, `{"workId":1,"field":"title","text":"Example title","language":"zh-cn"}`, 200)
	if result["text"] != "示例标题" || result["original"] != "Example title" || result["status"] != "translated" {
		t.Fatalf("unexpected translation: %#v", result)
	}
	translationRequest(t, host, listener, "POST", path, `{"workId":1,"field":"note","text":"Private note","language":"zh-cn"}`, 400)
	translationRequest(t, host, listener, "POST", path, `{"workId":1,"field":"title","text":"Private note","language":"zh-cn"}`, 409)
	translationRequest(t, host, nil, "POST", path, `{"workId":1,"field":"title","text":"Example title","language":"zh-cn"}`, 401)
	origin := translationRequest(t, host, listener, "POST", path, `{"workId":1,"field":"title","text":"Example title","language":"origin"}`, 200)
	if origin["text"] != "Example title" || calls.Load() != 1 {
		t.Fatalf("origin triggered translation: %#v; calls=%d", origin, calls.Load())
	}
}

func TestContentTranslationSharesBudgetDeduplicatesAndCancelsQueue(t *testing.T) {
	started := make(chan struct{}, 8)
	release := make(chan struct{})
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		number := calls.Add(1)
		started <- struct{}{}
		if number == 1 {
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"示例译文"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()
	defer close(release)
	s, host, admin, listener := translationHTTPFixture(t)
	if _, err := s.db.Exec(`INSERT INTO work (id,primary_code,title) VALUES (1,'RJ00000000','Example title'),(2,'RJ00000001','Another title'),(3,'RJ00000002','Third title')`); err != nil {
		t.Fatal(err)
	}
	cfg, _ := json.Marshal(map[string]any{"baseUrl": upstream.URL, "queueLimit": 1})
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", string(cfg), 200)
	firstBody := `{"workId":1,"field":"title","text":"Example title","language":"zh-cn"}`
	async := func(ctx context.Context, body string) <-chan map[string]any {
		done := make(chan map[string]any, 1)
		go func() {
			req, _ := http.NewRequestWithContext(ctx, "POST", host.URL+"/api/content-translations", strings.NewReader(body))
			req.AddCookie(listener)
			req.Header.Set("Content-Type", "application/json")
			res, err := host.Client().Do(req)
			if err != nil {
				done <- nil
				return
			}
			defer func() { _ = res.Body.Close() }()
			var result map[string]any
			_ = json.NewDecoder(res.Body).Decode(&result)
			done <- result
		}()
		return done
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := async(ctx, firstBody)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("translation did not start")
	}
	busy := translationRequest(t, host, admin, "POST", "/api/settings/translation/test", `{"text":"Hello.","targetLanguage":"en"}`, 429)
	if busy["code"] != "translation_busy" {
		t.Fatal("trial bypassed shared budget")
	}
	duplicate := async(ctx, firstBody)
	queuedCtx, cancelQueued := context.WithCancel(ctx)
	defer cancelQueued()
	queued := async(queuedCtx, `{"workId":2,"field":"title","text":"Another title","language":"zh-cn"}`)
	waitQueue := func(want float64) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			state := translationRequest(t, host, admin, "GET", "/api/settings/translation/runtime", "", 200)
			if state["queued"] == want {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("queue state did not settle")
	}
	waitQueue(1)
	full := translationRequest(t, host, listener, "POST", "/api/content-translations", `{"workId":3,"field":"title","text":"Third title","language":"zh-cn"}`, 200)
	if full["status"] != "failed" || full["code"] != "translation_queue_full" || full["text"] != "Third title" {
		t.Fatalf("full queue did not preserve original: %#v", full)
	}
	cancelQueued()
	<-queued
	waitQueue(0)
	// Releasing both readers cancels their one shared upstream call.
	cancel()
	<-first
	<-duplicate
	waitQueue(0)
	if calls.Load() != 1 {
		t.Fatalf("duplicate or cancelled queued work generated: %d", calls.Load())
	}
}

func TestContentTranslationProtectsNamesAndReusesAuthoredLanguage(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var payload struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		prompt := payload.Messages[0].Content
		if strings.Contains(prompt, "Example Voice") || strings.Contains(prompt, "Example Circle") {
			t.Error("protected names reached model")
		}
		parts := strings.SplitN(prompt, "\n\n", 2)
		content := strings.ReplaceAll(parts[1], "Example title", "示例标题")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}, "finish_reason": "stop"}}})
	}))
	defer upstream.Close()
	s, host, admin, listener := translationHTTPFixture(t)
	_, err := s.db.Exec(`INSERT INTO work (id,primary_code,title) VALUES (1,'RJ00000000','Example title by Example Voice and Example Circle');
 INSERT INTO person (id,display_name) VALUES (1,'Example Voice');
 INSERT INTO work_credit (work_id,person_id,role) VALUES (1,1,'voice_actor');
 INSERT INTO party (id,display_name) VALUES (1,'Example Circle');
 INSERT INTO work_party (work_id,party_id) VALUES (1,1);`)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := json.Marshal(map[string]any{"baseUrl": upstream.URL})
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", string(cfg), 200)
	const body = `{"workId":1,"field":"title","text":"Example title by Example Voice and Example Circle","language":"zh-cn"}`
	result := translationRequest(t, host, listener, "POST", "/api/content-translations", body, 200)
	if result["text"] != "示例标题 by Example Voice and Example Circle" {
		t.Fatalf("names changed: %#v", result)
	}
	translationRequest(t, host, admin, "PATCH", "/api/works/1/manual-overrides", `{"titles":{"zh-cn":"手写标题 Example Voice"}}`, 200)
	result = translationRequest(t, host, listener, "POST", "/api/content-translations", body, 200)
	if result["text"] != "手写标题 Example Voice" || result["status"] != "existing" || calls.Load() != 1 {
		t.Fatalf("authored target was not reused: %#v; calls=%d", result, calls.Load())
	}
	origin := translationRequest(t, host, listener, "POST", "/api/content-translations", `{"workId":1,"field":"title","text":"手写标题 Example Voice","language":"origin"}`, 200)
	if origin["text"] != "Example title by Example Voice and Example Circle" || calls.Load() != 1 {
		t.Fatalf("original selection kept projected title: %#v", origin)
	}
}

func TestContentTranslationCacheInvalidatesAndHonorsDiskBudget(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"示例译文"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()
	s, host, admin, listener := translationHTTPFixture(t)
	s.cfg.CacheRoot = t.TempDir()
	if _, err := s.db.Exec(`INSERT INTO work (id,primary_code,title) VALUES (1,'RJ00000000','Example title')`); err != nil {
		t.Fatal(err)
	}
	cfg, _ := json.Marshal(map[string]any{"baseUrl": upstream.URL, "concurrency": 1, "queueLimit": 32, "cacheBytes": 4096, "minFreeBytes": 0})
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", string(cfg), 200)
	const body = `{"workId":1,"field":"title","text":"Example title","language":"zh-cn"}`
	for range 2 {
		translationRequest(t, host, listener, "POST", "/api/content-translations", body, 200)
	}
	if calls.Load() != 1 {
		t.Fatalf("cache missed: calls=%d", calls.Load())
	}
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", `{"model":"Example Model 2"}`, 200)
	translationRequest(t, host, listener, "POST", "/api/content-translations", body, 200)
	if calls.Load() != 2 {
		t.Fatal("model change reused stale cache")
	}
	runtime := translationRequest(t, host, admin, "GET", "/api/settings/translation/runtime", "", 200)
	if runtime["cacheEntries"] != float64(1) || runtime["cacheBytes"].(float64) > 4096 {
		t.Fatalf("cache quota not enforced: %#v", runtime)
	}
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", `{"minFreeBytes":1099511627776}`, 200)
	// Existing entries remain readable, but new text cannot write under low space.
	cached := translationRequest(t, host, listener, "POST", "/api/content-translations", body, 200)
	if cached["cacheStatus"] != "hit" {
		t.Fatalf("low disk lost cached text: %#v", cached)
	}
	if _, err := s.db.Exec(`UPDATE work SET title='Changed title' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	changed := translationRequest(t, host, listener, "POST", "/api/content-translations", strings.ReplaceAll(body, "Example title", "Changed title"), 200)
	if changed["cacheStatus"] != "paused_low_disk" || changed["text"] != "示例译文" {
		t.Fatalf("low disk did not report cache pause: %#v", changed)
	}
	translationRequest(t, host, listener, "GET", "/api/settings/translation/runtime", "", 403)
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", `{"minFreeBytes":0}`, 200)
	translationRequest(t, host, listener, "POST", "/api/content-translations", strings.ReplaceAll(body, "Example title", "Changed title"), 200)
	if calls.Load() != 4 {
		t.Fatalf("cache did not resume: calls=%d", calls.Load())
	}
}

func TestContentTranslationHandlesDescriptionTagsAndDirectoryWithoutChangingIdentity(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"示例翻译"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()
	s, host, admin, listener := translationHTTPFixture(t)
	if _, err := s.db.Exec(`INSERT INTO work (id,primary_code,title,description) VALUES (1,'RJ00000000','Example title','Example description'); INSERT INTO media_item (id,work_id,kind,title) VALUES (1,1,'audio','Example track.wav')`); err != nil {
		t.Fatal(err)
	}
	cfg, _ := json.Marshal(map[string]any{"baseUrl": upstream.URL})
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", string(cfg), 200)
	translationRequest(t, host, admin, "PUT", "/api/works/1/metadata-tags", `{"newTags":["Example tag"]}`, 200)
	for _, item := range []struct{ field, text string }{{"description", "Example description"}, {"tag", "Example tag"}, {"directory", "Example track.wav"}} {
		body, _ := json.Marshal(map[string]any{"workId": 1, "field": item.field, "text": item.text, "language": "zh-cn"})
		result := translationRequest(t, host, listener, "POST", "/api/content-translations", string(body), 200)
		if result["text"] != "示例翻译" || result["original"] != item.text {
			t.Fatalf("field %s: %#v", item.field, result)
		}
	}
	detail := translationRequest(t, host, listener, "GET", "/api/works/1?includeMedia=false", "", 200)
	if detail["id"] != float64(1) || detail["title"] != "Example title" || detail["description"] != "Example description" {
		t.Fatalf("translation changed work: %#v", detail)
	}
	if calls.Load() != 3 {
		t.Fatalf("field calls=%d", calls.Load())
	}
}
