package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type looseWorkFixture struct {
	catalogIndexFixture
	mu      sync.Mutex
	gets    []string
	updates func(map[string][]map[string]any)
}

// looseWorkUpstream serves the listing tree and records every real media path
// requested through /api/fs/get. The tree is mutable so a test can rescan it.
func newLooseWorkFixture(t *testing.T, tree map[string][]map[string]any) *looseWorkFixture {
	t.Helper()
	f := &looseWorkFixture{}
	f.updates = func(next map[string][]map[string]any) {
		f.mu.Lock()
		defer f.mu.Unlock()
		tree = next
	}
	fixture := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/fs/get":
			var input struct {
				Path string `json:"path"`
			}
			_ = json.NewDecoder(r.Body).Decode(&input)
			f.mu.Lock()
			f.gets = append(f.gets, input.Path)
			f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{
				"name": pathBase(input.Path), "size": 4, "raw_url": "http://" + r.Host + "/audio",
			}})
		case "/audio":
			w.Header().Set("Content-Type", "audio/mpeg")
			_, _ = w.Write([]byte("data"))
		default:
			var input struct {
				Path string `json:"path"`
			}
			_ = json.NewDecoder(r.Body).Decode(&input)
			f.mu.Lock()
			items, ok := tree[input.Path]
			f.mu.Unlock()
			if !ok {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": len(items)}})
		}
	}))
	f.catalogIndexFixture = fixture
	return f
}

func pathBase(value string) string {
	if index := strings.LastIndex(value, "/"); index >= 0 {
		return value[index+1:]
	}
	return value
}

func looseAudio(name string) map[string]any {
	return map[string]any{"name": name, "size": 100}
}

func looseDir(name string) map[string]any {
	return map[string]any{"name": name, "is_dir": true}
}

func (f *looseWorkFixture) cards(t *testing.T) map[string]map[string]any {
	t.Helper()
	list := f.request(t, http.MethodGet, "/works?pageSize=100", "", 200)
	cards := map[string]map[string]any{}
	for _, raw := range list["works"].([]any) {
		card := raw.(map[string]any)
		cards[card["title"].(string)] = card
	}
	return cards
}

func (f *looseWorkFixture) tracks(t *testing.T, card map[string]any) []any {
	t.Helper()
	id := strconv.Itoa(int(card["catalogEntryId"].(float64)))
	body := reviewRequest(t, f.handler, f.cookie, http.MethodGet, "/api/source-catalog/"+id+"/tracks", "", 200)
	return body["tracks"].([]any)
}

// A pure numeric run is one audiobook with many episodes, not many works.
func TestSourceCatalogLooseWorksNumericRunStaysOneCard(t *testing.T) {
	tree := map[string][]map[string]any{
		"/": {looseDir("金麟岂是池中物--叶倩彤")},
	}
	items := []map[string]any{}
	for i := 1; i <= 12; i++ {
		items = append(items, looseAudio(fmt.Sprintf("%03d.mp3", i)))
	}
	tree["/金麟岂是池中物--叶倩彤"] = items
	f := newLooseWorkFixture(t, tree)
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	cards := f.cards(t)
	if len(cards) != 1 {
		t.Fatalf("cards=%v, want one audiobook card", cards)
	}
	card := cards["金麟岂是池中物--叶倩彤"]
	if card == nil {
		t.Fatalf("cards=%v, want the directory title", cards)
	}
	if got := len(f.tracks(t, card)); got != 12 {
		t.Fatalf("tracks=%d, want all 12 episodes on one card", got)
	}
}

// N006-1..3 are three tracks of one numbered work; N007 and N008 are two
// further works. The prefix, not the file count, decides the cards.
func TestSourceCatalogLooseWorksPrefixGroups(t *testing.T) {
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/": {looseDir("Creator")},
		"/Creator": {
			looseAudio("N006-1 First.mp3"),
			looseAudio("N006-2 Second.mp3"),
			looseAudio("N006-3 Third.mp3"),
			looseAudio("N007 Another.mp3"),
			looseAudio("N008 Last.mp3"),
		},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	cards := f.cards(t)
	if len(cards) != 3 {
		t.Fatalf("cards=%v, want three numbered works", cards)
	}
	n006 := cards["N006-1 First"]
	if n006 == nil {
		t.Fatalf("cards=%v, want the first N006 file as the group title", cards)
	}
	if got := len(f.tracks(t, n006)); got != 3 {
		t.Fatalf("N006 tracks=%d, want 3", got)
	}
	for _, query := range []string{"N006", "N007"} {
		result := f.request(t, http.MethodGet, "/works?q="+url.QueryEscape(query), "", 200)
		if result["total"] != float64(1) {
			t.Fatalf("search %s total=%v, want one card: %s", query, result["total"], mustJSON(result))
		}
	}
}

