package demopresentation

import (
	"crypto/sha256"
	"encoding/binary"
	"strconv"
	"strings"
	"time"
)

const ListeningVersion = "demo-listening-v1"

const (
	// listeningDayEnd is the last second of a day a session may reach.
	listeningDayEnd = 24 * 3600
	// listeningSessionMinimum drops a session the end of its day leaves too short.
	listeningSessionMinimum = 10 * 60
)

// ListeningSession is one simulated play on a calendar day.
type ListeningSession struct {
	// WorkRank picks a work by its position in the affinity order, 0 first.
	WorkRank int
	// StartSecond counts from the day's midnight.
	StartSecond int
	Seconds     int
}

// listeningDraw is a stable value in [0, 1) for a key.
func listeningDraw(parts ...string) float64 {
	digest := sha256.Sum256([]byte(ListeningVersion + "\x00" + strings.Join(parts, "\x00")))
	return float64(binary.BigEndian.Uint64(digest[:8])>>11) / float64(1<<53)
}

// ListeningAffinity orders works by simulated taste: a lower value is listened
// to more. It depends only on the unified work identity.
func ListeningAffinity(primaryCode string) float64 {
	return listeningDraw("affinity", strings.ToUpper(strings.TrimSpace(primaryCode)))
}

// ListeningDay is the simulated listening of one calendar day in a library of
// works works. The same day always yields the same sessions, so a history
// rebuilt later keeps its past. Sessions follow one another without overlap,
// and none passes the end of the day.
func ListeningDay(day time.Time, works int) []ListeningSession {
	if works <= 0 {
		return nil
	}
	date := day.Format(time.DateOnly)
	weekend := day.Weekday() == time.Saturday || day.Weekday() == time.Sunday
	// Listening comes in runs: every week has its own likelihood of a listening day.
	week := strconv.FormatInt(day.Unix()/(7*86400), 10)
	likelihood := 0.35 + 0.5*listeningDraw("week", week)
	if weekend {
		likelihood += 0.12
	}
	if listeningDraw("active", date) >= likelihood {
		return nil
	}
	span := 2.2
	if weekend {
		span = 3
	}
	count := 1 + int(listeningDraw("count", date)*span)

	// Most listening starts in the evening, some in the morning.
	start := (19 + int(listeningDraw("evening", date)*3)) * 3600
	if listeningDraw("morning", date) < 0.2 {
		start = (7 + int(listeningDraw("hour", date)*5)) * 3600
	}
	start += int(listeningDraw("minute", date)*60) * 60

	// A few works lead: the chance of a rank falls off with its position.
	total := 0.0
	for rank := 0; rank < works; rank++ {
		total += 1 / (float64(rank) + 1.5)
	}
	sessions := make([]ListeningSession, 0, count)
	for index := 0; index < count; index++ {
		key := date + "#" + strconv.Itoa(index)
		roll := listeningDraw("work", key) * total
		rank := 0
		for ; rank < works-1; rank++ {
			roll -= 1 / (float64(rank) + 1.5)
			if roll <= 0 {
				break
			}
		}
		// Short sessions are common and long ones rare.
		seconds := (15 + int(listeningDraw("length", key)*listeningDraw("length2", key)*105)) * 60
		if start+seconds > listeningDayEnd {
			seconds = listeningDayEnd - start
		}
		if seconds < listeningSessionMinimum {
			break
		}
		sessions = append(sessions, ListeningSession{WorkRank: rank, StartSecond: start, Seconds: seconds})
		// A short break before the next one.
		start += seconds + (2+int(listeningDraw("break", key)*14))*60
	}
	return sessions
}
