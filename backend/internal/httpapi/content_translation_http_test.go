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
		if !strings.Contains(string(body), "Example title") || strings.Contains(string(body), "Private note") || strings.Contains(string(body), "Private tag") {
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
	translationRequest(t, host, listener, "PATCH", "/api/works/1/user-state", `{"note":"Private note"}`, 200)
	translationRequest(t, host, listener, "PUT", "/api/works/1/tags", `{"tags":["Private tag"]}`, 200)
	const path = "/api/content-translations"
	result := translationRequest(t, host, listener, "POST", path, `{"workId":1,"field":"title","text":"Example title","language":"zh-cn"}`, 200)
	if result["text"] != "示例标题" || result["original"] != "Example title" || result["status"] != "translated" {
		t.Fatalf("unexpected translation: %#v", result)
	}
	translationRequest(t, host, listener, "POST", path, `{"workId":1,"field":"note","text":"Private note","language":"zh-cn"}`, 400)
	translationRequest(t, host, listener, "POST", path, `{"workId":1,"field":"title","text":"Private note","language":"zh-cn"}`, 409)
	translationRequest(t, host, listener, "POST", path, `{"workId":1,"field":"tag","text":"Private tag","language":"zh-cn"}`, 409)
	translationRequest(t, host, nil, "POST", path, `{"workId":1,"field":"title","text":"Example title","language":"zh-cn"}`, 401)
	origin := translationRequest(t, host, listener, "POST", path, `{"workId":1,"field":"title","text":"Example title","language":"origin"}`, 200)
	if origin["text"] != "Example title" || calls.Load() != 1 {
		t.Fatalf("origin triggered translation: %#v; calls=%d", origin, calls.Load())
	}
	detail := translationRequest(t, host, listener, "GET", "/api/works/1?includeMedia=false", "", 200)
	if detail["personalNote"] != "Private note" || detail["userTags"].([]any)[0].(map[string]any)["name"] != "Private tag" {
		t.Fatalf("translation changed private content: %#v", detail)
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
	cfg, _ := json.Marshal(map[string]any{"baseUrl": upstream.URL, "concurrency": 1, "queueLimit": 1})
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
		var last map[string]any
		for time.Now().Before(deadline) {
			state := translationRequest(t, host, admin, "GET", "/api/settings/translation/runtime", "", 200)
			last = state["queue"].(map[string]any)
			if last["queued"] == want {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("queue state did not settle at %v: %#v", want, last)
	}
	waitQueue(1)
	// queueLimit bounds the background lane only. Interactive work has its own
	// fixed 256-slot cap, so a page of titles is still admitted while a prefill
	// pass would already be refused. The interactive bound itself is covered by
	// TestQueueRejectsWhenTheInteractiveLaneIsFull, where 256 pending jobs are
	// cheap to construct without 256 HTTP clients.
	thirdCtx, cancelThird := context.WithCancel(ctx)
	defer cancelThird()
	third := async(thirdCtx, `{"workId":3,"field":"title","text":"Third title","language":"zh-cn"}`)
	waitQueue(2)
	cancelThird()
	<-third
	waitQueue(1)
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

// A catalog card and its series title belong to the same release as their
// registered work. The circle and cast are protected there too, even though the
// request names the catalog entry rather than the work.
func TestContentTranslationProtectsNamesForCatalogAndSeries(t *testing.T) {
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
			t.Error("protected catalog names reached the model")
		}
		parts := strings.SplitN(prompt, "\n\n", 2)
		content := strings.ReplaceAll(parts[1], "Example title", "示例标题")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}, "finish_reason": "stop"}}})
	}))
	defer upstream.Close()
	s, host, admin, listener := translationHTTPFixture(t)
	_, err := s.db.Exec(`INSERT INTO work (id,primary_code,title,description) VALUES (1,'','Example title','Example description')
		,(2,'','Example episode','Example episode description');
		INSERT INTO person (id,display_name) VALUES (1,'Example Voice');
		INSERT INTO work_credit (work_id,person_id,role) VALUES (1,1,'voice_actor'),(2,1,'voice_actor');
		INSERT INTO party (id,display_name) VALUES (1,'Example Circle');
		INSERT INTO work_party (work_id,party_id) VALUES (1,1),(2,1);
		INSERT INTO file_source(id,code,display_name,source_type) VALUES (7,'example_catalog','Example Catalog','openlist');
		INSERT INTO file_source_endpoint(file_source_id,api_url,base_url) VALUES (7,'https://catalog.example.invalid','https://catalog.example.invalid');
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,work_id) VALUES (1,7,'/Example title','Example title',1);
		INSERT INTO catalog_series(id,title) VALUES (1,'Example title');
		INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence) VALUES (1,1,1,'manual');`)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := json.Marshal(map[string]any{"baseUrl": upstream.URL})
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", string(cfg), 200)
	cases := []string{
		`{"catalogEntryId":1,"field":"title","text":"Example title","language":"zh-cn"}`,
		`{"seriesId":1,"field":"title","text":"Example title","language":"zh-cn"}`,
	}
	for _, body := range cases {
		result := translationRequest(t, host, listener, "POST", "/api/content-translations", body, 200)
		if result["text"] != "示例标题" {
			t.Fatalf("catalog translation = %#v", result)
		}
	}
	if calls.Load() == 0 {
		t.Fatal("catalog translation did not reach the configured model")
	}
}

