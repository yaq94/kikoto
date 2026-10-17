package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
)

// A playable work directory with its own product code is a work, even when it
// sits below an author or uploader directory. Folding it into the parent card
// makes the work unreachable by its code and hides its own title.
func TestSourceCatalogKeepsCodedChildDirectoriesAsWorks(t *testing.T) {
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		items := []map[string]any{}
		switch in.Path {
		case "/":
			items = []map[string]any{{"name": "Uploader", "is_dir": true}}
		case "/Uploader":
			items = []map[string]any{
				{"name": "uploader-loose.mp3", "size": 100},
				{"name": "RJ00000041 Coded Child", "is_dir": true},
				{"name": "Numberless Child", "is_dir": true},
			}
		case "/Uploader/RJ00000041 Coded Child":
			items = []map[string]any{{"name": "coded.mp3", "size": 100}}
		case "/Uploader/Numberless Child":
			items = []map[string]any{{"name": "numberless.mp3", "size": 100}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": len(items)}})
	}))
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	p := f.drain(t)
	if p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	for _, code := range []string{"RJ00000041"} {
		result := f.request(t, http.MethodGet, "/works?q="+code, "", 200)
		if result["total"] != float64(1) {
			t.Fatalf("search %s total=%v, want the coded child", code, result["total"])
		}
	}
}
