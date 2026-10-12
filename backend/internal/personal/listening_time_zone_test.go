package personal

import (
	"context"
	"testing"
	"time"
)

func TestListeningDaysFollowTheAccountCalendar(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	// A player nine hours ahead of UTC listens across its own midnight, which
	// is still the same UTC day.
	crossing := SessionInput{SessionID: "synthetic-session-zone", WorkID: 1, ListenedSeconds: 120, StartedAt: "2026-03-02T14:59:00Z", LastListenedAt: "2026-03-02T15:01:00Z", Days: []SessionDay{{Day: "2026-03-02", ListenedSeconds: 60}, {Day: "2026-03-03", ListenedSeconds: 60}}}
	// No time zone puts an instant two calendar days from its UTC day.
	impossible := SessionInput{SessionID: "synthetic-session-far", WorkID: 1, ListenedSeconds: 60, StartedAt: "2026-03-02T14:59:00Z", LastListenedAt: "2026-03-02T15:00:00Z", Days: []SessionDay{{Day: "2026-03-04", ListenedSeconds: 60}}}
	result, err := s.RecordPlaybackReport(ctx, 1, PlaybackReport{History: []SessionInput{crossing, impossible}})
	if err != nil || len(result.History) != 2 || result.History[0].Status != "recorded" || result.History[1].Status != "invalid" {
		t.Fatalf("report = %+v, %v", result, err)
	}

	zone := time.FixedZone("synthetic/zone", 9*3600)
	now := time.Date(2026, time.March, 3, 0, 30, 0, 0, zone) // 2026-03-02 15:30 UTC
	stats, err := s.StatisticsFor(ctx, 1, RangeLast30Days, now)
	if err != nil || stats.Today != "2026-03-03" || len(stats.Series) != 30 || stats.Series[29].Period != "2026-03-03" {
		t.Fatalf("statistics = %+v, %v", stats, err)
	}
	// The play counts on the day its first listening was reported under.
	if stats.Series[28] != (ListeningPeriod{Period: "2026-03-02", ListenedSeconds: 60, ListenCount: 1}) || stats.Series[29] != (ListeningPeriod{Period: "2026-03-03", ListenedSeconds: 60}) {
		t.Fatalf("series = %+v", stats.Series[28:])
	}
	calendar, err := s.CalendarFor(ctx, 1, RangeAll, now)
	if err != nil || calendar.To != "2026-03-03" || calendar.From != "2025-03-03" || calendar.TimeZone != "synthetic/zone" {
		t.Fatalf("calendar = %+v, %v", calendar, err)
	}
	// The same instant read in UTC ends the report a calendar day earlier.
	stats, err = s.StatisticsFor(ctx, 1, RangeLast30Days, now.UTC())
	if err != nil || stats.Today != "2026-03-02" || stats.Series[29].Period != "2026-03-02" {
		t.Fatalf("UTC statistics = %+v, %v", stats, err)
	}

	// A report without days is credited to the account's current day.
	if err = s.RecordSession(ctx, 1, SessionInput{SessionID: "synthetic-session-undated", WorkID: 1, ListenedSeconds: 30, Day: "2026-03-03"}); err != nil {
		t.Fatal(err)
	}
	var seconds float64
	if err = s.DB.QueryRow(`SELECT listened_seconds FROM user_listening_day WHERE user_id = 1 AND work_id = 1 AND day = '2026-03-03'`).Scan(&seconds); err != nil || seconds != 90 {
		t.Fatalf("undated day = %v, %v", seconds, err)
	}
}
