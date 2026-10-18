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
