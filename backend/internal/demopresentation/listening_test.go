package demopresentation

import (
	"reflect"
	"testing"
	"time"
)

func TestListeningDayIsStableAndStaysInsideItsDay(t *testing.T) {
	first := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	active, sessions, leaders := 0, 0, 0
	for offset := 0; offset < 400; offset++ {
		day := first.AddDate(0, 0, offset)
		got := ListeningDay(day, 12)
		if !reflect.DeepEqual(got, ListeningDay(day, 12)) {
			t.Fatalf("%s is not stable", day.Format(time.DateOnly))
		}
		if len(got) > 0 {
			active++
		}
		end := 0
		for _, session := range got {
			if session.WorkRank < 0 || session.WorkRank >= 12 || session.Seconds < listeningSessionMinimum {
				t.Fatalf("%s session = %+v", day.Format(time.DateOnly), session)
			}
			// Sessions follow one another and none passes midnight.
			if session.StartSecond < end || session.StartSecond+session.Seconds > listeningDayEnd {
				t.Fatalf("%s sessions overlap or leave the day: %+v", day.Format(time.DateOnly), got)
			}
			end = session.StartSecond + session.Seconds
			sessions++
			if session.WorkRank < 3 {
				leaders++
			}
		}
	}
	// Listening happens on most days but not all, and a few works lead it.
	if active < 120 || active > 360 {
		t.Fatalf("active days = %d of 400", active)
	}
	if leaders*2 < sessions || leaders == sessions {
		t.Fatalf("leading works took %d of %d sessions", leaders, sessions)
	}
}

func TestListeningDayNeedsWorksAndAffinityFollowsTheWorkIdentity(t *testing.T) {
	day := time.Date(2026, time.March, 7, 0, 0, 0, 0, time.UTC)
	if got := ListeningDay(day, 0); got != nil {
		t.Fatalf("empty library = %+v", got)
	}
	for _, session := range ListeningDay(day, 1) {
		if session.WorkRank != 0 {
			t.Fatalf("single work rank = %d", session.WorkRank)
		}
	}
	if ListeningAffinity(" rj00000001 ") != ListeningAffinity("RJ00000001") || ListeningAffinity("RJ00000001") == ListeningAffinity("RJ00000002") {
		t.Fatal("affinity does not follow the normalized work identity")
	}
}
