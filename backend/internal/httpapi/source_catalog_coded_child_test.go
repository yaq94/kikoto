package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
)

// A playable work directory with its own product code is a work, even when it
// sits below an author or uploader directory. Folding it into the parent card
// makes the work unreachable by its code and hides its own title.
func TestSourceCatalogKeepsCodedChildDirectoriesAsWorks(t *testing.T) {
	// The first run publishes only the uploader card, imitating a catalog built
	// before coded children had their own identity.
	var legacy atomic.Bool
	legacy.Store(true)
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
				{"name": "Example Work 2", "is_dir": true},
			}
			if !legacy.Load() {
				items = append(items, map[string]any{"name": "RJ00000041 Example Work 3", "is_dir": true})
			}
		case "/Example Voice/RJ00000041 Example Work 3":
			items = []map[string]any{{"name": "coded.mp3", "size": 100}}
		case "/Example Voice/Example Work 2":
			items = []map[string]any{{"name": "numberless.mp3", "size": 100}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": len(items)}})
	}))
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	p := f.drain(t)
	if p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	if result := f.request(t, http.MethodGet, "/works?q=RJ00000041", "", 200); result["total"] != float64(0) {
		t.Fatalf("legacy scan unexpectedly published the coded child: %#v", result)
	}
	legacy.Store(false)
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("rescan = %#v", p)
	}
	// The uploader card used to match this code only because it listed the coded
	// folder as a track. Once the child has its own card that folder belongs to
	// the child, so the code resolves to exactly one work.
	result := f.request(t, http.MethodGet, "/works?q=RJ00000041", "", 200)
	if result["total"] != float64(1) {
		t.Fatalf("rescan search total=%v, want only the coded child: %s", result["total"], mustJSON(result))
	}
	titles := []string{}
	for _, item := range result["works"].([]any) {
		titles = append(titles, item.(map[string]any)["title"].(string))
	}
	if !containsString(titles, "RJ00000041 Example Work 3") {
		t.Fatalf("search RJ00000041 titles=%v, want the coded child card", titles)
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// A category such as 未分类 groups unrelated uploads. Its loose files may keep a
// card, but an album directory below it is a separate work rather than another
// track of the category card.
func TestSourceCatalogCategoryDoesNotOwnItsChildren(t *testing.T) {
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		items := []map[string]any{}
		switch in.Path {
		case "/":
			items = []map[string]any{{"name": "Uncategorized", "is_dir": true}}
		case "/Uncategorized":
			items = []map[string]any{
				{"name": "loose.mp3", "size": 100},
				{"name": "Example Work", "is_dir": true},
			}
		case "/Uncategorized/Example Work":
			items = []map[string]any{{"name": "a.mp3", "size": 100}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": len(items)}})
	}))
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	cards := f.request(t, http.MethodGet, "/works", "", 200)
	if cards["total"] != float64(2) {
		t.Fatalf("cards=%v, want the category's loose files and Example Work separately", cards["total"])
	}
}

// An uploader or voice-actor folder that lists coded works directly below it is
// a grouping directory, not one work. A catalog published before this rule
// existed must divide those works on the next scan.
func TestSourceCatalogUploaderWithCodedChildrenIsNotAWork(t *testing.T) {
	var legacy atomic.Bool
	legacy.Store(true)
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
			items = []map[string]any{{"name": "profile.txt", "size": 100}}
			if !legacy.Load() {
				items = append(items,
					map[string]any{"name": "[RJ00000042]Example Work 4", "is_dir": true},
					map[string]any{"name": "[RJ00000043]Example Work 5", "is_dir": true})
			}
		case "/Example Voice/[RJ00000042]Example Work 4":
			items = []map[string]any{{"name": "first.mp3", "size": 100}}
		case "/Example Voice/[RJ00000043]Example Work 5":
			items = []map[string]any{{"name": "second.mp3", "size": 100}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": len(items)}})
	}))
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	if cards := f.request(t, http.MethodGet, "/works", "", 200); cards["total"] != float64(0) {
		t.Fatalf("legacy cards=%v, want no playable folder", cards["total"])
	}
	legacy.Store(false)
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("rescan = %#v", p)
	}
	for _, code := range []string{"RJ00000042", "RJ00000043"} {
		result := f.request(t, http.MethodGet, "/works?q="+code, "", 200)
		if result["total"] != float64(1) {
			t.Fatalf("search %s total=%v, want one coded work", code, result["total"])
		}
	}
}

