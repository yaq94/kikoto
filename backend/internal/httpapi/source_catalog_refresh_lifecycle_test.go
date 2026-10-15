package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
)

func TestSourceCatalogLifecycleRefreshPublishesFailureAndRecovery(t *testing.T) {
	var mode atomic.Int32
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if mode.Load() == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		items := []map[string]any{{"name": "track.mp3", "size": 100}}
		if mode.Load() == 2 {
			items = []map[string]any{}
		}
		if in.Path == "/" {
			items = []map[string]any{{"name": "Example Work", "is_dir": true}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": len(items)}})
	}))
	reviewRequest(t, f.handler, f.cookie, http.MethodPatch, "/api/settings", `{"remoteMaxBackoffSeconds":1}`, 200)
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	f.drain(t)
	card := f.request(t, http.MethodGet, "/works", "", 200)["works"].([]any)[0].(map[string]any)
	base := "/api/source-catalog/" + strconv.Itoa(int(card["catalogEntryId"].(float64)))
	state := reviewRequest(t, f.handler, f.cookie, http.MethodPatch, base+"/user-state", `{"rating":4,"note":"Example retained note"}`, 200)
	for _, tc := range []struct {
		mode     int32
		status   string
		response int
	}{{1, "maintenance", 502}, {2, "unconfirmed_empty", 502}, {0, "available", 200}} {
		mode.Store(tc.mode)
		reviewRequest(t, f.handler, f.cookie, http.MethodPost, base+"/refresh", `{}`, tc.response)
		detail := reviewRequest(t, f.handler, f.cookie, http.MethodGet, base, "", 200)
		if detail["status"] != tc.status {
			t.Fatalf("refresh status=%v, want %s", detail["status"], tc.status)
		}
		assertReview(t, detail, float64(4), "Example retained note")
		directories := reviewRequest(t, f.handler, f.cookie, http.MethodGet, "/api/works/"+strconv.Itoa(int(state["workId"].(float64)))+"/catalog-directories", "", 200)
		if directories["directories"].([]any)[0].(map[string]any)["status"] != tc.status {
			t.Fatalf("directory status stale: %#v", directories)
		}
		tracks := reviewRequest(t, f.handler, f.cookie, http.MethodGet, base+"/tracks", "", 200)
		if len(tracks["tracks"].([]any)) != 1 {
			t.Fatal("failed refresh discarded the saved directory")
		}
	}
}
