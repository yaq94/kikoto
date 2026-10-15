package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestContentTranslationCatalogTitleChecksSourceBeforeCacheWithoutRegistration(t *testing.T) {
	var modelCalls, sourceCalls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modelCalls.Add(1)
		var input struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
			return
		}
		if len(input.Messages) != 1 || !strings.HasSuffix(input.Messages[0].Content, "\n\nExample catalog title") {
			t.Error("unexpected public model input")
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"示例目录标题"},"finish_reason":"stop"}]}`))
	}))
	defer model.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sourceCalls.Add(1)
		http.NotFound(w, r)
	}))
	defer source.Close()
	s, host, admin, listener := translationHTTPFixture(t)
	metadataReviewExec(t, s.db, `INSERT INTO file_source(id,code,display_name,source_type) VALUES(7,'example_remote_a','Example Remote A','openlist');
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title) VALUES(1,7,'/Example catalog title','Example catalog title');`)
	if _, err := s.db.Exec(`INSERT INTO file_source_endpoint(file_source_id,base_url,api_url) VALUES(7,?,?)`, source.URL, source.URL); err != nil {
		t.Fatal(err)
	}
	cfg, _ := json.Marshal(map[string]any{"baseUrl": model.URL, "minFreeBytes": 0})
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", string(cfg), 200)
	const body = `{"catalogEntryId":1,"field":"title","text":"Example catalog title","language":"zh-cn"}`
	for _, cacheStatus := range []string{"stored", "hit"} {
		result := translationRequest(t, host, listener, "POST", "/api/content-translations", body, 200)
		if result["text"] != "示例目录标题" || result["original"] != "Example catalog title" || result["status"] != "translated" || result["cacheStatus"] != cacheStatus {
			t.Fatalf("catalog title translation: %#v", result)
		}
	}
	original := translationRequest(t, host, listener, "POST", "/api/content-translations", strings.Replace(body, "zh-cn", "origin", 1), 200)
	if original["text"] != "Example catalog title" || original["status"] != "original" {
		t.Fatalf("original: %#v", original)
	}
	translationRequest(t, host, listener, "POST", "/api/content-translations", strings.Replace(body, "Example catalog title", "Private note", 1), 409)
	translationRequest(t, host, nil, "POST", "/api/content-translations", body, 401)
	translationRequest(t, host, listener, "POST", "/api/content-translations", strings.Replace(body, `"catalogEntryId":1`, `"catalogEntryId":999,"workId":1`, 1), 404)
	entry := translationRequest(t, host, listener, "GET", "/api/source-catalog/1", "", 200)
	if entry["workId"] != nil || entry["title"] != "Example catalog title" {
		t.Fatalf("catalog read registered or changed a work: %#v", entry)
	}
	if library := translationRequest(t, host, listener, "GET", "/api/works", "", 200); library["total"] != float64(0) {
		t.Fatal("translation registered a work")
	}
	if runs := translationRequest(t, host, admin, "GET", "/api/workflow-runs", "", 200); runs["total"] != float64(0) {
		t.Fatal("translation queued a workflow")
	}
	if sourceCalls.Load() != 0 {
		t.Fatalf("translation requested source content: %d", sourceCalls.Load())
	}
	// Changing an administrator source can independently schedule availability probes.
	disabled, _ := json.Marshal(map[string]any{"displayName": "Example Remote A", "sourceType": "openlist", "enabled": false, "config": map[string]any{"openListRoots": []string{"/Example catalog title"}}, "endpoint": map[string]any{"baseUrl": source.URL, "apiUrl": source.URL}})
	translationRequest(t, host, admin, "PATCH", "/api/file-sources/7", string(disabled), 200)
	translationRequest(t, host, listener, "POST", "/api/content-translations", body, 503)
	changedType, _ := json.Marshal(map[string]any{"displayName": "Example Remote A", "sourceType": "kikoeru_compatible", "enabled": true, "config": map[string]any{"requestLanguage": "en-US"}, "endpoint": map[string]any{"baseUrl": source.URL, "apiUrl": source.URL}})
	translationRequest(t, host, admin, "PATCH", "/api/file-sources/7", string(changedType), 200)
	translationRequest(t, host, listener, "POST", "/api/content-translations", body, 503)
	if modelCalls.Load() != 1 {
		t.Fatalf("unreadable source reached model: %d", modelCalls.Load())
	}
}

func TestContentTranslationCatalogDirectoryRequiresRealPath(t *testing.T) {
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"示例目录译文"},"finish_reason":"stop"}]}`))
	}))
	defer model.Close()
	s, host, admin, listener := translationHTTPFixture(t)
	metadataReviewExec(t, s.db, `INSERT INTO file_source(id,code,display_name,source_type) VALUES(7,'example_remote_a','Example Remote A','openlist');
		INSERT INTO file_source_endpoint(file_source_id,base_url,api_url) VALUES(7,'https://source.example.invalid','https://source.example.invalid');
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title) VALUES(1,7,'/Example Work','Example Work');
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES(1,'MP3','Example folder','folder',0),(1,'MP3/01 original.wav','Example track','audio',100);`)
	cfg, _ := json.Marshal(map[string]any{"baseUrl": model.URL, "minFreeBytes": 0})
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", string(cfg), 200)
	for _, field := range []struct{ path, title string }{{"MP3", "Example folder"}, {"MP3/01 original.wav", "Example track"}} {
		body, _ := json.Marshal(map[string]any{"catalogEntryId": 1, "field": "directory", "text": field.title, "path": field.path, "language": "zh-cn"})
		result := translationRequest(t, host, listener, "POST", "/api/content-translations", string(body), 200)
		if result["text"] != "示例目录译文" || result["original"] != field.title || result["status"] != "translated" {
			t.Fatalf("directory translation: %#v", result)
		}
		origin := strings.Replace(string(body), "zh-cn", "origin", 1)
		if result := translationRequest(t, host, listener, "POST", "/api/content-translations", origin, 200); result["text"] != field.title {
			t.Fatalf("directory original: %#v", result)
		}
	}
	translationRequest(t, host, listener, "POST", "/api/content-translations", `{"catalogEntryId":1,"field":"directory","text":"Example track","path":"Example track","language":"zh-cn"}`, 404)
	translationRequest(t, host, listener, "POST", "/api/content-translations", `{"catalogEntryId":1,"field":"directory","text":"Example track","language":"zh-cn"}`, 404)
	translationRequest(t, host, listener, "POST", "/api/content-translations", `{"catalogEntryId":1,"field":"directory","text":"Private note","path":"MP3/01 original.wav","language":"zh-cn"}`, 409)
	tracks := translationRequest(t, host, listener, "GET", "/api/source-catalog/1/tracks", "", 200)
	folder := tracks["tracks"].([]any)[0].(map[string]any)
	track := folder["children"].([]any)[0].(map[string]any)
	if folder["title"] != "Example folder" || folder["path"] != "MP3" || track["title"] != "Example track" || track["path"] != "MP3/01 original.wav" {
		t.Fatalf("translation changed file identity: %#v", tracks)
	}
	if entry := translationRequest(t, host, listener, "GET", "/api/source-catalog/1", "", 200); entry["workId"] != nil {
		t.Fatal("directory translation registered a work")
	}
	if calls.Load() != 2 {
		t.Fatalf("unverified directory sent to model: %d", calls.Load())
	}
}