// A work root can hold both format directories and extras, artwork or
// subtitles. Only the format copies belong to the work; the extras must not
// make the parent look like a grouping directory and split each format into a
// separate card.
func TestSourceCatalogWorkRootWithFormatsAndExtrasStaysOneCard(t *testing.T) {
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		items := []map[string]any{}
		switch in.Path {
		case "/":
			items = []map[string]any{{"name": "Example Work", "is_dir": true}}
		case "/Example Work":
			items = []map[string]any{
				{"name": "MP3", "is_dir": true},
				{"name": "FLAC", "is_dir": true},
				{"name": "Extras", "is_dir": true},
				{"name": "cover.jpg", "size": 100},
			}
		case "/Example Work/MP3", "/Example Work/FLAC":
			items = []map[string]any{{"name": "track.mp3", "size": 100}}
		case "/Example Work/Extras":
			items = []map[string]any{{"name": "booklet.pdf", "size": 100}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": len(items)}})
	}))
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	cards := f.request(t, http.MethodGet, "/works", "", 200)
	if cards["total"] != float64(1) {
		t.Fatalf("cards=%v, want one card for the work root", cards["total"])
	}
	tracks := reviewRequest(t, f.handler, f.cookie, http.MethodGet,
		"/api/source-catalog/"+strconv.Itoa(int(cards["works"].([]any)[0].(map[string]any)["catalogEntryId"].(float64)))+"/tracks", "", 200)
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
	for _, want := range []string{"MP3", "FLAC", "Extras", "track.mp3", "booklet.pdf"} {
		if !containsString(names, want) {
			t.Fatalf("tracks=%v, want %q inside the work card", names, want)
		}
	}
}

// A grouping folder that already has a card for its own loose audio must stop
// owning newly discovered coded children, while its own card and tracks stay.
func TestSourceCatalogPublishedGroupingFolderReleasesCodedChildren(t *testing.T) {
	var legacy atomic.Bool
	legacy.Store(true)
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
			items = []map[string]any{{"name": "loose.mp3", "size": 100}}
			if !legacy.Load() {
				items = append(items, map[string]any{"name": "[RJ00000044]Example Work 6", "is_dir": true})
			}
		case "/Example Voice/[RJ00000044]Example Work 6":
			items = []map[string]any{{"name": "coded.mp3", "size": 100}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": len(items)}})
	}))
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	if cards := f.request(t, http.MethodGet, "/works", "", 200); cards["total"] != float64(1) {
		t.Fatalf("legacy cards=%v, want the grouping folder's loose audio", cards["total"])
	}
	legacy.Store(false)
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("rescan = %#v", p)
	}
	cards := f.request(t, http.MethodGet, "/works", "", 200)
	if cards["total"] != float64(2) {
		t.Fatalf("cards=%v, want the grouping folder and its coded child", cards["total"])
	}
	result := f.request(t, http.MethodGet, "/works?q=RJ00000044", "", 200)
	// Only the coded work carries this code. The grouping card lost the child
	// folder when the child became its own entry, so it must not match.
	if result["total"] != float64(1) {
		t.Fatalf("search RJ00000044 total=%v, want only the coded child: %s", result["total"], mustJSON(result))
	}
	titles := []string{}
	for _, item := range result["works"].([]any) {
		titles = append(titles, item.(map[string]any)["title"].(string))
	}
	if !containsString(titles, "[RJ00000044]Example Work 6") {
		t.Fatalf("search RJ00000044 titles=%v, want the coded child card", titles)
	}
	grouping := f.request(t, http.MethodGet, "/works?q=Example%20Voice", "", 200)
	if grouping["total"] != float64(1) {
		t.Fatalf("search Example Voice total=%v, want the loose-audio card", grouping["total"])
	}
	// The parent card must also stop listing the child's tracks. Publishing the
	// child under its own entry leaves the rows an older generation published
	// on the parent unless the republish retires them.
	parentID := int(grouping["works"].([]any)[0].(map[string]any)["catalogEntryId"].(float64))
	tracks := reviewRequest(t, f.handler, f.cookie, http.MethodGet,
		"/api/source-catalog/"+strconv.Itoa(parentID)+"/tracks", "", 200)
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
	if !containsString(names, "loose.mp3") {
		t.Fatalf("parent tracks=%v, want its own audio", names)
	}
	for _, stale := range []string{"coded.mp3", "[RJ00000044]Example Work 6"} {
		if containsString(names, stale) {
			t.Fatalf("parent tracks=%v, want %q to belong to the child card only", names, stale)
		}
	}
}

// A format copy that repeats the parent's release code is the same work, not a
// second card with the same number. The parent keeps one card and owns the
// nested audio.
func TestSourceCatalogFormatCopyWithSameCodeStaysInParentCard(t *testing.T) {
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		items := []map[string]any{}
		switch in.Path {
		case "/":
			items = []map[string]any{{"name": "RJ00000045 Example Work", "is_dir": true}}
		case "/RJ00000045 Example Work":
			items = []map[string]any{
				{"name": "track.mp3", "size": 100},
				{"name": "RJ00000045 MP3+lrc", "is_dir": true},
			}
		case "/RJ00000045 Example Work/RJ00000045 MP3+lrc":
			items = []map[string]any{{"name": "track.mp3", "size": 100}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": len(items)}})
	}))
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	result := f.request(t, http.MethodGet, "/works?q=RJ00000045", "", 200)
	if result["total"] != float64(1) {
		t.Fatalf("search RJ00000045 total=%v, want one card: %s", result["total"], mustJSON(result))
	}
	title := result["works"].([]any)[0].(map[string]any)["title"].(string)
	if title != "RJ00000045 Example Work" {
		t.Fatalf("card title=%q, want the work directory", title)
	}
}
