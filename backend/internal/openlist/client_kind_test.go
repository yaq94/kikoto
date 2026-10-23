package openlist

import "testing"

// The catalog name classifier recognizes more format words than the playback
// boundary does. Kind is the admission decision, and the client media stack
// only claims the formats it can actually play. APE, DSD/DSF and related
// container names must therefore stay "other" rather than becoming playable
// audio by name alone.
func TestCatalogKindKeepsUnsupportedLosslessContainersOutOfAudio(t *testing.T) {
	for _, name := range []string{
		"01.ape", "01.dsf", "01.dff", "01.wv", "01.tta", "01.alac", "01.mka",
	} {
		if kind := Kind(name, false); kind != "other" {
			t.Fatalf("Kind(%q)=%q, want other", name, kind)
		}
	}
	for _, name := range []string{"01.mp3", "01.flac", "01.wav", "01.m4a", "01.aac", "01.ogg", "01.opus", "01.wma", "01.aiff", "01.aif"} {
		if kind := Kind(name, false); kind != "audio" {
			t.Fatalf("Kind(%q)=%q, want audio", name, kind)
		}
	}
}
