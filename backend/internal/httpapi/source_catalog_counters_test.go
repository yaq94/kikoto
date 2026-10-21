package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"path"
	"strconv"
	"testing"

	"github.com/yexca/kikoto/backend/internal/config"
	"github.com/yexca/kikoto/backend/internal/workflow"
)

type catalogStoredCounters struct {
	directories, completed, pending, failed, restricted, empty int
	observed, indexed, publishPending                          int
}

func readCatalogStoredCounters(t *testing.T, db *sql.DB, sourceID int64) catalogStoredCounters {
	t.Helper()
	var c catalogStoredCounters
	if err := db.QueryRow(`SELECT directories,completed,pending,failed,restricted,empty,observed,indexed,publish_pending_remaining
		FROM source_catalog_scan WHERE file_source_id=?`, sourceID).Scan(
		&c.directories, &c.completed, &c.pending, &c.failed, &c.restricted, &c.empty,
		&c.observed, &c.indexed, &c.publishPending); err != nil {
		t.Fatalf("read stored counters: %v", err)
	}
	return c
}

func readCatalogDirectCounters(t *testing.T, db *sql.DB, sourceID, generation int64) catalogStoredCounters {
	t.Helper()
	var c catalogStoredCounters
	err := db.QueryRow(`SELECT
		(SELECT count(*) FROM source_catalog_directory WHERE file_source_id=? AND generation=?),
		(SELECT count(*) FROM source_catalog_directory WHERE file_source_id=? AND generation=?
			AND (status='complete' OR (status='empty' AND error_code='not_found'))),
		(SELECT count(*) FROM source_catalog_directory WHERE file_source_id=? AND generation=? AND status IN ('pending','listing_complete')),
		(SELECT count(*) FROM source_catalog_directory WHERE file_source_id=? AND generation=? AND status='failed'),
		(SELECT count(*) FROM source_catalog_directory WHERE file_source_id=? AND generation=? AND status='restricted'),
		(SELECT count(*) FROM source_catalog_directory WHERE file_source_id=? AND generation=? AND status='empty' AND error_code<>'not_found'),
		(SELECT count(*) FROM source_catalog_directory d JOIN source_catalog_observation o
			ON o.file_source_id=d.file_source_id AND o.directory_path=d.path AND o.revision=d.revision
			WHERE d.file_source_id=? AND d.generation=?),
		(SELECT count(*) FROM source_catalog_entry WHERE file_source_id=? AND last_seen_generation=?),
		(SELECT count(*) FROM source_catalog_directory d JOIN source_catalog_observation o
			ON o.file_source_id=d.file_source_id AND o.directory_path=d.path AND o.revision=d.revision
			WHERE d.file_source_id=? AND d.generation=? AND d.status='complete' AND d.catalog_root<>'' AND o.publish_pending=1
			AND EXISTS(SELECT 1 FROM source_catalog_audio_root r
				WHERE r.file_source_id=d.file_source_id AND r.generation=d.generation AND r.catalog_root=d.catalog_root))`,
		sourceID, generation, sourceID, generation, sourceID, generation, sourceID, generation,
		sourceID, generation, sourceID, generation, sourceID, generation, sourceID, generation,
		sourceID, generation).Scan(
		&c.directories, &c.completed, &c.pending, &c.failed, &c.restricted, &c.empty,
		&c.observed, &c.indexed, &c.publishPending)
	if err != nil {
		t.Fatalf("read direct counters: %v", err)
	}
	return c
}

