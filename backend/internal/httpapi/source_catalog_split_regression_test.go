package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
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

// A single-card refresh must keep the same files the index published. Nested
// format copies are part of the split card, so refreshing it cannot shrink the
// track list back to the direct files only.
func TestSourceCatalogSplitCardRefreshKeepsNestedFormatCopy(t *testing.T) {
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
	card := f.cards(t)["N001 Example Work-1"]
	if card == nil {
		t.Fatal("missing split card")
	}
	id := strconv.Itoa(int(card["catalogEntryId"].(float64)))
	reviewRequest(t, f.handler, f.cookie, http.MethodPost, "/api/source-catalog/"+id+"/refresh", `{}`, 200)
	if got := countAudioTracks(f.tracks(t, card)); got != 3 {
		t.Fatalf("refreshed tracks=%d, want the direct files plus the FLAC copy", got)
	}
}

// Nested audio without a split group key has no other card to live on. The
// parent must stay published and keep that file instead of retiring the card
// and losing the only reference to it.
func TestSourceCatalogSplitKeepsUngroupedNestedAudioOnParent(t *testing.T) {
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/": {looseDir("Creator")},
		"/Creator": {
			looseAudio("N001 Example Work-1.mp3"),
			looseAudio("N001 Example Work-2.mp3"),
			looseAudio("N002 Another Work-1.mp3"),
			looseAudio("N002 Another Work-2.mp3"),
			looseDir("Disc1"),
		},
		"/Creator/Disc1": {looseAudio("Extra Recording.mp3")},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	cards := f.cards(t)
	if len(cards) != 3 {
		t.Fatalf("cards=%d, want two split works and the retained parent: %v", len(cards), cards)
	}
	parent := cards["Creator"]
	if parent == nil {
		t.Fatalf("missing parent card for ungrouped nested audio: %v", cards)
	}
	if got := countAudioTracks(f.tracks(t, parent)); got != 1 {
		t.Fatalf("parent tracks=%d, want only the ungrouped nested file", got)
	}
}

// Format directories are not limited to bare extensions. A quality marker or
// a lossless label still names a copy of the same work, so it must not become
// a separate card with a duplicate track.
func TestSourceCatalogSplitRecognizesQualityFormatDirectories(t *testing.T) {
	for _, folder := range []string{"lossless", "FLAC 24bit", "MP3 320kbps", "Hi-Res"} {
		t.Run(folder, func(t *testing.T) {
			f := newLooseWorkFixture(t, map[string][]map[string]any{
				"/": {looseDir("Creator")},
				"/Creator": {
					looseAudio("N001 Example Work-1.mp3"),
					looseAudio("N001 Example Work-2.mp3"),
					looseAudio("N002 Another Work-1.mp3"),
					looseAudio("N002 Another Work-2.mp3"),
					looseDir(folder),
				},
				"/Creator/" + folder: {
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
				t.Fatalf("cards=%d, want the two works without a format card: %v", len(cards), cards)
			}
			if got := countAudioTracks(f.tracks(t, cards["N001 Example Work-1"])); got != 3 {
				t.Fatalf("tracks=%d, want the direct files plus the format copy", got)
			}
		})
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

// A directory of date-named recordings is all digits but not a track sequence.
// It must keep one card per recording even when no alphabetic title exists.
func TestSourceCatalogLooseWorksKeepsDateOnlyDirectoryDistinct(t *testing.T) {
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/": {looseDir("Creator")},
		"/Creator": {
			looseAudio("20210104.mp3"),
			looseAudio("20210110.mp3"),
		},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	cards := f.cards(t)
	if len(cards) != 2 {
		t.Fatalf("cards=%d, want one per dated recording: %v", len(cards), cards)
	}
	for _, title := range []string{"20210104", "20210110"} {
		if cards[title] == nil {
			t.Fatalf("missing dated card %q: %v", title, cards)
		}
	}
}

// A directory can move between split and single shapes across generations.
// The intermediate single card must not survive next to the new split cards.
func TestSourceCatalogSplitSingleSplitDoesNotReviveParent(t *testing.T) {
	split := []map[string]any{
		looseAudio("Alpha Rain.mp3"), looseAudio("Beta Wind.mp3"), looseAudio("Cobalt Night.mp3"),
		looseAudio("Delta Snow.mp3"), looseAudio("Ember Sky.mp3"), looseAudio("Frost Garden.mp3"),
		looseAudio("Golden Tide.mp3"), looseAudio("Harbor Light.mp3"), looseAudio("Ivory Road.mp3"),
		looseAudio("Jade River.mp3"), looseAudio("Kindled Star.mp3"), looseAudio("Lunar Field.mp3"),
	}
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/": {looseDir("Creator")}, "/Creator": split,
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("first scan = %#v", p)
	}
	if cards := f.cards(t); len(cards) != 12 {
		t.Fatalf("first cards=%d, want 12: %v", len(cards), cards)
	}
	single := make([]map[string]any, 0, 12)
	for i := 1; i <= 12; i++ {
		single = append(single, looseAudio(fmt.Sprintf("%03d.mp3", i)))
	}
	f.updates(map[string][]map[string]any{"/": {looseDir("Creator")}, "/Creator": single})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"force":true}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("second scan = %#v", p)
	}
	if cards := f.cards(t); len(cards) != 1 {
		t.Fatalf("second cards=%d, want one audiobook: %v", len(cards), cards)
	}
	f.updates(map[string][]map[string]any{"/": {looseDir("Creator")}, "/Creator": split})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"force":true}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("third scan = %#v", p)
	}
	if cards := f.cards(t); len(cards) != 12 {
		t.Fatalf("third cards=%d, want 12 split works without the parent: %v", len(cards), cards)
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
