package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestCatalogLocationStreamsRangeWithoutCache(t *testing.T) {
	var remote *httptest.Server
	remote = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/fs/get" {
			var input struct {
				Path string `json:"path"`
			}
			_ = json.NewDecoder(r.Body).Decode(&input)
			if input.Path != "/Existing Work/track.mp3" {
				t.Errorf("wrong real path: %s", input.Path)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"name": "track.mp3", "raw_url": remote.URL + "/audio"}})
			return
		}
		http.ServeContent(w, r, "track.mp3", time.Unix(0, 0), bytes.NewReader([]byte("0123456789abcdef")))
	}))
	defer remote.Close()
	server, handler, cookie := openListBoundaryApp(t, remote.URL)
	reviewRequest(t, handler, cookie, http.MethodPatch, "/api/source-catalog/1/user-state", `{"rating":4}`, 200)
	target := reviewRequest(t, handler, cookie, http.MethodPost, "/api/source-catalog/1/playback", `{"paths":["track.mp3"]}`, 200)["targets"].([]any)[0].(map[string]any)
	endpoint := fmt.Sprintf("/api/media/%.0f/stream", target["locationId"])
	request := httptest.NewRequest(http.MethodGet, endpoint, nil)
	request.AddCookie(cookie)
	request.Header.Set("Range", "bytes=10-15")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 206 || response.Body.String() != "abcdef" || response.Header().Get("Content-Range") != "bytes 10-15/16" {
		t.Fatalf("range: %d %q %s", response.Code, response.Body.String(), response.Header().Get("Content-Range"))
	}
	if entries, err := os.ReadDir(server.cfg.CacheRoot); err != nil || len(entries) != 0 {
		t.Fatalf("stream created cache files: %v %v", entries, err)
	}
	// The location cannot retarget a same-named file to a different source.
	if _, err := server.db.Exec(`INSERT INTO file_source(id,code,display_name,source_type) VALUES(8,'other_catalog','Other Catalog','openlist'); UPDATE media_file_location SET file_source_id=8 WHERE id=?`, target["locationId"]); err != nil {
		t.Fatal(err)
	}
	server.mediaStreamCache.Delete(int64(target["locationId"].(float64)))
	reviewRequest(t, handler, cookie, http.MethodGet, endpoint, "", 404)
}
