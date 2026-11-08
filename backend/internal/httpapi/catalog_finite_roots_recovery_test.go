package httpapi

import (
	"net/http"
	"testing"
)

func TestCatalogFiniteLaterRootPublicationResumesAfterRestart(t *testing.T) {
	db, file := openPublicationRetryDB(t)
	f := newLooseWorkFixtureWithDB(t, map[string][]map[string]any{
		"/Example": {looseAudio("First.mp3")},
		"/Other":   {looseAudio("Second.mp3")},
	}, db)
	metadataReviewExec(t, db, `UPDATE file_source SET config_json=json_set(config_json,'$.openListRoots',json('["/Example","/Other"]')) WHERE id=?`, f.sourceID(t))
	metadataReviewExec(t, db, `CREATE TRIGGER synthetic_second_root_marker_failure
		BEFORE UPDATE OF state ON source_catalog_publication
		WHEN OLD.owner='manual:/Other' AND NEW.state='committed'
		BEGIN SELECT RAISE(ABORT,'synthetic second root interruption'); END`)
	f.request(t, http.MethodPost, "/catalog/scan", `{}`, 500)
	var op int64
	if err := db.QueryRow(`SELECT id FROM source_catalog_publication WHERE owner='manual:/Other' AND state='preparing'`).Scan(&op); err != nil {
		t.Fatal(err)
	}
	if got := f.request(t, http.MethodGet, "/works?pageSize=10", "", 200)["total"]; got != float64(1) {
		t.Fatalf("published roots before restart=%v, want first root only", got)
	}
	reopenCatalogFiniteFixture(t, f, file)
	f.server.db.SetMaxOpenConns(1)
	metadataReviewExec(t, f.server.db, `DROP TRIGGER synthetic_second_root_marker_failure`)
	// Completed operations may be reclaimed when the next root starts. Observe
	// the marker as it commits to verify the original operation was resumed.
	metadataReviewExec(t, f.server.db, `CREATE TABLE synthetic_committed_root(operation_id INTEGER);
		CREATE TRIGGER synthetic_second_root_commit AFTER UPDATE OF state ON source_catalog_publication
		WHEN OLD.owner='manual:/Other' AND NEW.state='committed'
		BEGIN INSERT INTO synthetic_committed_root VALUES(NEW.id); END`)
	result := f.request(t, http.MethodPost, "/catalog/scan", `{}`, 200)
	if result["status"] != "complete" || len(result["roots"].([]any)) != 2 {
		t.Fatalf("resumed roots=%v", result)
	}
	if got := f.request(t, http.MethodGet, "/works?pageSize=10", "", 200)["total"]; got != float64(2) {
		t.Fatalf("resumed published roots=%v, want 2", got)
	}
	var committed int64
	if err := f.server.db.QueryRow(`SELECT operation_id FROM synthetic_committed_root`).Scan(&committed); err != nil || committed != op {
		t.Fatalf("committed operation=%d err=%v want=%d", committed, err, op)
	}
	var files, pending int
	if err := f.server.db.QueryRow(`SELECT (SELECT count(*) FROM published_source_catalog_file WHERE kind='audio'),
		(SELECT count(*) FROM source_catalog_publication WHERE state='preparing')`).Scan(&files, &pending); err != nil || files != 2 || pending != 0 {
		t.Fatalf("published files=%d pending=%d err=%v", files, pending, err)
	}
}
