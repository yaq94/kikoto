package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"strconv"
	"testing"

	"github.com/yexca/kikoto/backend/internal/openlist"
)

func catalogRegressionTracks(count int, sizeBase int) []map[string]any {
	tracks := make([]map[string]any, 0, count)
	for index := 1; index <= count; index++ {
		tracks = append(tracks, looseAudioSized(fmt.Sprintf("Track %02d.mp3", index), sizeBase+index*4096))
	}
	return tracks
}

func catalogRegressionCard(t *testing.T, cards map[string]map[string]any, title string) map[string]any {
	t.Helper()
	if len(cards) != 1 {
		t.Fatalf("cards=%v, want only %q", cards, title)
	}
	card := cards[title]
	if card == nil {
		t.Fatalf("cards=%v, want title %q", cards, title)
	}
	return card
}

func catalogRegressionPaths(tracks []any) map[string]bool {
	paths := map[string]bool{}
	var walk func([]any)
	walk = func(items []any) {
		for _, raw := range items {
			track := raw.(map[string]any)
			if track["type"] == "audio" {
				paths[track["path"].(string)] = true
			}
			if children, ok := track["children"].([]any); ok {
				walk(children)
			}
		}
	}
	walk(tracks)
	return paths
}

// A child package that repeats every parent track and adds one bonus recording
// is still a format copy: it must not create a card, and the bonus file must
// stay playable through the canonical parent card.
func TestSourceCatalogDuplicateChildKeepsUniqueBonusTrack(t *testing.T) {
	parent := catalogRegressionTracks(20, 10_000_000)
	child := append([]map[string]any{}, parent...)
	child = append(child, looseAudioSized("bonus.mp3", 9_999_999))
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/":                           {looseDir("Creator")},
		"/Creator":                    {looseDir("Release")},
		"/Creator/Release":            append(append([]map[string]any{}, parent...), looseDir("Bonus copy")),
		"/Creator/Release/Bonus copy": child,
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if progress := f.drain(t); progress["status"] != "complete" {
		t.Fatalf("scan = %#v", progress)
	}
	card := catalogRegressionCard(t, f.cards(t), "Release")
	tracks := f.tracks(t, card)
	if got := countCatalogAudio(tracks); got != 21 {
		t.Fatalf("parent tracks=%d, want all 20 originals plus the unique bonus", got)
	}
	if !catalogRegressionPaths(tracks)["Bonus copy/bonus.mp3"] {
		t.Fatalf("unique bonus track is not addressable: %#v", catalogRegressionPaths(tracks))
	}
}

// A package container can repeat the parent's audio through several nested
// work folders. Those folders are format copies of the parent card, not ten
// additional works.
func TestSourceCatalogNestedPackageCopyDoesNotPublishWorkCards(t *testing.T) {
	parent := catalogRegressionTracks(20, 10_000_000)
	packageContents := []map[string]any{}
	tree := map[string][]map[string]any{
		"/":                {looseDir("Creator")},
		"/Creator":         {looseDir("Release")},
		"/Creator/Release": append(append([]map[string]any{}, parent...), looseDir("Package")),
	}
	for work := 1; work <= 10; work++ {
		name := fmt.Sprintf("Work %02d", work)
		packageContents = append(packageContents, looseDir(name))
		tree["/Creator/Release/Package/"+name] = append([]map[string]any{}, parent[(work-1)*2:work*2]...)
	}
	tree["/Creator/Release/Package"] = packageContents
	f := newLooseWorkFixture(t, tree)
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if progress := f.drain(t); progress["status"] != "complete" {
		t.Fatalf("scan = %#v", progress)
	}
	card := catalogRegressionCard(t, f.cards(t), "Release")
	if got := countCatalogAudio(f.tracks(t, card)); got != 20 {
		t.Fatalf("parent tracks=%d, want the original 20 without copies", got)
	}
}

