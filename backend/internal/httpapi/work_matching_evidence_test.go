package httpapi

import (
	"slices"
	"testing"
)

// These cases exercise the pure matching rule directly. The HTTP tests prove
// persistence and permissions; this test keeps the evidence precedence and
// conflict rules readable and independent of SQLite.
func TestMatchWorkEvidenceRules(t *testing.T) {
	cases := []struct {
		name       string
		left       workMatchEvidence
		right      workMatchEvidence
		wantRule   string
		wantConf   string
		wantReason string
	}{
		{
			name:     "same product code is reliable",
			left:     workMatchEvidence{Title: "Example Work", Code: "RJ00000000"},
			right:    workMatchEvidence{Title: "Different title", Code: "RJ00000000"},
			wantRule: "product_code", wantConf: "reliable", wantReason: "product_code",
		},
		{
			name:     "title voice and circle are high confidence",
			left:     workMatchEvidence{Title: "Example Work", Voices: []int64{1}, Circles: []int64{2}},
			right:    workMatchEvidence{Title: "Example Work", Voices: []int64{1}, Circles: []int64{2}},
			wantRule: "title_voice_circle", wantConf: "high", wantReason: "voice_actors",
		},
		{
			name:     "different circles conflict",
			left:     workMatchEvidence{Title: "Example Work", Voices: []int64{1}, Circles: []int64{2}},
			right:    workMatchEvidence{Title: "Example Work", Voices: []int64{1}, Circles: []int64{3}},
			wantRule: "conflicting_evidence", wantConf: "review", wantReason: "circles",
		},
		{
			name: "sidecar voice with different circles conflicts",
			left: workMatchEvidence{Title: "Example Work", Sidecar: catalogSidecarEvidence{
				VoiceActors: []string{"Example Voice"}, Circles: []string{"Circle A"},
			}, VoiceNames: []string{"Example Voice"}, CircleNames: []string{"Circle A"}},
			right: workMatchEvidence{Title: "Example Work", Sidecar: catalogSidecarEvidence{
				VoiceActors: []string{"Example Voice"}, Circles: []string{"Circle B"},
			}, VoiceNames: []string{"Example Voice"}, CircleNames: []string{"Circle B"}},
			wantRule: "conflicting_evidence", wantConf: "review", wantReason: "circles",
		},
		{
			name: "sidecar voice with one circle is enough",
			left: workMatchEvidence{Title: "Example Work", Sidecar: catalogSidecarEvidence{
				VoiceActors: []string{"Example Voice"}, Circles: []string{"Circle A"},
			}, VoiceNames: []string{"Example Voice"}, CircleNames: []string{"Circle A"}},
			right: workMatchEvidence{Title: "Example Work", Sidecar: catalogSidecarEvidence{
				VoiceActors: []string{"Example Voice"},
			}, VoiceNames: []string{"Example Voice"}},
			wantRule: "title_voice_credits", wantConf: "high", wantReason: "voice_actors",
		},
		{
			name:     "different voice actors conflict",
			left:     workMatchEvidence{Title: "Example Work", Voices: []int64{1}},
			right:    workMatchEvidence{Title: "Example Work", Voices: []int64{2}},
			wantRule: "conflicting_evidence", wantConf: "review", wantReason: "voice_actors",
		},
		{
			name:     "title alone is insufficient",
			left:     workMatchEvidence{Title: "Example Work"},
			right:    workMatchEvidence{Title: "Example Work"},
			wantRule: "insufficient_evidence", wantConf: "review", wantReason: "original_title",
		},
		{
			name:     "different episode positions conflict",
			left:     workMatchEvidence{Title: "Example Work", Level: "episode", Series: "Example Series", Position: 1},
			right:    workMatchEvidence{Title: "Example Work", Level: "episode", Series: "Example Series", Position: 2},
			wantRule: "conflicting_evidence", wantConf: "review", wantReason: "episode_identity",
		},
		{
			name:     "declared family is reliable",
			left:     workMatchEvidence{Title: "Example Work", Family: 7},
			right:    workMatchEvidence{Title: "Other title", Family: 7},
			wantRule: "declared_relationship", wantConf: "reliable", wantReason: "declared_relationship",
		},
		{
			name:     "same title and content hash is high confidence",
			left:     workMatchEvidence{Title: "Example Work", Content: "hash"},
			right:    workMatchEvidence{Title: "Example Work", Content: "hash"},
			wantRule: "title_content_hash", wantConf: "high", wantReason: "complete_audio_hashes",
		},
		{
			name:     "different codes conflict before title matching",
			left:     workMatchEvidence{Title: "Example Work", Code: "RJ00000000"},
			right:    workMatchEvidence{Title: "Example Work", Code: "RJ00000001"},
			wantRule: "conflicting_evidence", wantConf: "review", wantReason: "different_codes",
		},
		{
			name:     "work and episode levels conflict",
			left:     workMatchEvidence{Title: "Example Work", Level: "work"},
			right:    workMatchEvidence{Title: "Example Work", Level: "episode", Series: "Example Series", Position: 1},
			wantRule: "conflicting_evidence", wantConf: "review", wantReason: "evaluation_level",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := matchWorkEvidence(tc.left, tc.right)
			if got.Rule != tc.wantRule || got.Confidence != tc.wantConf {
				t.Fatalf("rule=%s confidence=%s reasons=%v, want %s/%s", got.Rule, got.Confidence, got.Reasons, tc.wantRule, tc.wantConf)
			}
			if tc.wantReason != "" && !slices.Contains(got.Reasons, tc.wantReason) {
				t.Fatalf("reasons=%v, want %q", got.Reasons, tc.wantReason)
			}
		})
	}
}