// Twelve unrelated titles are twelve works. Each card carries one track and
// playback still resolves the file under the real creator directory.
func TestSourceCatalogLooseWorksFileCardsAndPlayback(t *testing.T) {
	names := []string{
		"Alpha Rain.mp3", "Beta Wind.mp3", "Cobalt Night.mp3", "Delta Snow.mp3",
		"Ember Sky.mp3", "Frost Garden.mp3", "Golden Tide.mp3", "Harbor Light.mp3",
		"Ivory Road.mp3", "Jade River.mp3", "Kindled Star.mp3", "Lunar Field.mp3",
	}
	items := []map[string]any{}
	for _, name := range names {
		items = append(items, looseAudio(name))
	}
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/":              {looseDir("Loose Creator")},
		"/Loose Creator": items,
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	cards := f.cards(t)
	if len(cards) != len(names) {
		t.Fatalf("cards=%d, want one per file: %v", len(cards), cards)
	}
	for _, name := range names {
		title := strings.TrimSuffix(name, ".mp3")
		card := cards[title]
		if card == nil {
			t.Fatalf("missing card %q in %v", title, cards)
		}
		tracks := f.tracks(t, card)
		if len(tracks) != 1 {
			t.Fatalf("%s tracks=%d, want 1", title, len(tracks))
		}
		id := strconv.Itoa(int(card["catalogEntryId"].(float64)))
		request, _ := http.NewRequest(http.MethodGet, "/api/source-catalog/"+id+"/media?path="+url.QueryEscape(name), nil)
		request.AddCookie(f.cookie)
		response := httptest.NewRecorder()
		f.handler.ServeHTTP(response, request)
		if response.Code != 200 || response.Body.String() != "data" {
			t.Fatalf("%s media: %d %q", title, response.Code, response.Body.String())
		}
	}
	f.mu.Lock()
	gets := slices.Clone(f.gets)
	f.mu.Unlock()
	if len(gets) != len(names) {
		t.Fatalf("media resolutions=%v, want %d", gets, len(names))
	}
	for _, name := range names {
		want := "/Loose Creator/" + name
		if !slices.Contains(gets, want) {
			t.Fatalf("media resolutions=%v, want %q", gets, want)
		}
	}
}

// A creator folder can mix independent works with multi-episode works whose
// episodes have different titles. Grouping by base title keeps 亦弟亦妹1..6 and
// Ending1..3 on one card each instead of splitting every episode into a work.
func TestSourceCatalogLooseWorksGroupsEpisodesByBaseTitle(t *testing.T) {
	names := []string{
		"Alpha Rain.mp3", "Beta Wind.mp3", "Cobalt Night.mp3", "Delta Snow.mp3",
		"Ember Sky.mp3", "Frost Garden.mp3", "Golden Tide.mp3", "Harbor Light.mp3",
		"Ivory Road.mp3", "Jade River.mp3",
		"亦弟亦妹1.mp3", "亦弟亦妹2.mp3", "亦弟亦妹3.mp3", "亦弟亦妹4.mp3",
		"亦弟亦妹5.mp3", "亦弟亦妹6.mp3",
		"Ending1.mp3", "Ending2.mp3", "Ending3.mp3",
	}
	items := []map[string]any{}
	for _, name := range names {
		items = append(items, looseAudio(name))
	}
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/":              {looseDir("Mixed Creator")},
		"/Mixed Creator": items,
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	cards := f.cards(t)
	// Ten independent works plus the two multi-episode works.
	if len(cards) != 12 {
		t.Fatalf("cards=%d, want 12: %v", len(cards), cards)
	}
	for title, want := range map[string]int{"亦弟亦妹1": 6, "Ending1": 3} {
		card := cards[title]
		if card == nil {
			t.Fatalf("cards=%v, want %q as the group title", cards, title)
		}
		if got := len(f.tracks(t, card)); got != want {
			t.Fatalf("%s tracks=%d, want %d", title, got, want)
		}
	}
	if _, ok := cards["亦弟亦妹2"]; ok {
		t.Fatal("episode 2 became its own work")
	}
}

// A second complete generation reuses the same identities and titles.
func TestSourceCatalogLooseWorksRescanIsIdempotent(t *testing.T) {
	names := []string{
		"Alpha Rain", "Beta Wind", "Cobalt Night", "Delta Snow",
		"Ember Sky", "Frost Garden", "Golden Tide", "Harbor Light",
		"Ivory Road", "Jade River", "Kindled Star", "Lunar Field",
	}
	items := []map[string]any{}
	for _, name := range names {
		items = append(items, looseAudio(name+".mp3"))
	}
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/":               {looseDir("Creator Folder")},
		"/Creator Folder": items,
	})
	for pass := 0; pass < 2; pass++ {
		f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
		if p := f.drain(t); p["status"] != "complete" {
			t.Fatalf("scan %d = %#v", pass, p)
		}
		cards := f.cards(t)
		if len(cards) != 12 {
			t.Fatalf("scan %d cards=%d, want 12: %v", pass, len(cards), cards)
		}
		for _, title := range names {
			if cards[title] == nil {
				t.Fatalf("scan %d missing %q: %v", pass, title, cards)
			}
		}
	}
}

// A card that already owns a registered work keeps its single identity even
// when its loose files look like separate works.
func TestSourceCatalogLooseWorksRegisteredCardIsNotSplit(t *testing.T) {
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/": {looseDir("Registered")},
		"/Registered": {
			looseAudio("One.mp3"), looseAudio("Two.mp3"), looseAudio("Three.mp3"),
		},
	})
	sourceID := 1
	if _, err := f.server.db.Exec(`INSERT INTO work(id,primary_code,title) VALUES(99,'RJ00000099','Registered work');
		INSERT INTO source_catalog_entry(file_source_id,root_path,title,work_id) VALUES(?,'/Registered','Registered',99)`, sourceID); err != nil {
		t.Fatal(err)
	}
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	cards := f.cards(t)
	if len(cards) != 1 || cards["Registered"] == nil {
		t.Fatalf("cards=%v, want the registered card unchanged", cards)
	}
}