func TestSourceCatalogTwoTrackPackageCopyDoesNotPublishCard(t *testing.T) {
	tracks := catalogRegressionTracks(2, 10_000_000)
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/":                     {looseDir("Creator")},
		"/Creator":              {looseDir("Release")},
		"/Creator/Release":      append(append([]map[string]any{}, tracks...), looseDir("Copy")),
		"/Creator/Release/Copy": append([]map[string]any{}, tracks...),
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if progress := f.drain(t); progress["status"] != "complete" {
		t.Fatalf("scan = %#v", progress)
	}
	card := catalogRegressionCard(t, f.cards(t), "Release")
	if got := countCatalogAudio(f.tracks(t, card)); got != 2 {
		t.Fatalf("parent tracks=%d, want 2 without a second copy card", got)
	}
}

// A complete one-file copy is still a format copy. Parent and child must agree
// on the exact file identity; a lone track below a larger parent stays a real
// independent excerpt.
func TestSourceCatalogSingleFileFormatCopyDoesNotPublishSecondCard(t *testing.T) {
	const digest = "0000000000000000000000000000000000000000000000000000000000000001"
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/":             {looseDir("Creator")},
		"/Creator":      {looseAudioHashed("only.mp3", 12345, digest), looseDir("Copy")},
		"/Creator/Copy": {looseAudioHashed("only.mp3", 12345, digest)},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if progress := f.drain(t); progress["status"] != "complete" {
		t.Fatalf("scan = %#v", progress)
	}
	card := catalogRegressionCard(t, f.cards(t), "Creator")
	if got := countCatalogAudio(f.tracks(t, card)); got != 1 {
		t.Fatalf("parent tracks=%d, want one", got)
	}
}

// Equal names and sizes are not proof without provider content hashes. A
// one-file directory stays a separate work instead of hiding an independent
// recording.
func TestSourceCatalogSingleFileWithoutHashKeepsIndependentCard(t *testing.T) {
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/":              {looseDir("Creator")},
		"/Creator":       {looseAudioSized("only.mp3", 12345), looseDir("Other")},
		"/Creator/Other": {looseAudioSized("only.mp3", 12345)},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, http.StatusAccepted)
	if progress := f.drain(t); progress["status"] != "complete" {
		t.Fatalf("scan = %#v", progress)
	}
	if got := len(f.cards(t)); got != 2 {
		t.Fatalf("cards=%d, want two independent one-file works", got)
	}
}

// The bonus allowance also applies to small packages: every parent file plus
// one extra recording remains one canonical card and keeps the bonus playable.
func TestSourceCatalogSmallPackageCopyKeepsUniqueBonus(t *testing.T) {
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/":             {looseDir("Creator")},
		"/Creator":      {looseAudioSized("01.mp3", 1000), looseAudioSized("02.mp3", 2000), looseDir("Copy")},
		"/Creator/Copy": {looseAudioSized("01.mp3", 1000), looseAudioSized("02.mp3", 2000), looseAudioSized("bonus.mp3", 3000)},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if progress := f.drain(t); progress["status"] != "complete" {
		t.Fatalf("scan = %#v", progress)
	}
	card := catalogRegressionCard(t, f.cards(t), "Creator")
	tracks := f.tracks(t, card)
	if got := countCatalogAudio(tracks); got != 3 {
		t.Fatalf("parent tracks=%d, want two originals plus bonus", got)
	}
	if !catalogRegressionPaths(tracks)["Copy/bonus.mp3"] {
		t.Fatalf("bonus is not addressable: %#v", catalogRegressionPaths(tracks))
	}
}

