package httpapi

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/yexca/kikoto/backend/internal/config"
)

func TestWorkMatchingDoesNotTreatUnfinishedSourceRangeAsUnique(t *testing.T) {
	db := openMigratedTestDB(t)
	s := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-editor", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	defer func() { _ = s.Shutdown(context.Background()) }()
	createTestAdministrator(t, s, "synthetic-editor", "synthetic-password")
	metadataReviewExec(t, db, `INSERT INTO work(id,primary_code,title,release_date) VALUES(11,'','Example Work','2020-01-02'),(12,'','Example Work','2020-01-02');
	INSERT INTO person(id,display_name) VALUES(1,'Example Voice'); INSERT INTO work_credit(work_id,person_id,role) VALUES(11,1,'voice_actor'),(12,1,'voice_actor');
	INSERT INTO file_source(id,code,display_name,source_type) VALUES(7,'example_remote_a','Example Remote A','openlist');
	INSERT INTO file_source_endpoint(file_source_id,api_url,base_url) VALUES(7,'https://source.example.invalid','https://source.example.invalid');
	INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,work_id) VALUES(71,7,'/Example A','Example Work',11);`)
	app := httptest.NewServer(s.Routes())
	defer app.Close()
	cookie := workReferenceLogin(t, app, "synthetic-editor")
	workReferenceRequest(t, app, cookie, "POST", "/api/remote-sources/7/catalog/index", `{}`, 202, nil)
	var result map[string]any
	workReferenceRequest(t, app, cookie, "POST", "/api/work-matching", `{"kind":"work","id":11}`, 200, &result)
	if result["status"] != "paused" || result["rule"] != "source_index_incomplete" {
		t.Fatalf("incomplete source scope treated as unique: %#v", result)
	}
}

// A series can contain members from several sources. An unfinished scan of one
// source must only pause series whose members belong to that source; a series
// made entirely of entries from another source still has complete evidence.
func TestWorkMatchingSeriesPauseFollowsMemberSources(t *testing.T) {
	db := openMigratedTestDB(t)
	s := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-editor", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	defer func() { _ = s.Shutdown(context.Background()) }()
	createTestAdministrator(t, s, "synthetic-editor", "synthetic-password")
	metadataReviewExec(t, db, `INSERT INTO file_source(id,code,display_name,source_type) VALUES
		(7,'example_remote_a','Example Remote A','openlist'),(8,'example_remote_b','Example Remote B','openlist');
	INSERT INTO file_source_endpoint(file_source_id,api_url,base_url) VALUES
		(7,'https://source.example.invalid','https://source.example.invalid'),
		(8,'https://source-b.example.invalid','https://source-b.example.invalid');
	INSERT INTO source_catalog_entry(id,file_source_id,root_path,title) VALUES
		(71,7,'/Creator/A','Series A - Episode 1'),(72,7,'/Creator/B','Series A - Episode 2'),
		(81,8,'/Creator/C','Series B - Episode 1');
	INSERT INTO catalog_series(id,title) VALUES(1,'Series A'),(2,'Series B');
	INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence) VALUES
		(1,71,1,'directory'),(1,72,2,'directory'),(2,81,1,'directory');`)
	app := httptest.NewServer(s.Routes())
	defer app.Close()
	cookie := workReferenceLogin(t, app, "synthetic-editor")
	// Only source 8 has an unfinished scan. Series B depends on it; Series A
	// does not, so source 8 must not freeze the unrelated series.
	workReferenceRequest(t, app, cookie, "POST", "/api/remote-sources/8/catalog/index", `{}`, 202, nil)
	var seriesA, seriesB map[string]any
	workReferenceRequest(t, app, cookie, "POST", "/api/work-matching", `{"kind":"series","id":1}`, 200, &seriesA)
	if seriesA["status"] == "paused" && seriesA["rule"] == "source_index_incomplete" {
		t.Fatalf("series from source 7 was paused by source 8: %#v", seriesA)
	}
	workReferenceRequest(t, app, cookie, "POST", "/api/work-matching", `{"kind":"series","id":2}`, 200, &seriesB)
	if seriesB["status"] != "paused" || seriesB["rule"] != "source_index_incomplete" {
		t.Fatalf("series from an unfinished source was not paused: %#v", seriesB)
	}
}

