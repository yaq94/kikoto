package personal

import (
	"context"
	"testing"
	"time"

	"github.com/yexca/kikoto/backend/internal/testfixture"
)

func TestImportedProgressRejectsEarlierOfflineReportsAndAllowsLaterBackwardSeeks(t *testing.T) {
	for _, tc := range []struct {
		name     string
		existing bool
		offset   time.Duration
	}{{"new cursor", false, 0}, {"existing cursor", true, -10 * time.Second}, {"existing clock ahead", true, time.Second}} {
		t.Run(tc.name, func(t *testing.T) {
			s := testStore(t)
			ctx := context.Background()
			execFixture(t, s.DB, `INSERT INTO media_item (id,work_id,kind,title,fingerprint) VALUES (1,1,'audio','Example Track','synthetic-track')`)
			now := time.Now()
			checkpoint := ProgressInput{ReportID: "synthetic-before-import", Order: now.Add(-5 * time.Second).UnixMilli(), MediaItemID: 1, PositionSeconds: 20}
			if tc.existing {
				existing := checkpoint
				existing.Order = now.Add(tc.offset).UnixMilli()
				existing.PositionSeconds = 120
				if _, err := s.RecordPlaybackReport(ctx, 1, PlaybackReport{Progress: []ProgressInput{existing}}); err != nil {
					t.Fatal(err)
				}
			}
			code := testfixture.WorkCode(testfixture.PrefixRJ, 0)
			backup := Backup{Works: []BackupWork{{PrimaryCode: code, ListeningStatus: "paused", Progress: &BackupProgress{MediaWorkCode: code, MediaTitle: "Example Track", PositionSeconds: 90, LastPlayedAt: "2026-01-01 00:00:00"}}}}
			beforeImport := time.Now().UnixMilli()
			result, err := s.Import(ctx, 1, backup, true)
			if err != nil || result.ImportedWorks != 1 || result.SkippedProgress != 0 {
				t.Fatalf("import = %+v %v", result, err)
			}
			var barrier int64
			var lastPlayed string
			if err = s.DB.QueryRow(`SELECT report_order,last_played_at FROM user_work_playback_cursor WHERE user_id=1 AND work_id=1`).Scan(&barrier, &lastPlayed); err != nil || barrier < beforeImport || (tc.existing && barrier <= now.Add(tc.offset).UnixMilli()) || lastPlayed != backup.Works[0].Progress.LastPlayedAt {
				t.Fatalf("import barrier = %d / %s: %v", barrier, lastPlayed, err)
			}
			for _, order := range []int64{checkpoint.Order, barrier} {
				checkpoint.Order = order
				checkpoint.ReportID = "zz-synthetic-offline"
				replayed, err := s.RecordPlaybackReport(ctx, 1, PlaybackReport{Progress: []ProgressInput{checkpoint}})
				if err != nil || replayed.Progress[0].Status != "stale" {
					t.Fatalf("earlier/tied replay = %+v %v", replayed, err)
				}
			}
			var position float64
			if err = s.DB.QueryRow(`SELECT position_seconds FROM user_work_playback_cursor WHERE user_id=1 AND work_id=1`).Scan(&position); err != nil || position != 90 {
				t.Fatalf("imported position = %v %v", position, err)
			}
			for i, position := range []float64{25, 10} {
				checkpoint.Order = barrier + int64(i) + 1
				checkpoint.PositionSeconds = position
				later, err := s.RecordPlaybackReport(ctx, 1, PlaybackReport{Progress: []ProgressInput{checkpoint}})
				if err != nil || later.Progress[0].Cursor == nil || later.Progress[0].Cursor.PositionSeconds != position {
					t.Fatalf("later backward checkpoint = %+v %v", later, err)
				}
			}
		})
	}
}

// A shared identity has one review. A conflict on one member must keep that
// review for every member, even when another member carries a review value
// that does not itself conflict.
func TestImportKeepSharedIdentityConflictAppliesToEveryMember(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	// Install the existing review before linking the members so the shared
	// review trigger does not pre-populate the second member.
	execFixture(t, s.DB, `INSERT INTO user_work_state(user_id,work_id,listening_status,rating,note) VALUES(2,1,'finished',5,'')`)
	execFixture(t, s.DB, `INSERT INTO shared_association(id,operation,status,version,fingerprint,snapshot_json) VALUES(91,'link','completed','v1','example','{}');
		INSERT INTO shared_identity_member(work_id,identity_work_id,operation_id) VALUES(1,1,91),(2,1,91);`)
	rating := 4
	b := Backup{Format: "kikoto-user-data", Version: 1, Works: []BackupWork{
		{PrimaryCode: testfixture.WorkCode(testfixture.PrefixRJ, 0), ListeningStatus: "finished", Rating: &rating},
		{PrimaryCode: testfixture.WorkCode(testfixture.PrefixRJ, 1), ListeningStatus: "finished", Note: "Second member note"},
	}}
	preview, err := s.PreviewImport(ctx, 2, b)
	if err != nil || preview.Conflicts != 1 {
		t.Fatalf("preview = %+v, %v; want one identity conflict", preview, err)
	}
	result, err := s.Import(ctx, 2, b, false)
	if err != nil || result.ImportedWorks != 2 || result.SkippedWorks != 0 {
		t.Fatalf("keep import = %+v, %v; want both members processed", result, err)
	}
	var gotRating int
	var note string
	if err = s.DB.QueryRow(`SELECT COALESCE(rating,0),note FROM user_work_state WHERE user_id=2 AND work_id=1`).Scan(&gotRating, &note); err != nil || gotRating != 5 || note != "" {
		t.Fatalf("identity review = %d/%q, %v; want the existing review kept", gotRating, note, err)
	}
}