// Explicit episode numbers on loose files describe a series, not one work with
// six tracks. Each episode remains independently addressable and reviewable.
func TestSourceCatalogLooseEpisodeFilesBecomeASeries(t *testing.T) {
	files := make([]map[string]any, 0, 6)
	for episode := 1; episode <= 6; episode++ {
		files = append(files, looseAudio(fmt.Sprintf("Example Episode%d.mp3", episode)))
	}
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/":        {looseDir("Creator")},
		"/Creator": files,
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if progress := f.drain(t); progress["status"] != "complete" {
		t.Fatalf("scan = %#v", progress)
	}
	cards := f.cards(t)
	if len(cards) != 1 {
		t.Fatalf("cards=%v, want one series card", cards)
	}
	var card map[string]any
	for _, candidate := range cards {
		card = candidate
	}
	seriesID, ok := card["seriesId"].(float64)
	if !ok || seriesID == 0 {
		t.Fatalf("loose episodes stayed one work card: %#v", card)
	}
	view := reviewRequest(t, f.handler, f.cookie, http.MethodGet, "/api/catalog-series/"+strconv.Itoa(int(seriesID)), "", 200)
	episodes := view["episodes"].([]any)
	if len(episodes) != 6 {
		t.Fatalf("series episodes=%d, want 6: %#v", len(episodes), episodes)
	}
	for index, raw := range episodes {
		episode := raw.(map[string]any)
		if int(episode["position"].(float64)) != index+1 {
			t.Fatalf("episode positions: %#v", episodes)
		}
		if episode["title"] != fmt.Sprintf("Example Episode%d", index+1) {
			t.Fatalf("episode title=%v", episode["title"])
		}
		entryID := strconv.Itoa(int(episode["catalogEntryId"].(float64)))
		detail := reviewRequest(t, f.handler, f.cookie, http.MethodGet, "/api/source-catalog/"+entryID, "", 200)
		if detail["seriesId"] != seriesID {
			t.Fatalf("episode %d series=%v, want %v", index+1, detail["seriesId"], seriesID)
		}
		tracks := reviewRequest(t, f.handler, f.cookie, http.MethodGet, "/api/source-catalog/"+entryID+"/tracks", "", 200)
		if len(tracks["tracks"].([]any)) != 1 {
			t.Fatalf("episode %d tracks=%#v", index+1, tracks["tracks"])
		}
	}
	first := strconv.Itoa(int(episodes[0].(map[string]any)["catalogEntryId"].(float64)))
	second := strconv.Itoa(int(episodes[1].(map[string]any)["catalogEntryId"].(float64)))
	reviewRequest(t, f.handler, f.cookie, http.MethodPatch, "/api/source-catalog/"+first+"/user-state", `{"rating":4,"note":"Episode one"}`, 200)
	assertReview(t, reviewRequest(t, f.handler, f.cookie, http.MethodGet, "/api/source-catalog/"+first, "", 200), float64(4), "Episode one")
	assertReview(t, reviewRequest(t, f.handler, f.cookie, http.MethodGet, "/api/source-catalog/"+second, "", 200), nil, "")
}

