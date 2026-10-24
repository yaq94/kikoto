package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func shanghaiTestTime(year int, month time.Month, day, hour, minute int) time.Time {
	return time.Date(year, month, day, hour, minute, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
}

func readCatalogDue(t *testing.T, f catalogIndexFixture, prefix string) int64 {
	t.Helper()
	value := readCatalogSetting(t, f, prefix+"1")
	due, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		t.Fatalf("parse %s: %v", prefix, err)
	}
	return due
}

// The coordinator stores wall-clock boundaries, not completion time plus a
// rolling interval. A weekday boundary checks 03:00 Asia/Shanghai exactly, a
// multi-day stop catches up once, and an immediate second poll cannot repeat
// the dispatch.
func TestSourceCatalogDailyWallClockBoundaryAndMissedCatchUp(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/", catalogSyncTestEntry{Name: "Partition A", IsDir: true, Modified: "p1"})
	tree.set("/Partition A", catalogSyncTestEntry{Name: "Work 001", IsDir: true, Modified: "w1"})
	tree.set("/Partition A/Work 001", catalogSyncTestEntry{Name: "01.mp3", Size: 100, Modified: "f1"})
	f := newCatalogSyncFixture(t, tree)
	now := shanghaiTestTime(2030, time.January, 1, 10, 0)
	f.server.catalogNow = func() time.Time { return now }
	if p := f.runFullScan(t, ""); p["status"] != "complete" {
		t.Fatalf("full scan did not complete: %#v", p)
	}

	nextDaily := shanghaiTestTime(2030, time.January, 2, 3, 0).UTC().Unix()
	nextFull := shanghaiTestTime(2030, time.January, 6, 3, 0).UTC().Unix()
	if got := readCatalogDue(t, f, catalogDailySettingPrefix); got != nextDaily {
		t.Fatalf("next daily due = %d (%s), want %d (%s)", got, time.Unix(got, 0).In(shanghaiTestTime(2030, 1, 1, 0, 0).Location()).Format(time.RFC3339), nextDaily, time.Unix(nextDaily, 0).In(shanghaiTestTime(2030, 1, 1, 0, 0).Location()).Format(time.RFC3339))
	}
	if got := readCatalogDue(t, f, catalogFullSettingPrefix); got != nextFull {
		t.Fatalf("next full due = %d, want %d", got, nextFull)
	}
	if p := f.request(t, http.MethodGet, "/catalog/index", "", 200); p["nextUpdateAt"] != time.Unix(nextDaily, 0).In(catalogScheduleLocation).Format(time.RFC3339) ||
		p["nextFullAt"] != time.Unix(nextFull, 0).In(catalogScheduleLocation).Format(time.RFC3339) {
		t.Fatalf("progress did not expose wall-clock boundaries: %#v", p)
	}

	// One second before the wall-clock boundary does nothing.
	now = shanghaiTestTime(2030, time.January, 2, 2, 59)
	f.server.catalogNow = func() time.Time { return now }
	if err := f.server.dispatchDueCatalogUpdates(context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true)); err != nil {
		t.Fatal(err)
	}
	var syncRuns int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_sync_run WHERE file_source_id=?`, f.sourceID(t)).Scan(&syncRuns); err != nil {
		t.Fatal(err)
	}
	if syncRuns != 0 {
		t.Fatalf("sync ran before 03:00: %d", syncRuns)
	}

	// The exact boundary dispatches one incremental sync.
	now = shanghaiTestTime(2030, time.January, 2, 3, 0)
	f.server.catalogNow = func() time.Time { return now }
	if err := f.server.dispatchDueCatalogUpdates(context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true)); err != nil {
		t.Fatal(err)
	}
	waitCatalogSyncSettled(t, f)
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_sync_run WHERE file_source_id=?`, f.sourceID(t)).Scan(&syncRuns); err != nil {
		t.Fatal(err)
	}
	if syncRuns != 1 {
		t.Fatalf("boundary produced %d sync runs, want 1", syncRuns)
	}
	firstSync := syncRuns
	if err := f.server.dispatchDueCatalogUpdates(context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true)); err != nil {
		t.Fatal(err)
	}
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_sync_run WHERE file_source_id=?`, f.sourceID(t)).Scan(&syncRuns); err != nil {
		t.Fatal(err)
	}
	if syncRuns != firstSync {
		t.Fatalf("immediate second poll repeated the trigger: %d sync runs", syncRuns)
	}

	// A multi-day stop dispatches at most one catch-up, then waits for the next
	// local 03:00 instead of replaying each missed day.
	now = shanghaiTestTime(2030, time.January, 5, 10, 0)
	f.server.catalogNow = func() time.Time { return now }
	if err := f.server.dispatchDueCatalogUpdates(context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true)); err != nil {
		t.Fatal(err)
	}
	waitCatalogSyncSettled(t, f)
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_sync_run WHERE file_source_id=?`, f.sourceID(t)).Scan(&syncRuns); err != nil {
		t.Fatal(err)
	}
	if syncRuns != 2 {
		t.Fatalf("missed boundary produced %d total sync runs, want 2", syncRuns)
	}
	if got, want := readCatalogDue(t, f, catalogDailySettingPrefix), shanghaiTestTime(2030, time.January, 6, 3, 0).UTC().Unix(); got != want {
		t.Fatalf("next daily due after catch-up = %d, want %d", got, want)
	}
	if err := f.server.dispatchDueCatalogUpdates(context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true)); err != nil {
		t.Fatal(err)
	}
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_sync_run WHERE file_source_id=?`, f.sourceID(t)).Scan(&syncRuns); err != nil {
		t.Fatal(err)
	}
	if syncRuns != 2 {
		t.Fatalf("catch-up replayed more than one missed boundary: %d sync runs", syncRuns)
	}
}

// Sunday 03:00 starts a new full generation rather than another incremental
// sync. Weekday 03:00 in the same week stays incremental.
func TestSourceCatalogDailySundayRunsFullReconciliation(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/", catalogSyncTestEntry{Name: "Partition A", IsDir: true, Modified: "p1"})
	tree.set("/Partition A", catalogSyncTestEntry{Name: "Work 001", IsDir: true, Modified: "w1"})
	tree.set("/Partition A/Work 001", catalogSyncTestEntry{Name: "01.mp3", Size: 100, Modified: "f1"})
	f := newCatalogSyncFixture(t, tree)
	now := shanghaiTestTime(2030, time.January, 1, 10, 0)
	f.server.catalogNow = func() time.Time { return now }
	if p := f.runFullScan(t, ""); p["status"] != "complete" {
		t.Fatalf("full scan did not complete: %#v", p)
	}
	var firstGeneration int64
	if err := f.server.db.QueryRow(`SELECT workflow_run_id FROM source_catalog_scan WHERE file_source_id=?`, f.sourceID(t)).Scan(&firstGeneration); err != nil {
		t.Fatal(err)
	}

	// Tuesday 03:00 is an incremental update.
	now = shanghaiTestTime(2030, time.January, 2, 3, 0)
	f.server.catalogNow = func() time.Time { return now }
	if err := f.server.dispatchDueCatalogUpdates(context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true)); err != nil {
		t.Fatal(err)
	}
	waitCatalogSyncSettled(t, f)
	var generation int64
	if err := f.server.db.QueryRow(`SELECT workflow_run_id FROM source_catalog_scan WHERE file_source_id=?`, f.sourceID(t)).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if generation != firstGeneration {
		t.Fatalf("weekday update replaced generation %d with %d", firstGeneration, generation)
	}

	// Sunday 03:00 is the weekly full reconciliation.
	now = shanghaiTestTime(2030, time.January, 6, 3, 0)
	f.server.catalogNow = func() time.Time { return now }
	if err := f.server.dispatchDueCatalogUpdates(context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true)); err != nil {
		t.Fatal(err)
	}
	var jobs int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM workflow_job WHERE worker_type=? AND status='queued'`, sourceCatalogIndexWorkerType).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 {
		t.Fatalf("Sunday dispatch queued %d full jobs, want 1", jobs)
	}
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("Sunday full reconciliation did not complete: %#v", p)
	}
	if err := f.server.db.QueryRow(`SELECT workflow_run_id FROM source_catalog_scan WHERE file_source_id=?`, f.sourceID(t)).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if generation == firstGeneration {
		t.Fatal("Sunday dispatch reused the incremental generation")
	}
	if got, want := readCatalogDue(t, f, catalogFullSettingPrefix), shanghaiTestTime(2030, time.January, 13, 3, 0).UTC().Unix(); got != want {
		t.Fatalf("next full due = %d, want %d", got, want)
	}
	if got, want := readCatalogDue(t, f, catalogDailySettingPrefix), shanghaiTestTime(2030, time.January, 7, 3, 0).UTC().Unix(); got != want {
		t.Fatalf("next daily due = %d, want %d", got, want)
	}
}