// assertCatalogCounterProjection is the acceptance check for the incremental
// columns: each stored value must equal the direct aggregate, and the progress
// endpoint must report the same numbers as ?exact=1.
func assertCatalogCounterProjection(t *testing.T, server *Server, sourceID int64) {
	t.Helper()
	stored := readCatalogStoredCounters(t, server.db, sourceID)
	direct := readCatalogDirectCounters(t, server.db, sourceID, storedGeneration(t, server.db, sourceID))
	if stored != direct {
		t.Fatalf("stored counters = %+v, direct aggregate = %+v", stored, direct)
	}
	incremental, err := server.sourceCatalogProgress(context.Background(), sourceID, 1)
	if err != nil {
		t.Fatal(err)
	}
	exact, err := server.sourceCatalogProgressExact(context.Background(), sourceID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if incremental.Directories != exact.Directories || incremental.Completed != exact.Completed ||
		incremental.Pending != exact.Pending || incremental.Failed != exact.Failed ||
		incremental.Restricted != exact.Restricted || incremental.Empty != exact.Empty ||
		incremental.Observed != exact.Observed || incremental.Indexed != exact.Indexed {
		t.Fatalf("incremental progress = %+v, exact = %+v", incremental, exact)
	}
}

func storedGeneration(t *testing.T, db *sql.DB, sourceID int64) int64 {
	t.Helper()
	var generation int64
	if err := db.QueryRow(`SELECT workflow_run_id FROM source_catalog_scan WHERE file_source_id=?`, sourceID).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	return generation
}

func TestSourceCatalogCountersMatchExactAcrossScanStates(t *testing.T) {
	tree := map[string][]map[string]any{
		"/":          {dirItem("Alpha"), dirItem("Beta"), dirItem("Missing"), dirItem("Empty"), dirItem("Locked"), dirItem("Broken")},
		"/Alpha":     {audioItem("a.mp3"), audioItem("b.flac"), dirItem("Sub")},
		"/Alpha/Sub": {audioItem("s.mp3")},
		"/Beta":      {audioItem("c.mp3")},
		"/Empty":     {},
		"/Missing":   nil,
		"/Locked":    nil,
		"/Broken":    nil,
	}
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&input)
		if input.Path == "/Missing" {
			http.NotFound(w, r)
			return
		}
		if input.Path == "/Locked" {
			http.Error(w, "restricted", http.StatusForbidden)
			return
		}
		if input.Path == "/Broken" {
			http.Error(w, "unavailable", http.StatusInternalServerError)
			return
		}
		items := tree[input.Path]
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": len(items)}})
	}))
	sourceID, err := strconv.ParseInt(path.Base(f.base), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	f.request(t, http.MethodPost, "/catalog/index", `{"pageSize":10,"pagesPerBatch":1,"intervalMs":100}`, 202)
	assertCatalogCounterProjection(t, f.server, sourceID)
	var settled catalogIndexProgress
	for n := 0; n < 80; n++ {
		if err := f.server.runNextQueuedWorkflowJob(context.Background()); err != nil {
			t.Fatal(err)
		}
		assertCatalogCounterProjection(t, f.server, sourceID)
		p, err := f.server.sourceCatalogProgress(context.Background(), sourceID, 1)
		if err != nil {
			t.Fatal(err)
		}
		if p.Status != "queued" && p.Status != "running" {
			settled = p
			break
		}
		if n == 79 {
			t.Fatalf("scan did not settle: %+v", p)
		}
	}
	// The tree produces one of each state, so a projection that collapses two
	// classes would be caught here even if the totals happened to match.
	if settled.Completed != 5 || settled.Failed != 1 || settled.Restricted != 1 || settled.Empty != 1 || settled.Pending != 0 {
		t.Fatalf("settled classes = completed %d failed %d restricted %d empty %d pending %d, want 5/1/1/1/0",
			settled.Completed, settled.Failed, settled.Restricted, settled.Empty, settled.Pending)
	}
	// A resume rewrites directory statuses and revisions; the projection must
	// stay equal to the aggregate right after the enqueue transaction.
	f.request(t, http.MethodPost, "/catalog/index", `{}`, 202)
	assertCatalogCounterProjection(t, f.server, sourceID)
	for n := 0; n < 80; n++ {
		if err := f.server.runNextQueuedWorkflowJob(context.Background()); err != nil {
			t.Fatal(err)
		}
		assertCatalogCounterProjection(t, f.server, sourceID)
		p, err := f.server.sourceCatalogProgress(context.Background(), sourceID, 1)
		if err != nil {
			t.Fatal(err)
		}
		if p.Status != "queued" && p.Status != "running" {
			break
		}
	}
	// A forced run starts a new generation; the seed after the root enqueue
	// must describe that generation, not the previous one.
	f.request(t, http.MethodPost, "/catalog/index", `{"force":true}`, 202)
	assertCatalogCounterProjection(t, f.server, sourceID)
	page := reviewRequest(t, f.handler, f.cookie, http.MethodGet, f.base+"/catalog/index?exact=1", "", 200)
	plain := f.request(t, http.MethodGet, "/catalog/index", "", 200)
	for _, key := range []string{"directories", "completedDirectories", "pendingDirectories", "failedDirectories", "restrictedDirectories", "unconfirmedEmptyDirectories", "observedEntries", "indexedEntries"} {
		if page[key] != plain[key] {
			t.Fatalf("exact %s=%v, incremental=%v", key, page[key], plain[key])
		}
	}
}

