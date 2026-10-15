package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSourceCatalogLifecycleDistinguishesFailureMissingAndRecovery(t *testing.T) {
	var mode atomic.Int32
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		switch mode.Load() {
		case 1:
			w.WriteHeader(503)
			return
		case 2:
			w.WriteHeader(403)
			return
		}
		items := []map[string]any{{"name": "track.mp3", "size": 4096, "hash_info": map[string]string{"sha256": strings.Repeat("a", 64)}}}
		if in.Path == "/" {
			items = []map[string]any{{"name": "Example Work", "is_dir": true}}
			if mode.Load() == 3 {
				items = []map[string]any{{"name": "README.txt", "size": 1}}
			}
			if mode.Load() == 4 {
				items = []map[string]any{}
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": len(items)}})
	}))
	reviewRequest(t, f.handler, f.cookie, http.MethodPatch, "/api/settings", `{"remoteMaxBackoffSeconds":1}`, 200)
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatal(p)
	}
	list := f.request(t, http.MethodGet, "/works", "", 200)
	id := strconv.Itoa(int(list["works"].([]any)[0].(map[string]any)["catalogEntryId"].(float64)))
	base := "/api/source-catalog/" + id
	reviewRequest(t, f.handler, f.cookie, http.MethodPatch, base+"/user-state", `{"rating":4,"note":"Example retained note"}`, 200)
	for _, tc := range []struct {
		mode         int32
		status, scan string
	}{{1, "maintenance", "partial"}, {2, "restricted", "partial"}, {4, "unconfirmed_empty", "partial"}, {3, "missing", "complete"}, {0, "available", "complete"}} {
		mode.Store(tc.mode)
		f.request(t, http.MethodPost, "/catalog/index", `{}`, 202)
		p := f.drain(t)
		if p["status"] != tc.scan {
			t.Fatalf("mode %d coverage: %#v", tc.mode, p)
		}
		detail := reviewRequest(t, f.handler, f.cookie, http.MethodGet, base, "", 200)
		if detail["status"] != tc.status {
			t.Fatalf("mode %d status=%v, want %s", tc.mode, detail["status"], tc.status)
		}
		assertReview(t, detail, float64(4), "Example retained note")
		if got := f.request(t, http.MethodGet, "/works", "", 200); got["total"] != float64(1) {
			t.Fatal("source transition removed its card")
		}
	}
}

func TestSourceCatalogLifecycleUniqueHashRenameKeepsReviewAndPlaybackIdentity(t *testing.T) {
	var renamed atomic.Bool
	var requested atomic.Value
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/audio" {
			http.ServeContent(w, r, "track.mp3", time.Unix(0, 0), bytes.NewReader(bytes.Repeat([]byte{'a'}, 4096)))
			return
		}
		var in struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		root := "/Example Work"
		if renamed.Load() {
			root = "/Renamed Work"
		}
		if r.URL.Path == "/api/fs/get" {
			requested.Store(in.Path)
			if in.Path != root+"/track.mp3" {
				w.WriteHeader(404)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"name": "track.mp3", "size": 4096, "raw_url": "http://" + r.Host + "/audio"}})
			return
		}
		items := []map[string]any{{"name": "track.mp3", "size": 4096, "hash_info": map[string]string{"sha256": strings.Repeat("b", 64)}}}
		if in.Path == "/" {
			items = []map[string]any{{"name": strings.TrimPrefix(root, "/"), "is_dir": true}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": len(items)}})
	}))
	reviewRequest(t, f.handler, f.cookie, http.MethodPatch, "/api/settings", `{"remoteMaxBackoffSeconds":1}`, 200)
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	f.drain(t)
	list := f.request(t, http.MethodGet, "/works", "", 200)
	id := strconv.Itoa(int(list["works"].([]any)[0].(map[string]any)["catalogEntryId"].(float64)))
	base := "/api/source-catalog/" + id
	state := reviewRequest(t, f.handler, f.cookie, http.MethodPatch, base+"/user-state", `{"rating":4,"note":"Example retained note"}`, 200)
	before := reviewRequest(t, f.handler, f.cookie, http.MethodPost, base+"/playback", `{"path":"track.mp3"}`, 200)
	renamed.Store(true)
	f.request(t, http.MethodPost, "/catalog/index", `{}`, 202)
	f.drain(t)
	list = f.request(t, http.MethodGet, "/works", "", 200)
	if list["total"] != float64(1) {
		t.Fatalf("reliable rename created duplicate cards: %#v", list)
	}
	detail := reviewRequest(t, f.handler, f.cookie, http.MethodGet, base, "", 200)
	if detail["title"] != "Renamed Work" || detail["workId"] != state["workId"] {
		t.Fatalf("rename replaced identity: %#v", detail)
	}
	assertReview(t, detail, float64(4), "Example retained note")
	after := reviewRequest(t, f.handler, f.cookie, http.MethodPost, base+"/playback", `{"path":"track.mp3"}`, 200)
	if before["mediaItemId"] != after["mediaItemId"] || before["locationId"] != after["locationId"] || before["streamUrl"] != after["streamUrl"] {
		t.Fatal("rename changed current queue's media references")
	}
	req := httptest.NewRequest(http.MethodGet, before["streamUrl"].(string), nil)
	req.AddCookie(f.cookie)
	req.Header.Set("Range", "bytes=0-3")
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, req)
	if response.Code != 206 || response.Body.String() != "aaaa" || requested.Load() != "/Renamed Work/track.mp3" {
		t.Fatalf("old queue failed to resolve new path: status %d", response.Code)
	}
}
