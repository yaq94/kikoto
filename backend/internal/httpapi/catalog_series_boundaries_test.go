package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestCatalogSeriesIndexKeepsAnExistingReviewedContainer(t *testing.T) {
	s, host, admin, _ := translationHTTPFixture(t)
	tree := map[string][]map[string]any{
		"/":                  {{"name": "Example", "is_dir": true}},
		"/Example":           {{"name": "Episode 1", "is_dir": true}, {"name": "Episode 2", "is_dir": true}},
		"/Example/Episode 1": {{"name": "audio.mp3"}},
		"/Example/Episode 2": {{"name": "audio.mp3"}},
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/fs/list" {
			t.Error("unexpected media request")
			http.NotFound(w, r)
			return
		}
		var in struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": tree[in.Path], "total": len(tree[in.Path])}})
	}))
	defer upstream.Close()
	source := translationRequest(t, host, admin, "POST", "/api/file-sources", mustJSON(map[string]any{"displayName": "Example Remote A", "sourceType": "openlist", "enabled": true, "endpoint": map[string]any{"apiUrl": upstream.URL, "baseUrl": upstream.URL}}), 201)
	sid := int(source["id"].(float64))
	metadataReviewExec(t, s.db, `INSERT INTO work(id,primary_code,title) VALUES(11,'','Example');`)
	if _, err := s.db.Exec(`INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,work_id) VALUES(1,?,'/Example','Example',11)`, sid); err != nil {
		t.Fatal(err)
	}
	translationRequest(t, host, admin, "PATCH", "/api/source-catalog/1/user-state", `{"rating":4,"note":"Original review"}`, 200)
	base := "/api/remote-sources/" + strconv.Itoa(sid)
	translationRequest(t, host, admin, "POST", base+"/catalog/index", `{"intervalMs":100,"minFreeBytes":0}`, 202)
	for batch := 0; batch < 20; batch++ {
		if err := s.runNextQueuedWorkflowJob(context.Background()); err != nil {
			t.Fatal(err)
		}
		progress := translationRequest(t, host, admin, "GET", base+"/catalog/index", "", 200)
		if progress["status"] == "complete" {
			break
		}
		if batch == 19 {
			t.Fatalf("index incomplete: %#v", progress)
		}
	}
	cards := translationRequest(t, host, admin, "GET", base+"/works", "", 200)
	if cards["total"] != float64(1) {
		t.Fatalf("existing evaluation container was fragmented: %#v", cards)
	}
	entry := translationRequest(t, host, admin, "GET", "/api/source-catalog/1", "", 200)
	if entry["workId"] != float64(11) || entry["seriesId"] != nil {
		t.Fatalf("identity changed: %#v", entry)
	}
	assertReview(t, entry, float64(4), "Original review")
	tracks := translationRequest(t, host, admin, "GET", "/api/source-catalog/1/tracks", "", 200)
	if len(tracks["tracks"].([]any)) != 2 {
		t.Fatalf("episode folders lost: %#v", tracks)
	}
}

func TestCatalogSeriesPreviewBoundsAnonymousReadAndTranslation(t *testing.T) {
	s, host, admin, listener := translationHTTPFixture(t)
	metadataReviewExec(t, s.db, `INSERT INTO file_source(id,code,display_name,source_type) VALUES(7,'example_remote_a','Example Remote A','openlist');
 INSERT INTO file_source_endpoint(file_source_id,base_url,api_url) VALUES(7,'https://source.example.invalid','https://source.example.invalid');
 INSERT INTO source_catalog_entry(id,file_source_id,root_path,title) VALUES(1,7,'/Example A','Example A'),(2,7,'/Example B','Example B');`)
	const body = `{"title":"Example series","catalogEntryIds":[1,2]}`
	translationRequest(t, host, admin, "PATCH", "/api/access-policy", `{"anonymousAccessEnabled":true}`, 200)
	translationRequest(t, host, nil, "POST", "/api/catalog-series/previews", body, 401)
	translationRequest(t, host, listener, "POST", "/api/catalog-series/previews", body, 403)
	for _, invalid := range []string{
		`{"title":"","catalogEntryIds":[1,2]}`,
		`{"title":"Example","catalogEntryIds":[1]}`,
		`{"title":"` + strings.Repeat("x", 513) + `","catalogEntryIds":[1,2]}`,
		`{"title":"` + strings.Repeat("x", 17<<10) + `","catalogEntryIds":[1,2]}`,
	} {
		translationRequest(t, host, admin, "POST", "/api/catalog-series/previews", invalid, 400)
	}
	translationRequest(t, host, admin, "POST", "/api/catalog-series/previews", `{"title":"Example","catalogEntryIds":[1,1]}`, 409)
	var preview map[string]any
	for range 32 {
		preview = translationRequest(t, host, admin, "POST", "/api/catalog-series/previews", body, 201)
	}
	translationRequest(t, host, admin, "POST", "/api/catalog-series/previews", body, 429)
	id := strconv.Itoa(int(preview["previewId"].(float64)))
	translationRequest(t, host, listener, "GET", "/api/catalog-series/previews/"+id, "", 403)
	// Clock boundary fixture: the expired preview cannot mutate shared membership.
	metadataReviewExec(t, s.db, `UPDATE catalog_series_preview SET created_at=datetime('now','-2 days')`)
	translationRequest(t, host, admin, "POST", "/api/catalog-series/previews/"+id+"/confirm", "{}", 404)
	preview = translationRequest(t, host, admin, "POST", "/api/catalog-series/previews", body, 201)
	id = strconv.Itoa(int(preview["previewId"].(float64)))
	result := translationRequest(t, host, admin, "POST", "/api/catalog-series/previews/"+id+"/confirm", "{}", 200)
	seriesID := strconv.Itoa(int(result["seriesId"].(float64)))
	view := translationRequest(t, host, nil, "GET", "/api/catalog-series/"+seriesID, "", 200)
	if view["title"] != "Example series" || len(view["episodes"].([]any)) != 2 {
		t.Fatalf("anonymous shared view: %#v", view)
	}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Error(err)
			return
		}
		if len(in.Messages) != 1 || (!strings.HasSuffix(in.Messages[0].Content, "\n\nExample series") && !strings.HasSuffix(in.Messages[0].Content, "\n\nExample A")) {
			t.Error("translation included non-public content")
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"示例译文"},"finish_reason":"stop"}]}`))
	}))
	defer model.Close()
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", mustJSON(map[string]any{"baseUrl": model.URL, "minFreeBytes": 0}), 200)
	for _, resource := range []map[string]any{{"seriesId": result["seriesId"], "text": "Example series"}, {"catalogEntryId": 1, "text": "Example A"}} {
		resource["field"] = "title"
		resource["language"] = "zh-cn"
		translated := translationRequest(t, host, listener, "POST", "/api/content-translations", mustJSON(resource), 200)
		if translated["text"] != "示例译文" {
			t.Fatalf("translation: %#v", translated)
		}
		resource["text"] = "Private note"
		translationRequest(t, host, listener, "POST", "/api/content-translations", mustJSON(resource), 409)
	}
	if library := translationRequest(t, host, admin, "GET", "/api/works", "", 200); library["total"] != float64(0) {
		t.Fatal("grouping or translation registered a work")
	}
}
