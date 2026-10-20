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
)

type promptCapture struct {
	mu      sync.Mutex
	content string
}

func (p *promptCapture) Set(value string) {
	p.mu.Lock()
	p.content = value
	p.mu.Unlock()
}

func (p *promptCapture) Get() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.content
}

// translationEditionFamily stores one origin edition and one en-us edition of
// the same logical work. Manual titles stay on the origin work row.
func translationEditionFamily(t *testing.T, s *Server) {
	t.Helper()
	var providerID int64
	if err := s.db.QueryRow(`SELECT id FROM metadata_provider WHERE code='dlsite'`).Scan(&providerID); err != nil {
		t.Fatal(err)
	}
	metadataReviewExec(t, s.db, `INSERT INTO work(id,primary_code,title) VALUES(1,'RJ00000000','Example origin title'),(2,'RJ00000001','English Edition Title')`)
	metadataReviewExec(t, s.db, `INSERT INTO logical_work(id,canonical_work_id,canonical_code) VALUES(1,1,'RJ00000000')`)
	metadataReviewExec(t, s.db, `INSERT INTO work_edition(work_id,logical_work_id,provider_id,primary_code,metadata_language,is_canonical,translation_kind) VALUES
		(1,1,?,'RJ00000000','JPN',1,'origin'),(2,1,?,'RJ00000001','ENG',0,'third_party')`, providerID, providerID)
	metadataReviewExec(t, s.db, `INSERT INTO dlsite_metadata_variant(logical_work_id,work_id,provider_id,external_id,edition_language,request_locale,title) VALUES
		(1,1,?,'RJ00000000','JPN','ja-jp','Example origin title'),
		(1,2,?,'RJ00000001','ENG','en-us','English Edition Title')`, providerID, providerID)
}

// An en-us edition title is its own source text. A zh-cn manual title belongs
// to the origin text, so it must not answer the edition selection.
func TestContentTranslationEditionSelectionIgnoresAnotherVariantTitle(t *testing.T) {
	var prompt promptCapture
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if len(payload.Messages) != 1 {
			t.Errorf("unexpected model messages: %#v", payload.Messages)
		}
		prompt.Set(payload.Messages[0].Content)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"英文版标题译文"},"finish_reason":"stop"}]}`)
	}))
	defer model.Close()
	s, host, admin, listener := translationHTTPFixture(t)
	translationEditionFamily(t, s)
	translationRequest(t, host, admin, "PATCH", "/api/works/1/manual-overrides", `{"titles":{"zh-cn":"手写中文标题"}}`, 200)
	cfg, _ := json.Marshal(map[string]any{"baseUrl": model.URL, "minFreeBytes": 0})
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", string(cfg), 200)
	result := translationRequest(t, host, listener, "POST", "/api/content-translations", `{"workId":1,"field":"title","text":"English Edition Title","language":"zh-cn"}`, 200)
	if result["status"] != "translated" || result["text"] != "英文版标题译文" {
		t.Fatalf("edition selection returned another variant's title: %#v", result)
	}
	if result["original"] != "Example origin title" {
		t.Fatalf("original lost the canonical text: %#v", result)
	}
	if got := prompt.Get(); !strings.HasSuffix(got, "\n\nEnglish Edition Title") {
		t.Fatalf("model did not receive the selected edition text: %q", got)
	}
}

// A selected variant without any target-language field must reach the model
// unchanged instead of being replaced by the canonical work title.
func TestContentTranslationVariantTextReachesTheModel(t *testing.T) {
	var prompt promptCapture
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if len(payload.Messages) != 1 {
			t.Errorf("unexpected model messages: %#v", payload.Messages)
		}
		prompt.Set(payload.Messages[0].Content)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"英文版標題譯文"},"finish_reason":"stop"}]}`)
	}))
	defer model.Close()
	s, host, admin, listener := translationHTTPFixture(t)
	translationEditionFamily(t, s)
	cfg, _ := json.Marshal(map[string]any{"baseUrl": model.URL, "minFreeBytes": 0})
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", string(cfg), 200)
	result := translationRequest(t, host, listener, "POST", "/api/content-translations", `{"workId":1,"field":"title","text":"English Edition Title","language":"zh-tw"}`, 200)
	if result["status"] != "translated" || result["text"] != "英文版標題譯文" {
		t.Fatalf("variant translation: %#v", result)
	}
	got := prompt.Get()
	if !strings.HasSuffix(got, "\n\nEnglish Edition Title") {
		t.Fatalf("model did not receive the variant text: %q", got)
	}
	if strings.Contains(got, "Example origin title") {
		t.Fatalf("canonical text reached the model: %q", got)
	}
}

// Directory translation must read files through the resolved catalog entry so a
// relocated card still finds its successor's file list.
func TestContentTranslationRelocatedCatalogEntryUsesSuccessorFiles(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"继承条目译文"},"finish_reason":"stop"}]}`)
	}))
	defer model.Close()
	s, host, admin, listener := translationHTTPFixture(t)
	metadataReviewExec(t, s.db, `INSERT INTO file_source(id,code,display_name,source_type) VALUES(7,'example_remote_a','Example Remote A','openlist');
		INSERT INTO file_source_endpoint(file_source_id,base_url,api_url) VALUES(7,'https://source.example.invalid','https://source.example.invalid');
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title) VALUES(1,7,'/Retired','Retired card'),(2,7,'/Successor','Successor card');
		UPDATE source_catalog_entry SET relocated_to_id=2 WHERE id=1;
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES(2,'MP3/01 successor.wav','Successor track','audio',100);`)
	cfg, _ := json.Marshal(map[string]any{"baseUrl": model.URL, "minFreeBytes": 0})
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", string(cfg), 200)
	result := translationRequest(t, host, listener, "POST", "/api/content-translations", `{"catalogEntryId":1,"field":"directory","text":"Successor track","path":"MP3/01 successor.wav","language":"zh-cn"}`, 200)
	if result["status"] != "translated" || result["text"] != "继承条目译文" || result["original"] != "Successor track" {
		t.Fatalf("relocated directory translation: %#v", result)
	}
}

// Two editions of one work are different source texts and must not share a
// cache entry keyed by the canonical title.
func TestContentTranslationCacheKeyFollowsSelectedText(t *testing.T) {
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var payload struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		_, text, found := strings.Cut(payload.Messages[0].Content, "\n\n")
		if !found {
			t.Error("model prompt lost the source text")
		}
		body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": "译:" + text}, "finish_reason": "stop"}}})
		_, _ = w.Write(body)
	}))
	defer model.Close()
	s, host, admin, listener := translationHTTPFixture(t)
	translationEditionFamily(t, s)
	cfg, _ := json.Marshal(map[string]any{"baseUrl": model.URL, "minFreeBytes": 0})
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", string(cfg), 200)
	for _, sample := range []string{"English Edition Title", "Example origin title"} {
		body, _ := json.Marshal(map[string]any{"workId": 1, "field": "title", "text": sample, "language": "zh-cn"})
		result := translationRequest(t, host, listener, "POST", "/api/content-translations", string(body), 200)
		if result["text"] != "译:"+sample || result["original"] != "Example origin title" {
			t.Fatalf("selection %q: %#v", sample, result)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("variants shared a cache entry: %d calls", calls.Load())
	}
}
