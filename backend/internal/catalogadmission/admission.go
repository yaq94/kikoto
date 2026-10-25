// Package catalogadmission holds the card-level admission thresholds shared by
// the indexer, the browse API and the one-off prune tool. One definition keeps
// a card that the publisher refuses from reappearing through another path.
package catalogadmission

// Probe bounds are the admission thresholds. The publisher, the browse API and
// the prune tool read these three constants, and SQL counts stop one row past
// each bound so a large card costs the same as a small one. A directory with
// audio therefore stays playable only while it remains inside these limits.
const (
	ProbeFiles    = 5000
	ProbeNonAudio = 2000
	ProbeAudio    = 5
)

// Reasons explain why a card cannot be published. They are stable strings used
// in logs and in the prune report.
const (
	ReasonNoAudio         = "no_audio"
	ReasonTooManyFiles    = "too_many_files"
	ReasonTooManyNonAudio = "too_many_non_audio"
)

// Decision returns the blocking reason for a published card, or "" when the
// card is admissible.
func Decision(files, audio, nonAudio int) string {
	if audio == 0 {
		return ReasonNoAudio
	}
	if files > ProbeFiles {
		return ReasonTooManyFiles
	}
	if nonAudio > ProbeNonAudio && audio < ProbeAudio {
		return ReasonTooManyNonAudio
	}
	return ""
}

// Admissible reports whether a card passes every threshold.
func Admissible(files, audio, nonAudio int) bool {
	return Decision(files, audio, nonAudio) == ""
}
