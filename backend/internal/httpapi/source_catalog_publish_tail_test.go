package httpapi

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"path"
	"strconv"
	"testing"
	"time"
)

// seedLargeCatalog stages the shape a real full index leaves behind: thousands
// of completed directories and tens of thousands of published observations.
// The publisher must seek to the remaining pending rows, not scan every
// completed directory for each batch.
func seedLargeCatalog(t *testing.T, db *sql.DB, sourceID, runID int64, directories, entriesPerDirectory int) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	directoryInsert, err := tx.Prepare(`INSERT INTO source_catalog_directory(file_source_id,path,generation,revision,partition_path,catalog_root,status,completed_at) VALUES(?,?,?,1,'/Partition',?,'complete',CURRENT_TIMESTAMP)`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = directoryInsert.Close() }()
	observationInsert, err := tx.Prepare(`INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes,published_generation,publish_pending,content_hash) VALUES(?,?,?,1,?,100,?,0,'')`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = observationInsert.Close() }()
	rootInsert, err := tx.Prepare(`INSERT OR IGNORE INTO source_catalog_audio_root(file_source_id,generation,catalog_root) VALUES(?,?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rootInsert.Close() }()

	for index := 0; index < directories; index++ {
		directory := path.Join("/Partition", fmt.Sprintf("Work %05d", index))
		if _, err = directoryInsert.Exec(sourceID, directory, runID, directory); err != nil {
			t.Fatal(err)
		}
		if _, err = rootInsert.Exec(sourceID, runID, directory); err != nil {
			t.Fatal(err)
		}
		for entry := 0; entry < entriesPerDirectory; entry++ {
			kind := "audio"
			if entry%entriesPerDirectory != 0 {
				kind = "folder"
			}
			if _, err = observationInsert.Exec(sourceID, directory, fmt.Sprintf("entry-%03d", entry), kind, runID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func runningCatalogJob(t *testing.T, db *sql.DB, sourceID, runID int64) workflowJobRecord {
	t.Helper()
	var id int64
	if err := db.QueryRow(`SELECT id FROM workflow_job WHERE workflow_run_id=? AND worker_type=?`, runID, sourceCatalogIndexWorkerType).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE workflow_job SET status='running',locked_by='tail-test',locked_at=CURRENT_TIMESTAMP,heartbeat_at=CURRENT_TIMESTAMP WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	return workflowJobRecord{ID: id, RunID: runID, WorkerType: sourceCatalogIndexWorkerType, LockedBy: "tail-test"}
}

func TestSourceCatalogPublishTailStaysBounded(t *testing.T) {
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	sourceID, err := strconv.ParseInt(path.Base(f.base), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	started := f.request(t, http.MethodPost, "/catalog/index", `{"pagesPerBatch":32}`, 202)
	runID := int64(started["runId"].(float64))
	if _, err = f.server.db.Exec(`DELETE FROM source_catalog_directory WHERE file_source_id=?`, sourceID); err != nil {
		t.Fatal(err)
	}
	seedLargeCatalog(t, f.server.db, sourceID, runID, 3000, 13)
	job := runningCatalogJob(t, f.server.db, sourceID, runID)

	// Five unpublished observations at the end of the tree. Before the pending
	// queue, LIMIT 100 was applied only after the correlated audio-root check
	// examined all 3,000 completed directories (36 seconds on the live copy).
	tail, err := f.server.db.Query(`SELECT directory_path,name FROM source_catalog_observation WHERE file_source_id=? ORDER BY directory_path DESC,name DESC LIMIT 5`, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	type key struct{ directory, name string }
	keys := []key{}
	for tail.Next() {
		var item key
		if err = tail.Scan(&item.directory, &item.name); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, item)
	}
	if err = tail.Err(); err != nil {
		t.Fatal(err)
	}
	_ = tail.Close()
	for _, item := range keys {
		if _, err = f.server.db.Exec(`UPDATE source_catalog_observation SET published_generation=0,publish_pending=1 WHERE file_source_id=? AND directory_path=? AND name=?`, sourceID, item.directory, item.name); err != nil {
			t.Fatal(err)
		}
	}

	startedAt := time.Now()
	published, err := f.server.publishCatalogBatch(context.Background(), job, sourceID)
	elapsed := time.Since(startedAt)
	if err != nil {
		t.Fatal(err)
	}
	if published != len(keys) {
		t.Fatalf("published=%d want %d", published, len(keys))
	}
	if elapsed > 2*time.Second {
		t.Fatalf("tail publication took %s; it must seek the pending rows", elapsed)
	}

	startedAt = time.Now()
	published, err = f.server.publishCatalogBatch(context.Background(), job, sourceID)
	elapsed = time.Since(startedAt)
	if err != nil {
		t.Fatal(err)
	}
	if published != 0 || elapsed > time.Second {
		t.Fatalf("drained queue published=%d in %s; want an empty queue to return immediately", published, elapsed)
	}
}

func TestSourceCatalogPublishSkipsIncompleteDirectories(t *testing.T) {
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	sourceID, err := strconv.ParseInt(path.Base(f.base), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	started := f.request(t, http.MethodPost, "/catalog/index", `{"pagesPerBatch":32}`, 202)
	runID := int64(started["runId"].(float64))
	if _, err = f.server.db.Exec(`DELETE FROM source_catalog_directory WHERE file_source_id=?`, sourceID); err != nil {
		t.Fatal(err)
	}
	seedLargeCatalog(t, f.server.db, sourceID, runID, 1000, 13)
	if _, err = f.server.db.Exec(`UPDATE source_catalog_directory SET status='pending' WHERE file_source_id=? AND path IN (
		SELECT path FROM source_catalog_directory WHERE file_source_id=? ORDER BY path DESC LIMIT 1)`, sourceID, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.server.db.Exec(`UPDATE source_catalog_observation SET published_generation=0,publish_pending=1 WHERE file_source_id=? AND directory_path IN (
		SELECT path FROM source_catalog_directory WHERE file_source_id=? ORDER BY path DESC LIMIT 1)`, sourceID, sourceID); err != nil {
		t.Fatal(err)
	}
	job := runningCatalogJob(t, f.server.db, sourceID, runID)

	startedAt := time.Now()
	published, err := f.server.publishCatalogBatch(context.Background(), job, sourceID)
	elapsed := time.Since(startedAt)
	if err != nil {
		t.Fatal(err)
	}
	if published != 0 {
		t.Fatalf("published %d observations from an incomplete directory", published)
	}
	// The legacy shape took 36 seconds here, so one second separates the two
	// by a wide margin on an unloaded host. The race detector instruments
	// every SQLite scan and the sharded CI job runs four of them at once;
	// the dedicated non-race job owns this wall-clock contract.
	if !raceInstrumentation && elapsed > time.Second {
		t.Fatalf("incomplete-only queue took %s", elapsed)
	}
}

func TestCatalogAudioRootTracksCompletedAudio(t *testing.T) {
	db := openMigratedTestDB(t)
	if _, err := db.Exec(`INSERT INTO file_source(id,code,display_name,source_type,enabled,config_json) VALUES(11,'example-openlist','Example','openlist',1,'{}')`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.Exec(`INSERT INTO source_catalog_directory(file_source_id,path,generation,revision,partition_path,catalog_root,status) VALUES(11,'/Example',1,1,'/Example','/Example','complete')`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes,published_generation,publish_pending) VALUES(11,'/Example','track.mp3',1,'audio',100,0,1)`); err != nil {
		t.Fatal(err)
	}
	if err = refreshCatalogAudioRootTx(context.Background(), tx, 11, 1, "/Example"); err != nil {
		t.Fatal(err)
	}
	var roots int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM source_catalog_audio_root WHERE file_source_id=11 AND generation=1 AND catalog_root='/Example'`).Scan(&roots); err != nil {
		t.Fatal(err)
	}
	if roots != 1 {
		t.Fatalf("root missing after a complete audio directory: %d", roots)
	}
	if _, err = tx.Exec(`UPDATE source_catalog_directory SET status='pending' WHERE file_source_id=11 AND path='/Example'`); err != nil {
		t.Fatal(err)
	}
	if err = refreshCatalogAudioRootTx(context.Background(), tx, 11, 1, "/Example"); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(`SELECT COUNT(*) FROM source_catalog_audio_root WHERE file_source_id=11`).Scan(&roots); err != nil {
		t.Fatal(err)
	}
	if roots != 0 {
		t.Fatalf("root retained without a complete audio directory: %d", roots)
	}
}
