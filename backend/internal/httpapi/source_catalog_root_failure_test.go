package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestCatalogFailedConfiguredRootSettlesWithoutSweepingPriorCoverage(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/", catalogSyncTestEntry{Name: "Example Work", IsDir: true, Modified: "w1"})
	tree.set("/Example Work", catalogSyncTestEntry{Name: "old.mp3", Size: 100, Modified: "f1"})
	f := newCatalogSyncFixture(t, tree)
	if p := f.runFullScan(t, ""); p["status"] != "complete" {
		t.Fatalf("initial index: %#v", p)
	}
	list := f.request(t, http.MethodGet, "/works?q=Example%20Work", "", http.StatusOK)
	card := list["works"].([]any)[0].(map[string]any)
	base := "/api/source-catalog/" + strconv.Itoa(int(card["catalogEntryId"].(float64)))
	reviewRequest(t, f.handler, f.cookie, http.MethodPatch, base+"/user-state", `{"rating":3,"note":"Example retained note"}`, http.StatusOK)

	db := f.server.db
	metadataReviewExec(t, db, `CREATE TRIGGER synthetic_guard_root_failure_cleanup BEFORE DELETE ON source_catalog_observation
		WHEN OLD.directory_path='/Example Work' BEGIN SELECT RAISE(ABORT,'failed root tried to retire prior coverage'); END`)
	tree.mu.Lock()
	tree.listFail["/"] = true
	tree.mu.Unlock()
	result := f.request(t, http.MethodPost, "/catalog/index", `{"force":true,"pagesPerBatch":1,"intervalMs":100}`, http.StatusAccepted)
	if p := f.drainIndex(t); p["status"] != "partial" || p["failedDirectories"] != float64(1) || p["indexedEntries"] != float64(0) {
		t.Fatalf("failed configured root did not settle as partial: %#v", p)
	}
	// The failed source keeps its playable snapshot and the review. A later
	// observation must still be able to replace it through the same generation.
	assertReview(t, reviewRequest(t, f.handler, f.cookie, http.MethodGet, base, "", http.StatusOK), float64(3), "Example retained note")
	metadataReviewExec(t, db, `DROP TRIGGER synthetic_guard_root_failure_cleanup`)
	tree.mu.Lock()
	delete(tree.listFail, "/")
	tree.mu.Unlock()
	tree.addFile("/Example Work", "new.mp3", 120, "f2")
	now := shanghaiTestTime(2030, time.January, 2, 3, 0)
	f.server.catalogNow = func() time.Time { return now }
	setCatalogScheduleTestState(t, f, catalogScheduleState{Daily: now.Unix(), Weekly: now.Add(24 * time.Hour).Unix()})
	if err := f.server.dispatchDueCatalogUpdates(context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true)); err != nil {
		t.Fatal(err)
	}
	progress := waitCatalogSyncSettled(t, f)
	last, ok := progress["lastSync"].(map[string]any)
	if !ok || last["status"] != "succeeded" || countCatalogSyncRuns(t, f) != 1 {
		t.Fatalf("daily maintenance after the failed root: %#v", last)
	}
	if generation, err := f.server.catalogGenerationRunID(context.Background(), f.sourceID(t)); err != nil || float64(generation) != result["runId"] {
		t.Fatalf("daily maintenance replaced generation: %d, %v", generation, err)
	}
	if search := f.request(t, http.MethodGet, "/works?q=new", "", http.StatusOK); len(search["works"].([]any)) != 1 {
		t.Fatalf("daily maintenance did not publish new content: %#v", search)
	}
	assertReview(t, reviewRequest(t, f.handler, f.cookie, http.MethodGet, base, "", http.StatusOK), float64(3), "Example retained note")
}
