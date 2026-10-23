package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"testing"
)

type retiredCatalogPlaybackCursor struct {
	WorkID          int64
	MediaItemID     int64
	FileSourceID    sql.NullInt64
	LocationID      sql.NullInt64
	LocationType    string
	PositionSeconds float64
}

func prepareFormatCardPlaybackRetirement(t *testing.T) (*looseWorkFixture, int64, int64, int64) {
	t.Helper()
	f := newLooseWorkFixture(t, formatCardTree())
	sourceID := fixtureSourceID(t, f)
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("first scan = %#v", p)
	}
	release := f.cards(t)["Release"]
	if release == nil {
		t.Fatalf("cards=%v, want the release card", f.cards(t))
	}
	releaseID := int64(release["catalogEntryId"].(float64))
	reviewRequest(t, f.handler, f.cookie, http.MethodPatch, "/api/source-catalog/"+strconv.FormatInt(releaseID, 10)+"/user-state", `{"rating":4}`, 200)
	var releaseWorkID int64
	if err := f.server.db.QueryRow(`SELECT work_id FROM source_catalog_entry WHERE id=?`, releaseID).Scan(&releaseWorkID); err != nil {
		t.Fatal(err)
	}
	return f, sourceID, releaseID, releaseWorkID
}

func seedFormatCardPlaybackWork(t *testing.T, f *looseWorkFixture, sourceID int64) (int64, int64) {
	t.Helper()
	staleID := seedFormatCard(t, f.server.db, sourceID, "/Creator/Release/MP3", "MP3", 0, []string{"01 first.mp3", "02 second.mp3"})
	saved := reviewRequest(t, f.handler, f.cookie, http.MethodPatch, "/api/source-catalog/"+strconv.FormatInt(staleID, 10)+"/user-state", `{"rating":4}`, 200)
	return staleID, int64(saved["workId"].(float64))
}

func insertPlaybackMedia(t *testing.T, db *sql.DB, id, workID int64, title string, discNo, trackNo, durationSeconds any, fingerprint string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO media_item(
			id,work_id,kind,title,disc_no,track_no,duration_seconds,fingerprint)
		VALUES(?,?,'audio',?,?,?,?,?)`,
		id, workID, title, discNo, trackNo, durationSeconds, fingerprint); err != nil {
		t.Fatal(err)
	}
}

func insertPlaybackLocation(t *testing.T, db *sql.DB, id, mediaItemID, sourceID int64, locationType, path string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO media_file_location(
			id,media_item_id,file_source_id,location_type,path,availability)
		VALUES(?,?,?,?,?,'available')`, id, mediaItemID, sourceID, locationType, path); err != nil {
		t.Fatal(err)
	}
}

