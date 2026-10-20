package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// An OpenList directory may return provider hashes on one scan and none on the
// next. The stored manifest is still comparable, so a rename must not degrade
// to "insufficient evidence" merely because the new listing lost its hashes.
func TestCatalogRelocationUsesLayoutWhenTheOldCardHasHashes(t *testing.T) {
	var renamed atomic.Bool
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		hash := any(map[string]string{"sha256": strings.Repeat("a", 64)})
		if renamed.Load() {
			hash = nil
		}
		items := []map[string]any{{"name": "01 track.mp3", "size": 100, "hash_info": hash}, {"name": "02 track.mp3", "size": 200, "hash_info": hash}}
		if in.Path == "/" {
			name := "Original Work"
			if renamed.Load() {
				name = "Renamed Work"
			}
			items = []map[string]any{{"name": name, "is_dir": true}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": len(items)}})
	}))
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("first scan = %#v", p)
	}
	old := f.request(t, http.MethodGet, "/works", "", 200)["works"].([]any)[0].(map[string]any)
	oldID := int64(old["catalogEntryId"].(float64))
	renamed.Store(true)
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"force":true}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("rescan = %#v", p)
	}
	var root string
	var aliases int
	if err := f.server.db.QueryRow(`SELECT root_path FROM source_catalog_entry WHERE id=? AND relocated_to_id IS NULL`, oldID).Scan(&root); err != nil {
		t.Fatalf("established card is not live: %v", err)
	}
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_entry WHERE relocated_to_id=?`, oldID).Scan(&aliases); err != nil || aliases != 1 {
		t.Fatalf("renamed location aliases=%d: %v", aliases, err)
	}
	if root != "/Renamed Work" {
		t.Fatalf("established card stayed at %q", root)
	}
}

// A rename published by an interrupted generation is still the same card when
// the next generation observes it. Requiring the exact current generation
// would split the identity after any restart or cancel.
func TestCatalogRelocationAcceptsAnInterruptedGeneration(t *testing.T) {
	var renamed atomic.Bool
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		items := []map[string]any{{"name": "01 track.mp3", "size": 100}, {"name": "02 track.mp3", "size": 200}}
		if in.Path == "/" {
			name := "Original Work"
			if renamed.Load() {
				name = "Renamed Work"
			}
			items = []map[string]any{{"name": name, "is_dir": true}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": len(items)}})
	}))
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("first scan = %#v", p)
	}
	var oldID int64
	if err := f.server.db.QueryRow(`SELECT id FROM source_catalog_entry WHERE root_path='/Original Work' AND relocated_to_id IS NULL`).Scan(&oldID); err != nil {
		t.Fatal(err)
	}
	renamed.Store(true)
	second := f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"pagesPerBatch":1,"force":true}`, 202)
	runID := int64(second["runId"].(float64))
	for range 20 {
		if err := f.server.runNextQueuedWorkflowJob(context.Background()); err != nil {
			t.Fatal(err)
		}
		var published int
		if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_entry WHERE root_path='/Renamed Work'`).Scan(&published); err != nil {
			t.Fatal(err)
		}
		if published > 0 {
			break
		}
	}
	var published int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_entry WHERE root_path='/Renamed Work'`).Scan(&published); err != nil || published != 1 {
		t.Fatalf("provisional card was not published before the interruption: %d %v", published, err)
	}
	reviewRequest(t, f.handler, f.cookie, http.MethodPost, "/api/workflow-runs/"+strconv.FormatInt(runID, 10)+"/cancel", `{}`, 200)
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"force":true}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("recovery scan = %#v", p)
	}
	var live int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_entry WHERE id=? AND file_source_id IS NOT NULL AND relocated_to_id IS NULL`, oldID).Scan(&live); err != nil || live != 1 {
		t.Fatalf("established card lost its identity: %d %v", live, err)
	}
	var root string
	if err := f.server.db.QueryRow(`SELECT root_path FROM source_catalog_entry WHERE id=?`, oldID).Scan(&root); err != nil || root != "/Renamed Work" {
		t.Fatalf("established card stayed at %q: %v", root, err)
	}
}

// An older relocation could leave a membership on an alias. Compatibility must
// read that closure, otherwise the same card silently joins a second series.
func TestCatalogSeriesMembershipConflictReadsAliases(t *testing.T) {
	db := openMigratedTestDB(t)
	metadataReviewExec(t, db, `INSERT INTO file_source(id,code,display_name,source_type) VALUES(7,'example_remote_a','Example Remote A','openlist');
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,relocated_to_id) VALUES
			(1,NULL,'/Series One/Episode 1','Episode 1',2),
			(2,7,'/Series One/Episode 1','Episode 1',NULL),
			(3,7,'/Series Two/Episode 9','Episode 9',NULL);
		INSERT INTO catalog_series(id,title,source_id,source_key) VALUES(11,'Series One',7,'one'),(12,'Series Two',7,'two');
		INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence) VALUES(11,1,1,'directory'),(12,3,1,'directory');`)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	ctx := context.Background()
	compatible, err := catalogSeriesMembershipsCompatibleTx(ctx, tx, 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	if compatible {
		t.Fatal("an alias membership was ignored during relocation")
	}
	if err = mergeCatalogSeriesMembershipTx(ctx, tx, 3, 2); !errors.Is(err, errCatalogSeriesRetireConflict) {
		t.Fatalf("merge error=%v, want a review conflict", err)
	}
}

// A card above the fingerprint row limit can never produce a manifest. It must
// not keep the reconcile pre-pass selecting the same row forever.
func TestCatalogLifecycleFingerprintLimitSettles(t *testing.T) {
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		items := []map[string]any{{"name": "track.mp3", "size": 100}}
		if in.Path == "/" {
			items = []map[string]any{{"name": "Example Work", "is_dir": true}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": len(items)}})
	}))
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	p := f.drain(t)
	runID := int64(p["runId"].(float64))
	var entryID, jobID, nodeID int64
	if err := f.server.db.QueryRow(`SELECT id FROM source_catalog_entry WHERE file_source_id=1`).Scan(&entryID); err != nil {
		t.Fatal(err)
	}
	if err := f.server.db.QueryRow(`SELECT id,workflow_node_run_id FROM workflow_job WHERE workflow_run_id=? LIMIT 1`, runID).Scan(&jobID, &nodeID); err != nil {
		t.Fatal(err)
	}
	lockedBy := "synthetic-lifecycle"
	metadataReviewExec(t, f.server.db, `UPDATE workflow_job SET status='running',locked_by='synthetic-lifecycle' WHERE id=?`, jobID)
	metadataReviewExec(t, f.server.db, `UPDATE workflow_run SET status='running',finished_at=NULL WHERE id=?`, runID)
	metadataReviewExec(t, f.server.db, `UPDATE source_catalog_scan SET lifecycle_stage='reconcile',lifecycle_cursor=0 WHERE file_source_id=1`)
	metadataReviewExec(t, f.server.db, `DELETE FROM source_catalog_file WHERE catalog_entry_id=?`, entryID)
	metadataReviewExec(t, f.server.db, `WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<10001)
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes,observed_generation)
		SELECT ?,printf('t%05d.mp3',x),printf('t%05d',x),'audio',100,? FROM n`, entryID, runID)
	metadataReviewExec(t, f.server.db, `UPDATE source_catalog_entry SET content_fingerprint='',layout_fingerprint='',last_seen_generation=? WHERE id=?`, runID, entryID)
	job := workflowJobRecord{ID: jobID, RunID: runID, NodeRunID: nodeID, LockedBy: lockedBy}
	pending := true
	for range 3 {
		var err error
		pending, err = f.server.settleCatalogLifecycleBatch(context.Background(), job, 1)
		if err != nil {
			t.Fatal(err)
		}
		if !pending {
			break
		}
	}
	if pending {
		t.Fatal("an over-limit card kept the reconcile pass pending forever")
	}
}
