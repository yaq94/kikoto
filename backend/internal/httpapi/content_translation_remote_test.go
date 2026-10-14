package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestContentTranslationRemoteFieldsRecheckSourceBeforeCachedReads(t *testing.T) {
	var modelCalls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modelCalls.Add(1)
		var body struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if strings.Contains(body.Messages[0].Content, "Example Voice") || strings.Contains(body.Messages[0].Content, "Example Circle") {
			t.Error("remote name reached model")
		}
		value := strings.SplitN(body.Messages[0].Content, "\n\n", 2)[1]
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": "Translated: " + value}, "finish_reason": "stop"}}})
	}))
	defer model.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/workInfo/RJ00000001":
			_, _ = io.WriteString(w, `{"id":1,"source_id":"RJ00000001","title":"Example title by Example Voice","circle":{"id":1,"name":"Example Circle"},"vas":[{"id":"1","name":"Example Voice"}],"tags":[{"id":1,"name":"Example tag","i18n":{"zh-cn":{"name":"既有标签"}}}]}`)
		case "/api/tracks/1":
			_, _ = io.WriteString(w, `[{"type":"folder","title":"Example folder","children":[{"type":"audio","title":"Example track.wav","mediaStreamUrl":"https://media.example.invalid/track.wav"}]}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer source.Close()
	s, host, admin, listener := translationHTTPFixture(t)
	if _, err := s.db.Exec(`INSERT INTO file_source(id,code,display_name,source_type) VALUES(1,'example_remote_a','Example Remote A','kikoeru_compatible'); INSERT INTO file_source_endpoint(file_source_id,base_url,api_url) VALUES(1,?,?)`, source.URL, source.URL); err != nil {
		t.Fatal(err)
	}
	cfg, _ := json.Marshal(map[string]any{"baseUrl": model.URL})
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", string(cfg), 200)
	const title = `{"sourceId":1,"code":"RJ00000001","field":"title","text":"Example title by Example Voice","language":"zh-cn"}`
	for range 2 {
		result := translationRequest(t, host, listener, "POST", "/api/content-translations", title, 200)
		if result["text"] != "Translated: Example title by Example Voice" {
			t.Fatalf("remote title: %#v", result)
		}
	}
	tag := translationRequest(t, host, listener, "POST", "/api/content-translations", `{"sourceId":1,"code":"RJ00000001","field":"tag","text":"Example tag","language":"zh-cn"}`, 200)
	if tag["status"] != "existing" || tag["text"] != "既有标签" {
		t.Fatalf("existing remote tag: %#v", tag)
	}
	dir := translationRequest(t, host, listener, "POST", "/api/content-translations", `{"sourceId":1,"code":"RJ00000001","field":"directory","text":"Example track.wav","path":"Example folder/Example track.wav","language":"zh-cn"}`, 200)
	if dir["text"] != "Translated: Example track.wav" {
		t.Fatalf("directory: %#v", dir)
	}
	translationRequest(t, host, listener, "POST", "/api/content-translations", `{"sourceId":1,"code":"RJ00000001","field":"directory","text":"Example track.wav","path":"unlisted/Example track.wav","language":"zh-cn"}`, 409)
	if _, err := s.db.Exec(`UPDATE file_source SET enabled=0 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	translationRequest(t, host, listener, "POST", "/api/content-translations", title, 503)
	if modelCalls.Load() != 2 {
		t.Fatalf("unexpected generations: %d", modelCalls.Load())
	}
}
