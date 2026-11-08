package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yexca/kikoto/backend/internal/config"
)

// Selecting the original title is a public-field read, including its permission
// check. It must not assemble the whole detail page before returning that text.
func TestContentTranslationTitleReadHasBoundedQueries(t *testing.T) {
	db, counter := openCatalogCountingTestDB(t)
	s := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-editor", CacheRoot: t.TempDir()})
	createTestAdministrator(t, s, "synthetic-editor", "synthetic-password")
	metadataReviewExec(t, db, `INSERT INTO work(id,primary_code,title) VALUES(1,'RJ00000000','Example title')`)
	host := httptest.NewServer(s.Routes())
	defer host.Close()
	cookie := workReferenceLogin(t, host, "synthetic-editor")
	counter.reset()
	result := translationRequest(t, host, cookie, "POST", "/api/content-translations", `{"workId":1,"field":"title","text":"Example title","language":"origin"}`, 200)
	if result["text"] != "Example title" || result["status"] != "original" {
		t.Fatalf("original title response: %#v", result)
	}
	queries := len(counter.snapshot())
	t.Logf("title_read_queries=%d", queries)
	// Includes session, permission and configuration reads as well as the public
	// metadata and name-protection fields; no model or persistent-cache writes.
	if queries > 24 {
		t.Fatalf("title read used %d database statements, budget 24", queries)
	}
}

func TestContentTranslationProseKeepsManualFieldsAndProtectedNames(t *testing.T) {
	s, host, admin, listener := translationHTTPFixture(t)
	metadataReviewExec(t, s.db, `INSERT INTO work(id,primary_code,title,description) VALUES(1,'RJ00000000','Example title','Example description')`)
	translationRequest(t, host, admin, "PATCH", "/api/works/1/manual-overrides", `{"titles":{"":"Authored title Example Circle Example Voice"},"description":"Authored description Example Voice","circle":{"name":"Example Circle"},"voiceActors":[{"name":"Example Voice"}]}`, 200)
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		prompt := body.Messages[0].Content
		if strings.Contains(prompt, "Example Circle") || strings.Contains(prompt, "Example Voice") {
			t.Error("authored protected name reached the model")
		}
		_, text, _ := strings.Cut(prompt, "\n\n")
		text = strings.ReplaceAll(text, "Authored title", "手写标题")
		text = strings.ReplaceAll(text, "Authored description", "手写简介")
		text = strings.ReplaceAll(text, "Example description", "原始简介")
		encoded, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": text}, "finish_reason": "stop"}}})
		_, _ = w.Write(encoded)
	}))
	defer model.Close()
	cfg, _ := json.Marshal(map[string]any{"baseUrl": model.URL, "minFreeBytes": 0})
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", string(cfg), 200)
	detail := translationRequest(t, host, listener, "GET", "/api/works/1?includeMedia=false", "", 200)
	presentation := detail["metadataPresentation"].(map[string]any)
	variants := presentation["variants"].([]any)
	if len(variants) == 0 || variants[0].(map[string]any)["description"] != "Example description" {
		t.Fatalf("published original description missing: %#v", presentation)
	}
	for _, sample := range []struct{ field, text, translated, original string }{
		{"title", "Authored title Example Circle Example Voice", "手写标题 Example Circle Example Voice", "Authored title Example Circle Example Voice"},
		{"description", "Authored description Example Voice", "手写简介 Example Voice", "Example description"},
		{"description", "Example description", "原始简介", "Example description"},
	} {
		body, _ := json.Marshal(map[string]any{"workId": 1, "field": sample.field, "text": sample.text, "language": "zh-cn"})
		result := translationRequest(t, host, listener, "POST", "/api/content-translations", string(body), 200)
		if result["text"] != sample.translated || result["original"] != sample.original {
			t.Fatalf("authored %s response: %#v", sample.field, result)
		}
	}
}
