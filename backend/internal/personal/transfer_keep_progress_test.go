package personal

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/yexca/kikoto/backend/internal/testfixture"
)

func TestImportKeepPreservesPortableProgressWithoutCursor(t *testing.T) {
	const existingProgress = `{"mediaTitle":"Existing portable take","positionSeconds":22,"lastPlayedAt":"2025-01-01 00:00:00"}`
	for _, tc := range []struct {
		name            string
		incomingMatches bool
	}{
		{name: "unmatched incoming progress"},
		{name: "matched incoming progress", incomingMatches: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testStore(t)
			ctx := context.Background()
			code := testfixture.WorkCode(testfixture.PrefixRJ, 0)
			execFixture(t, s.DB, `INSERT INTO media_item (id,work_id,kind,title) VALUES (1,1,'audio','Example Track')`)
			execFixture(t, s.DB, `INSERT INTO personal_transfer_progress(user_id,work_id,progress_json) VALUES(2,1,?)`, existingProgress)
			var ref string
			if err := s.DB.QueryRow(`SELECT ref FROM personal_transfer_ref WHERE work_id=1 AND is_primary=1`).Scan(&ref); err != nil {
				t.Fatal(err)
			}
			progress := &BackupProgress{
				MediaWorkCode:   code,
				MediaTitle:      "Different portable take",
				PositionSeconds: 91,
				LastPlayedAt:    "2026-01-01 00:00:00",
			}
			if tc.incomingMatches {
				progress.MediaTitle = "Example Track"
			}
			backup := Backup{Format: "kikoto-user-data", Version: 2, Works: []BackupWork{{
				PrimaryCode: code,
				Identity:    &BackupIdentity{Ref: ref, Kind: "work", Title: "Example Work"},
				Progress:    progress,
			}}}
			preview, err := s.PreviewImport(ctx, 2, backup)
			if err != nil || preview.Conflicts != 1 {
				t.Fatalf("preview = %+v, %v; want one progress conflict", preview, err)
			}
			found := false
			for _, issue := range preview.Issues {
				if issue.Reason == "existing_progress" {
					found = true
				}
			}
			if !found {
				t.Fatalf("preview issues = %+v; want existing_progress", preview.Issues)
			}
			result, err := s.Import(ctx, 2, backup, false)
			if err != nil || result.ImportedWorks != 1 || result.SkippedWorks != 0 {
				t.Fatalf("keep import = %+v, %v", result, err)
			}
			var got string
			if err = s.DB.QueryRow(`SELECT progress_json FROM personal_transfer_progress WHERE user_id=2 AND work_id=1`).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != existingProgress {
				t.Fatalf("portable progress = %s; want existing value %s", got, existingProgress)
			}
			var cursors int
			if err = s.DB.QueryRow(`SELECT count(*) FROM user_work_playback_cursor WHERE user_id=2 AND work_id=1`).Scan(&cursors); err != nil || cursors != 0 {
				t.Fatalf("keep import created %d cursors, %v", cursors, err)
			}
		})
	}
}

func TestImportKeepPreservesCursorAndPortableProgress(t *testing.T) {
	const existingProgress = `{"mediaTitle":"Existing portable take","positionSeconds":33,"lastPlayedAt":"2025-01-01 00:00:00"}`
	s := testStore(t)
	ctx := context.Background()
	code := testfixture.WorkCode(testfixture.PrefixRJ, 0)
	execFixture(t, s.DB, `INSERT INTO media_item (id,work_id,kind,title) VALUES (1,1,'audio','Example Track')`)
	execFixture(t, s.DB, `INSERT INTO user_work_playback_cursor(user_id,work_id,media_item_id,position_seconds,duration_seconds,last_played_at)
		VALUES (2,1,1,120,600,'2026-01-01 00:00:00')`)
	execFixture(t, s.DB, `INSERT INTO personal_transfer_progress(user_id,work_id,progress_json) VALUES(2,1,?)`, existingProgress)
	var ref string
	if err := s.DB.QueryRow(`SELECT ref FROM personal_transfer_ref WHERE work_id=1 AND is_primary=1`).Scan(&ref); err != nil {
		t.Fatal(err)
	}
	backup := Backup{Format: "kikoto-user-data", Version: 2, Works: []BackupWork{{
		PrimaryCode: code,
		Identity:    &BackupIdentity{Ref: ref, Kind: "work", Title: "Example Work"},
		Progress: &BackupProgress{
			MediaWorkCode:   code,
			MediaTitle:      "Example Track",
			PositionSeconds: 91,
			LastPlayedAt:    "2026-01-02 00:00:00",
		},
	}}}
	preview, err := s.PreviewImport(ctx, 2, backup)
	if err != nil || preview.Conflicts != 1 {
		t.Fatalf("preview = %+v, %v; want one progress conflict", preview, err)
	}
	result, err := s.Import(ctx, 2, backup, false)
	if err != nil || result.ImportedWorks != 1 || result.SkippedWorks != 0 {
		t.Fatalf("keep import = %+v, %v", result, err)
	}
	var position float64
	var lastPlayed string
	if err = s.DB.QueryRow(`SELECT position_seconds,last_played_at FROM user_work_playback_cursor WHERE user_id=2 AND work_id=1`).Scan(&position, &lastPlayed); err != nil {
		t.Fatal(err)
	}
	if position != 120 || lastPlayed != "2026-01-01 00:00:00" {
		t.Fatalf("cursor = %v / %q; want existing checkpoint", position, lastPlayed)
	}
	var raw string
	if err = s.DB.QueryRow(`SELECT progress_json FROM personal_transfer_progress WHERE user_id=2 AND work_id=1`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var progress BackupProgress
	if err = json.Unmarshal([]byte(raw), &progress); err != nil {
		t.Fatal(err)
	}
	if progress.PositionSeconds != 33 || progress.MediaTitle != "Existing portable take" {
		t.Fatalf("portable progress = %+v; want existing checkpoint", progress)
	}
}
