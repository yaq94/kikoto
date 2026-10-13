package llm

import (
	"strings"
	"testing"
)

func TestLyricsTemplatePreservesTimelineAndOrder(t *testing.T) {
	for _, tc := range []struct{ name, source, want string }{
		{"example.lrc", "[ar:Example]\r\n[00:01.20][00:02.00]first\r\n[00:03.00]second\r\n", "[ar:Example]\r\n[00:01.20][00:02.00]one\r\n[00:03.00]two\r\n"},
		{"example.srt", "1\n00:00:01,000 --> 00:00:02,000\nfirst\n\n2\n00:00:03,000 --> 00:00:04,000\nsecond\n", "1\n00:00:01,000 --> 00:00:02,000\none\n\n2\n00:00:03,000 --> 00:00:04,000\ntwo\n"},
		{"example.vtt", "WEBVTT\n\nexample-cue\n00:01.000 --> 00:02.000 align:start\nfirst\nsecond\n", "WEBVTT\n\nexample-cue\n00:01.000 --> 00:02.000 align:start\none\ntwo\n"},
		{"example.ass", "[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\nDialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,first\nDialogue: 0,0:00:03.00,0:00:04.00,Default,,0,0,0,,second\n", "[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\nDialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,one\nDialogue: 0,0:00:03.00,0:00:04.00,Default,,0,0,0,,two\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := ParseLyrics(tc.name, tc.source)
			if err != nil {
				t.Fatal(err)
			}
			got, err := d.Render([]string{"one", "two"})
			if err != nil || got != tc.want {
				t.Fatalf("render=%q err=%v", got, err)
			}
			if _, err := d.Render([]string{"one"}); err == nil {
				t.Fatal("lost cue accepted")
			}
		})
	}
}
func TestLyricsWholeInputDetectsCrossChunkRule(t *testing.T) {
	lines := make([]string, LyricsChunkLines+1)
	for i := range lines {
		lines[i] = "Example"
	}
	lines[LyricsChunkLines-1] = "restricted"
	lines[LyricsChunkLines] = "phrase"
	p := Policy{Keywords: []KeywordRule{{ID: "cross-chunk", Term: "restricted phrase", Fields: []string{"lyrics"}, Action: "blocked"}}}
	if got := p.Assess("general", map[string]string{"lyrics": strings.Join(lines, "\n")}, nil); got.Class != "blocked" {
		t.Fatal(got)
	}
}
func TestLyricsRejectsModelIntroducedTiming(t *testing.T) {
	for _, tc := range []struct{ name, source, injected string }{
		{"example.lrc", "[00:01.00]Example", "[00:99.00]Extra timing"},
		{"example.vtt", "WEBVTT\n\n00:01.000 --> 00:02.000\nExample", "00:03.000 --> 00:04.000"},
	} {
		document, err := ParseLyrics(tc.name, tc.source)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := document.Render([]string{tc.injected}); err == nil {
			t.Fatal("model altered timing syntax")
		}
	}
}