// A series whose only files are format copies of its episodes still has one
// series card and two episodes. The format directories never become cards.
func TestSourceCatalogFormatOnlyEpisodesFormOneSeries(t *testing.T) {
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/":                              {looseDir("Creator")},
		"/Creator":                       {looseDir("Show")},
		"/Creator/Show":                  {looseDir("Episode 1 (MP3)"), looseDir("Episode 1 (FLAC)"), looseDir("Episode 2 (MP3)")},
		"/Creator/Show/Episode 1 (MP3)":  {looseAudioSized("track.mp3", 100)},
		"/Creator/Show/Episode 1 (FLAC)": {looseAudioSized("track.flac", 300)},
		"/Creator/Show/Episode 2 (MP3)":  {looseAudioSized("track.mp3", 200)},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if progress := f.drain(t); progress["status"] != "complete" {
		t.Fatalf("scan = %#v", progress)
	}
	card := catalogRegressionCard(t, f.cards(t), "Show")
	seriesID, ok := card["seriesId"].(float64)
	if !ok || seriesID == 0 {
		t.Fatalf("format-only episodes stayed separate cards: %#v", card)
	}
	view := reviewRequest(t, f.handler, f.cookie, http.MethodGet, "/api/catalog-series/"+strconv.Itoa(int(seriesID)), "", 200)
	episodes := view["episodes"].([]any)
	if len(episodes) != 2 {
		t.Fatalf("series episodes=%d, want 2: %#v", len(episodes), episodes)
	}
	byTitle := map[string]map[string]any{}
	for _, raw := range episodes {
		episode := raw.(map[string]any)
		byTitle[episode["title"].(string)] = episode
	}
	for _, title := range []string{"Episode 1", "Episode 2"} {
		if byTitle[title] == nil {
			t.Fatalf("missing episode %q: %#v", title, episodes)
		}
	}
	firstID := strconv.Itoa(int(byTitle["Episode 1"]["catalogEntryId"].(float64)))
	firstTracks := reviewRequest(t, f.handler, f.cookie, http.MethodGet, "/api/source-catalog/"+firstID+"/tracks", "", 200)
	if got := countCatalogAudio(firstTracks["tracks"].([]any)); got != 2 {
		t.Fatalf("episode 1 tracks=%d, want both formats", got)
	}
}

// Episode-like words are series evidence only when they carry an explicit
// episode marker. Bare continuous numbers and track-part directories stay one
// work.
func TestCatalogLooseEpisodeModeRequiresExplicitMarkers(t *testing.T) {
	tests := []struct {
		name  string
		names []string
		mode  string
	}{
		{name: "explicit episodes", names: []string{"Example Episode1.mp3", "Example Episode2.mp3"}, mode: "episode"},
		{name: "bare numbered run", names: []string{"01.mp3", "02.mp3", "03.mp3"}, mode: "single"},
		{name: "trailing number run", names: []string{"Example Session 1.mp3", "Example Session 2.mp3"}, mode: "single"},
		{name: "track containers", names: []string{"Disc1 Opening.mp3", "Disc2 Ending.mp3"}, mode: "single"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := catalogWorkSplitModeNames(tc.names); got != tc.mode {
				t.Fatalf("mode=%q, want %q", got, tc.mode)
			}
		})
	}
}

func TestCatalogEpisodeSplitHandlesLooseAndFormatOnlyEpisodes(t *testing.T) {
	loose := []openlist.File{}
	for episode := 1; episode <= 3; episode++ {
		loose = append(loose, openlist.File{
			Path:  fmt.Sprintf("Example Episode%d.mp3", episode),
			Title: fmt.Sprintf("Example Episode%d.mp3", episode),
			Kind:  "audio",
			Size:  int64(episode * 100),
		})
	}
	groups, parent := splitCatalogEpisodeRoots("/Creator", loose)
	if len(groups) != 3 || len(parent) != 0 {
		t.Fatalf("loose episode groups=%d parent=%d", len(groups), len(parent))
	}
	for episode := 1; episode <= 3; episode++ {
		root := fmt.Sprintf("/Creator::episode:example|%d", episode)
		group, ok := groups[root]
		if !ok || group.SourceRoot != "/Creator" || len(group.Files) != 1 {
			t.Fatalf("loose episode %d group=%#v", episode, group)
		}
	}

	formatOnly := []openlist.File{
		{Path: "Episode 1 (MP3)/track.mp3", Title: "track.mp3", Kind: "audio", Size: 100},
		{Path: "Episode 1 (FLAC)/track.flac", Title: "track.flac", Kind: "audio", Size: 300},
		{Path: "Episode 2 (MP3)/track.mp3", Title: "track.mp3", Kind: "audio", Size: 200},
	}
	groups, parent = splitCatalogEpisodeRoots("/Show", formatOnly)
	if len(groups) != 2 || len(parent) != 0 {
		t.Fatalf("format episode groups=%d parent=%d", len(groups), len(parent))
	}
	if groups["/Show/Episode 1"].SourceRoot != "/Show" || len(groups["/Show/Episode 1"].Files) != 2 {
		t.Fatalf("episode 1 formats were not folded: %#v", groups["/Show/Episode 1"])
	}
	if groups["/Show/Episode 2"].SourceRoot != "/Show" || len(groups["/Show/Episode 2"].Files) != 1 {
		t.Fatalf("episode 2 format card missing: %#v", groups["/Show/Episode 2"])
	}

	bare := []openlist.File{
		{Path: "01.mp3", Title: "01.mp3", Kind: "audio", Size: 100},
		{Path: "02.mp3", Title: "02.mp3", Kind: "audio", Size: 200},
	}
	if groups, _ = splitCatalogEpisodeRoots("/Creator", bare); groups != nil {
		t.Fatalf("bare numbered files became a series: %#v", groups)
	}
}

