package httpapi

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
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
				"name": path.Base(input.Path), "size": 4, "raw_url": "http://" + r.Host + "/audio",
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

func looseAudio(name string) map[string]any {
	return map[string]any{"name": name, "size": 100}
}

func looseAudioSized(name string, size int) map[string]any {
	return map[string]any{"name": name, "size": size}
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

// indexCreator publishes one creator directory through the real index route and
// returns the directory's audio names. Every grouping test below starts from
// this entry point so an internal mode function alone can never pass while the
// published cards are wrong.
func indexCreator(t *testing.T, names []string) (*looseWorkFixture, map[string]map[string]any) {
	t.Helper()
	items := make([]map[string]any, 0, len(names))
	for _, name := range names {
		items = append(items, looseAudio(name))
	}
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/":        {looseDir("Creator")},
		"/Creator": items,
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	return f, f.cards(t)
}

// A release that only publishes its audio below a format directory must not
// bring that directory back as a second work. The card belongs to the release
// directory above "MP3"/"2-mp3"/"1.音声". The parent's own files are artwork,
// a script and a readme, so without this rule every format copy would become
// a playable card of its own.
func TestSourceCatalogFormatCopyDoesNotCreateACard(t *testing.T) {
	cases := []struct {
		name       string
		release    string
		formatName string
		sibling    string
	}{
		{name: "upper case mp3", release: "Example Release", formatName: "MP3", sibling: "ART"},
		{name: "ordinal mp3", release: "RJ00000001 Example", formatName: "2-mp3", sibling: "3-photo"},
		{name: "ordinal japanese audio", release: "Example Voice", formatName: "1.音声", sibling: "2.画像"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newLooseWorkFixture(t, map[string][]map[string]any{
				"/":        {looseDir("Creator")},
				"/Creator": {looseDir(tc.release)},
				"/Creator/" + tc.release: {
					looseDir(tc.formatName), looseDir(tc.sibling), looseAudio("readme.txt"),
				},
				"/Creator/" + tc.release + "/" + tc.formatName: {
					looseAudio("01 first.mp3"), looseAudio("02 second.mp3"),
				},
				"/Creator/" + tc.release + "/" + tc.sibling: {looseAudio("cover.jpg")},
			})
			f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
			if p := f.drain(t); p["status"] != "complete" {
				t.Fatalf("scan = %#v", p)
			}
			cards := f.cards(t)
			if len(cards) != 1 {
				t.Fatalf("cards=%v, want the release card only", cards)
			}
			card := cards[tc.release]
			if card == nil {
				t.Fatalf("cards=%v, want title %q", cards, tc.release)
			}
			if got := countCatalogAudio(f.tracks(t, card)); got != 2 {
				t.Fatalf("tracks=%d, want both format-copy files on the release card", got)
			}
			// The format directory must not be searchable as its own work.
			query := url.QueryEscape(tc.formatName)
			listed := f.request(t, http.MethodGet, "/works?pageSize=100&q="+query, "", 200)
			for _, raw := range listed["works"].([]any) {
				if title := raw.(map[string]any)["title"].(string); title == tc.formatName {
					t.Fatalf("format directory leaked into search: %#v", listed["works"])
				}
			}
		})
	}
}

// countCatalogAudio walks the nested track tree the detail route returns.
func countCatalogAudio(tracks []any) int {
	count := 0
	for _, raw := range tracks {
		track := raw.(map[string]any)
		if track["type"] == "audio" {
			count++
		}
		if children, ok := track["children"].([]any); ok {
			count += countCatalogAudio(children)
		}
	}
	return count
}

// A title that starts with a year or an episode number keeps its number. Only a
// short ordinal directly attached to a format word is a copy marker.
func TestSourceCatalogFormatDirectoryOrdinalIsNotSampleSpecific(t *testing.T) {
	for _, name := range []string{"MP3", "mp3", "2-mp3", "01_mp3", "[2] FLAC", "1.音声", "音声", "ボイス"} {
		if !catalogFormatDirectory(name) {
			t.Fatalf("%q is not a format directory", name)
		}
	}
	for _, name := range []string{"2021 Live", "24.歡迎光臨閃亮銀CV床貓", "音声剧", "MP3 Collection", "ART", "3-photo"} {
		if catalogFormatDirectory(name) {
			t.Fatalf("%q must not be treated as a format copy", name)
		}
	}
	if !catalogAuxiliaryDirectory("ART") || !catalogAuxiliaryDirectory("3-photo") {
		t.Fatal("artwork directories must stay inside the release")
	}
}