// A terminal partial generation keeps its checkpoints, but the daily update is
// still a directory-first sync: it retries the failed branch and independently
// discovers changes in the branches that completed.
func TestSourceCatalogDailyPartialGenerationRunsIncrementalDiscovery(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/",
		catalogSyncTestEntry{Name: "Partition A", IsDir: true, Modified: "p1"},
		catalogSyncTestEntry{Name: "Partition B", IsDir: true, Modified: "q1"})
	tree.set("/Partition A", catalogSyncTestEntry{Name: "Work A", IsDir: true, Modified: "wa1"})
	tree.set("/Partition A/Work A", catalogSyncTestEntry{Name: "a1.mp3", Size: 100, Modified: "fa1"})
	tree.set("/Partition B", catalogSyncTestEntry{Name: "Work B", IsDir: true, Modified: "wb1"})
	tree.set("/Partition B/Work B", catalogSyncTestEntry{Name: "b1.mp3", Size: 100, Modified: "fb1"})
	tree.mu.Lock()
	tree.listFail["/Partition B"] = true
	tree.mu.Unlock()
	f := newCatalogSyncFixture(t, tree)
	now := shanghaiTestTime(2030, time.January, 1, 10, 0)
	f.server.catalogNow = func() time.Time { return now }
	p := f.runFullScan(t, "")
	if p["status"] != "partial" || p["pendingDirectories"] != float64(0) {
		t.Fatalf("initial partial scan = %#v", p)
	}
	var generation int64
	if err := f.server.db.QueryRow(`SELECT workflow_run_id FROM source_catalog_scan WHERE file_source_id=?`, f.sourceID(t)).Scan(&generation); err != nil {
		t.Fatal(err)
	}

	// A successful branch changes while the other branch is still failing.
	tree.addFile("/Partition A/Work A", "a2.mp3", 200, "fa2")
	tree.setModified("/Partition A", "Work A", "wa2")
	now = shanghaiTestTime(2030, time.January, 2, 3, 0)
	f.server.catalogNow = func() time.Time { return now }
	if err := f.server.dispatchDueCatalogUpdates(context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true)); err != nil {
		t.Fatal(err)
	}
	p = waitCatalogSyncSettled(t, f)
	if p["runId"] != float64(generation) {
		t.Fatalf("partial daily update replaced generation: %#v", p)
	}
	sync := p["lastSync"].(map[string]any)
	if sync["status"] != "partial" {
		t.Fatalf("sync with a still-failed branch = %#v", sync)
	}
	if sync["entriesPublished"] != float64(1) {
		t.Fatalf("sync published %v entries from the healthy branch, want 1: %#v", sync["entriesPublished"], sync)
	}
	if list := f.request(t, http.MethodGet, "/works?q=a2", "", 200); len(list["works"].([]any)) != 1 {
		t.Fatalf("new file in the completed branch is not searchable: %#v", list)
	}
	// The failed branch was retried, and its absence is not treated as a
	// deletion: no card or file may be marked missing.
	var missing int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_entry WHERE file_source_id=? AND availability_status='missing'`, f.sourceID(t)).Scan(&missing); err != nil {
		t.Fatal(err)
	}
	if missing != 0 {
		t.Fatalf("failed branch produced %d missing cards", missing)
	}

	// Once the source recovers, the next maintenance sync retries the same
	// failed range and publishes its card without a new full generation.
	tree.mu.Lock()
	delete(tree.listFail, "/Partition B")
	tree.mu.Unlock()
	now = shanghaiTestTime(2030, time.January, 3, 3, 0)
	f.server.catalogNow = func() time.Time { return now }
	if err := f.server.dispatchDueCatalogUpdates(context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true)); err != nil {
		t.Fatal(err)
	}
	p = waitCatalogSyncSettled(t, f)
	sync = p["lastSync"].(map[string]any)
	if sync["status"] != "succeeded" {
		t.Fatalf("recovered maintenance sync = %#v", sync)
	}
	if list := f.request(t, http.MethodGet, "/works?q=Work%20B", "", 200); len(list["works"].([]any)) != 1 {
		t.Fatalf("recovered branch is not searchable: %#v", list)
	}
	var after int64
	if err := f.server.db.QueryRow(`SELECT workflow_run_id FROM source_catalog_scan WHERE file_source_id=?`, f.sourceID(t)).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != generation {
		t.Fatalf("maintenance sync replaced generation: before=%d after=%d", generation, after)
	}
}

// A partial scan that stopped before the end of the tree still retries its
// pending range through the same directory-first sync, and that sync also
// probes the completed branches. A full resume must not freeze the successful
// branches until the pending range finishes.
func TestSourceCatalogDailyPartialWithPendingRangesStillProbesCompletedBranches(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/",
		catalogSyncTestEntry{Name: "Partition A", IsDir: true, Modified: "p1"},
		catalogSyncTestEntry{Name: "Partition B", IsDir: true, Modified: "q1"})
	tree.set("/Partition A", catalogSyncTestEntry{Name: "Work A", IsDir: true, Modified: "wa1"})
	tree.set("/Partition A/Work A", catalogSyncTestEntry{Name: "a1.mp3", Size: 100, Modified: "fa1"})
	tree.set("/Partition B", catalogSyncTestEntry{Name: "Work B", IsDir: true, Modified: "wb1"})
	tree.set("/Partition B/Work B", catalogSyncTestEntry{Name: "b1.mp3", Size: 100, Modified: "fb1"})
	tree.mu.Lock()
	tree.listFail["/Partition B"] = true
	tree.mu.Unlock()
	f := newCatalogSyncFixture(t, tree)
	now := shanghaiTestTime(2030, time.January, 1, 10, 0)
	f.server.catalogNow = func() time.Time { return now }
	p := f.runFullScan(t, "")
	if p["status"] != "partial" {
		t.Fatalf("initial scan = %#v", p)
	}
	var generation int64
	if err := f.server.db.QueryRow(`SELECT workflow_run_id FROM source_catalog_scan WHERE file_source_id=?`, f.sourceID(t)).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	// Model a scan that stopped before reaching Partition B: that branch stays
	// pending while Partition A completed.
	if _, err := f.server.db.Exec(`UPDATE source_catalog_directory
		SET status='pending',error_code='',sync_state=''
		WHERE file_source_id=? AND path='/Partition B'`, f.sourceID(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.db.Exec(`UPDATE source_catalog_scan SET pending=1,failed=0 WHERE file_source_id=?`, f.sourceID(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.db.Exec(`UPDATE workflow_run SET status='partial',finished_at=CURRENT_TIMESTAMP WHERE id=?`, generation); err != nil {
		t.Fatal(err)
	}
	tree.addFile("/Partition A/Work A", "a2.mp3", 200, "fa2")
	tree.setModified("/Partition A", "Work A", "wa2")
	tree.mu.Lock()
	delete(tree.listFail, "/Partition B")
	tree.mu.Unlock()

	now = shanghaiTestTime(2030, time.January, 2, 3, 0)
	f.server.catalogNow = func() time.Time { return now }
	if err := f.server.dispatchDueCatalogUpdates(context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true)); err != nil {
		t.Fatal(err)
	}
	p = waitCatalogSyncSettled(t, f)
	if p["runId"] != float64(generation) {
		t.Fatalf("pending partial dispatch replaced generation: %#v", p)
	}
	sync := p["lastSync"].(map[string]any)
	if sync["status"] != "succeeded" {
		t.Fatalf("combined maintenance sync = %#v", sync)
	}
	if sync["entriesPublished"] != float64(2) {
		t.Fatalf("combined sync published %v entries, want the changed branch and pending branch: %#v", sync["entriesPublished"], sync)
	}
	if list := f.request(t, http.MethodGet, "/works?q=a2", "", 200); len(list["works"].([]any)) != 1 {
		t.Fatalf("completed branch did not discover the new file: %#v", list)
	}
	if list := f.request(t, http.MethodGet, "/works?q=Work%20B", "", 200); len(list["works"].([]any)) != 1 {
		t.Fatalf("pending branch was not completed by the same sync: %#v", list)
	}
	var indexJobs int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM workflow_job WHERE worker_type=?`, sourceCatalogIndexWorkerType).Scan(&indexJobs); err != nil {
		t.Fatal(err)
	}
	if indexJobs != 1 {
		t.Fatalf("pending partial dispatched %d full index jobs, want the original one", indexJobs)
	}
	var fullJobStatus string
	if err := f.server.db.QueryRow(`SELECT status FROM workflow_job WHERE workflow_run_id=? AND worker_type=?`, generation, sourceCatalogIndexWorkerType).Scan(&fullJobStatus); err != nil {
		t.Fatal(err)
	}
	if fullJobStatus == "queued" || fullJobStatus == "running" {
		t.Fatalf("partial daily dispatch also resumed the full index job: %q", fullJobStatus)
	}
}

