// Package catalogadmission holds the card-level admission thresholds shared by
// the indexer, the browse API and the one-off prune tool. One definition keeps
// a card that the publisher refuses from reappearing through another path.
package catalogadmission

// Thresholds are inclusive bounds: a card may have exactly 5000 files, exactly
// 2000 non-audio rows and exactly 5 audio rows.
const (
	MaxFiles    = 5000
	MaxNonAudio = 2000
	MinAudio    = 5
)

// Reasons explain why a card cannot be published. They are stable strings used
// in logs and in the prune report.
const (
	ReasonNoAudio       = "no_audio"
	ReasonTooManyFiles  = "too_many_files"
	ReasonNonAudioHeavy = "non_audio_dominant"
)

// Decision returns the blocking reason for a published card, or "" when the
// card is admissible.
func Decision(files, audio, nonAudio int) string {
	switch {
	case audio == 0:
		return ReasonNoAudio
	case files > MaxFiles:
		return ReasonTooManyFiles
	case nonAudio > MaxNonAudio && audio < MinAudio:
		return ReasonNonAudioHeavy
	default:
		return ""
	}
}

// Admissible reports whether a card passes every threshold.
func Admissible(files, audio, nonAudio int) bool {
	return Decision(files, audio, nonAudio) == ""
}
