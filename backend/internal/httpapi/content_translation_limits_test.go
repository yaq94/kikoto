package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestContentTranslationLimitsAndTimeoutAreObservableWithoutAutomaticRetry(t *testing.T) {
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer model.Close()
	s, host, admin, listener := translationHTTPFixture(t)
	if _, err := s.db.Exec(`INSERT INTO work(id,primary_code,title) VALUES(1,'RJ00000000','Example title')`); err != nil {
		t.Fatal(err)
	}
	defaults := translationRequest(t, host, admin, "GET", "/api/settings/translation", "", 200)
	if defaults["concurrency"] != float64(1) || defaults["queueLimit"] != float64(32) || defaults["cacheBytes"] != float64(256<<20) || defaults["minFreeBytes"] != float64(2<<30) {
		t.Fatalf("invalid defaults: %#v", defaults)
	}
	for _, patch := range []string{`{"concurrency":0}`, `{"queueLimit":257}`, `{"cacheBytes":4095}`, `{"minFreeBytes":-1}`, `{"timeoutSeconds":61}`} {
		translationRequest(t, host, admin, "PATCH", "/api/settings/translation", patch, 400)
	}
	cfg, _ := json.Marshal(map[string]any{"baseUrl": model.URL, "timeoutSeconds": 1})
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", string(cfg), 200)
	result := translationRequest(t, host, listener, "POST", "/api/content-translations", `{"workId":1,"field":"title","text":"Example title","language":"zh-cn"}`, 200)
	if result["status"] != "failed" || result["code"] != "translation_timeout" || result["text"] != "Example title" || calls.Load() != 1 {
		t.Fatalf("timeout outcome: %#v calls=%d", result, calls.Load())
	}
	runtime := translationRequest(t, host, admin, "GET", "/api/settings/translation/runtime", "", 200)
	if runtime["cacheEntries"] != float64(0) {
		t.Fatalf("timeout cached: %#v", runtime)
	}
}

func TestContentTranslationUsesNewDiskLimitForInFlightResults(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var once sync.Once
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		started <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"Example result"},"finish_reason":"stop"}]}`)
	}))
	defer model.Close()
	defer once.Do(func() { close(release) })
	s, host, admin, listener := translationHTTPFixture(t)
	if _, err := s.db.Exec(`INSERT INTO work(id,primary_code,title) VALUES(1,'RJ00000000','Example title'),(2,'RJ00000001','Another title')`); err != nil {
		t.Fatal(err)
	}
	cfg, _ := json.Marshal(map[string]any{"baseUrl": model.URL, "concurrency": 2, "minFreeBytes": 0})
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", string(cfg), 200)
	results := make(chan map[string]any, 2)
	for _, body := range []string{`{"workId":1,"field":"title","text":"Example title","language":"zh-cn"}`, `{"workId":2,"field":"title","text":"Another title","language":"zh-cn"}`} {
		go func() {
			req, _ := http.NewRequest("POST", host.URL+"/api/content-translations", strings.NewReader(body))
			req.AddCookie(listener)
			req.Header.Set("Content-Type", "application/json")
			res, err := host.Client().Do(req)
			if err != nil {
				results <- nil
				return
			}
			defer func() { _ = res.Body.Close() }()
			var result map[string]any
			_ = json.NewDecoder(res.Body).Decode(&result)
			results <- result
		}()
	}
	for range 2 {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("configured concurrency did not admit two calls")
		}
	}
	runtime := translationRequest(t, host, admin, "GET", "/api/settings/translation/runtime", "", 200)
	if runtime["active"] != float64(2) {
		t.Fatalf("configured concurrency: %#v", runtime)
	}
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", `{"minFreeBytes":1099511627776,"cacheBytes":4096,"concurrency":1}`, 200)
	once.Do(func() { close(release) })
	for range 2 {
		result := <-results
		if result["cacheStatus"] != "paused_low_disk" || result["text"] != "Example result" {
			t.Fatalf("in-flight result ignored current quota: %#v", result)
		}
	}
	runtime = translationRequest(t, host, admin, "GET", "/api/settings/translation/runtime", "", 200)
	if runtime["cacheEntries"] != float64(0) {
		t.Fatalf("paused quota wrote files: %#v", runtime)
	}
}

func TestContentTranslationFailureIsNotCachedAndRetryPreservesOriginal(t *testing.T) {
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":""},"finish_reason":"stop"}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"Example translated title"},"finish_reason":"stop"}]}`)
	}))
	defer model.Close()
	s, host, admin, listener := translationHTTPFixture(t)
	if _, err := s.db.Exec(`INSERT INTO work(id,primary_code,title) VALUES(1,'RJ00000000','Example title')`); err != nil {
		t.Fatal(err)
	}
	cfg, _ := json.Marshal(map[string]any{"baseUrl": model.URL})
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", string(cfg), 200)
	const body = `{"workId":1,"field":"title","text":"Example title","language":"zh-cn"}`
	result := translationRequest(t, host, listener, "POST", "/api/content-translations", body, 200)
	if result["status"] != "failed" || result["text"] != "Example title" {
		t.Fatalf("failure lost source: %#v", result)
	}
	result = translationRequest(t, host, listener, "POST", "/api/content-translations", body, 200)
	if result["text"] != "Example translated title" || calls.Load() != 2 {
		t.Fatalf("retry did not generate: %#v", result)
	}
	// Current content is verified before any matching cache can be returned.
	if _, err := s.db.Exec(`DELETE FROM work WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	missing := translationRequest(t, host, listener, "POST", "/api/content-translations", body, 404)
	encoded, _ := json.Marshal(missing)
	if strings.Contains(string(encoded), "Example translated title") {
		t.Fatal("removed content escaped through cache")
	}
}