// A registered work can be present in a source before its catalog entry was
// published. An unfinished scan of that source could still add evidence, so it
// must pause the decision just like an existing catalog entry does.
func TestWorkMatchingPausesAWorkPresentOnlyAsSourcePresence(t *testing.T) {
	db := openMigratedTestDB(t)
	s := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-editor", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	defer func() { _ = s.Shutdown(context.Background()) }()
	createTestAdministrator(t, s, "synthetic-editor", "synthetic-password")
	metadataReviewExec(t, db, `INSERT INTO work(id,primary_code,title) VALUES(11,'','Example Work');
	INSERT INTO file_source(id,code,display_name,source_type) VALUES(7,'example_remote_a','Example Remote A','openlist');
	INSERT INTO file_source_endpoint(file_source_id,api_url,base_url) VALUES(7,'https://source.example.invalid','https://source.example.invalid');
	INSERT INTO work_source_presence(work_id,file_source_id,presence_type,availability) VALUES(11,7,'source','available');`)
	app := httptest.NewServer(s.Routes())
	defer app.Close()
	cookie := workReferenceLogin(t, app, "synthetic-editor")
	workReferenceRequest(t, app, cookie, "POST", "/api/remote-sources/7/catalog/index", `{}`, 202, nil)
	var result map[string]any
	workReferenceRequest(t, app, cookie, "POST", "/api/work-matching", `{"kind":"work","id":11}`, 200, &result)
	if result["status"] != "paused" || result["rule"] != "source_index_incomplete" {
		t.Fatalf("presence-only work was matched while its source was incomplete: %#v", result)
	}
}

// A card retired by the source scan has no file source and is no longer
// listed. The matching view must not spend the per-subject budget on it.
func TestWorkMatchingSkipsRetiredCatalogSubjects(t *testing.T) {
	db := openMigratedTestDB(t)
	s := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-editor", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	defer func() { _ = s.Shutdown(context.Background()) }()
	metadataReviewExec(t, db, `INSERT INTO file_source(id,code,display_name,source_type) VALUES(7,'example_remote_a','Example Remote A','openlist');
		INSERT INTO file_source_endpoint(file_source_id,api_url,base_url) VALUES(7,'https://source.example.invalid','https://source.example.invalid');
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title) VALUES
			(71,NULL,'/Retired','Retired Work'),(72,7,'/Live','Live Work');
		UPDATE work_matching_control SET revision=1,scan_revision=1,cursor_kind='',cursor_id=0,status='queued',attempts=0,retry_after=0 WHERE id=1;`)
	var subjects int
	if err := db.QueryRow(`SELECT count(*) FROM work_matching_subject WHERE kind='catalog'`).Scan(&subjects); err != nil {
		t.Fatal(err)
	}
	if subjects != 1 {
		t.Fatalf("matching view exposes %d catalog subjects, want only the live one", subjects)
	}
	options := workMatchingOptions{IntervalMS: 100, CandidateLimit: 100, MinFreeBytes: 0}
	if err := s.advanceWorkMatching(context.Background(), options); err != nil {
		t.Fatalf("advance matching: %v", err)
	}
	var kind string
	var cursor int64
	if err := db.QueryRow(`SELECT cursor_kind,cursor_id FROM work_matching_control WHERE id=1`).Scan(&kind, &cursor); err != nil {
		t.Fatal(err)
	}
	if kind != "catalog" || cursor != 72 {
		t.Fatalf("matching cursor = %s/%d, want catalog/72", kind, cursor)
	}
	var retired int
	if err := db.QueryRow(`SELECT count(*) FROM work_matching_result WHERE kind='catalog' AND entity_id=71`).Scan(&retired); err != nil {
		t.Fatal(err)
	}
	if retired != 0 {
		t.Fatalf("retired subject produced %d results", retired)
	}
}
