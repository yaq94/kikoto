package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// A bounded catalog invocation settles as queued while directories remain.
// Without immediate continuation the coordinator waits one tick per batch; on
// the 965-directory calibration tree that was 61 ticks, about 122 s, more than
// every database write in the run.
func TestCatalogIndexContinuationLoopsWithoutTick(t *testing.T) {
	f := newCatalogIndexFixture(t, emptyCatalogUpstream(t))
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"pagesPerBatch":1}`, http.StatusAccepted)
	continued, err := f.server.runNextQueuedWorkflowJobStep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !continued {
		t.Fatal("a bounded catalog invocation with pending work did not ask for an immediate claim")
	}
	progress := f.request(t, http.MethodGet, "/catalog/index", "", http.StatusOK)
	if progress["status"] != "queued" {
		t.Fatalf("the bounded invocation left the run %v, want queued", progress["status"])
	}
}

// Every other worker, and a catalog run that reached a terminal state, must
// keep the existing tick so a failing or finished job does not spin.
func TestWorkflowContinuationStaysOffForOtherOutcomes(t *testing.T) {
	f := newCatalogIndexFixture(t, emptyCatalogUpstream(t))
	if f.server.workflowJobContinuesImmediately(context.Background(), workflowJobRecord{WorkerType: "metadata_sync", RunID: 1}) {
		t.Fatal("a non-index worker asked for immediate continuation")
	}
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, http.StatusAccepted)
	for range 20 {
		continued, err := f.server.runNextQueuedWorkflowJobStep(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !continued {
			return
		}
	}
	t.Fatal("a finished catalog run kept asking for immediate continuation")
}

// A retry delay or a paused source must keep the coordinator tick. Continuing
// through available_at would turn a blocked source into a claim-query spin.
func TestCatalogIndexContinuationHonorsARetryDelay(t *testing.T) {
	f := newCatalogIndexFixture(t, emptyCatalogUpstream(t))
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"pagesPerBatch":1}`, http.StatusAccepted)
	continued, err := f.server.runNextQueuedWorkflowJobStep(context.Background())
	if err != nil || !continued {
		t.Fatalf("first bounded step = %t, %v; want immediate continuation", continued, err)
	}
	if _, err = f.server.db.Exec(`UPDATE workflow_job SET available_at=datetime('now','+1 hour') WHERE worker_type=?`, sourceCatalogIndexWorkerType); err != nil {
		t.Fatal(err)
	}
	var jobID int64
	if err = f.server.db.QueryRow(`SELECT id FROM workflow_job WHERE worker_type=?`, sourceCatalogIndexWorkerType).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	if f.server.workflowJobContinuesImmediately(context.Background(), workflowJobRecord{ID: jobID, WorkerType: sourceCatalogIndexWorkerType, RunID: 1}) {
		t.Fatal("a job waiting for its retry delay asked for immediate continuation")
	}
}

func emptyCatalogUpstream(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": []map[string]any{}, "total": 0}})
	})
}