// A pure numeric run is one audiobook with many episodes, not many works.
func TestSourceCatalogLooseWorksNumericRunStaysOneCard(t *testing.T) {
	tree := map[string][]map[string]any{
		"/": {looseDir("Example Audiobook")},
	}
	items := []map[string]any{}
	for i := 1; i <= 12; i++ {
		items = append(items, looseAudio(fmt.Sprintf("%03d.mp3", i)))
	}
	tree["/Example Audiobook"] = items
	f := newLooseWorkFixture(t, tree)
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	cards := f.cards(t)
	if len(cards) != 1 {
		t.Fatalf("cards=%v, want one audiobook card", cards)
	}
	card := cards["Example Audiobook"]
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
// episodes have different titles. Grouping by base title keeps Example
// Episode1..6 and Ending1..3 on one card each instead of splitting every
// episode into a work.
func TestSourceCatalogLooseWorksGroupsEpisodesByBaseTitle(t *testing.T) {
	names := []string{
		"Alpha Rain.mp3", "Beta Wind.mp3", "Cobalt Night.mp3", "Delta Snow.mp3",
		"Ember Sky.mp3", "Frost Garden.mp3", "Golden Tide.mp3", "Harbor Light.mp3",
		"Ivory Road.mp3", "Jade River.mp3",
		"Example Episode1.mp3", "Example Episode2.mp3", "Example Episode3.mp3",
		"Example Episode4.mp3", "Example Episode5.mp3", "Example Episode6.mp3",
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
	for title, want := range map[string]int{"Example Episode1": 6, "Ending1": 3} {
		card := cards[title]
		if card == nil {
			t.Fatalf("cards=%v, want %q as the group title", cards, title)
		}
		if got := len(f.tracks(t, card)); got != want {
			t.Fatalf("%s tracks=%d, want %d", title, got, want)
		}
	}
	if _, ok := cards["Example Episode2"]; ok {
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

// A package copy of the parent's loose audio is one version of that folder.
// Splitting it would publish every recording a second time under a new card.
func TestSourceCatalogLooseWorksPackageCopyStaysOneCard(t *testing.T) {
	parent := []map[string]any{}
	copyItems := []map[string]any{}
	for i := 1; i <= 24; i++ {
		size := 1_000_000 + i*7
		parent = append(parent, looseAudioSized(fmt.Sprintf("Work %02d.mp3", i), size))
		copyItems = append(copyItems, looseAudioSized(fmt.Sprintf("Creator %02d.mp3", i), size+3))
	}
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/":                             {looseDir("Creator")},
		"/Creator":                      append(parent, looseDir("Creator 24 works 14G")),
		"/Creator/Creator 24 works 14G": copyItems,
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	cards := f.cards(t)
	copied := cards["Creator 24 works 14G"]
	if copied == nil {
		t.Fatalf("cards=%v, want the package copy card", cards)
	}
	if got := len(f.tracks(t, copied)); got != len(copyItems) {
		t.Fatalf("package copy tracks=%d, want %d on one card", got, len(copyItems))
	}
	for title := range cards {
		if strings.HasPrefix(title, "Creator ") && title != "Creator 24 works 14G" {
			t.Fatalf("package copy split into card %q: %v", title, cards)
		}
	}
}

// A suffix digit is a track number only when a real title remains. Dates and
// bare counters keep their full stem, so unrelated recordings do not collapse
// onto a fragment such as 2021 or 12.
func TestCatalogBaseTitleKeepsNumberOnlyNames(t *testing.T) {
	for name, want := range map[string]string{
		"20210104.mp3":          "20210104",
		"2020-12-16.mp3":        "2020-12-16",
		"2021.01.03.m4a":        "2021.01.03",
		"61 .mp3":               "61",
		"Example Session 1.mp3": "example session",
		"01 Example Work.mp3":   "example work",
	} {
		if got := catalogBaseTitle(name); got != want {
			t.Fatalf("catalogBaseTitle(%q) = %q, want %q", name, got, want)
		}
	}
}

// The same rule must hold through a complete index: date-only recordings keep
// their own cards, while a title with a track number still shares one card.
func TestSourceCatalogLooseWorksKeepsNumberOnlyNamesDistinct(t *testing.T) {
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/": {looseDir("Creator")},
		"/Creator": {
			looseAudio("20210104.mp3"),
			looseAudio("20210110.mp3"),
			looseAudio("2020-12-16.mp3"),
			looseAudio("2021-01-03.mp3"),
			looseAudio("Alpha Recording.mp3"),
			looseAudio("Beta Recording.mp3"),
			looseAudio("Gamma Recording.mp3"),
			looseAudio("Delta Recording.mp3"),
			looseAudio("Epsilon Recording.mp3"),
			looseAudio("Zeta Recording.mp3"),
			looseAudio("Eta Recording.mp3"),
			looseAudio("Theta Recording.mp3"),
			looseAudio("Example Session 1.mp3"),
			looseAudio("Example Session 2.mp3"),
			looseAudio("Example Session 3.mp3"),
			looseAudio("Example Session 4.mp3"),
		},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	cards := f.cards(t)
	session := cards["Example Session 1"]
	if session == nil {
		t.Fatalf("cards=%v, want one card for the numbered session", cards)
	}
	if got := len(f.tracks(t, session)); got != 4 {
		t.Fatalf("session tracks=%d, want 4 on one card", got)
	}
	for _, title := range []string{"20210104", "20210110", "2020-12-16", "2021-01-03"} {
		if cards[title] == nil {
			t.Fatalf("date recording %q lost its own card: %v", title, cards)
		}
	}
	if len(cards) != 13 {
		t.Fatalf("cards=%d, want 4 date cards, eight singles and one session card: %v", len(cards), cards)
	}
}

// A repeated work number is not enough to make two files one work. Creator
// folders reuse a number for unrelated releases, so titles that share neither
// a base title nor a numbered track shape stay on separate cards. A second
// scan must publish the same cards instead of folding them back together.
func TestSourceCatalogLooseWorksPrefixWithDifferentTitlesStaysSeparate(t *testing.T) {
	names := []string{
		"B011 性感女销售 第一话.mp3",
		"B011 粗口空姐 第二话.mp3",
		"B012 温柔护士 上.mp3",
		"B012 冷艳医生 下.mp3",
	}
	items := make([]map[string]any, 0, len(names))
	for _, name := range names {
		items = append(items, looseAudio(name))
	}
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/":        {looseDir("Creator")},
		"/Creator": items,
	})
	for pass := 0; pass < 2; pass++ {
		body := `{"intervalMs":100}`
		if pass > 0 {
			body = `{"intervalMs":100,"force":true}`
		}
		f.request(t, http.MethodPost, "/catalog/index", body, 202)
		if p := f.drain(t); p["status"] != "complete" {
			t.Fatalf("scan %d = %#v", pass, p)
		}
		cards := f.cards(t)
		if len(cards) != len(names) {
			t.Fatalf("scan %d cards=%d, want %d separate works: %v", pass, len(cards), len(names), cards)
		}
		for _, name := range names {
			title := strings.TrimSuffix(name, ".mp3")
			card := cards[title]
			if card == nil {
				t.Fatalf("scan %d missing %q: %v", pass, title, cards)
			}
			if got := len(f.tracks(t, card)); got != 1 {
				t.Fatalf("scan %d %s tracks=%d, want 1", pass, title, got)
			}
		}
	}
}

// The same prefix and the same title with different episode numbers is one
// work with ordered tracks, not one work per episode.
func TestSourceCatalogLooseWorksPrefixSameTitleNumbersStayTogether(t *testing.T) {
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/": {looseDir("Creator")},
		"/Creator": {
			looseAudio("B20 妻子的绿帽地狱01.mp3"),
			looseAudio("B20 妻子的绿帽地狱02.mp3"),
			looseAudio("B21 另一部作品01.mp3"),
			looseAudio("B21 另一部作品02.mp3"),
		},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	cards := f.cards(t)
	if len(cards) != 2 {
		t.Fatalf("cards=%d, want two numbered works: %v", len(cards), cards)
	}
	card := cards["B20 妻子的绿帽地狱01"]
	if card == nil {
		t.Fatalf("missing numbered work card: %v", cards)
	}
	if got := len(f.tracks(t, card)); got != 2 {
		t.Fatalf("tracks=%d, want 2", got)
	}
}

// A four-digit year is not a track position. Three dated recordings with
// different titles must remain three cards instead of forming a numbered run.
func TestSourceCatalogLooseWorksFourDigitYearsAreNotTrackRuns(t *testing.T) {
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/": {looseDir("Creator")},
		"/Creator": {
			looseAudio("2021 Opening.mp3"),
			looseAudio("2022 Confession.mp3"),
			looseAudio("2023 Ending.mp3"),
		},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	cards := f.cards(t)
	if len(cards) != 3 {
		t.Fatalf("cards=%d, want three dated works: %v", len(cards), cards)
	}
	for _, title := range []string{"2021 Opening", "2022 Confession", "2023 Ending"} {
		if cards[title] == nil {
			t.Fatalf("missing dated work %q: %v", title, cards)
		}
	}
}

func TestCatalogNumberedTrackRunRejectsFourDigitNumbers(t *testing.T) {
	for _, names := range [][]string{
		{"2021 Opening.mp3", "2022 Confession.mp3", "2023 Ending.mp3"},
		{"2020-12-16.mp3", "2021-01-03.mp3"},
		{"1000 Opening.mp3", "1001 Ending.mp3"},
	} {
		if catalogNumberedTrackRun(names) {
			t.Fatalf("catalogNumberedTrackRun(%v) = true, want false", names)
		}
	}
	if !catalogNumberedTrackRun([]string{"01 Opening.mp3", "02 Confession.mp3", "03 Ending.mp3"}) {
		t.Fatal("a 01..03 track run must stay one work")
	}
}

// A release code may appear only once in a creator folder. Two files that share
// it are parts of one release, not two independent works.
func TestCatalogWorkSplitModeGroupsSingleRepeatedReleaseCode(t *testing.T) {
	names := []string{"RJ00000001 上.mp3", "RJ00000001 下.mp3"}
	if got := catalogWorkSplitModeNames(names); got != catalogWorkSplitPrefix {
		t.Fatalf("split mode = %q, want prefix for one repeated release code", got)
	}
}

// The split rules are only useful when the published cards follow them. Each
// case below indexes a real creator directory and counts the cards and tracks
// the API returns, so an internal predicate that disagrees with publication
// fails the test.
func TestSourceCatalogSplitRulesPublishExpectedCards(t *testing.T) {
	cases := []struct {
		name   string
		names  []string
		cards  int
		tracks int
		titles []string
	}{
		{
			name:   "paired release halves",
			names:  []string{"RJ00000001 上.mp3", "RJ00000001 下.mp3"},
			cards:  1,
			tracks: 2,
			titles: []string{"RJ00000001 上"},
		},
		{
			name:   "disc markers",
			names:  []string{"Disc1 Opening.mp3", "Disc2 Ending.mp3"},
			cards:  1,
			tracks: 2,
			titles: []string{"Creator"},
		},
		{
			name:   "cd markers",
			names:  []string{"CD1 First.mp3", "CD2 Second.mp3"},
			cards:  1,
			tracks: 2,
			titles: []string{"Creator"},
		},
		{
			name:   "part markers",
			names:  []string{"Part1 First.mp3", "Part2 Second.mp3"},
			cards:  1,
			tracks: 2,
			titles: []string{"Creator"},
		},
		{
			name:   "disc markers with a work code",
			names:  []string{"B20 Disc1 First.mp3", "B20 Disc2 Second.mp3"},
			cards:  1,
			tracks: 2,
			titles: []string{"Creator"},
		},
		{
			name:   "year is part of the title",
			names:  []string{"2021 Live.mp3", "2022 Live.mp3"},
			cards:  2,
			tracks: 1,
			titles: []string{"2021 Live", "2022 Live"},
		},
		{
			name:   "volumes stay separate",
			names:  []string{"作品 Vol.1.mp3", "作品 Vol.2.mp3"},
			cards:  2,
			tracks: 1,
			titles: []string{"作品 Vol.1", "作品 Vol.2"},
		},
		{
			name:   "of total tracks",
			names:  []string{"1of3 Title.mp3", "2of3 Title.mp3", "3of3 Title.mp3"},
			cards:  1,
			tracks: 3,
			titles: []string{"Creator"},
		},
		{
			name:   "zero prelude with numbered tracks",
			names:  []string{"00 Intro.mp3", "01 Main.mp3", "02 End.mp3"},
			cards:  1,
			tracks: 3,
			titles: []string{"Creator"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, cards := indexCreator(t, tc.names)
			if len(cards) != tc.cards {
				t.Fatalf("cards=%d, want %d: %v", len(cards), tc.cards, cards)
			}
			for _, title := range tc.titles {
				card := cards[title]
				if card == nil {
					t.Fatalf("missing card %q in %v", title, cards)
				}
				if got := len(f.tracks(t, card)); got != tc.tracks {
					t.Fatalf("%s tracks=%d, want %d", title, got, tc.tracks)
				}
			}
		})
	}
}

// A single-character trailing 上/下 is part of an ordinary word unless the
// directory pairs it with the other half. 之前 and 之后 are two recordings, not
// two halves of one release.
func TestSourceCatalogSingleCharacterPartWordsStayDistinct(t *testing.T) {
	_, cards := indexCreator(t, []string{"之前.mp3", "之后.mp3"})
	if len(cards) != 2 {
		t.Fatalf("cards=%d, want two recordings: %v", len(cards), cards)
	}
	for _, title := range []string{"之前", "之后"} {
		if cards[title] == nil {
			t.Fatalf("missing recording %q in %v", title, cards)
		}
	}
}

// A grouping change publishes a new card key for files an older card already
// owned. The older card can carry alias children from an earlier relocation.
// Its aliases must move to the card that took over the same files, and the old
// card must retire, otherwise the same work stays visible twice.
func TestSourceCatalogStaleSplitCardMovesAliasesToItsReplacement(t *testing.T) {
	f, cards := indexCreator(t, []string{"N006-1 First.mp3", "N006-2 Second.mp3"})
	if len(cards) != 1 {
		t.Fatalf("cards=%d, want one split card: %v", len(cards), cards)
	}
	current := int64(cards["N006-1 First"]["catalogEntryId"].(float64))
	// Rebuild the state a previous generation leaves behind: an old split card
	// that owned exactly these files, plus an alias child that points at it.
	if _, err := f.server.db.Exec(`INSERT INTO source_catalog_entry(id,file_source_id,root_path,source_root,title,last_seen_generation)
			VALUES(900,1,'/Creator::old-key','/Creator','N006-1 First',0);
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes,observed_generation)
			VALUES(900,'N006-1 First.mp3','N006-1 First.mp3','audio',100,0),
				(900,'N006-2 Second.mp3','N006-2 Second.mp3','audio',100,0);
		INSERT INTO source_catalog_entry(id,root_path,title,relocated_to_id)
			VALUES(901,'/Creator::legacy-alias','N006-1 First',900);`); err != nil {
		t.Fatal(err)
	}
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"force":true}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("rescan = %#v", p)
	}
	after := f.cards(t)
	if len(after) != 1 || after["N006-1 First"] == nil {
		t.Fatalf("visible cards after retirement = %v, want one card", after)
	}
	var sourceID sql.NullInt64
	var relocated sql.NullInt64
	if err := f.server.db.QueryRow(`SELECT file_source_id,relocated_to_id FROM source_catalog_entry WHERE id=900`).Scan(&sourceID, &relocated); err != nil {
		t.Fatal(err)
	}
	if sourceID.Valid || !relocated.Valid || relocated.Int64 != current {
		t.Fatalf("stale card was not retired to the current card: source=%v relocated=%v want=%d", sourceID, relocated, current)
	}
	if err := f.server.db.QueryRow(`SELECT relocated_to_id FROM source_catalog_entry WHERE id=901`).Scan(&relocated); err != nil {
		t.Fatal(err)
	}
	if !relocated.Valid || relocated.Int64 != current {
		t.Fatalf("alias child still points at %v, want the current card %d", relocated, current)
	}
	// The id is what personal records reference, so it must survive the move.
	var refs int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM personal_transfer_ref WHERE catalog_entry_id=901`).Scan(&refs); err != nil {
		t.Fatal(err)
	}
	if refs != 1 {
		t.Fatalf("alias personal refs=%d, want the original id kept", refs)
	}
	var files int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_file WHERE catalog_entry_id=900`).Scan(&files); err != nil {
		t.Fatal(err)
	}
	if files != 0 {
		t.Fatalf("retired card kept %d file rows", files)
	}
}

