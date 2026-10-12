package personal

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestListeningCalendarCoversWholeWeeksOfDatedListening(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Date(2026, time.March, 4, 18, 0, 0, 0, time.FixedZone("synthetic", -5*3600)) // Wednesday 23:00 UTC
	execFixture(t, s.DB, `INSERT INTO user_listening_day (user_id,work_id,day,listened_seconds,listen_count) VALUES
	 (1,1,'2026-03-04',600,2),(1,2,'2026-03-04',300,1),(1,1,'2025-03-03',3600,3),(1,3,'2025-03-02',7200,1),(2,1,'2025-12-20',60,1)`)
	execFixture(t, s.DB, `INSERT INTO user_listening_import (user_id,work_id,listened_seconds,listen_count) VALUES (1,3,100,1)`)

	calendar, err := s.CalendarFor(ctx, 1, now)
	if err != nil || calendar.From != "2025-03-03" || calendar.To != "2026-03-04" {
		t.Fatalf("calendar span = %+v, %v", calendar, err)
	}
	// The day before the first Monday and undated imports stay out; one day sums its works.
	if len(calendar.Days) != 2 || calendar.Days[0] != (ListeningDay{Date: "2025-03-03", ListenedSeconds: 3600, ListenCount: 3}) || calendar.Days[1] != (ListeningDay{Date: "2026-03-04", ListenedSeconds: 900, ListenCount: 3}) {
		t.Fatalf("calendar days = %+v", calendar.Days)
	}
	calendar, err = s.CalendarFor(ctx, 2, now)
	if err != nil || len(calendar.Days) != 1 || calendar.Days[0].Date != "2025-12-20" {
		t.Fatalf("other account = %+v, %v", calendar, err)
	}
}

func TestListeningBreakdownRanksTagsVoicesAndCirclesPerRange(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Date(2026, time.March, 2, 23, 0, 0, 0, time.UTC)
	var provider int64
	if err := s.DB.QueryRow(`SELECT id FROM metadata_provider WHERE code='dlsite'`).Scan(&provider); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO tag (id,namespace,normalized_name,display_name) VALUES (1,'metadata','example-a','Example Tag A'),(2,'dlsite','example-b','Example Tag B'),(3,'other','example-c','Example Tag C')`,
		// One shared tag from two sources counts a work once.
		`INSERT INTO work_tag (work_id,tag_id,source) VALUES (1,1,'dlsite_snapshot'),(1,1,'manual'),(2,1,'dlsite_snapshot'),(3,2,'dlsite_snapshot'),(1,3,'manual')`,
		`INSERT INTO person (id,display_name) VALUES (1,'Example Voice A'),(2,'Example Voice B'),(3,'Example Writer')`,
		`INSERT INTO work_credit (work_id,person_id,role) VALUES (1,1,'voice_actor'),(2,1,'voice_actor'),(1,2,'voice_actor'),(1,3,'scenario')`,
		`INSERT INTO party (id,display_name) VALUES (1,'Example Circle A'),(2,'Example Circle B')`,
		`INSERT INTO work_party (work_id,party_id,role,source) VALUES (1,1,'circle','dlsite_snapshot'),(2,1,'circle','dlsite_snapshot'),(3,2,'circle','dlsite_snapshot')`,
		`INSERT INTO user_listening_day (user_id,work_id,day,listened_seconds,listen_count) VALUES (1,1,'2026-03-02',600,2),(1,2,'2026-02-10',1800,1),(1,3,'2025-06-15',3600,3),(2,3,'2026-03-01',60,1)`,
		`INSERT INTO user_listening_import (user_id,work_id,listened_seconds,listen_count) VALUES (1,3,100,1)`,
	} {
		execFixture(t, s.DB, statement)
	}
	execFixture(t, s.DB, `INSERT INTO party_external_id (party_id,provider_id,id_type,external_id,is_primary) VALUES (1,?,'maker_id','RG00000000',1)`, provider)

	recent, err := s.BreakdownFor(ctx, 1, RangeLast30Days, now)
	if err != nil || recent.Range != RangeLast30Days || recent.ListenedSeconds != 2400 {
		t.Fatalf("30d = %+v, %v", recent, err)
	}
	if len(recent.Tags) != 1 || recent.Tags[0] != (BreakdownEntry{ID: 1, Name: "Example Tag A", ListenedSeconds: 2400, ListenCount: 3, WorkCount: 2}) {
		t.Fatalf("30d tags = %+v", recent.Tags)
	}
	// A work counts in full toward each credited voice; other roles are not voices.
	if len(recent.Voices) != 2 || recent.Voices[0] != (BreakdownEntry{ID: 1, Name: "Example Voice A", ListenedSeconds: 2400, ListenCount: 3, WorkCount: 2}) || recent.Voices[1] != (BreakdownEntry{ID: 2, Name: "Example Voice B", ListenedSeconds: 600, ListenCount: 2, WorkCount: 1}) {
		t.Fatalf("30d voices = %+v", recent.Voices)
	}
	if len(recent.Circles) != 1 || recent.Circles[0] != (BreakdownEntry{ID: 1, Name: "Example Circle A", ExternalID: "RG00000000", ListenedSeconds: 2400, ListenCount: 3, WorkCount: 2}) {
		t.Fatalf("30d circles = %+v", recent.Circles)
	}

	year, err := s.BreakdownFor(ctx, 1, RangeLast12Months, now)
	if err != nil || year.ListenedSeconds != 6000 || len(year.Tags) != 2 || year.Tags[0].ID != 2 || year.Tags[0].ListenedSeconds != 3600 || len(year.Circles) != 2 || year.Circles[0].ExternalID != "" {
		t.Fatalf("12m = %+v, %v", year, err)
	}

	// All time ranks lifetime totals, which include undated imports.
	all, err := s.BreakdownFor(ctx, 1, RangeAll, now)
	if err != nil || all.ListenedSeconds != 100 || len(all.Tags) != 1 || all.Tags[0].ID != 2 || len(all.Voices) != 0 || len(all.Circles) != 1 || all.Circles[0].ID != 2 {
		t.Fatalf("all = %+v, %v", all, err)
	}
	if _, err = s.BreakdownFor(ctx, 1, StatisticsRange("7d"), now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown range = %v", err)
	}
}
