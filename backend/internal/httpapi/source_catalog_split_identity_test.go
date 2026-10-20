package httpapi

import (
	"net/http"
	"testing"
)

// A numberless work can name its tracks instead of numbering them. Without
// this rule Prologue/Main Story/Epilogue becomes three works with three cards.
func TestCatalogSplitModeKeepsNamedPartsOfOneWork(t *testing.T) {
	for name, names := range map[string][]string{
		"narrative parts":     {"Prologue.mp3", "Main Story.mp3", "Epilogue.mp3"},
		"opening and ending":  {"Opening.mp3", "Ending.mp3"},
		"numbered with bonus": {"01.mp3", "02.mp3", "03.mp3", "Bonus.mp3"},
		"three halves":        {"表妹上.mp3", "表妹中.mp3", "表妹下.mp3"},
	} {
		if got := catalogWorkSplitModeNames(names); got != catalogWorkSplitSingle {
			t.Fatalf("%s: split mode = %q, want single for %v", name, got, names)
		}
	}
}

// A trailing number is only a track number when the whole set forms a run.
// Room 101 and Room 102 are separate works, so the split must keep them apart.
func TestCatalogSplitModeKeepsNumberedTitlesDistinct(t *testing.T) {
	for _, names := range [][]string{
		{"Room 101.mp3", "Room 102.mp3"},
		{"作品 Vol.1.mp3", "作品 Vol.2.mp3"},
	} {
		if got := catalogWorkSplitModeNames(names); got != catalogWorkSplitFile {
			t.Fatalf("split mode for %v = %q, want file", names, got)
		}
		grouping := newCatalogLooseGrouping(names, catalogWorkSplitFile)
		if len(names) > 1 && grouping.keyFor(names[0]) == grouping.keyFor(names[1]) {
			t.Fatalf("%v share identity %q", names, grouping.keyFor(names[0]))
		}
	}
}

// The rule must hold through the real index: one card, three tracks.
func TestSourceCatalogPublishesNamedPartsAsOneCard(t *testing.T) {
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/":             {looseDir("Creator")},
		"/Creator":      {looseDir("Work")},
		"/Creator/Work": {looseAudio("Prologue.mp3"), looseAudio("Main Story.mp3"), looseAudio("Epilogue.mp3")},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	cards := f.cards(t)
	if len(cards) != 1 || cards["Work"] == nil {
		t.Fatalf("cards=%v, want one card named Work", cards)
	}
	if got := countCatalogAudio(f.tracks(t, cards["Work"])); got != 3 {
		t.Fatalf("tracks=%d, want 3 on one card", got)
	}
}

// A numbered release keeps its bonus track on the same card.
func TestSourceCatalogPublishesNumberedReleaseWithBonusAsOneCard(t *testing.T) {
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/":             {looseDir("Creator")},
		"/Creator":      {looseDir("Work")},
		"/Creator/Work": {looseAudio("01.mp3"), looseAudio("02.mp3"), looseAudio("Bonus.mp3")},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	cards := f.cards(t)
	if len(cards) != 1 || cards["Work"] == nil {
		t.Fatalf("cards=%v, want one card named Work", cards)
	}
	if got := countCatalogAudio(f.tracks(t, cards["Work"])); got != 3 {
		t.Fatalf("tracks=%d, want 3 on one card", got)
	}
}

