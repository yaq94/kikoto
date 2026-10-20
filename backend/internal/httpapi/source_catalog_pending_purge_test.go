package httpapi

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
)

func seedCatalogPendingPurgeJob(t *testing.T, db *sql.DB, sourceID, runID int64) workflowJobRecord {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO file_source(id,code,display_name,source_type) VALUES(?,?,?,'openlist')`,
		sourceID, fmt.Sprintf("purge-source-%d", sourceID), "Purge source"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_run(id,workflow_code,display_name,status,trigger_type)
		VALUES(?,?,'Index source catalog','running','manual')`, runID, sourceCatalogIndexWorkerType); err != nil {
		t.Fatal(err)
	}
	result, err := db.Exec(`INSERT INTO workflow_job(workflow_run_id,worker_type,status,locked_by)
		VALUES(?,?,'running','purge-test')`, runID, sourceCatalogIndexWorkerType)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return workflowJobRecord{ID: id, RunID: runID, WorkerType: sourceCatalogIndexWorkerType, LockedBy: "purge-test"}
}

func countCatalogPending(t *testing.T, db *sql.DB, sourceID int64) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM source_catalog_observation WHERE file_source_id=? AND publish_pending=1`, sourceID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func countCatalogTableRows(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A partial scan must not publish rows that belong to a directory the new
// generation never saw, and clearing those flags must not remove the staged
// observation or any catalog identity.
func TestSourceCatalogPurgesStalePendingBeforePublish(t *testing.T) {
	db := openMigratedTestDB(t)
	server := &Server{db: db}
	const sourceID, runID = int64(71), int64(702)
	job := seedCatalogPendingPurgeJob(t, db, sourceID, runID)

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, q := range []string{
		`INSERT INTO source_catalog_directory(file_source_id,path,generation,revision,partition_path,catalog_root,status)
		 VALUES(71,'/stale',701,1,'/stale','/stale','complete')`,
		`INSERT INTO source_catalog_directory(file_source_id,path,generation,revision,partition_path,catalog_root,status)
		 VALUES(71,'/current',702,1,'/current','/current','complete')`,
		`INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes,published_generation,publish_pending)
		 VALUES(71,'/stale','old.mp3',1,'audio',100,0,1)`,
		`INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes,published_generation,publish_pending)
		 VALUES(71,'/stale','done.mp3',1,'audio',100,701,0)`,
		`INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes,published_generation,publish_pending)
		 VALUES(71,'/current','current.mp3',1,'audio',100,0,1)`,
		`INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,last_seen_generation,discovered_generation)
		 VALUES(1,71,'/stale','Stale',701,701)`,
		`INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes)
		 VALUES(1,'old.mp3','old.mp3','audio',100)`,
	} {
		if _, err = tx.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}

	tables := []string{"source_catalog_directory", "source_catalog_observation", "source_catalog_entry", "source_catalog_file", "source_catalog_audio_root"}
	before := map[string]int{}
	for _, table := range tables {
		before[table] = countCatalogTableRows(t, db, table)
	}

	if err = server.purgeStaleCatalogPending(context.Background(), job, sourceID); err != nil {
		t.Fatal(err)
	}

	var stalePending, currentPending, stalePublished int
	if err = db.QueryRow(`SELECT publish_pending,published_generation FROM source_catalog_observation
		WHERE file_source_id=71 AND directory_path='/stale' AND name='old.mp3'`).Scan(&stalePending, &stalePublished); err != nil {
		t.Fatal(err)
	}
	if stalePending != 0 {
		t.Fatalf("stale publish_pending=%d, want 0", stalePending)
	}
	if stalePublished != 0 {
		t.Fatalf("purge changed published_generation to %d", stalePublished)
	}
	if err = db.QueryRow(`SELECT publish_pending FROM source_catalog_observation
		WHERE file_source_id=71 AND directory_path='/current' AND name='current.mp3'`).Scan(&currentPending); err != nil {
		t.Fatal(err)
	}
	if currentPending != 1 {
		t.Fatalf("current publish_pending=%d, want 1", currentPending)
	}
	var donePending, donePublished int
	if err = db.QueryRow(`SELECT publish_pending,published_generation FROM source_catalog_observation
		WHERE file_source_id=71 AND directory_path='/stale' AND name='done.mp3'`).Scan(&donePending, &donePublished); err != nil {
		t.Fatal(err)
	}
	if donePending != 0 || donePublished != 701 {
		t.Fatalf("already-cleared row became pending=%d published=%d", donePending, donePublished)
	}
	var generation int64
	if err = db.QueryRow(`SELECT generation FROM source_catalog_directory WHERE file_source_id=71 AND path='/stale'`).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if generation != 701 {
		t.Fatalf("stale directory generation=%d, want 701", generation)
	}
	for _, table := range tables {
		if after := countCatalogTableRows(t, db, table); after != before[table] {
			t.Fatalf("%s rows changed from %d to %d", table, before[table], after)
		}
	}
}

func TestSourceCatalogPendingPurgeBounded(t *testing.T) {
	db := openMigratedTestDB(t)
	server := &Server{db: db}
	const sourceID, runID = int64(72), int64(703)
	job := seedCatalogPendingPurgeJob(t, db, sourceID, runID)

	if _, err := db.Exec(`INSERT INTO source_catalog_directory(file_source_id,path,generation,revision,partition_path,catalog_root,status)
		VALUES(72,'/stale',701,1,'/stale','/stale','complete')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n<17000)
		INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes,publish_pending)
		SELECT 72,'/stale',printf('entry-%05d',n),1,'audio',100,1 FROM seq`); err != nil {
		t.Fatal(err)
	}
	if pending := countCatalogPending(t, db, sourceID); pending != 17000 {
		t.Fatalf("seeded pending=%d, want 17000", pending)
	}

	originalPause := catalogPendingPurgePause
	t.Cleanup(func() { catalogPendingPurgePause = originalPause })
	pauses := 0
	catalogPendingPurgePause = func(context.Context) error {
		pauses++
		return nil
	}

	if err := server.purgeStaleCatalogPending(context.Background(), job, sourceID); err != nil {
		t.Fatal(err)
	}
	if remaining := countCatalogPending(t, db, sourceID); remaining != 1000 {
		t.Fatalf("first call left %d pending, want 1000 (16000 cleared)", remaining)
	}
	if want := catalogStalePendingPurgeBatch - 1; pauses != want {
		t.Fatalf("pause calls=%d, want %d", pauses, want)
	}
	if catalogStalePendingPurge != 2000 || catalogStalePendingPurgeBatch != 8 {
		t.Fatalf("bounds changed: rows=%d batches=%d", catalogStalePendingPurge, catalogStalePendingPurgeBatch)
	}

	if err := server.purgeStaleCatalogPending(context.Background(), job, sourceID); err != nil {
		t.Fatal(err)
	}
	if remaining := countCatalogPending(t, db, sourceID); remaining != 0 {
		t.Fatalf("second call left %d pending, want 0", remaining)
	}
	if pauses != catalogStalePendingPurgeBatch-1 {
		t.Fatalf("second call added pauses: %d", pauses)
	}
	if total := countCatalogTableRows(t, db, "source_catalog_observation"); total != 17000 {
		t.Fatalf("observation rows=%d, want 17000 (purge must not delete)", total)
	}
}
