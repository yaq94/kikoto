package httpapi

import "testing"

// Episode markers start a token. A title that merely ends in "ep" or
// "episode" is not episode evidence, even when a number follows it.
func TestCatalogSeriesEpisodeMarkerRequiresWordBoundary(t *testing.T) {
	tests := []struct {
		name     string
		dir      string
		ok       bool
		title    string
		position int
	}{
		{name: "episode word", dir: "Episode 1", ok: true, title: "Creator", position: 1},
		{name: "episode word without separator", dir: "Episode1", ok: true, title: "Creator", position: 1},
		{name: "ep with period", dir: "Ep. 1", ok: true, title: "Creator", position: 1},
		{name: "ep without period", dir: "Ep 1", ok: true, title: "Creator", position: 1},
		{name: "uppercase ep", dir: "EP.12", ok: true, title: "Creator", position: 12},
		{name: "chinese episode", dir: "第1集", ok: true, title: "Creator", position: 1},
		{name: "chinese episode with spaces", dir: "第 2 話", ok: true, title: "Creator", position: 2},
		{name: "episode after work code", dir: "RJ123 Episode 3", ok: true, title: "RJ123", position: 3},
		{name: "deep ends in ep", dir: "Deep 2", ok: false},
		{name: "sleep ends in ep", dir: "Sleep 2", ok: false},
		{name: "step ends in ep", dir: "Step 2", ok: false},
		{name: "prep ends in ep", dir: "Prep 5", ok: false},
		{name: "deep ends in ep before punctuation", dir: "Deep.2", ok: false},
		{name: "prep ends in ep without separator", dir: "Prep5", ok: false},
		{name: "word ends in episode", dir: "MyEpisode 1", ok: false},
		{name: "lowercase word ends in episode", dir: "myepisode 2", ok: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			evidence, ok := catalogEpisodeEvidence("/Creator/" + tc.dir)
			if ok != tc.ok {
				t.Fatalf("catalogEpisodeEvidence(%q) ok = %v, want %v (evidence %#v)", tc.dir, ok, tc.ok, evidence)
			}
			if !ok {
				return
			}
			if evidence.Title != tc.title || evidence.Position != tc.position {
				t.Fatalf("catalogEpisodeEvidence(%q) = title %q position %d, want title %q position %d",
					tc.dir, evidence.Title, evidence.Position, tc.title, tc.position)
			}
		})
	}
}