// T0 exported the default review and status. T1 added personal data. A keep
// import of the T0 backup must not interpret the defaults as explicit clears.
func TestImportKeepExportedDefaultsDoNotClearLaterPersonalData(t *testing.T) {
	for _, status := range []struct {
		name  string
		value string
	}{
		{"exported none", "none"},
		{"absent status", ""},
	} {
		t.Run(status.name, func(t *testing.T) {
			s := testStore(t)
			ctx := context.Background()
			code := testfixture.WorkCode(testfixture.PrefixRJ, 0)
			execFixture(t, s.DB, `INSERT INTO user_work_state(user_id,work_id,listening_status,rating,note,updated_at) VALUES(2,1,'none',NULL,'','2025-01-01 00:00:00')`)
			data, err := s.Export(ctx, 2)
			if err != nil {
				t.Fatal(err)
			}
			b, err := ParseImport(ImportRequest{Format: "kikoto", Data: data, Conflict: "keep"})
			if err != nil {
				t.Fatal(err)
			}
			if len(b.Works) != 1 || b.Works[0].PrimaryCode != code || b.Works[0].Rating != nil || b.Works[0].Note != "" || b.Works[0].ListeningStatus != "none" {
				t.Fatalf("T0 backup = %+v; want empty review and none status", b.Works)
			}
			b.Works[0].ListeningStatus = status.value
			execFixture(t, s.DB, `UPDATE user_work_state SET listening_status='finished',rating=4,note='T1 note',updated_at='2026-01-01 00:00:00' WHERE user_id=2 AND work_id=1`)
			preview, err := s.PreviewImport(ctx, 2, b)
			if err != nil || preview.Conflicts != 0 {
				t.Fatalf("T2 preview = %+v, %v", preview, err)
			}
			result, err := s.Import(ctx, 2, b, false)
			if err != nil || result.ImportedWorks != 1 || result.SkippedWorks != 0 {
				t.Fatalf("T2 keep import = %+v, %v", result, err)
			}
			var rating int
			var note, listeningStatus, updatedAt string
			if err = s.DB.QueryRow(`SELECT COALESCE(rating,0),note,listening_status,updated_at FROM user_work_state WHERE user_id=2 AND work_id=1`).Scan(&rating, &note, &listeningStatus, &updatedAt); err != nil {
				t.Fatal(err)
			}
			if rating != 4 || note != "T1 note" || listeningStatus != "finished" || updatedAt != "2026-01-01 00:00:00" {
				t.Fatalf("T1 state = %d/%q/%q/%q; want it retained", rating, note, listeningStatus, updatedAt)
			}
		})
	}
}

// A keep import writes only the review fields the backup actually carries.
// The absent field on the same identity must keep its stored value.
func TestImportKeepMergesOnlyReviewFieldsPresentInBackup(t *testing.T) {
	code := testfixture.WorkCode(testfixture.PrefixRJ, 0)
	rating := 4
	for _, tc := range []struct {
		name       string
		state      string
		work       BackupWork
		wantRating int
		wantNote   string
	}{
		{
			name:       "rating without note",
			state:      `INSERT INTO user_work_state(user_id,work_id,listening_status,rating,note) VALUES(2,1,'finished',NULL,'existing note')`,
			work:       BackupWork{PrimaryCode: code, ListeningStatus: "none", Rating: &rating},
			wantRating: 4,
			wantNote:   "existing note",
		},
		{
			name:       "note without rating",
			state:      `INSERT INTO user_work_state(user_id,work_id,listening_status,rating,note) VALUES(2,1,'finished',4,'')`,
			work:       BackupWork{PrimaryCode: code, ListeningStatus: "none", Note: "incoming note"},
			wantRating: 4,
			wantNote:   "incoming note",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testStore(t)
			ctx := context.Background()
			execFixture(t, s.DB, tc.state)
			b := Backup{Format: "kikoto-user-data", Version: 1, Works: []BackupWork{tc.work}}
			preview, err := s.PreviewImport(ctx, 2, b)
			if err != nil || preview.Conflicts != 0 {
				t.Fatalf("preview = %+v, %v", preview, err)
			}
			result, err := s.Import(ctx, 2, b, false)
			if err != nil || result.ImportedWorks != 1 || result.SkippedWorks != 0 {
				t.Fatalf("keep import = %+v, %v", result, err)
			}
			var gotRating int
			var note, listeningStatus string
			if err = s.DB.QueryRow(`SELECT COALESCE(rating,0),note,listening_status FROM user_work_state WHERE user_id=2 AND work_id=1`).Scan(&gotRating, &note, &listeningStatus); err != nil {
				t.Fatal(err)
			}
			if gotRating != tc.wantRating || note != tc.wantNote || listeningStatus != "finished" {
				t.Fatalf("state = %d/%q/%q; want %d/%q/finished", gotRating, note, listeningStatus, tc.wantRating, tc.wantNote)
			}
		})
	}
}
