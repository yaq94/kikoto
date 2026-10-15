package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestContentTranslationTraditionalSharesSimplifiedAndPreservesNames(t *testing.T) {
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var input struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
			return
		}
		prompt := input.Messages[0].Content
		if !strings.HasPrefix(prompt, "请将以下文本翻译为简体中文") || strings.Contains(prompt, "示例社团") || strings.Contains(prompt, "示例声优") {
			t.Error("model received the wrong language or a protected name")
		}
		_, text, _ := strings.Cut(prompt, "\n\n")
		text = strings.ReplaceAll(text, "Example title", "风吹过森林，听见音乐")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": text}, "finish_reason": "stop"}}})
	}))
	defer model.Close()
	s, host, admin, listener := translationHTTPFixture(t)
	metadataReviewExec(t, s.db, `INSERT INTO work(id,primary_code,title) VALUES(1,'RJ00000000','Example title 示例社团 示例声优');
		INSERT INTO party(id,display_name) VALUES(1,'示例社团');
		INSERT INTO work_party(work_id,party_id) VALUES(1,1);
		INSERT INTO person(id,display_name) VALUES(1,'示例声优');
		INSERT INTO work_credit(work_id,person_id,role) VALUES(1,1,'voice_actor');`)
	cfg, _ := json.Marshal(map[string]any{"baseUrl": model.URL, "minFreeBytes": 0})
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", string(cfg), 200)
	translationRequest(t, host, listener, "PATCH", "/api/works/1/user-state", `{"note":"个人备注听音乐"}`, 200)
	translationRequest(t, host, listener, "PUT", "/api/works/1/tags", `{"tags":["个人标签听音乐"]}`, 200)
	const original = "Example title 示例社团 示例声优"
	for _, sample := range []struct{ language, text, cache string }{
		{"zh-tw", "風吹過森林，聽見音樂 示例社团 示例声优", "stored"},
		{"zh-cn", "风吹过森林，听见音乐 示例社团 示例声优", "hit"},
		{"zh-tw", "風吹過森林，聽見音樂 示例社团 示例声优", "hit"},
	} {
		body, _ := json.Marshal(map[string]any{"workId": 1, "field": "title", "text": original, "language": sample.language})
		result := translationRequest(t, host, listener, "POST", "/api/content-translations", string(body), 200)
		if result["status"] != "translated" || result["text"] != sample.text || result["original"] != original || result["language"] != sample.language || result["cacheStatus"] != sample.cache {
			t.Fatalf("script switching result: %#v", result)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("script switching generated %d times", calls.Load())
	}
	translationRequest(t, host, nil, "POST", "/api/content-translations", `{"workId":1,"field":"title","text":"Example title 示例社团 示例声优","language":"zh-tw"}`, 401)
	translationRequest(t, host, listener, "POST", "/api/content-translations", `{"workId":1,"field":"tag","text":"个人标签听音乐","language":"zh-tw"}`, 409)
	translationRequest(t, host, listener, "POST", "/api/content-translations", `{"workId":1,"field":"note","text":"个人备注听音乐","language":"zh-tw"}`, 400)
	detail := translationRequest(t, host, listener, "GET", "/api/works/1?includeMedia=false", "", 200)
	if detail["id"] != float64(1) || detail["title"] != original || detail["personalNote"] != "个人备注听音乐" || detail["userTags"].([]any)[0].(map[string]any)["name"] != "个人标签听音乐" || calls.Load() != 1 {
		t.Fatalf("conversion changed identity/private text or called model: %#v", detail)
	}
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", `{"model":"Example replacement model"}`, 200)
	translationRequest(t, host, listener, "POST", "/api/content-translations", `{"workId":1,"field":"title","text":"Example title 示例社团 示例声优","language":"zh-tw"}`, 200)
	if calls.Load() != 2 {
		t.Fatal("Traditional reused a different model's Simplified translation")
	}
}

