package catalogadmission

import "testing"

func TestCatalogAdmissionDecisionBoundaries(t *testing.T) {
	for _, testCase := range []struct {
		name                   string
		files, audio, nonAudio int
		want                   string
	}{
		{name: "normal multi-track", files: 1281, audio: 3, nonAudio: 1278, want: ""},
		{name: "no audio", files: 10, audio: 0, nonAudio: 10, want: ReasonNoAudio},
		{name: "large attachment set", files: 5001, audio: 1, nonAudio: 5000, want: ""},
		{name: "one audio beside many attachments", files: 2005, audio: 1, nonAudio: 2004, want: ""},
		{name: "four audio beside many attachments", files: 2005, audio: 4, nonAudio: 2001, want: ""},
		{name: "empty card", files: 0, audio: 0, nonAudio: 0, want: ReasonNoAudio},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := Decision(testCase.files, testCase.audio, testCase.nonAudio); got != testCase.want {
				t.Fatalf("Decision(%d,%d,%d) = %q, want %q", testCase.files, testCase.audio, testCase.nonAudio, got, testCase.want)
			}
		})
	}
}
