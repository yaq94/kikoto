package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// A series directory rename changes the series key. The folded survivor has to
// adopt that key: otherwise the next episode discovered under the new name
// creates a second series for the same group.
func TestCatalogSeriesRenameKeepsLaterEpisodesInOneSeries(t *testing.T) {
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/":                          {looseDir("Series: Example")},
		"/Series: Example":           {looseDir("Episode 1"), looseDir("Episode 2")},
		"/Series: Example/Episode 1": {looseAudioSized("one.mp3", 111)},
		"/Series: Example/Episode 2": {looseAudioSized("01.mp3", 222)},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("first scan = %#v", p)
	}
	var seriesID int64
	if err := f.server.db.QueryRow(`SELECT id FROM catalog_series`).Scan(&seriesID); err != nil {
		t.Fatal(err)
	}

	f.updates(map[string][]map[string]any{
		"/":                          {looseDir("Series: Renamed")},
		"/Series: Renamed":           {looseDir("Episode 1"), looseDir("Episode 2")},
		"/Series: Renamed/Episode 1": {looseAudioSized("one.mp3", 111)},
		"/Series: Renamed/Episode 2": {looseAudioSized("01.mp3", 222)},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"force":true}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("rename scan = %#v", p)
	}

	var series int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM catalog_series`).Scan(&series); err != nil || series != 1 {
		t.Fatalf("series rows after rename=%d, want 1: %v", series, err)
	}
	var key string
	if err := f.server.db.QueryRow(`SELECT COALESCE(source_key,'') FROM catalog_series WHERE id=?`, seriesID).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(key), "renamed") {
		t.Fatalf("series key=%q did not adopt the renamed directory", key)
	}

	f.updates(map[string][]map[string]any{
		"/":                          {looseDir("Series: Renamed")},
		"/Series: Renamed":           {looseDir("Episode 1"), looseDir("Episode 2"), looseDir("Episode 3")},
		"/Series: Renamed/Episode 1": {looseAudioSized("one.mp3", 111)},
		"/Series: Renamed/Episode 2": {looseAudioSized("01.mp3", 222)},
		"/Series: Renamed/Episode 3": {looseAudioSized("03.mp3", 333)},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"force":true}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("episode scan = %#v", p)
	}
	if err := f.server.db.QueryRow(`SELECT count(*) FROM catalog_series`).Scan(&series); err != nil || series != 1 {
		t.Fatalf("series rows after a new episode=%d, want 1: %v", series, err)
	}
	var members int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM catalog_series_member WHERE series_id=?`, seriesID).Scan(&members); err != nil || members != 3 {
		t.Fatalf("members=%d, want 3: %v", members, err)
	}
}

// A directory rename and a new episode can be observed in the same generation.
// The old membership is a stable subset of the new one, so it is still the same
// series rather than an old two-episode series beside a new three-episode one.
func TestCatalogSeriesRenameAndNewEpisodeInOneScanStayOneSeries(t *testing.T) {
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/":                          {looseDir("Series: Example")},
		"/Series: Example":           {looseDir("Episode 1"), looseDir("Episode 2")},
		"/Series: Example/Episode 1": {looseAudio("one.mp3")},
		"/Series: Example/Episode 2": {looseAudio("two.mp3")},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("first scan = %#v", p)
	}
	var seriesID int64
	if err := f.server.db.QueryRow(`SELECT id FROM catalog_series`).Scan(&seriesID); err != nil {
		t.Fatal(err)
	}

	f.updates(map[string][]map[string]any{
		"/":                          {looseDir("Series: Renamed")},
		"/Series: Renamed":           {looseDir("Episode 1"), looseDir("Episode 2"), looseDir("Episode 3")},
		"/Series: Renamed/Episode 1": {looseAudio("one.mp3")},
		"/Series: Renamed/Episode 2": {looseAudio("two.mp3")},
		"/Series: Renamed/Episode 3": {looseAudio("three.mp3")},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"force":true}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("rename scan = %#v", p)
	}

	var series, members int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM catalog_series`).Scan(&series); err != nil {
		t.Fatal(err)
	}
	if err := f.server.db.QueryRow(`SELECT count(*) FROM catalog_series_member WHERE series_id=?`, seriesID).Scan(&members); err != nil {
		t.Fatal(err)
	}
	if series != 1 || members != 3 {
		t.Fatalf("series=%d members=%d, want one series with three episodes", series, members)
	}
}

// Folding a renamed series must keep the personal state of the series that is
// about to disappear. Reviews hang off the series work row, so the survivor
// takes that row over instead of deleting it with the series.
func TestCatalogSeriesFoldKeepsSeriesReviewReachable(t *testing.T) {
	db := openMigratedTestDB(t)
	metadataReviewExec(t, db, `INSERT INTO user_account(id,username,display_name,role) VALUES(1,'fold-user','Fold User','user');
		INSERT INTO work(id,primary_code,title) VALUES(77,'','Example Series');
		INSERT INTO file_source(id,code,display_name,source_type) VALUES(7,'fold_source','Fold Source','openlist');
		INSERT INTO catalog_series(id,title,source_id,source_key,work_id) VALUES(11,'Old Name',7,'old',NULL),(12,'New Name',7,'new',77);
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title) VALUES(1,7,'/Old Name/Episode 1','Episode 1'),(2,7,'/New Name/Episode 1','Episode 1');
		INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence) VALUES(11,1,1,'directory'),(12,2,1,'directory');
		INSERT INTO user_work_state(user_id,work_id,rating,note) VALUES(1,77,5,'kept');`)

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err = foldCatalogSeriesTx(context.Background(), tx, 12, 11); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}

	var workID sql.NullInt64
	if err = db.QueryRow(`SELECT work_id FROM catalog_series WHERE id=11`).Scan(&workID); err != nil || !workID.Valid || workID.Int64 != 77 {
		t.Fatalf("surviving series work=%v err=%v", workID, err)
	}
	var series int
	if err = db.QueryRow(`SELECT count(*) FROM catalog_series`).Scan(&series); err != nil || series != 1 {
		t.Fatalf("series rows=%d err=%v", series, err)
	}
	var rating int
	var note string
	if err = db.QueryRow(`SELECT rating,note FROM user_work_state WHERE user_id=1 AND work_id=?`, workID.Int64).Scan(&rating, &note); err != nil {
		t.Fatal(err)
	}
	if rating != 5 || note != "kept" {
		t.Fatalf("review=%d %q, want 5 kept", rating, note)
	}
}

// Two registered series describe two sets of personal records. Folding them
// would have to choose one set, so the caller keeps both for review.
func TestCatalogSeriesFoldRefusesTwoRegisteredSeries(t *testing.T) {
	db := openMigratedTestDB(t)
	metadataReviewExec(t, db, `INSERT INTO file_source(id,code,display_name,source_type) VALUES(7,'fold_source','Fold Source','openlist');
		INSERT INTO work(id,primary_code,title) VALUES(77,'','Old Work'),(78,'','New Work');
		INSERT INTO catalog_series(id,title,source_id,source_key,work_id) VALUES(11,'Old',7,'old',77),(12,'New',7,'new',78);`)

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err = foldCatalogSeriesTx(context.Background(), tx, 12, 11); !errors.Is(err, errCatalogSeriesRetireConflict) {
		t.Fatalf("fold error=%v, want a review conflict", err)
	}
	// The open transaction owns the only pooled connection, so read through it.
	var series int
	if err = tx.QueryRow(`SELECT count(*) FROM catalog_series`).Scan(&series); err != nil || series != 2 {
		t.Fatalf("series rows=%d err=%v, want both kept", series, err)
	}
}