func dirItem(name string) map[string]any   { return map[string]any{"name": name, "is_dir": true} }
func audioItem(name string) map[string]any { return map[string]any{"name": name, "size": 100} }

// seedCursorCatalog creates a complete three-directory catalog with 30 pending
// rows per directory. The batch size (50) therefore splits inside the second
// directory, which is the case a per-directory cursor would get wrong.
func seedCursorCatalog(t *testing.T, db *sql.DB, sourceID int64) (runID, jobID int64) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO file_source(id,code,display_name,source_type,enabled,config_json) VALUES(?,?,?,?,1,'{}')`, sourceID, "counter-source", "Counter source", "openlist"); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	definitionID, err := workflow.EnsureDefinition(context.Background(), tx, sourceCatalogIndexWorkerType, "Index source catalog", "test", map[string]any{"nodes": []map[string]string{{"id": "scan", "type": sourceCatalogIndexWorkerType, "displayName": "Index source catalog"}}})
	if err != nil {
		t.Fatal(err)
	}
	runID, err = workflow.InsertRun(context.Background(), tx, definitionID, sourceCatalogIndexWorkerType, "Index source catalog", "running", "manual", "manual", map[string]any{"sourceId": sourceID}, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	nodeID, err := workflow.InsertNodeRun(context.Background(), tx, runID, workflow.NodeRunSpec{NodeID: "scan", NodeType: sourceCatalogIndexWorkerType, DisplayName: "Index source catalog", Position: 1, Status: "running", Input: map[string]any{"sourceId": sourceID}})
	if err != nil {
		t.Fatal(err)
	}
	jobID, err = workflow.InsertJob(context.Background(), tx, runID, workflow.JobSpec{NodeRunID: nodeID, WorkerType: sourceCatalogIndexWorkerType, Status: "running", Priority: workflow.JobPriorityBackground, ResourceKey: "remote:catalog", Payload: catalogIndexInput{sourceID}, Checkpoint: map[string]any{"phase": "queued"}, Recoverable: true, MaxRetries: 3})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`UPDATE workflow_job SET locked_by='counter-test',locked_at=CURRENT_TIMESTAMP,heartbeat_at=CURRENT_TIMESTAMP WHERE id=?`, jobID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO source_catalog_scan(file_source_id,workflow_run_id,options_json) VALUES(?,?,?)`, sourceID, runID, mustJSON(defaultCatalogIndexOptions())); err != nil {
		t.Fatal(err)
	}
	dirInsert, err := tx.Prepare(`INSERT INTO source_catalog_directory(file_source_id,path,generation,revision,partition_path,catalog_root,status) VALUES(?,?,?,1,'/Example',?,'complete')`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dirInsert.Close() }()
	obsInsert, err := tx.Prepare(`INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes,content_hash,modified_at,published_generation,publish_pending) VALUES(?,?,?,1,'audio',100,'',CURRENT_TIMESTAMP,0,1)`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = obsInsert.Close() }()
	rootInsert, err := tx.Prepare(`INSERT INTO source_catalog_audio_root(file_source_id,generation,catalog_root) VALUES(?,?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rootInsert.Close() }()
	for part := 0; part < 3; part++ {
		dir := path.Join("/Example", "Part "+strconv.Itoa(part))
		if _, err = dirInsert.Exec(sourceID, dir, runID, dir); err != nil {
			t.Fatal(err)
		}
		if _, err = rootInsert.Exec(sourceID, runID, dir); err != nil {
			t.Fatal(err)
		}
		for n := 0; n < 30; n++ {
			if _, err = obsInsert.Exec(sourceID, dir, "track-"+strconv.Itoa(n)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = seedCatalogCountersTx(context.Background(), tx, sourceID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return runID, jobID
}

func catalogJobRecord(db *sql.DB, runID, jobID int64) workflowJobRecord {
	return workflowJobRecord{ID: jobID, RunID: runID, WorkerType: sourceCatalogIndexWorkerType, LockedBy: "counter-test"}
}

func TestSourceCatalogPublishIdleWaitsForTheLastRow(t *testing.T) {
	db := openMigratedTestDB(t)
	const sourceID = int64(42)
	runID, jobID := seedCursorCatalog(t, db, sourceID)
	// A format card from an older generation that the current owner covers.
	// It is retired only by the final-batch pass, so its state exposes whether
	// publishIdle was decided too early.
	staleID := int64(900)
	if _, err := db.Exec(`INSERT INTO source_catalog_directory(file_source_id,path,generation,revision,partition_path,catalog_root,status)
		VALUES(?,?,?,1,'/Example','/Example/Part 0','complete')`, sourceID, "/Example/Part 0/MP3", runID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO source_catalog_entry(id,file_source_id,root_path,source_root,title,last_seen_generation,discovered_generation)
		VALUES(?,?,'/Example/Part 0','','Part 0',?,1)`, 901, sourceID, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes,observed_generation)
		VALUES(901,'MP3/track.mp3','track','audio',100,?)`, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO source_catalog_entry(id,file_source_id,root_path,source_root,title,last_seen_generation,discovered_generation)
		VALUES(?,?,'/Example/Part 0/MP3','','MP3',?,1)`, staleID, sourceID, runID-1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes,observed_generation)
		VALUES(?,'track.mp3','track','audio',100,?)`, staleID, runID-1); err != nil {
		t.Fatal(err)
	}
	server := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	defer func() { _ = server.Shutdown(context.Background()) }()
	job := catalogJobRecord(db, runID, jobID)
	if _, err := server.publishCatalogBatch(context.Background(), job, sourceID); err != nil {
		t.Fatal(err)
	}
	var fileSource sql.NullInt64
	if err := db.QueryRow(`SELECT file_source_id FROM source_catalog_entry WHERE id=?`, staleID).Scan(&fileSource); err != nil {
		t.Fatal(err)
	}
	if !fileSource.Valid {
		t.Fatal("the stale format card was retired before the publish queue drained")
	}
	if remaining := readCatalogStoredCounters(t, db, sourceID).publishPending; remaining == 0 {
		t.Fatal("first batch reported an empty queue")
	}
	if _, err := server.publishCatalogBatch(context.Background(), job, sourceID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT file_source_id FROM source_catalog_entry WHERE id=?`, staleID).Scan(&fileSource); err != nil {
		t.Fatal(err)
	}
	if fileSource.Valid {
		t.Fatal("the final batch did not retire the stale format card")
	}
	if remaining := readCatalogStoredCounters(t, db, sourceID).publishPending; remaining != 0 {
		t.Fatalf("final batch left %d publishable rows", remaining)
	}
}