// Two numbers that are part of the title stay two cards, and the parent
// directory keeps its readme instead of dropping it during the split.
func TestSourceCatalogSplitKeepsTitledWorksApartAndRetainsReadme(t *testing.T) {
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/":        {looseDir("Creator")},
		"/Creator": {looseAudio("Room 101.mp3"), looseAudio("Room 102.mp3"), looseAudio("readme.txt")},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	cards := f.cards(t)
	if len(cards) != 2 || cards["Room 101"] == nil || cards["Room 102"] == nil {
		t.Fatalf("cards=%v, want Room 101 and Room 102", cards)
	}
	var readme int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_file f
		JOIN source_catalog_entry e ON e.id=f.catalog_entry_id
		WHERE e.root_path='/Creator' AND f.relative_path='readme.txt'`).Scan(&readme); err != nil {
		t.Fatal(err)
	}
	if readme != 1 {
		t.Fatal("split dropped the release readme; credit evidence would be lost")
	}
}

// A directory that stops publishing an episode must not leave the episode card
// visible after the next complete snapshot.
func TestSourceCatalogSnapshotRetiresRemovedEpisode(t *testing.T) {
	tree := map[string][]map[string]any{
		"/":                 {looseDir("Series")},
		"/Series":           {looseDir("Episode 1"), looseDir("Episode 2")},
		"/Series/Episode 1": {looseAudio("audio.mp3")},
		"/Series/Episode 2": {looseAudio("audio.mp3")},
	}
	f := newLooseWorkFixture(t, tree)
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	if cards := f.cards(t); len(cards) == 0 {
		t.Fatalf("first scan published no card: %v", cards)
	}
	f.updates(map[string][]map[string]any{
		"/":                 {looseDir("Series")},
		"/Series":           {looseDir("Episode 1")},
		"/Series/Episode 1": {looseAudio("audio.mp3")},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("rescan = %#v", p)
	}
	var status string
	if err := f.server.db.QueryRow(`SELECT availability_status FROM source_catalog_entry
		WHERE root_path='/Series/Episode 2' AND relocated_to_id IS NULL`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "missing" {
		t.Fatalf("removed episode status=%q, want missing", status)
	}
}

// A reviewed creator card is not a container. A new numberless work below it
// gets its own card instead of being absorbed into the reviewed object.
func TestSourceCatalogReviewedCreatorAdmitsNewNumberlessWork(t *testing.T) {
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/":                 {looseDir("Creator")},
		"/Creator":          {looseAudio("loose.mp3"), looseDir("New Work")},
		"/Creator/New Work": {looseAudio("track.mp3")},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	var creatorID int64
	if err := f.server.db.QueryRow(`SELECT id FROM source_catalog_entry WHERE root_path='/Creator'`).Scan(&creatorID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.db.Exec(`INSERT INTO work(id,primary_code,title) VALUES (77,'','Creator'); UPDATE source_catalog_entry SET work_id=77 WHERE id=?`, creatorID); err != nil {
		t.Fatal(err)
	}
	f.updates(map[string][]map[string]any{
		"/":                 {looseDir("Creator")},
		"/Creator":          {looseAudio("loose.mp3"), looseDir("New Work")},
		"/Creator/New Work": {looseAudio("track.mp3")},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"force":true}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("rescan = %#v", p)
	}
	cards := f.cards(t)
	if cards["New Work"] == nil {
		t.Fatalf("new numberless work was absorbed into the reviewed creator card: %v", cards)
	}
	var root string
	if err := f.server.db.QueryRow(`SELECT catalog_root FROM source_catalog_directory
		WHERE path='/Creator/New Work'`).Scan(&root); err != nil {
		t.Fatal(err)
	}
	if root != "/Creator/New Work" {
		t.Fatalf("new work catalog_root=%q, want its own path", root)
	}
}

// A zero-padded short code is a work identifier, not a track number. Two
// different codes in one creator folder stay two works.
func TestCatalogSplitModeKeepsZeroPaddedCodesApart(t *testing.T) {
	names := []string{"T001 Crossing.mp3", "T002 Departure.mp3"}
	if got := catalogWorkSplitModeNames(names); got != catalogWorkSplitFile {
		t.Fatalf("split mode = %q, want file", got)
	}
	grouping := newCatalogLooseGrouping(names, catalogWorkSplitFile)
	if grouping.keyFor(names[0]) == grouping.keyFor(names[1]) {
		t.Fatalf("T001 and T002 share identity %q", grouping.keyFor(names[0]))
	}
}

// A chapter folder belongs to the work above it. Only explicit episode
// counters create a series, so 第1章/第2章 must not appear as episodes.
func TestCatalogChapterFoldersAreNotEpisodes(t *testing.T) {
	for _, name := range []string{"第1章", "第2章", "第1節", "第3章 前篇"} {
		if evidence, ok := catalogEpisodeEvidence("/Work/" + name); ok {
			t.Fatalf("%q was treated as episode evidence: %#v", name, evidence)
		}
	}
	for _, name := range []string{"第1話", "第2集", "Episode 3"} {
		if _, ok := catalogEpisodeEvidence("/Series/" + name); !ok {
			t.Fatalf("%q lost its episode evidence", name)
		}
	}
	if !catalogTrackContainerDirectory("第1章") {
		t.Fatal("a chapter folder must stay inside its work")
	}
}

// A format copy of an episode belongs to that episode rather than becoming a
// second episode card.
func TestCatalogEpisodeFormatCopyStaysWithEpisode(t *testing.T) {
	for _, name := range []string{"Episode 1 (MP3)", "Episode 2 [FLAC]", "Episode 3 MP3"} {
		if evidence, ok := catalogEpisodeEvidence("/Series/" + name); ok {
			t.Fatalf("%q became episode evidence: %#v", name, evidence)
		}
	}
}