func insertPlaybackCursor(t *testing.T, db *sql.DB, userID, workID, mediaItemID int64, position float64, fileSourceID, locationID any, locationType string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO user_work_playback_cursor(
			user_id,work_id,media_item_id,file_source_id,location_id,location_type,
			position_seconds,duration_seconds,last_played_at,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,180,'2026-01-02 00:00:00','2026-01-02 00:00:00','2026-01-02 00:00:00')`,
		userID, workID, mediaItemID, fileSourceID, locationID, locationType, position); err != nil {
		t.Fatal(err)
	}
}

func readPlaybackCursor(t *testing.T, db *sql.DB, userID, workID int64) retiredCatalogPlaybackCursor {
	t.Helper()
	var cursor retiredCatalogPlaybackCursor
	if err := db.QueryRow(`SELECT work_id,media_item_id,file_source_id,location_id,location_type,position_seconds
		FROM user_work_playback_cursor WHERE user_id=? AND work_id=?`, userID, workID).Scan(
		&cursor.WorkID, &cursor.MediaItemID, &cursor.FileSourceID,
		&cursor.LocationID, &cursor.LocationType, &cursor.PositionSeconds); err != nil {
		t.Fatal(err)
	}
	return cursor
}

func assertFormatCardRetained(t *testing.T, f *looseWorkFixture, staleID, sourceID int64) {
	t.Helper()
	var source sql.NullInt64
	var relocated sql.NullInt64
	if err := f.server.db.QueryRow(`SELECT file_source_id,relocated_to_id FROM source_catalog_entry WHERE id=?`, staleID).Scan(&source, &relocated); err != nil {
		t.Fatal(err)
	}
	if !source.Valid || source.Int64 != sourceID || relocated.Valid {
		t.Fatalf("conflicting card source=%v relocated=%v, want both cards retained", source, relocated)
	}
	if cards := f.cards(t); len(cards) != 2 || cards["Release"] == nil || cards["MP3"] == nil {
		t.Fatalf("cards=%v, want both release and format cards", cards)
	}
}

func assertFormatCardRetired(t *testing.T, f *looseWorkFixture, staleID, releaseID int64) {
	t.Helper()
	var source sql.NullInt64
	var relocated sql.NullInt64
	if err := f.server.db.QueryRow(`SELECT file_source_id,relocated_to_id FROM source_catalog_entry WHERE id=?`, staleID).Scan(&source, &relocated); err != nil {
		t.Fatal(err)
	}
	if source.Valid || !relocated.Valid || relocated.Int64 != releaseID {
		t.Fatalf("stale card source=%v relocated=%v, want the release card %d", source, relocated, releaseID)
	}
	if cards := f.cards(t); len(cards) != 1 || cards["Release"] == nil {
		t.Fatalf("cards=%v, want only the release card", cards)
	}
}

func drainExpectedPlaybackConflict(t *testing.T, f *looseWorkFixture) {
	t.Helper()
	for range 100 {
		err := f.server.runNextQueuedWorkflowJob(context.Background())
		if errors.Is(err, errRetiredCatalogPersonalStateConflict) {
			return
		}
		if err != nil {
			t.Fatalf("rescan failed for an unexpected reason: %v", err)
		}
		p := f.request(t, http.MethodGet, "/catalog/index", "", 200)
		if p["status"] != "queued" && p["status"] != "running" {
			t.Fatalf("rescan settled without the expected playback conflict: %#v", p)
		}
	}
	t.Fatal("rescan did not report the expected playback conflict")
}

// A cursor already stored on the release work is user-owned resume state. It
// must not be replaced by the format card's older or newer cursor.
func TestSourceCatalogFormatCardPlaybackCursorConflictKeepsBothCards(t *testing.T) {
	f, sourceID, _, releaseWorkID := prepareFormatCardPlaybackRetirement(t)
	staleID, staleWorkID := seedFormatCardPlaybackWork(t, f, sourceID)

	createAccountManagedTestUser(t, f.server, "format-cursor-conflict", "format-password")
	var listenerID int64
	if err := f.server.db.QueryRow(`SELECT id FROM user_account WHERE username='format-cursor-conflict'`).Scan(&listenerID); err != nil {
		t.Fatal(err)
	}
	insertPlaybackMedia(t, f.server.db, 901, staleWorkID, "Format track", nil, 1, 180, "format-track")
	insertPlaybackMedia(t, f.server.db, 902, releaseWorkID, "Release track", nil, 1, 180, "release-track")
	insertPlaybackCursor(t, f.server.db, listenerID, staleWorkID, 901, 30, nil, nil, "")
	insertPlaybackCursor(t, f.server.db, listenerID, releaseWorkID, 902, 70, nil, nil, "")

	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"force":true}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("rescan = %#v", p)
	}

	assertFormatCardRetained(t, f, staleID, sourceID)
	target := readPlaybackCursor(t, f.server.db, listenerID, releaseWorkID)
	if target.MediaItemID != 902 || target.PositionSeconds != 70 {
		t.Fatalf("release cursor = %#v, want its original media and position", target)
	}
	stale := readPlaybackCursor(t, f.server.db, listenerID, staleWorkID)
	if stale.MediaItemID != 901 || stale.PositionSeconds != 30 {
		t.Fatalf("format cursor = %#v, want its original media and position", stale)
	}
}

// A unique target track is the only safe automatic remap. The cursor keeps its
// position while its media and matching source location move to the release work.
func TestSourceCatalogFormatCardPlaybackCursorRemapsToTargetMedia(t *testing.T) {
	f, sourceID, releaseID, releaseWorkID := prepareFormatCardPlaybackRetirement(t)
	staleID, staleWorkID := seedFormatCardPlaybackWork(t, f, sourceID)

	createAccountManagedTestUser(t, f.server, "format-cursor-remap", "format-password")
	var listenerID int64
	if err := f.server.db.QueryRow(`SELECT id FROM user_account WHERE username='format-cursor-remap'`).Scan(&listenerID); err != nil {
		t.Fatal(err)
	}
	insertPlaybackMedia(t, f.server.db, 901, staleWorkID, "01 first", nil, 1, 125, "track-one")
	insertPlaybackMedia(t, f.server.db, 902, releaseWorkID, "01 first", nil, 1, 126, "track-one")
	insertPlaybackLocation(t, f.server.db, 910, 901, sourceID, "local", "stale/01 first.mp3")
	insertPlaybackLocation(t, f.server.db, 911, 902, sourceID, "local", "release/01 first.mp3")
	insertPlaybackCursor(t, f.server.db, listenerID, staleWorkID, 901, 42, sourceID, 910, "local")

	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"force":true}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("rescan = %#v", p)
	}

	assertFormatCardRetired(t, f, staleID, releaseID)
	cursor := readPlaybackCursor(t, f.server.db, listenerID, releaseWorkID)
	if cursor.MediaItemID != 902 || cursor.PositionSeconds != 42 {
		t.Fatalf("cursor = %#v, want target media 902 at position 42", cursor)
	}
	if !cursor.FileSourceID.Valid || cursor.FileSourceID.Int64 != sourceID ||
		!cursor.LocationID.Valid || cursor.LocationID.Int64 != 911 || cursor.LocationType != "local" {
		t.Fatalf("cursor location = %#v, want target local location 911", cursor)
	}
	var oldRows int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM user_work_playback_cursor WHERE user_id=? AND work_id=?`, listenerID, staleWorkID).Scan(&oldRows); err != nil {
		t.Fatal(err)
	}
	if oldRows != 0 {
		t.Fatalf("format work kept %d playback cursors", oldRows)
	}
}

