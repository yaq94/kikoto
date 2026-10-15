package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/yexca/kikoto/backend/internal/config"
)

func TestOpenListFiniteCatalogDiscoversWithoutRegisteringWorks(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/fs/list" {
			http.NotFound(w, r)
			return
		}
		var input struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
			return
		}
		files := []map[string]any{}
		switch input.Path {
		case "/Example/RJ00000001":
			files = append(files, map[string]any{"name": "track.mp3", "size": 100, "type": 3})
		case "/Example/Example Work":
			files = append(files, map[string]any{"name": "MP3", "is_dir": true}, map[string]any{"name": "subtitles.srt", "size": 15})
		case "/Example/Example Work/MP3":
			files = append(files, map[string]any{"name": "01 opening.mp3", "size": 100, "type": 3}, map[string]any{"name": "02 ending.mp3", "size": 200, "type": 3})
		case "/Example/Archive":
			files = append(files, map[string]any{"name": "only.zip", "size": 100})
		case "/Example/Subtitles":
			files = append(files, map[string]any{"name": "only.srt", "size": 100})
		default:
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": files, "total": len(files)}})
	}))
	defer upstream.Close()
	db := openMigratedTestDB(t)
	server := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", CacheRoot: t.TempDir(), DataRoot: t.TempDir()})
	defer func() { _ = server.Shutdown(context.Background()) }()
	createTestAdministrator(t, server, "synthetic-user", "synthetic-password")
	handler := server.Routes()
	cookie := loginTestSession(t, handler, "synthetic-user", "synthetic-password")
	create, _ := json.Marshal(map[string]any{"displayName": "Example Remote A", "sourceType": "openlist", "enabled": true, "config": map[string]any{"openListRoots": []string{"/Example/RJ00000001", "/Example/Example Work", "/Example/Archive", "/Example/Subtitles"}}, "endpoint": map[string]any{"apiUrl": upstream.URL, "baseUrl": upstream.URL}})
	source := reviewRequest(t, handler, cookie, http.MethodPost, "/api/file-sources", string(create), http.StatusCreated)
	sourceID := strconv.Itoa(int(source["id"].(float64)))
	scan := reviewRequest(t, handler, cookie, http.MethodPost, "/api/remote-sources/"+sourceID+"/catalog/scan", `{}`, http.StatusOK)
	if scan["status"] != "complete" {
		t.Fatalf("scan status: %#v", scan)
	}
	list := reviewRequest(t, handler, cookie, http.MethodGet, "/api/remote-sources/"+sourceID+"/works", "", http.StatusOK)
	if list["total"] != float64(2) {
		t.Fatalf("finite catalog: %#v", list)
	}
	var entry map[string]any
	for _, raw := range list["works"].([]any) {
		item := raw.(map[string]any)
		if item["workId"] != nil {
			t.Fatalf("discovery registered work: %#v", item)
		}
		if item["primaryCode"] == "" {
			entry = item
		}
	}
	if entry == nil || entry["title"] != "Example Work" {
		t.Fatalf("missing numberless work: %#v", list)
	}
	id := strconv.Itoa(int(entry["catalogEntryId"].(float64)))
	detail := reviewRequest(t, handler, cookie, http.MethodGet, "/api/source-catalog/"+id, "", http.StatusOK)
	if detail["primaryCode"] != "" || detail["workId"] != nil {
		t.Fatalf("numberless identity: %#v", detail)
	}
	tracks := reviewRequest(t, handler, cookie, http.MethodGet, "/api/source-catalog/"+id+"/tracks", "", http.StatusOK)
	if len(tracks["tracks"].([]any)) != 2 {
		t.Fatalf("directory roots: %#v", tracks)
	}
	search := reviewRequest(t, handler, cookie, http.MethodGet, "/api/remote-sources/"+sourceID+"/works?q=Example%20Work", "", http.StatusOK)
	if search["total"] != float64(1) {
		t.Fatalf("search excludes unknown work: %#v", search)
	}
	library := reviewRequest(t, handler, cookie, http.MethodGet, "/api/works", "", http.StatusOK)
	if library["total"] != float64(0) {
		t.Fatalf("catalog wrote library: %#v", library)
	}
}

