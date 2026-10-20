package httpapi

import (
	"context"
	"testing"
)

// A resume re-queues the directories the previous attempt could not finish, so
// it can publish entries the run had already passed. The derived-state cursors
// must start over; otherwise a run that stopped after its lifecycle pass leaves
// republished entries without a status and keeps their older file rows.
func TestSourceCatalogResumeRestartsDerivedState(t *testing.T) {
	db := openMigratedTestDB(t)
	server := &Server{db: db}
	const sourceID, runID = int64(91), int64(902)
	if _, err := db.Exec(`INSERT INTO file_source(id,code,display_name,source_type) VALUES(?,?,'Resume source','openlist')`,
		sourceID, "resume-source"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_run(id,workflow_code,display_name,status,trigger_type)
		VALUES(?,?,'Index source catalog','partial','manual')`, runID, sourceCatalogIndexWorkerType); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO source_catalog_scan(file_source_id,workflow_run_id,options_json,lifecycle_stage,lifecycle_cursor,publish_cleanup_cursor)
		VALUES(?,?,'{}','done',500,700)`, sourceID, runID); err != nil {
		t.Fatal(err)
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	queued, err := server.enqueueSourceCatalogIndexTx(context.Background(), tx, sourceID, defaultCatalogIndexOptions(), "manual", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if queued.RunID != runID || !queued.Existing {
		t.Fatalf("resume queued %+v, want the existing run %d", queued, runID)
	}
	var stage string
	var lifecycle, cleanup int64
	if err := db.QueryRow(`SELECT lifecycle_stage,lifecycle_cursor,publish_cleanup_cursor FROM source_catalog_scan WHERE file_source_id=?`, sourceID).
		Scan(&stage, &lifecycle, &cleanup); err != nil {
		t.Fatal(err)
	}
	if stage != "observe" || lifecycle != 0 || cleanup != 0 {
		t.Fatalf("resume left derived state at stage=%q lifecycle=%d cleanup=%d", stage, lifecycle, cleanup)
	}
}

// A completed run starts a new generation instead of resuming, even when its
// derived state sits at the tail.
func TestSourceCatalogSucceededRunStartsANewGeneration(t *testing.T) {
	db := openMigratedTestDB(t)
	server := &Server{db: db}
	const sourceID, runID = int64(92), int64(903)
	if _, err := db.Exec(`INSERT INTO file_source(id,code,display_name,source_type) VALUES(?,?,'Done source','openlist')`,
		sourceID, "done-source"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_run(id,workflow_code,display_name,status,trigger_type)
		VALUES(?,?,'Index source catalog','succeeded','manual')`, runID, sourceCatalogIndexWorkerType); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO source_catalog_scan(file_source_id,workflow_run_id,options_json,lifecycle_stage,lifecycle_cursor,publish_cleanup_cursor)
		VALUES(?,?,'{}','done',500,700)`, sourceID, runID); err != nil {
		t.Fatal(err)
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	queued, err := server.enqueueSourceCatalogIndexTx(context.Background(), tx, sourceID, defaultCatalogIndexOptions(), "manual", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if queued.Existing || queued.RunID == runID || queued.RunID == 0 {
		t.Fatalf("completed run queued %+v, want a new generation", queued)
	}
	var stage string
	var lifecycle, cleanup int64
	if err := db.QueryRow(`SELECT lifecycle_stage,lifecycle_cursor,publish_cleanup_cursor FROM source_catalog_scan WHERE file_source_id=?`, sourceID).
		Scan(&stage, &lifecycle, &cleanup); err != nil {
		t.Fatal(err)
	}
	if stage != "observe" || lifecycle != 0 || cleanup != 0 {
		t.Fatalf("new generation left derived state at stage=%q lifecycle=%d cleanup=%d", stage, lifecycle, cleanup)
	}
}
