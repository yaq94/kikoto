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
		{name: "exactly five thousand files", files: 5000, audio: 10, nonAudio: 4990, want: ""},
		{name: "one file over", files: 5001, audio: 10, nonAudio: 4991, want: ReasonTooManyFiles},
		{name: "exactly two thousand non-audio", files: 2004, audio: 4, nonAudio: 2000, want: ""},
		{name: "one non-audio over with four audio", files: 2005, audio: 4, nonAudio: 2001, want: ReasonNonAudioHeavy},
		{name: "five audio rescues non-audio heavy", files: 2005, audio: 5, nonAudio: 2000, want: ""},
		{name: "exactly five audio", files: 5, audio: 5, nonAudio: 0, want: ""},
		{name: "four audio", files: 5, audio: 4, nonAudio: 1, want: ""},
		{name: "empty card", files: 0, audio: 0, nonAudio: 0, want: ReasonNoAudio},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := Decision(testCase.files, testCase.audio, testCase.nonAudio); got != testCase.want {
				t.Fatalf("Decision(%d,%d,%d) = %q, want %q", testCase.files, testCase.audio, testCase.nonAudio, got, testCase.want)
			}
		})
	}
}