func TestOpenListPlaybackUsesRealPathAndRefreshesExpiredURL(t *testing.T) {
	data := []byte("0123456789abcdefghijklmnopqrstuvwxyz")
	gets := 0
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/fs/get":
			var input struct {
				Path string `json:"path"`
			}
			_ = json.NewDecoder(r.Body).Decode(&input)
			if input.Path != "/Example Work/MP3/01 original.mp3" {
				t.Errorf("wrong source path %q", input.Path)
			}
			gets++
			link := "/bytes"
			if gets == 1 {
				link = "/expired"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"name": "01 original.mp3", "size": len(data), "raw_url": upstream.URL + link}})
		case "/expired":
			http.Error(w, "expired", http.StatusForbidden)
		case "/bytes":
			w.Header().Set("Content-Type", "audio/mpeg")
			http.ServeContent(w, r, "01 original.mp3", time.Unix(0, 0), bytes.NewReader(data))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	db := openMigratedTestDB(t)
	_, err := db.Exec(`INSERT INTO file_source(id,code,display_name,source_type,config_json) VALUES(7,'example_remote_a','Example Remote A','openlist','{"openListIntervalMs":100}');
		INSERT INTO file_source_endpoint(file_source_id,api_url,base_url) VALUES(7,?,?);
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title) VALUES(1,7,'/Example Work','Example Work');
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES(1,'MP3/01 original.mp3','Translated display name','audio',36);`, upstream.URL, upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	defer func() { _ = server.Shutdown(context.Background()) }()
	createTestAdministrator(t, server, "synthetic-user", "synthetic-password")
	handler := server.Routes()
	cookie := loginTestSession(t, handler, "synthetic-user", "synthetic-password")
	request := httptest.NewRequest(http.MethodGet, "/api/source-catalog/1/media?path=MP3%2F01%20original.mp3", nil)
	request.AddCookie(cookie)
	request.Header.Set("Range", "bytes=10-15")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 206 || response.Body.String() != "abcdef" || response.Header().Get("Content-Range") != "bytes 10-15/36" {
		t.Fatalf("range response %d %s %s", response.Code, response.Header().Get("Content-Range"), response.Body.String())
	}
	if gets != 2 {
		t.Fatalf("URL resolutions %d, want 2", gets)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/source-catalog/1/media?path=Translated%20display%20name", nil)
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 404 {
		t.Fatalf("display title was treated as a file: %d", response.Code)
	}
	if gets != 2 {
		t.Fatal("unknown media path caused an upstream request")
	}
	request = httptest.NewRequest(http.MethodGet, "/api/source-catalog/1/media?path=MP3%2F01%20original.mp3", nil)
	request.AddCookie(cookie)
	request.Header.Set("Range", "bytes=2-4")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	raw, _ := io.ReadAll(response.Result().Body)
	if response.Code != 206 || string(raw) != "234" || gets != 3 {
		t.Fatalf("fresh playback resolution: %d %q %d", response.Code, raw, gets)
	}
}

func TestOpenListReviewRegistersNumberlessWorksWithoutMedia(t *testing.T) {
	db := openMigratedTestDB(t)
	_, err := db.Exec(`INSERT INTO file_source(id,code,display_name,source_type) VALUES(7,'example_remote_a','Example Remote A','openlist');
		INSERT INTO file_source_endpoint(file_source_id,api_url,base_url) VALUES(7,'https://source.example.invalid','https://source.example.invalid');
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title) VALUES(1,7,'/Example A','Example A'),(2,7,'/Example B','Example B');`)
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", CacheRoot: t.TempDir(), DataRoot: t.TempDir()})
	defer func() { _ = server.Shutdown(context.Background()) }()
	createTestAdministrator(t, server, "synthetic-user", "synthetic-password")
	createAccountManagedTestUser(t, server, "synthetic-listener", "synthetic-password")
	handler := server.Routes()
	admin := loginTestSession(t, handler, "synthetic-user", "synthetic-password")
	listener := loginTestSession(t, handler, "synthetic-listener", "synthetic-password")
	reviewRequest(t, handler, listener, http.MethodPatch, "/api/source-catalog/1/user-state", `{"rating":4}`, 403)
	first := reviewRequest(t, handler, admin, http.MethodPatch, "/api/source-catalog/1/user-state", `{"rating":0,"note":"Example note"}`, 200)
	second := reviewRequest(t, handler, admin, http.MethodPatch, "/api/source-catalog/2/user-state", `{"rating":5}`, 200)
	if first["workId"] == second["workId"] {
		t.Fatal("unrelated numberless works were merged")
	}
	id := strconv.Itoa(int(first["workId"].(float64)))
	assertReview(t, reviewRequest(t, handler, admin, http.MethodGet, "/api/works/"+id+"?includeMedia=false", "", 200), float64(0), "Example note")
	assertReview(t, reviewRequest(t, handler, listener, http.MethodGet, "/api/source-catalog/1", "", 200), nil, "")
	assertReview(t, reviewRequest(t, handler, listener, http.MethodPatch, "/api/source-catalog/1/user-state", `{"rating":2,"note":"Listener note"}`, 200), float64(2), "Listener note")
	assertReview(t, reviewRequest(t, handler, admin, http.MethodPatch, "/api/source-catalog/1/user-state", `{"note":"Changed note"}`, 200), float64(0), "Changed note")
	assertReview(t, reviewRequest(t, handler, admin, http.MethodPatch, "/api/source-catalog/1/user-state", `{"rating":null}`, 200), nil, "Changed note")
	assertReview(t, reviewRequest(t, handler, listener, http.MethodGet, "/api/source-catalog/1", "", 200), float64(2), "Listener note")
	list := reviewRequest(t, handler, admin, http.MethodGet, "/api/works?q=Example", "", 200)
	if list["total"] != float64(2) {
		t.Fatalf("registered works missing: %#v", list)
	}
	media := reviewRequest(t, handler, admin, http.MethodGet, "/api/works/"+id+"/media", "", 200)
	if len(media["mediaItems"].([]any)) != 0 {
		t.Fatal("review created media")
	}
	runs := reviewRequest(t, handler, admin, http.MethodGet, "/api/workflow-runs", "", 200)
	if runs["total"] != float64(0) {
		t.Fatal("review scheduled a workflow")
	}
}