// A stale card whose identity file no longer belongs to any current card has no
// named replacement. Its aliases stay put instead of being reassigned to an
// unrelated work merely to make the duplicate disappear.
func TestSourceCatalogStaleSplitCardWithoutSingleReplacementKeepsAliases(t *testing.T) {
	f, cards := indexCreator(t, []string{"N006-1 First.mp3", "N006-2 Second.mp3"})
	if len(cards) != 1 {
		t.Fatalf("cards=%d, want one split card: %v", len(cards), cards)
	}
	if _, err := f.server.db.Exec(`INSERT INTO source_catalog_entry(id,file_source_id,root_path,source_root,title,last_seen_generation)
			VALUES(910,1,'/Creator::old-key','/Creator','Removed Work',0);
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes,observed_generation)
			VALUES(910,'Removed Work.mp3','Removed Work.mp3','audio',100,0);
		INSERT INTO source_catalog_entry(id,root_path,title,relocated_to_id)
			VALUES(911,'/Creator::legacy-alias','Removed Work',910);`); err != nil {
		t.Fatal(err)
	}
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"force":true}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("rescan = %#v", p)
	}
	var sourceID sql.NullInt64
	if err := f.server.db.QueryRow(`SELECT file_source_id FROM source_catalog_entry WHERE id=910`).Scan(&sourceID); err != nil {
		t.Fatal(err)
	}
	if !sourceID.Valid {
		t.Fatal("a stale card without a named replacement was retired anyway")
	}
	var relocated sql.NullInt64
	if err := f.server.db.QueryRow(`SELECT relocated_to_id FROM source_catalog_entry WHERE id=911`).Scan(&relocated); err != nil {
		t.Fatal(err)
	}
	if !relocated.Valid || relocated.Int64 != 910 {
		t.Fatalf("alias child moved to %v without evidence", relocated)
	}
}

