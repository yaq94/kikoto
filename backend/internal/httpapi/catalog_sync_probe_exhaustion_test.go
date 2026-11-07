package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
)

func TestCatalogSyncProbeExhaustionCheckpointCarriesAcrossLeases(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/", catalogSyncTestEntry{Name: "Example Work", IsDir: true, Modified: "v1"})
	tree.set("/Example Work", catalogSyncTestEntry{Name: "track.mp3", Size: 1, Modified: "v1"})
	f := newCatalogSyncFixture(t, tree)
	f.runFullScan(t, "")
	run := f.runCatalogSync(t, false)
	var generation int64
	if err := f.server.db.QueryRow(`SELECT workflow_run_id FROM source_catalog_scan WHERE file_source_id=?`, f.sourceID(t)).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	metadataReviewExec(t, f.server.db, `UPDATE workflow_job SET checkpoint_json=json_set(checkpoint_json,'$.detail.otherCursor',17)
		WHERE worker_type='source_catalog_sync' AND json_extract(payload_json,'$.syncRunId')=?`, run.ID)
	lease, err := f.server.beginCatalogSyncLease(context.Background(), run.ID, generation)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.release("cancelled")
	var raw string
	if err = f.server.db.QueryRow(`SELECT checkpoint_json FROM workflow_job WHERE id=?`, lease.Job.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var checkpoint struct {
		Phase     string `json:"phase"`
		Exhausted int    `json:"probeExhausted"`
		Detail    struct {
			Cursor int `json:"otherCursor"`
		} `json:"detail"`
	}
	if err = json.Unmarshal([]byte(raw), &checkpoint); err != nil {
		t.Fatal(err)
	}
	if checkpoint.Exhausted != 1 || checkpoint.Detail.Cursor != 17 || checkpoint.Phase != "sync" {
		t.Fatalf("resumed checkpoint discarded committed progress: %s", raw)
	}
	if exhausted, err := f.server.loadCatalogSyncProbeExhausted(context.Background(), run.ID, generation+1); err != nil || exhausted {
		t.Fatalf("other generation inherited exhaustion: %v %v", exhausted, err)
	}
}

// The worker must persist the exhausted probe phase before listing a long tail
// of leaves. Otherwise each 32 listings scans that same tail again.
func TestCatalogSyncProbeExhaustionSurvivesListingBatches(t *testing.T) {
	tree := newCatalogSyncTestTree()
	children := []catalogSyncTestEntry{}
	for i := 0; i < 70; i++ {
		name := fmt.Sprintf("Example Work %03d", i)
		children = append(children, catalogSyncTestEntry{Name: name, IsDir: true, Modified: "v1"})
		tree.set("/"+name, catalogSyncTestEntry{Name: "track.mp3", Size: 1, Modified: "v1"})
	}
	tree.set("/", children...)
	var armed atomic.Bool
	var listings atomic.Int64
	var missingPhase atomic.Int64
	var db *sql.DB
	handler := tree.handler(t)
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if armed.Load() && r.URL.Path == "/api/fs/list" {
			listings.Add(1)
			var exhausted int
			err := db.QueryRow(`SELECT COALESCE(json_extract(checkpoint_json,'$.probeExhausted'),0)
				FROM workflow_job WHERE worker_type='source_catalog_sync' ORDER BY id DESC LIMIT 1`).Scan(&exhausted)
			if err != nil || exhausted != 1 {
				missingPhase.Add(1)
			}
		}
		handler.ServeHTTP(w, r)
	}))
	db = f.server.db
	f.runFullScan(t, "")
	armed.Store(true)
	run := f.runCatalogSync(t, true)
	armed.Store(false)
	if missingPhase.Load() != 0 {
		t.Fatalf("%d of %d listing requests had no durable exhausted probe phase", missingPhase.Load(), listings.Load())
	}
	if listings.Load() != 71 || run.Status != "succeeded" {
		t.Fatalf("listings=%d status=%s, want complete 71-directory set", listings.Load(), run.Status)
	}
	var files int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM source_catalog_file WHERE kind='audio'`).Scan(&files); err != nil {
		t.Fatal(err)
	}
	if files != 70 {
		t.Fatalf("published audio=%d, want70", files)
	}
}

func TestCatalogSyncProbeFailureDoesNotAdvanceOrExhaust(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/", catalogSyncTestEntry{Name: "Example Work", IsDir: true})
	tree.set("/Example Work", catalogSyncTestEntry{Name: "old.mp3", Size: 1})
	var fail atomic.Bool
	handler := tree.handler(t)
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() && r.URL.Path == "/api/fs/dirs" {
			_, _ = fmt.Fprint(w, `{"code":500,"message":"synthetic unavailable probe"}`)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	f.runFullScan(t, "")
	fail.Store(true)
	run := f.runCatalogSync(t, false)
	var after string
	if err := f.server.db.QueryRow(`SELECT json_extract(checkpoint_json,'$.probeAfter') FROM workflow_job WHERE worker_type='source_catalog_sync' AND json_extract(payload_json,'$.syncRunId')=? ORDER BY id DESC LIMIT 1`, run.ID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != "" || run.Status != "partial" {
		t.Fatalf("failed probe advanced progress: after=%q status=%s", after, run.Status)
	}
	var generation int64
	if err := f.server.db.QueryRow(`SELECT workflow_run_id FROM source_catalog_scan WHERE file_source_id=?`, f.sourceID(t)).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if exhausted, err := f.server.loadCatalogSyncProbeExhausted(context.Background(), run.ID, generation); err != nil || exhausted {
		t.Fatalf("failed probe exhausted: %v %v", exhausted, err)
	}
	fail.Store(false)
	resumed := f.runCatalogSync(t, false)
	if resumed.DirsProbed == 0 || resumed.Status != "succeeded" {
		t.Fatalf("probe recovery=%+v", resumed)
	}
}