func TestContentTranslationInvalidatesOnModelChangeAndReportsSoftBudget(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"示例译文"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()
	s, host, admin, listener := translationHTTPFixture(t)
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
	languages := runtime["languages"].([]any)
	if len(languages) != 1 {
		t.Fatalf("language summary: %#v", runtime)
	}
	// The corpus keeps one row per model instead of overwriting a single cache
	// entry, so the previous model's text stays searchable.
	zh := languages[0].(map[string]any)
	if zh["language"] != "zh-Hans" || zh["rows"] != float64(2) || zh["model"] != float64(2) || runtime["softBytes"] != float64(4096) {
		t.Fatalf("soft budget reported as enforced: %#v", runtime)
	}
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", `{"minFreeBytes":1099511627776,"cacheBytes":4096}`, 200)
	cached := translationRequest(t, host, listener, "POST", "/api/content-translations", body, 200)
	if cached["cacheStatus"] != "hit" {
		t.Fatalf("soft budget lost a stored row: %#v", cached)
	}
	if _, err := s.db.Exec(`UPDATE work SET title='Changed title' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	changed := translationRequest(t, host, listener, "POST", "/api/content-translations", strings.ReplaceAll(body, "Example title", "Changed title"), 200)
	if changed["cacheStatus"] != "stored" || changed["text"] != "示例译文" {
		t.Fatalf("a soft budget blocked a write: %#v", changed)
	}
	translationRequest(t, host, listener, "GET", "/api/settings/translation/runtime", "", 403)
	if calls.Load() != 3 {
		t.Fatalf("unexpected generations: calls=%d", calls.Load())
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

// A remote work listing has no description field. The API must reject the
// request as invalid input rather than reporting a stale-read conflict.
func TestContentTranslationRejectsRemoteDescription(t *testing.T) {
	_, host, admin, _ := translationHTTPFixture(t)
	result := translationRequest(t, host, admin, "POST", "/api/content-translations", `{"sourceId":7,"code":"RJ00000000","field":"description","text":"Example","language":"zh-cn"}`, 400)
	if result["code"] != "translation_invalid_input" {
		t.Fatalf("remote description response: %#v", result)
	}
}

// An edition that already carries a target-language description answers the
// request without another model call.
func TestContentTranslationReusesAnExistingEditionDescription(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"generated"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()
	s, host, admin, listener := translationHTTPFixture(t)
	if _, err := s.db.Exec(`INSERT INTO work(id,primary_code,title,description) VALUES(1,'RJ00000000','Example title','Example description'),(2,'RJ00000001','Example edition','Authored Chinese description');
	INSERT INTO logical_work(id,canonical_work_id,canonical_code) VALUES(101,1,'RJ00000000');
	INSERT INTO work_edition(work_id,logical_work_id,primary_code,metadata_language,edition_label,is_canonical) VALUES
		(1,101,'RJ00000000','ja-jp','Original',1),(2,101,'RJ00000001','zh-cn','Translated',0);
	INSERT INTO dlsite_metadata_variant(id,logical_work_id,work_id,provider_id,external_id,edition_language,title)
	SELECT 1,101,2,id,'RJ00000001','zh-cn','Example edition' FROM metadata_provider WHERE code='dlsite';`); err != nil {
		t.Fatal(err)
	}
	cfg, _ := json.Marshal(map[string]any{"baseUrl": upstream.URL, "minFreeBytes": 0})
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", string(cfg), 200)
	result := translationRequest(t, host, listener, "POST", "/api/content-translations", `{"workId":2,"field":"description","text":"Authored Chinese description","language":"zh-cn"}`, 200)
	if result["status"] != "existing" || result["text"] != "Authored Chinese description" || calls.Load() != 0 {
		t.Fatalf("existing description was not reused: %#v calls=%d", result, calls.Load())
	}
}

// A source context does not hide a higher-priority resource. When the request
// also names a card, the card supplies the field.
func TestContentTranslationRemoteDescriptionUsesAHigherPriorityResource(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"示例简介"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()
	s, host, admin, listener := translationHTTPFixture(t)
	if _, err := s.db.Exec(`INSERT INTO work(id,primary_code,title,description) VALUES(1,'RJ00000000','Example title','Example description');`); err != nil {
		t.Fatal(err)
	}
	source := translationRequest(t, host, admin, "POST", "/api/file-sources", `{"displayName":"Example Remote A","sourceType":"openlist","enabled":true,"endpoint":{"apiUrl":"https://source.example.invalid","baseUrl":"https://source.example.invalid"}}`, 201)
	sourceID := int64(source["id"].(float64))
	if _, err := s.db.Exec(`INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,work_id) VALUES(3,?,'/Example Work','Example title',1)`, sourceID); err != nil {
		t.Fatal(err)
	}
	cfg, _ := json.Marshal(map[string]any{"baseUrl": upstream.URL, "minFreeBytes": 0})
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", string(cfg), 200)
	result := translationRequest(t, host, listener, "POST", "/api/content-translations", `{"catalogEntryId":3,"sourceId":7,"workId":1,"field":"title","text":"Example title","language":"zh-cn"}`, 200)
	if result["text"] != "示例简介" || calls.Load() != 1 {
		t.Fatalf("higher-priority resource was not used: %#v calls=%d", result, calls.Load())
	}
}

// A catalog card that carries the work's own title reuses the work's existing
// target-language edition instead of paying for another model call.
func TestContentTranslationReusesAWorkTitleOnACatalogCard(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"generated"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()
	s, host, admin, listener := translationHTTPFixture(t)
	if _, err := s.db.Exec(`INSERT INTO work(id,primary_code,title) VALUES(1,'RJ00000000','Example title'),(2,'RJ00000001','示例标题');
		INSERT INTO logical_work(id,canonical_work_id,canonical_code) VALUES(101,1,'RJ00000000');
		INSERT INTO work_edition(work_id,logical_work_id,primary_code,metadata_language,is_canonical) VALUES(1,101,'RJ00000000','ja-jp',1),(2,101,'RJ00000001','zh-cn',0);
		INSERT INTO dlsite_metadata_variant(id,logical_work_id,work_id,provider_id,external_id,edition_language,title)
		SELECT 1,101,2,id,'RJ00000001','zh-cn','示例标题' FROM metadata_provider WHERE code='dlsite';
		INSERT INTO file_source(id,code,display_name,source_type,enabled) VALUES(7,'example_remote_a','Example Remote A','openlist',1);
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,work_id) VALUES(3,7,'/Example Work','Example title',1);`); err != nil {
		t.Fatal(err)
	}
	cfg, _ := json.Marshal(map[string]any{"baseUrl": upstream.URL, "minFreeBytes": 0})
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", string(cfg), 200)
	result := translationRequest(t, host, listener, "POST", "/api/content-translations", `{"catalogEntryId":3,"field":"title","text":"Example title","language":"zh-cn"}`, 200)
	if result["status"] != "existing" || result["text"] != "示例标题" || calls.Load() != 0 {
		t.Fatalf("catalog card did not reuse the work title: %#v calls=%d", result, calls.Load())
	}
}

// An unregistered catalog card has no work row, but its release sidecar still
// names the circle and cast. Those names are published identity and must not be
// sent to the model inside a title.
func TestContentTranslationProtectsSidecarNamesOfAnUnregisteredCard(t *testing.T) {
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
		if strings.Contains(prompt, "Example Circle") || strings.Contains(prompt, "Example Voice") {
			t.Error("sidecar names reached the model")
		}
		parts := strings.SplitN(prompt, "\n\n", 2)
		body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": parts[1]}, "finish_reason": "stop"}}})
		_, _ = w.Write(body)
	}))
	defer upstream.Close()
	s, host, admin, listener := translationHTTPFixture(t)
	sidecar, _ := json.Marshal(map[string]any{"sidecars": []any{map[string]any{
		"relativePath": "readme.txt", "size": 10, "voiceActors": []string{"Example Voice"},
		"circles": []string{"Example Circle"}, "fetchedAt": "2026-01-01T00:00:00Z",
	}}})
	if _, err := s.db.Exec(`INSERT INTO file_source(id,code,display_name,source_type,enabled) VALUES(7,'example_remote_a','Example Remote A','openlist',1);
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,sidecar_json) VALUES(3,7,'/Example Work','A collection by Example Circle with Example Voice',?)`, string(sidecar)); err != nil {
		t.Fatal(err)
	}
	cfg, _ := json.Marshal(map[string]any{"baseUrl": upstream.URL, "minFreeBytes": 0})
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", string(cfg), 200)
	result := translationRequest(t, host, listener, "POST", "/api/content-translations", `{"catalogEntryId":3,"field":"title","text":"A collection by Example Circle with Example Voice","language":"zh-cn"}`, 200)
	if result["status"] != "translated" || result["text"] != "A collection by Example Circle with Example Voice" || calls.Load() != 1 {
		t.Fatalf("sidecar-protected title was not translated safely: %#v calls=%d", result, calls.Load())
	}
}