func TestContentTranslationTraditionalCatalogKeepsPathsAndChecksAccessBeforeCache(t *testing.T) {
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"风吹过森林"},"finish_reason":"stop"}]}`)
	}))
	defer model.Close()
	s, host, admin, listener := translationHTTPFixture(t)
	metadataReviewExec(t, s.db, `INSERT INTO file_source(id,code,display_name,source_type) VALUES(7,'example_remote_a','Example Remote A','openlist');
		INSERT INTO file_source_endpoint(file_source_id,base_url,api_url) VALUES(7,'https://source.example.invalid','https://source.example.invalid');
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title) VALUES(1,7,'/Example Work','Example Work');
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES(1,'音乐','Example folder','folder',0),(1,'音乐/风声.wav','Example track','audio',100);`)
	cfg, _ := json.Marshal(map[string]any{"baseUrl": model.URL, "minFreeBytes": 0})
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", string(cfg), 200)
	for _, language := range []string{"zh-cn", "zh-tw", "zh-cn"} {
		body, _ := json.Marshal(map[string]any{"catalogEntryId": 1, "field": "directory", "text": "Example track", "path": "音乐/风声.wav", "language": language})
		result := translationRequest(t, host, listener, "POST", "/api/content-translations", string(body), 200)
		want := "风吹过森林"
		if language == "zh-tw" {
			want = "風吹過森林"
		}
		if result["text"] != want || result["original"] != "Example track" {
			t.Fatalf("catalog directory script: %#v", result)
		}
	}
	tracks := translationRequest(t, host, listener, "GET", "/api/source-catalog/1/tracks", "", 200)
	folder := tracks["tracks"].([]any)[0].(map[string]any)
	track := folder["children"].([]any)[0].(map[string]any)
	if track["title"] != "Example track" || track["path"] != "音乐/风声.wav" || calls.Load() != 1 {
		t.Fatalf("catalog identity or cache changed: %#v", tracks)
	}
	if entry := translationRequest(t, host, listener, "GET", "/api/source-catalog/1", "", 200); entry["workId"] != nil || entry["catalogEntryId"] != float64(1) {
		t.Fatal("conversion registered or replaced catalog identity")
	}
	translationRequest(t, host, listener, "POST", "/api/content-translations", `{"catalogEntryId":1,"field":"directory","text":"Example track","path":"音樂/風聲.wav","language":"zh-tw"}`, 404)
	metadataReviewExec(t, s.db, `UPDATE file_source SET enabled=0 WHERE id=7`)
	translationRequest(t, host, listener, "POST", "/api/content-translations", `{"catalogEntryId":1,"field":"directory","text":"Example track","path":"音乐/风声.wav","language":"zh-tw"}`, 503)
	if calls.Load() != 1 {
		t.Fatal("unreadable catalog triggered generation")
	}
}

func TestContentTranslationTraditionalAndSimplifiedShareInflightGeneration(t *testing.T) {
	started, release := make(chan struct{}, 2), make(chan struct{})
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		started <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"听见音乐"},"finish_reason":"stop"}]}`)
	}))
	defer model.Close()
	s, host, admin, listener := translationHTTPFixture(t)
	defer close(release)
	metadataReviewExec(t, s.db, `INSERT INTO work(id,primary_code,title) VALUES(1,'RJ00000000','Example title')`)
	cfg, _ := json.Marshal(map[string]any{"baseUrl": model.URL, "minFreeBytes": 0})
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", string(cfg), 200)
	request := func(language string) <-chan map[string]any {
		done := make(chan map[string]any, 1)
		go func() {
			req, _ := http.NewRequest("POST", host.URL+"/api/content-translations", strings.NewReader(fmt.Sprintf(`{"workId":1,"field":"title","text":"Example title","language":%q}`, language)))
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
	simplified := request("zh-cn")
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("Simplified generation did not start")
	}
	traditional := request("zh-tw")
	deadline := time.Now().Add(2 * time.Second)
	for {
		stats := translationRequest(t, host, admin, "GET", "/api/settings/translation/runtime", "", 200)
		if stats["readers"] == float64(2) && stats["active"] == float64(1) && stats["queued"] == float64(0) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("scripts did not share the active request: %#v", stats)
		}
		time.Sleep(10 * time.Millisecond)
	}
	release <- struct{}{}
	if result := <-simplified; result["text"] != "听见音乐" {
		t.Fatalf("Simplified response: %#v", result)
	}
	if result := <-traditional; result["text"] != "聽見音樂" || calls.Load() != 1 {
		t.Fatalf("Traditional response: %#v; calls=%d", result, calls.Load())
	}
}