func TestSourceCatalogFiniteScanBuildsEpisodeSeries(t *testing.T) {
	tests := []struct {
		name     string
		tree     map[string][]map[string]any
		episodes int
	}{
		{
			name: "loose files",
			tree: map[string][]map[string]any{
				"/":        {looseDir("Example")},
				"/Example": {looseAudio("Example Episode1.mp3"), looseAudio("Example Episode2.mp3"), looseAudio("Example Episode3.mp3")},
			},
			episodes: 3,
		},
		{
			name: "format directories",
			tree: map[string][]map[string]any{
				"/":                         {looseDir("Example")},
				"/Example":                  {looseDir("Episode 1 (MP3)"), looseDir("Episode 1 (FLAC)"), looseDir("Episode 2 (MP3)")},
				"/Example/Episode 1 (MP3)":  {looseAudio("track.mp3")},
				"/Example/Episode 1 (FLAC)": {looseAudio("track.flac")},
				"/Example/Episode 2 (MP3)":  {looseAudio("track.mp3")},
			},
			episodes: 2,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var input struct {
					Path string `json:"path"`
				}
				_ = json.NewDecoder(r.Body).Decode(&input)
				items, ok := tc.tree[input.Path]
				if !ok {
					http.NotFound(w, r)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": len(items)}})
			}))
			f.request(t, http.MethodPost, "/catalog/scan", `{}`, 200)
			list := f.request(t, http.MethodGet, "/works?pageSize=100", "", 200)
			cards := map[string]map[string]any{}
			for _, raw := range list["works"].([]any) {
				card := raw.(map[string]any)
				cards[card["title"].(string)] = card
			}
			card := catalogRegressionCard(t, cards, "Example")
			seriesID, ok := card["seriesId"].(float64)
			if !ok || seriesID == 0 {
				t.Fatalf("finite scan did not build a series: %#v", card)
			}
			view := reviewRequest(t, f.handler, f.cookie, http.MethodGet, "/api/catalog-series/"+strconv.Itoa(int(seriesID)), "", 200)
			if got := len(view["episodes"].([]any)); got != tc.episodes {
				t.Fatalf("episodes=%d, want %d: %#v", got, tc.episodes, view)
			}
		})
	}
}

