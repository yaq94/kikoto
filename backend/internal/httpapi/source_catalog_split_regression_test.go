package httpapi

import (
	"fmt"
	"net/http"
	"testing"
)

// A format copy can carry a different extension from the direct file. The
// split card owns both copies; the retired creator card must not drop the
// nested copy just because its file name does not match a direct name exactly.
func TestSourceCatalogSplitKeepsRenamedFormatCopy(t *testing.T) {
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/": {looseDir("Creator")},
		"/Creator": {
			looseAudio("N001 Example Work-1.mp3"),
			looseAudio("N001 Example Work-2.mp3"),
			looseAudio("N002 Another Work-1.mp3"),
			looseAudio("N002 Another Work-2.mp3"),
			looseDir("FLAC"),
		},
		"/Creator/FLAC": {
			looseAudio("N001 Example Work-1.flac"),
			looseAudio("N002 Another Work-1.flac"),
		},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	cards := f.cards(t)
	if len(cards) != 2 {
		t.Fatalf("cards=%d, want two split works: %v", len(cards), cards)
	}
	for title, want := range map[string]int{"N001 Example Work-1": 3, "N002 Another Work-1": 3} {
		card := cards[title]
		if card == nil {
			t.Fatalf("missing %q: %v", title, cards)
		}
		if got := countAudioTracks(f.tracks(t, card)); got != want {
			t.Fatalf("%s audio tracks=%d, want %d (direct + renamed FLAC copy)", title, got, want)
		}
	}
}

// A later complete generation must recompute the split when the same creator
// directory now holds a numeric audiobook run instead of separate works.
func TestSourceCatalogSplitDecisionFollowsANewGeneration(t *testing.T) {
	names := []string{
		"Alpha Rain.mp3", "Beta Wind.mp3", "Cobalt Night.mp3", "Delta Snow.mp3",
		"Ember Sky.mp3", "Frost Garden.mp3", "Golden Tide.mp3", "Harbor Light.mp3",
		"Ivory Road.mp3", "Jade River.mp3", "Kindled Star.mp3", "Lunar Field.mp3",
	}
	first := make([]map[string]any, 0, len(names))
	for _, name := range names {
		first = append(first, looseAudio(name))
	}
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/":        {looseDir("Creator")},
		"/Creator": first,
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("first scan = %#v", p)
	}
	if cards := f.cards(t); len(cards) != 12 {
		t.Fatalf("first cards=%d, want 12: %v", len(cards), cards)
	}
	second := make([]map[string]any, 0, 12)
	for i := 1; i <= 12; i++ {
		second = append(second, looseAudio(fmt.Sprintf("%03d.mp3", i)))
	}
	f.updates(map[string][]map[string]any{
		"/":        {looseDir("Creator")},
		"/Creator": second,
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"force":true}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("second scan = %#v", p)
	}
	if cards := f.cards(t); len(cards) != 1 {
		t.Fatalf("second cards=%d, want one audiobook: %v", len(cards), cards)
	}
}

// Small creator folders are common: two or three unrelated recordings must be
// separate cards even though they are below the old file-count heuristic.
func TestSourceCatalogLooseWorksSplitSmallFolders(t *testing.T) {
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/": {looseDir("Creator")},
		"/Creator": {
			looseAudio("First Recording.mp3"),
			looseAudio("Second Recording.mp3"),
			looseAudio("Third Recording.mp3"),
		},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	cards := f.cards(t)
	if len(cards) != 3 {
		t.Fatalf("cards=%d, want one per independent recording: %v", len(cards), cards)
	}
	for _, title := range []string{"First Recording", "Second Recording", "Third Recording"} {
		card := cards[title]
		if card == nil {
			t.Fatalf("missing %q: %v", title, cards)
		}
		if tracks := f.tracks(t, card); len(tracks) != 1 {
			t.Fatalf("%s tracks=%d, want 1", title, len(tracks))
		}
	}
}

// A numbered track listing is one work even when every track has its own
// descriptive title. Two or three ordered parts are common in a single
// recording, so they must not become separate cards.
func TestSourceCatalogLooseWorksKeepsNumberedTrackRunTogether(t *testing.T) {
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/": {looseDir("Creator")},
		"/Creator": {
			looseAudio("01 Opening.mp3"),
			looseAudio("02 Confession.mp3"),
			looseAudio("03 Ending.mp3"),
		},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	cards := f.cards(t)
	if len(cards) != 1 {
		t.Fatalf("cards=%d, want one ordered work: %v", len(cards), cards)
	}
	card := cards["Creator"]
	if card == nil {
		t.Fatalf("missing ordered work card: %v", cards)
	}
	if tracks := f.tracks(t, card); len(tracks) != 3 {
		t.Fatalf("ordered tracks=%d, want 3", len(tracks))
	}
}

// Missing positions are not a reliable track sequence, so the ordinary
// base-title rule still separates unrelated recordings.
func TestSourceCatalogLooseWorksBreaksNonContiguousNumbers(t *testing.T) {
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/": {looseDir("Creator")},
		"/Creator": {
			looseAudio("01 First Recording.mp3"),
			looseAudio("03 Second Recording.mp3"),
		},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	if cards := f.cards(t); len(cards) != 2 {
		t.Fatalf("cards=%d, want separate works for a broken sequence: %v", len(cards), cards)
	}
}

// countAudioTracks counts audio leaves under the returned track tree.
func countAudioTracks(rows []any) int {
	total := 0
	for _, raw := range rows {
		row := raw.(map[string]any)
		if row["type"] == "audio" {
			total++
		}
		if children, ok := row["children"].([]any); ok {
			total += countAudioTracks(children)
		}
	}
	return total
}