// Short prefix forms are not limited to N/B. A creator may reuse any short
// alphanumeric label for a multi-part release.
func TestCatalogWorkPrefixAcceptsShortAlphanumericCodes(t *testing.T) {
	for _, name := range []string{"ABC12 part one.mp3", "Z9 ending.mp3", "VJ00000000 上.mp3"} {
		if got := catalogWorkPrefix(name); got == "" {
			t.Fatalf("catalogWorkPrefix(%q) = empty, want a code", name)
		}
	}
}

// A volume marker is part of the title. Vol.1 and Vol.2 are separate releases,
// so the trailing-number rule must not fold them onto one card.
func TestCatalogVolumeTitlesStayDistinct(t *testing.T) {
	names := []string{"作品 Vol.1.mp3", "作品 Vol.2.mp3"}
	if got := catalogWorkSplitModeNames(names); got != catalogWorkSplitFile {
		t.Fatalf("split mode = %q, want one card per volume", got)
	}
	grouping := newCatalogLooseGrouping(names, catalogWorkSplitFile)
	if grouping.keyFor(names[0]) == grouping.keyFor(names[1]) {
		t.Fatalf("Vol.1 and Vol.2 share key %q", grouping.keyFor(names[0]))
	}
	for _, name := range []string{"作品 Vol.1.mp3", "作品 Volume 2.mp3", "作品 Vol. 3.mp3"} {
		if base := catalogBaseTitle(name); !strings.Contains(base, "vol") {
			t.Fatalf("catalogBaseTitle(%q) = %q, volume marker was stripped", name, base)
		}
	}
}

// "1of3 Title" is a track of one release, not three works. The ordinal suffix
// must be removed with the track number rather than keeping the number in the
// base title, which used to make every track its own card.
func TestCatalogOfTotalTrackNamesStayOneWork(t *testing.T) {
	for _, names := range [][]string{
		{"1of3 Title.mp3", "2of3 Title.mp3", "3of3 Title.mp3"},
		{"1 of 2例の作品.mp3", "2 of 2例の作品.mp3"},
	} {
		if !catalogNumberedTrackRun(names) {
			t.Fatalf("catalogNumberedTrackRun(%v) = false, want a track run", names)
		}
		if bases := map[string]bool{}; true {
			for _, name := range names {
				bases[catalogBaseTitle(name)] = true
			}
			if len(bases) != 1 {
				t.Fatalf("base titles %v did not collapse: %v", names, bases)
			}
		}
	}
	if catalogNumberedTrackRun([]string{"1 of 2 Opening.mp3", "2 of 3 Ending.mp3"}) {
		t.Fatal("a mismatched total is not one ordered run")
	}
}
