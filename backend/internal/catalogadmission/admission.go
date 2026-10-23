// Package catalogadmission holds the card-level admission thresholds shared by
// the indexer, the browse API and the one-off prune tool. One definition keeps
// a card that the publisher refuses from reappearing through another path.
package catalogadmission

// Probe bounds keep diagnostic counts small. They do not decide admission: a
// directory with audio stays playable however many artwork, subtitle or archive
// files accompany it.
const (
	ProbeFiles    = 5000
	ProbeNonAudio = 2000
	ProbeAudio    = 5
)

// Reasons explain why a card cannot be published. They are stable strings used
// in logs and in the prune report.
const (
	ReasonNoAudio = "no_audio"
)

// Decision returns the blocking reason for a published card, or "" when the
// card is admissible.
func Decision(files, audio, nonAudio int) string {
	if audio == 0 {
		return ReasonNoAudio
	}
	return ""
}

// Admissible reports whether a card passes every threshold.
func Admissible(files, audio, nonAudio int) bool {
	return Decision(files, audio, nonAudio) == ""
}
