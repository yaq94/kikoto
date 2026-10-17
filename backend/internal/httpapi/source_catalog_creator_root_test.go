package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
)

// A creator or collection folder is not one work. Each child with its own audio
// becomes a card; format copies, discs, artwork and subtitle folders stay with
// the release above them.
func TestSourceCatalogCreatorRootAdmitsEachChildWork(t *testing.T) {
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		items := []map[string]any{}
		switch in.Path {
		case "/":
			items = []map[string]any{{"name": "Example Voice", "is_dir": true}}
		case "/Example Voice":
			items = []map[string]any{
				{"name": "loose.mp3", "size": 100},
				{"name": "Example Work 1", "is_dir": true},
				{"name": "Example Work 2", "is_dir": true},
			}
		case "/Example Voice/Example Work 1":
			items = []map[string]any{
				{"name": "MP3", "is_dir": true},
				{"name": "FLAC", "is_dir": true},
				{"name": "Disc 1", "is_dir": true},
				{"name": "Extras", "is_dir": true},
			}
		case "/Example Voice/Example Work 1/MP3", "/Example Voice/Example Work 1/Disc 1":
			items = []map[string]any{{"name": "track.mp3", "size": 100}}
		case "/Example Voice/Example Work 1/FLAC":
			items = []map[string]any{{"name": "track.flac", "size": 100}}
		case "/Example Voice/Example Work 1/Extras":
			items = []map[string]any{{"name": "booklet.pdf", "size": 100}}
		case "/Example Voice/Example Work 2":
			items = []map[string]any{{"name": "only.mp3", "size": 100}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": len(items)}})
	}))
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	cards := f.request(t, http.MethodGet, "/works", "", 200)
	if cards["total"] != float64(3) {
		t.Fatalf("cards=%v, want the creator's loose audio and two child works: %s", cards["total"], mustJSON(cards))
	}
	titles := cardTitles(cards)
	for _, want := range []string{"Example Voice", "Example Work 1", "Example Work 2"} {
		if !containsString(titles, want) {
			t.Fatalf("cards=%v, want %q", titles, want)
		}
	}
	for _, raw := range cards["works"].([]any) {
		card := raw.(map[string]any)
		if card["title"] != "Example Work 1" {
			continue
		}
		tracks := reviewRequest(t, f.handler, f.cookie, http.MethodGet,
			"/api/source-catalog/"+strconv.Itoa(int(card["catalogEntryId"].(float64)))+"/tracks", "", 200)
		names := []string{}
		var walk func([]any)
		walk = func(nodes []any) {
			for _, node := range nodes {
				item := node.(map[string]any)
				names = append(names, item["title"].(string))
				if children, ok := item["children"].([]any); ok {
					walk(children)
				}
			}
		}
		walk(tracks["tracks"].([]any))
		for _, want := range []string{"MP3", "FLAC", "Disc 1", "Extras", "track.mp3", "track.flac", "booklet.pdf"} {
			if !containsString(names, want) {
				t.Fatalf("work 1 tracks=%v, want %q inside the release", names, want)
			}
		}
	}
}

// A coded release keeps its own disc and bonus folders in one card even though
// each of them holds audio.
func TestSourceCatalogCodedReleaseKeepsDiscsInOneCard(t *testing.T) {
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		items := []map[string]any{}
		switch in.Path {
		case "/":
			items = []map[string]any{{"name": "[RJ00000046]Example Release", "is_dir": true}}
		case "/[RJ00000046]Example Release":
			items = []map[string]any{
				{"name": "Disc 1", "is_dir": true},
				{"name": "Disc 2", "is_dir": true},
				{"name": "特典", "is_dir": true},
			}
		case "/[RJ00000046]Example Release/Disc 1", "/[RJ00000046]Example Release/Disc 2",
			"/[RJ00000046]Example Release/特典":
			items = []map[string]any{{"name": "track.mp3", "size": 100}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": len(items)}})
	}))
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	cards := f.request(t, http.MethodGet, "/works", "", 200)
	if cards["total"] != float64(1) {
		t.Fatalf("cards=%v, want one coded release: %s", cards["total"], mustJSON(cards))
	}
}