// The daily update must re-list the source root itself. Direct root files are
// not visible through the directory probe, so an addition would otherwise stay
// invisible and a deletion would never reach lifecycle rules.
func TestSourceCatalogDailyRelistsRootFiles(t *testing.T) {
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/": {looseAudioSized("old.mp3", 100)},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if progress := f.drain(t); progress["status"] != "complete" {
		t.Fatalf("initial scan = %#v", progress)
	}
	cards := f.cards(t)
	if len(cards) != 1 {
		t.Fatalf("initial cards=%v", cards)
	}
	var card map[string]any
	for _, candidate := range cards {
		card = candidate
	}
	entryID := strconv.Itoa(int(card["catalogEntryId"].(float64)))
	reviewRequest(t, f.handler, f.cookie, http.MethodPatch, "/api/source-catalog/"+entryID+"/user-state", `{"rating":3,"note":"Retained"}`, 200)

	f.updates(map[string][]map[string]any{"/": {looseAudioSized("old.mp3", 100), looseAudioSized("new.mp3", 200)}})
	sourceID, err := strconv.ParseInt(path.Base(f.base), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.server.runCatalogIncrementalSync(context.Background(), sourceID, false); err != nil {
		t.Fatal(err)
	}
	card = f.cards(t)[card["title"].(string)]
	if card == nil {
		t.Fatal("root card disappeared after adding a file")
	}
	if got := countCatalogAudio(f.tracks(t, card)); got != 2 {
		t.Fatalf("tracks after addition=%d, want 2", got)
	}
	if search := f.request(t, http.MethodGet, "/works?q=new", "", 200); len(search["works"].([]any)) != 1 {
		t.Fatalf("new root file is not searchable: %#v", search)
	}

	f.updates(map[string][]map[string]any{"/": {looseAudioSized("new.mp3", 200)}})
	if _, err = f.server.runCatalogIncrementalSync(context.Background(), sourceID, false); err != nil {
		t.Fatal(err)
	}
	card = f.cards(t)[card["title"].(string)]
	if card == nil {
		t.Fatal("root card disappeared after deleting one file")
	}
	if got := countCatalogAudio(f.tracks(t, card)); got != 1 {
		t.Fatalf("tracks after deletion=%d, want 1", got)
	}
	if search := f.request(t, http.MethodGet, "/works?q=old", "", 200); len(search["works"].([]any)) != 0 {
		t.Fatalf("deleted root file still matches search: %#v", search)
	}
	assertReview(t, reviewRequest(t, f.handler, f.cookie, http.MethodGet, "/api/source-catalog/"+entryID, "", 200), float64(3), "Retained")

	f.updates(map[string][]map[string]any{"/": {}})
	if _, err = f.server.runCatalogIncrementalSync(context.Background(), sourceID, false); err != nil {
		t.Fatal(err)
	}
	var availability string
	if err = f.server.db.QueryRow(`SELECT availability_status FROM source_catalog_entry WHERE id=?`, entryID).Scan(&availability); err != nil {
		t.Fatal(err)
	}
	if availability != "unconfirmed_empty" {
		t.Fatalf("empty root card status=%q, want unconfirmed_empty", availability)
	}
	assertReview(t, reviewRequest(t, f.handler, f.cookie, http.MethodGet, "/api/source-catalog/"+entryID, "", 200), float64(3), "Retained")
}

// A confirmed 404/410 directory is completed coverage, so it must not appear in
// the detail list or its total. An unconfirmed empty directory and a failed
// directory do appear.
func TestSourceCatalogDirectoryDetailsMatchTotalForEmptyAndNotFound(t *testing.T) {
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&input)
		var items []map[string]any
		switch input.Path {
		case "/":
			items = []map[string]any{dirItem("Alpha"), dirItem("Missing"), dirItem("Empty"), dirItem("Failed")}
		case "/Alpha":
			items = []map[string]any{audioItem("track.mp3")}
		case "/Missing":
			http.NotFound(w, r)
			return
		case "/Empty":
			items = []map[string]any{}
		case "/Failed":
			http.Error(w, "upstream unavailable", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": len(items)}})
	}))
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	progress := f.drain(t)
	detailsTotal := int(progress["detailsTotal"].(float64))
	details := progress["details"].([]any)
	if detailsTotal != len(details) {
		t.Fatalf("detailsTotal=%d, details=%d: %#v", detailsTotal, len(details), progress)
	}
	if detailsTotal != 2 {
		t.Fatalf("detailsTotal=%d, want the unconfirmed empty and failed directories", detailsTotal)
	}
	for _, raw := range details {
		detail := raw.(map[string]any)
		if detail["path"] == "/Missing" {
			t.Fatalf("confirmed not_found directory leaked into details: %#v", details)
		}
	}
}