func TestContentTranslationTraditionalConversionFailureKeepsOriginalAndCanRetry(t *testing.T) {
	s, host, admin, listener := translationHTTPFixture(t)
	metadataReviewExec(t, s.db, `INSERT INTO work(id,primary_code,title) VALUES(1,'RJ00000000','Example title')`)
	// Seed an oversized authored translation to exercise the conversion boundary
	// without submitting oversized arbitrary text through the public request.
	oversized, _ := json.Marshal(strings.Repeat("音乐", 50000))
	metadataReviewExec(t, s.db, `INSERT INTO work_manual_override(work_id,field_name,language,value_json) VALUES(1,'title','zh-cn',?)`, string(oversized))
	const body = `{"workId":1,"field":"title","text":"Example title","language":"zh-tw"}`
	failed := translationRequest(t, host, listener, "POST", "/api/content-translations", body, 200)
	if failed["status"] != "failed" || failed["code"] != "translation_conversion_failed" || failed["text"] != "Example title" || failed["original"] != "Example title" {
		t.Fatalf("conversion failure lost original/status: %#v", failed)
	}
	translationRequest(t, host, admin, "PATCH", "/api/works/1/manual-overrides", `{"titles":{"zh-cn":"听见音乐"}}`, 200)
	retried := translationRequest(t, host, listener, "POST", "/api/content-translations", body, 200)
	if retried["text"] != "聽見音樂" || retried["status"] != "translated" {
		t.Fatalf("conversion retry failed: %#v", retried)
	}
}

func TestContentTranslationTraditionalAuthoredTagUsesSimplified(t *testing.T) {
	s, host, admin, listener := translationHTTPFixture(t)
	metadataReviewExec(t, s.db, `INSERT INTO work(id,primary_code,title) VALUES(1,'RJ00000000','Example title')`)
	tags := translationRequest(t, host, admin, "PUT", "/api/works/1/metadata-tags", `{"newTags":["Example tag"]}`, 200)
	id := tags["tags"].([]any)[0].(map[string]any)["id"].(float64)
	translationRequest(t, host, admin, "PATCH", fmt.Sprintf("/api/metadata/tags/%.0f", id), `{"names":{"ja-jp":"例のタグ","zh-cn":"自然音乐"}}`, 200)
	result := translationRequest(t, host, listener, "POST", "/api/content-translations", `{"workId":1,"field":"tag","text":"Example tag","language":"zh-tw"}`, 200)
	if result["text"] != "自然音樂" || result["status"] != "translated" || result["original"] != "例のタグ" {
		t.Fatalf("system tag conversion: %#v", result)
	}
}

func TestContentTranslationTraditionalReusesAuthoredSimplifiedWithoutModel(t *testing.T) {
	s, host, admin, listener := translationHTTPFixture(t)
	metadataReviewExec(t, s.db, `INSERT INTO work(id,primary_code,title) VALUES(1,'RJ00000000','Example title 示例声优');
		INSERT INTO person(id,display_name) VALUES(1,'示例声优');
		INSERT INTO work_credit(work_id,person_id,role) VALUES(1,1,'voice_actor');`)
	translationRequest(t, host, admin, "PATCH", "/api/works/1/manual-overrides", `{"titles":{"zh-cn":"听见音乐 示例声优"}}`, 200)
	const body = `{"workId":1,"field":"title","text":"Example title 示例声优","language":"zh-tw"}`
	result := translationRequest(t, host, listener, "POST", "/api/content-translations", body, 200)
	if result["status"] != "translated" || result["text"] != "聽見音樂 示例声优" || result["original"] != "Example title 示例声优" {
		t.Fatalf("authored Simplified field was not converted: %#v", result)
	}
	// Authored changes take effect directly; neither script is a stale derivative.
	translationRequest(t, host, admin, "PATCH", "/api/works/1/manual-overrides", `{"titles":{"zh-cn":"森林里的风 示例声优"}}`, 200)
	result = translationRequest(t, host, listener, "POST", "/api/content-translations", body, 200)
	if result["text"] != "森林裏的風 示例声优" {
		t.Fatalf("conversion reused an outdated authored field: %#v", result)
	}
	translationRequest(t, host, admin, "PATCH", "/api/works/1/manual-overrides", `{"titles":{"zh-tw":"現成標題 示例声优"}}`, 200)
	result = translationRequest(t, host, listener, "POST", "/api/content-translations", body, 200)
	if result["status"] != "existing" || result["text"] != "現成標題 示例声优" {
		t.Fatalf("authored Traditional field lost precedence: %#v", result)
	}
}
