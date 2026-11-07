package httpapi

import (
	"fmt"
	"net/http"
	"testing"
)

// A failed prepared file statement must roll back its entire page and release
// the sole SQLite connection. A new server resumes the published checkpoints.
func TestCatalogFiniteFileBatchFailureReleasesWriterAndResumes(t *testing.T) {
	const count = 521
	audit := &catalogWriteAudit{}
	db := openCatalogWriteAuditedDB(t, audit)
	items := make([]map[string]any, count)
	for i := range items {
		items[i] = looseAudio(fmt.Sprintf("Example Recording %c%c.mp3", 'a'+i/26, 'a'+i%26))
	}
	f := newLooseWorkFixtureWithDB(t, map[string][]map[string]any{"/Example": items}, db)
	metadataReviewExec(t, db, `CREATE TRIGGER synthetic_finite_file_failure BEFORE INSERT ON source_catalog_file
	 WHEN (SELECT count(*) FROM source_catalog_file)>=65
	 BEGIN SELECT RAISE(ABORT,'synthetic finite file failure'); END;`)
	audit.start()
	f.request(t, http.MethodPost, "/catalog/scan", `{}`, http.StatusInternalServerError)
	audit.stop()
	var files int
	var cursor string
	var operation int64
	if err := db.QueryRow(`SELECT count(*),(SELECT apply_cursor FROM source_catalog_listing WHERE observed_version='finite'),
	 (SELECT id FROM source_catalog_publication WHERE state='preparing') FROM source_catalog_file`).Scan(&files, &cursor, &operation); err != nil {
		t.Fatal(err)
	}
	if files != 64 || cursor == "" || operation == 0 {
		t.Fatalf("file checkpoint=%d/%q operation=%d; want one committed 64-file batch", files, cursor, operation)
	}
	if view := f.request(t, http.MethodGet, "/works?pageSize=1", "", 200); view["total"] != float64(0) {
		t.Fatalf("failed file batch published partial cards=%v", view["total"])
	}
	rows, entries, maxFiles, _ := audit.details()
	if rows > 64 || entries > 50 || maxFiles > 64 {
		t.Fatalf("finite batch exceeded write budget: rows=%d entries=%d files=%d", rows, entries, maxFiles)
	}
	var sequence int
	var name, databasePath string
	if err := db.QueryRow(`PRAGMA database_list`).Scan(&sequence, &name, &databasePath); err != nil {
		t.Fatal(err)
	}
	reopenCatalogFiniteFixture(t, f, databasePath)
	f.server.db.SetMaxOpenConns(1)
	metadataReviewExec(t, f.server.db, `DROP TRIGGER synthetic_finite_file_failure`)
	result := f.request(t, http.MethodPost, "/catalog/scan", `{}`, 200)
	if result["status"] != "complete" {
		t.Fatalf("resumed scan=%v", result)
	}
	if view := f.request(t, http.MethodGet, "/works?pageSize=1", "", 200); view["total"] != float64(count) {
		t.Fatalf("resumed complete set=%v want=%d", view["total"], count)
	}
	if err := f.server.db.QueryRow(`SELECT count(*) FROM published_source_catalog_file WHERE kind='audio'`).Scan(&files); err != nil || files != count {
		t.Fatalf("resumed files=%d err=%v", files, err)
	}
	t.Logf("committed_file_prefix=64 final_cards=%d final_files=%d batch_rows=%d", count, files, rows)
}