// A successful dirs response proves which direct child folders exist only when
// the same directory's file listing is complete. If the listing fails, a folder
// missing from dirs must keep its card and files until a later complete
// listing confirms the absence.
func TestCatalogSyncParentListingFailureKeepsChildrenUntilListingSucceeds(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/", catalogSyncTestEntry{Name: "Partition", IsDir: true, Modified: "p1"})
	tree.set("/Partition",
		catalogSyncTestEntry{Name: "keep.txt", Size: 10, Modified: "k1"},
		catalogSyncTestEntry{Name: "Work", IsDir: true, Modified: "w1"})
	tree.set("/Partition/Work", catalogSyncTestEntry{Name: "track.mp3", Size: 100, Modified: "f1"})
	f := newCatalogSyncFixture(t, tree)
	if p := f.runFullScan(t, ""); p["status"] != "complete" {
		t.Fatalf("full scan did not complete: %#v", p)
	}
	var cardID int64
	if err := f.server.db.QueryRow(`SELECT id FROM source_catalog_entry WHERE file_source_id=?`, f.sourceID(t)).Scan(&cardID); err != nil {
		t.Fatal(err)
	}
	reviewRequest(t, f.handler, f.cookie, http.MethodPatch, "/api/source-catalog/"+strconv.FormatInt(cardID, 10)+"/user-state", `{"rating":4,"note":"Example retained"}`, 200)

	// The child disappears from the tree, but the parent's own listing fails.
	tree.set("/Partition", catalogSyncTestEntry{Name: "keep.txt", Size: 10, Modified: "k1"})
	tree.setModified("/", "Partition", "p2")
	tree.mu.Lock()
	tree.listFail["/Partition"] = true
	tree.mu.Unlock()
	state := f.runCatalogSync(t, false)
	if state.Status != "partial" {
		t.Fatalf("failed parent listing sync = %q/%q", state.Status, state.Note)
	}
	var availability string
	if err := f.server.db.QueryRow(`SELECT availability_status FROM source_catalog_entry WHERE id=?`, cardID).Scan(&availability); err != nil {
		t.Fatal(err)
	}
	if availability == "missing" {
		t.Fatal("unresolved parent listing retired the child card")
	}
	var audio int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_file WHERE catalog_entry_id=? AND kind='audio'`, cardID).Scan(&audio); err != nil {
		t.Fatal(err)
	}
	if audio != 1 {
		t.Fatalf("unresolved parent listing removed %d audio rows, want 1", 1-audio)
	}
	assertReview(t, reviewRequest(t, f.handler, f.cookie, http.MethodGet, "/api/source-catalog/"+strconv.FormatInt(cardID, 10), "", 200), float64(4), "Example retained")

	// A later complete listing may retire the absent child.
	tree.mu.Lock()
	delete(tree.listFail, "/Partition")
	tree.mu.Unlock()
	state = f.runCatalogSync(t, false)
	if state.Status != "succeeded" {
		t.Fatalf("recovered parent listing sync = %q/%q", state.Status, state.Note)
	}
	if err := f.server.db.QueryRow(`SELECT availability_status FROM source_catalog_entry WHERE id=?`, cardID).Scan(&availability); err != nil {
		t.Fatal(err)
	}
	if availability != "missing" {
		t.Fatalf("complete listing did not retire the absent child: %q", availability)
	}
	assertReview(t, reviewRequest(t, f.handler, f.cookie, http.MethodGet, "/api/source-catalog/"+strconv.FormatInt(cardID, 10), "", 200), float64(4), "Example retained")
}
