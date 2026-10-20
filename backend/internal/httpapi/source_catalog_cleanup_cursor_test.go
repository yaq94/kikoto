package httpapi

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
)

func seedCatalogCleanupJob(t *testing.T, db *sql.DB, sourceID, runID int64) workflowJobRecord {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO file_source(id,code,display_name,source_type) VALUES(?,?,?,'openlist')`,
		sourceID, fmt.Sprintf("cleanup-source-%d", sourceID), "Cleanup source"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_run(id,workflow_code,display_name,status,trigger_type)
		VALUES(?,?,'Index source catalog','running','manual')`, runID, sourceCatalogIndexWorkerType); err != nil {
		t.Fatal(err)
	}
	result, err := db.Exec(`INSERT INTO workflow_job(workflow_run_id,worker_type,status,locked_by)
		VALUES(?,?,'running','cleanup-test')`, runID, sourceCatalogIndexWorkerType)
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO source_catalog_scan(file_source_id,workflow_run_id,options_json) VALUES(?,?,'{}')`,
		sourceID, runID); err != nil {
		t.Fatal(err)
	}
	return workflowJobRecord{ID: jobID, RunID: runID, WorkerType: sourceCatalogIndexWorkerType, LockedBy: "cleanup-test"}
}

// A card whose subtree the current generation could not observe keeps the file
// rows an older generation wrote. The cleanup cursor still advances: the verdict
// is fixed for this run, so a second sweep would only repeat the same reads.
func TestCatalogCleanupKeepsUncertainRowsAndAdvancesCursor(t *testing.T) {
	db := openMigratedTestDB(t)
	server := &Server{db: db}
	const sourceID, runID = int64(81), int64(802)
	job := seedCatalogCleanupJob(t, db, sourceID, runID)
	ctx := context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true)

	for _, q := range []string{
		`INSERT INTO source_catalog_directory(file_source_id,path,generation,revision,partition_path,catalog_root,status)
		 VALUES(81,'/work-a',802,1,'/work-a','/work-a','complete'),
		       (81,'/work-a/sub',802,1,'/work-a','/work-a','failed'),
		       (81,'/work-b',802,1,'/work-b','/work-b','complete'),
		       (81,'/work-c',802,1,'/work-c','/work-c','complete')`,
		`INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,last_seen_generation,discovered_generation)
		 VALUES(1,81,'/work-a','Work A',802,802),
		       (2,81,'/work-b','Work B',802,802),
		       (3,81,'/work-c','Work C',802,802)`,
		`INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes,observed_generation)
		 VALUES(1,'old.mp3','old.mp3','audio',1,801),
		       (1,'new.mp3','new.mp3','audio',1,802),
		       (2,'old.mp3','old.mp3','audio',1,801),
		       (2,'new.mp3','new.mp3','audio',1,802),
		       (3,'old.mp3','old.mp3','audio',1,801),
		       (3,'new.mp3','new.mp3','audio',1,802)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}

	if scanned, _, err := server.cleanupCatalogPublishBatch(ctx, job, sourceID); err != nil || scanned != 3 {
		t.Fatalf("first sweep: scanned=%d err=%v", scanned, err)
	}
	var cursor int64
	if err := db.QueryRow(`SELECT publish_cleanup_cursor FROM source_catalog_scan WHERE file_source_id=?`, sourceID).Scan(&cursor); err != nil {
		t.Fatal(err)
	}
	if cursor != 3 {
		t.Fatalf("cursor after a full sweep = %d, want the last id 3", cursor)
	}
	var kept int
	if err := db.QueryRow(`SELECT count(*) FROM source_catalog_file WHERE catalog_entry_id=1 AND observed_generation=801`).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if kept != 1 {
		t.Fatalf("unobserved subtree kept %d older rows, want 1", kept)
	}
	for _, entry := range []int64{2, 3} {
		if err := db.QueryRow(`SELECT count(*) FROM source_catalog_file WHERE catalog_entry_id=? AND observed_generation=801`, entry).Scan(&kept); err != nil {
			t.Fatal(err)
		}
		if kept != 0 {
			t.Fatalf("completed entry %d kept %d older rows, want 0", entry, kept)
		}
	}

	// A second sweep must not rewind or rescan; the tail is already reached.
	if scanned, _, err := server.cleanupCatalogPublishBatch(ctx, job, sourceID); err != nil || scanned != 0 {
		t.Fatalf("second sweep: scanned=%d err=%v", scanned, err)
	}
	if err := db.QueryRow(`SELECT publish_cleanup_cursor FROM source_catalog_scan WHERE file_source_id=?`, sourceID).Scan(&cursor); err != nil {
		t.Fatal(err)
	}
	if cursor != 3 {
		t.Fatalf("cursor after the tail = %d, want 3", cursor)
	}

	// A negative cursor from an older build is consumed once, not repeated.
	if _, err := db.Exec(`UPDATE source_catalog_scan SET publish_cleanup_cursor=-1 WHERE file_source_id=?`, sourceID); err != nil {
		t.Fatal(err)
	}
	if scanned, _, err := server.cleanupCatalogPublishBatch(ctx, job, sourceID); err != nil || scanned != 3 {
		t.Fatalf("legacy marker sweep: scanned=%d err=%v", scanned, err)
	}
	if err := db.QueryRow(`SELECT publish_cleanup_cursor FROM source_catalog_scan WHERE file_source_id=?`, sourceID).Scan(&cursor); err != nil {
		t.Fatal(err)
	}
	if cursor != 3 {
		t.Fatalf("cursor after a legacy marker = %d, want 3", cursor)
	}

	// Once a later generation observes the subtree, the same rows are retired.
	if _, err := db.Exec(`UPDATE source_catalog_directory SET status='complete', error_code='' WHERE file_source_id=? AND path='/work-a/sub'`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE source_catalog_scan SET publish_cleanup_cursor=0 WHERE file_source_id=?`, sourceID); err != nil {
		t.Fatal(err)
	}
	if scanned, _, err := server.cleanupCatalogPublishBatch(ctx, job, sourceID); err != nil || scanned != 3 {
		t.Fatalf("observed sweep: scanned=%d err=%v", scanned, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM source_catalog_file WHERE catalog_entry_id=1 AND observed_generation=801`).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if kept != 0 {
		t.Fatalf("observed subtree kept %d older rows, want 0", kept)
	}
}
