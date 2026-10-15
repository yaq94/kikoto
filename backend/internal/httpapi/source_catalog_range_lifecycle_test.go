package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
)

func TestSourceCatalogLifecycleRestrictedFormatDirectoryRetainsOwningWork(t *testing.T) {
	var restricted atomic.Bool
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		items := []map[string]any{{"name": "track.wav", "size": 100}}
		switch in.Path {
		case "/":
			items = []map[string]any{{"name": "Example Work", "is_dir": true}}
		case "/Example Work":
			items = []map[string]any{{"name": "WAV", "is_dir": true}}
		case "/Example Work/WAV":
			if restricted.Load() {
				w.WriteHeader(403)
				return
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": len(items)}})
	}))
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	f.drain(t)
	card := f.request(t, http.MethodGet, "/works", "", 200)["works"].([]any)[0].(map[string]any)
	base := "/api/source-catalog/" + strconv.Itoa(int(card["catalogEntryId"].(float64)))
	reviewRequest(t, f.handler, f.cookie, http.MethodPatch, base+"/user-state", `{"rating":3,"note":"Example kept note"}`, 200)
	restricted.Store(true)
	f.request(t, http.MethodPost, "/catalog/index", `{}`, 202)
	if p := f.drain(t); p["status"] != "partial" {
		t.Fatal(p)
	}
	detail := reviewRequest(t, f.handler, f.cookie, http.MethodGet, base, "", 200)
	if detail["status"] != "restricted" {
		t.Fatalf("owning catalog status=%v, want restricted", detail["status"])
	}
	assertReview(t, detail, float64(3), "Example kept note")
	tracks := reviewRequest(t, f.handler, f.cookie, http.MethodGet, base+"/tracks", "", 200)
	if len(tracks["tracks"].([]any)) != 1 {
		t.Fatal("restricted child discarded saved format directory")
	}
}