// Unknown old duration and fingerprint must not invent a mismatch, while a
// missing target location must be cleared instead of copied from the old track.
func TestSourceCatalogFormatCardPlaybackCursorMatchesUnknownMetadata(t *testing.T) {
	f, sourceID, releaseID, releaseWorkID := prepareFormatCardPlaybackRetirement(t)
	staleID, staleWorkID := seedFormatCardPlaybackWork(t, f, sourceID)

	createAccountManagedTestUser(t, f.server, "format-cursor-unknown", "format-password")
	var listenerID int64
	if err := f.server.db.QueryRow(`SELECT id FROM user_account WHERE username='format-cursor-unknown'`).Scan(&listenerID); err != nil {
		t.Fatal(err)
	}
	insertPlaybackMedia(t, f.server.db, 901, staleWorkID, "01 first", nil, 1, nil, "")
	insertPlaybackMedia(t, f.server.db, 902, releaseWorkID, "01 first", nil, 1, 300, "target-hash")
	insertPlaybackCursor(t, f.server.db, listenerID, staleWorkID, 901, 17, nil, nil, "")

	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"force":true}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("rescan = %#v", p)
	}

	assertFormatCardRetired(t, f, staleID, releaseID)
	cursor := readPlaybackCursor(t, f.server.db, listenerID, releaseWorkID)
	if cursor.MediaItemID != 902 || cursor.PositionSeconds != 17 {
		t.Fatalf("cursor = %#v, want target media 902 at position 17", cursor)
	}
	if cursor.FileSourceID.Valid || cursor.LocationID.Valid || cursor.LocationType != "" {
		t.Fatalf("cursor location = %#v, want missing target location cleared", cursor)
	}
}

// Duplicate target tracks make the identity ambiguous. Retreating to the card
// pair is safer than guessing between two resume targets.
func TestSourceCatalogFormatCardPlaybackCursorDuplicateMediaKeepsCards(t *testing.T) {
	f, sourceID, _, releaseWorkID := prepareFormatCardPlaybackRetirement(t)
	staleID, staleWorkID := seedFormatCardPlaybackWork(t, f, sourceID)

	createAccountManagedTestUser(t, f.server, "format-cursor-duplicate", "format-password")
	var listenerID int64
	if err := f.server.db.QueryRow(`SELECT id FROM user_account WHERE username='format-cursor-duplicate'`).Scan(&listenerID); err != nil {
		t.Fatal(err)
	}
	insertPlaybackMedia(t, f.server.db, 901, staleWorkID, "01 first", nil, 1, 125, "track-one")
	insertPlaybackMedia(t, f.server.db, 902, releaseWorkID, "01 first", nil, 1, 125, "track-one")
	insertPlaybackMedia(t, f.server.db, 903, releaseWorkID, "01 first", nil, 1, 125, "track-one")
	insertPlaybackCursor(t, f.server.db, listenerID, staleWorkID, 901, 33, nil, nil, "")

	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"force":true}`, 202)
	drainExpectedPlaybackConflict(t, f)

	assertFormatCardRetained(t, f, staleID, sourceID)
	stale := readPlaybackCursor(t, f.server.db, listenerID, staleWorkID)
	if stale.MediaItemID != 901 || stale.PositionSeconds != 33 {
		t.Fatalf("format cursor = %#v, want the original media and position", stale)
	}
	var targetRows int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM user_work_playback_cursor WHERE user_id=? AND work_id=?`, listenerID, releaseWorkID).Scan(&targetRows); err != nil {
		t.Fatal(err)
	}
	if targetRows != 0 {
		t.Fatalf("release work gained %d ambiguous playback cursors", targetRows)
	}
}

// A target work with playable media but no matching track is also ambiguous.
func TestSourceCatalogFormatCardPlaybackCursorMissingMediaKeepsCards(t *testing.T) {
	f, sourceID, _, releaseWorkID := prepareFormatCardPlaybackRetirement(t)
	staleID, staleWorkID := seedFormatCardPlaybackWork(t, f, sourceID)

	createAccountManagedTestUser(t, f.server, "format-cursor-missing", "format-password")
	var listenerID int64
	if err := f.server.db.QueryRow(`SELECT id FROM user_account WHERE username='format-cursor-missing'`).Scan(&listenerID); err != nil {
		t.Fatal(err)
	}
	insertPlaybackMedia(t, f.server.db, 901, staleWorkID, "01 first", nil, 1, 125, "track-one")
	insertPlaybackMedia(t, f.server.db, 902, releaseWorkID, "02 second", nil, 2, 125, "track-two")
	insertPlaybackCursor(t, f.server.db, listenerID, staleWorkID, 901, 22, nil, nil, "")

	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"force":true}`, 202)
	drainExpectedPlaybackConflict(t, f)

	assertFormatCardRetained(t, f, staleID, sourceID)
	stale := readPlaybackCursor(t, f.server.db, listenerID, staleWorkID)
	if stale.MediaItemID != 901 || stale.PositionSeconds != 22 {
		t.Fatalf("format cursor = %#v, want the original media and position", stale)
	}
}
