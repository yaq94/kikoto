package httpapi

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func shanghaiTestTime(year int, month time.Month, day, hour, minute int) time.Time {
	return time.Date(year, month, day, hour, minute, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
}

type catalogScheduleState struct {
	Daily, Weekly, Deferred   int64
	DeferredReason            string
	ManualPending, ResumeFull bool
	PendingFullRunID          sql.NullInt64
}

func readCatalogSchedule(t *testing.T, f catalogIndexFixture) catalogScheduleState {
	t.Helper()
	var state catalogScheduleState
	var manual, resume int
	if err := f.server.db.QueryRow(`SELECT daily_due_at,weekly_due_at,deferred_until,deferred_reason,manual_pending,resume_full_pending,pending_full_run_id
		FROM source_catalog_schedule WHERE file_source_id=?`, f.sourceID(t)).
		Scan(&state.Daily, &state.Weekly, &state.Deferred, &state.DeferredReason, &manual, &resume, &state.PendingFullRunID); err != nil {
		t.Fatalf("read catalog schedule: %v", err)
	}
	state.ManualPending = manual != 0
	state.ResumeFull = resume != 0
	return state
}

// setCatalogScheduleTestState writes the schedule fields a dispatcher test
// wants without the values it intentionally leaves alone. A plain UPDATE would
// keep a stale manual_pending or resume_full_pending from an earlier step and
// silently turn the next dispatch into a different branch.
func setCatalogScheduleTestState(t *testing.T, f catalogIndexFixture, state catalogScheduleState) {
	t.Helper()
	if _, err := f.server.db.Exec(`UPDATE source_catalog_schedule SET
			daily_due_at=?,weekly_due_at=?,deferred_until=?,deferred_reason='',manual_pending=?,resume_full_pending=?,pending_full_run_id=NULL,updated_at=CURRENT_TIMESTAMP
		WHERE file_source_id=?`, state.Daily, state.Weekly, state.Deferred,
		catalogSyncBoolInt(state.ManualPending), catalogSyncBoolInt(state.ResumeFull), f.sourceID(t)); err != nil {
		t.Fatalf("set catalog schedule: %v", err)
	}
}

// seedCatalogScheduleAt writes the boundaries a source would have after its
// initial full crawl. Tests create the fixture before they install their fake
// clock, so the row inserted at source creation uses real time; seeding makes
// the wall-clock expectations independent of when the test runs.
func seedCatalogScheduleAt(t *testing.T, f catalogIndexFixture, now time.Time) catalogScheduleState {
	t.Helper()
	state := catalogScheduleState{
		Daily:  nextCatalogDailyDue(now).Unix(),
		Weekly: nextCatalogWeeklyDue(now).Unix(),
	}
	setCatalogScheduleTestState(t, f, state)
	return state
}

func countCatalogSyncRuns(t *testing.T, f catalogIndexFixture) int {
	t.Helper()
	var count int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_sync_run WHERE file_source_id=?`, f.sourceID(t)).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func countQueuedFullJobs(t *testing.T, f catalogIndexFixture) int {
	t.Helper()
	var count int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM workflow_job WHERE worker_type=? AND status IN ('queued','running')`,
		sourceCatalogIndexWorkerType).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func dispatchCatalog(t *testing.T, f catalogIndexFixture) {
	t.Helper()
	if err := f.server.dispatchDueCatalogUpdates(context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true)); err != nil {
		t.Fatalf("dispatch catalog updates: %v", err)
	}
}

// The persisted boundaries are wall clock values. A weekday tick before 03:00
// does nothing, the exact boundary starts one sync, and a multi-day stop
// catches up once instead of replaying every missed day.
func TestCatalogScheduleDailyBoundaryAndMissedCatchUp(t *testing.T) {
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
	nextWeekly := shanghaiTestTime(2030, time.January, 6, 3, 0).UTC().Unix()
	setCatalogScheduleTestState(t, f, catalogScheduleState{Daily: nextDaily, Weekly: nextWeekly})
	if p := f.request(t, http.MethodGet, "/catalog/index", "", 200); p["nextUpdateAt"] != time.Unix(nextDaily, 0).In(catalogScheduleLocation).Format(time.RFC3339) ||
		p["nextFullAt"] != time.Unix(nextWeekly, 0).In(catalogScheduleLocation).Format(time.RFC3339) {
		t.Fatalf("progress did not expose wall-clock boundaries: %#v", p)
	}

	now = shanghaiTestTime(2030, time.January, 2, 2, 59)
	f.server.catalogNow = func() time.Time { return now }
	dispatchCatalog(t, f)
	if runs := countCatalogSyncRuns(t, f); runs != 0 {
		t.Fatalf("sync ran before 03:00: %d", runs)
	}

	now = shanghaiTestTime(2030, time.January, 2, 3, 0)
	f.server.catalogNow = func() time.Time { return now }
	dispatchCatalog(t, f)
	waitCatalogSyncSettled(t, f)
	if runs := countCatalogSyncRuns(t, f); runs != 1 {
		t.Fatalf("boundary produced %d sync runs, want 1", runs)
	}
	if got := readCatalogSchedule(t, f).Daily; got != shanghaiTestTime(2030, time.January, 3, 3, 0).UTC().Unix() {
		t.Fatalf("daily due after dispatch = %d, want the next local 03:00", got)
	}
	dispatchCatalog(t, f)
	if runs := countCatalogSyncRuns(t, f); runs != 1 {
		t.Fatalf("immediate second poll repeated the trigger: %d sync runs", runs)
	}

	// Three missed days produce one catch-up, not three.
	now = shanghaiTestTime(2030, time.January, 5, 10, 0)
	f.server.catalogNow = func() time.Time { return now }
	dispatchCatalog(t, f)
	waitCatalogSyncSettled(t, f)
	if runs := countCatalogSyncRuns(t, f); runs != 2 {
		t.Fatalf("missed boundary produced %d total sync runs, want 2", runs)
	}
	if got := readCatalogSchedule(t, f).Daily; got != shanghaiTestTime(2030, time.January, 6, 3, 0).UTC().Unix() {
		t.Fatalf("daily due after catch-up = %d, want the next local 03:00", got)
	}
	dispatchCatalog(t, f)
	if runs := countCatalogSyncRuns(t, f); runs != 2 {
		t.Fatalf("catch-up replayed more than one missed boundary: %d sync runs", runs)
	}
}

// Sunday starts a new full generation; a weekday boundary in the same week
// stays a directory-first sync over the existing generation.
func TestCatalogScheduleSundayRunsFullReconciliation(t *testing.T) {
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
	setCatalogScheduleTestState(t, f, catalogScheduleState{
		Daily:  shanghaiTestTime(2030, time.January, 2, 3, 0).UTC().Unix(),
		Weekly: shanghaiTestTime(2030, time.January, 6, 3, 0).UTC().Unix(),
	})

	now = shanghaiTestTime(2030, time.January, 2, 3, 0)
	f.server.catalogNow = func() time.Time { return now }
	dispatchCatalog(t, f)
	waitCatalogSyncSettled(t, f)
	var generation int64
	if err := f.server.db.QueryRow(`SELECT workflow_run_id FROM source_catalog_scan WHERE file_source_id=?`, f.sourceID(t)).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if generation != firstGeneration {
		t.Fatalf("weekday update replaced generation %d with %d", firstGeneration, generation)
	}

	now = shanghaiTestTime(2030, time.January, 6, 3, 0)
	f.server.catalogNow = func() time.Time { return now }
	// Normal daily dispatches have advanced the daily boundary to this same
	// tick; the Sunday boundary is then the earlier or equal one and wins.
	setCatalogScheduleTestState(t, f, catalogScheduleState{
		Daily:  now.Unix(),
		Weekly: now.Unix(),
	})
	dispatchCatalog(t, f)
	if jobs := countQueuedFullJobs(t, f); jobs != 1 {
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
	state := readCatalogSchedule(t, f)
	if want := shanghaiTestTime(2030, time.January, 13, 3, 0).UTC().Unix(); state.Weekly != want {
		t.Fatalf("next weekly due = %d, want %d", state.Weekly, want)
	}
	if want := shanghaiTestTime(2030, time.January, 7, 3, 0).UTC().Unix(); state.Daily != want {
		t.Fatalf("next daily due after weekly = %d, want %d", state.Daily, want)
	}
	if state.ResumeFull {
		t.Fatal("weekly dispatch left resume_full_pending set")
	}
}

// Daily and weekly boundaries that are both due on one tick produce a single
// weekly full, not a sync followed by a full.
func TestCatalogScheduleSimultaneousDueCreatesOneWeekly(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/", catalogSyncTestEntry{Name: "Partition A", IsDir: true, Modified: "p1"})
	tree.set("/Partition A", catalogSyncTestEntry{Name: "Work 001", IsDir: true, Modified: "w1"})
	tree.set("/Partition A/Work 001", catalogSyncTestEntry{Name: "01.mp3", Size: 100, Modified: "f1"})
	f := newCatalogSyncFixture(t, tree)
	now := shanghaiTestTime(2030, time.January, 6, 3, 0)
	f.server.catalogNow = func() time.Time { return now }
	if p := f.runFullScan(t, ""); p["status"] != "complete" {
		t.Fatalf("full scan did not complete: %#v", p)
	}
	setCatalogScheduleTestState(t, f, catalogScheduleState{Daily: now.Unix(), Weekly: now.Unix()})
	dispatchCatalog(t, f)
	if jobs := countQueuedFullJobs(t, f); jobs != 1 {
		t.Fatalf("simultaneous due queued %d full jobs, want 1", jobs)
	}
	if runs := countCatalogSyncRuns(t, f); runs != 0 {
		t.Fatalf("simultaneous due also started %d syncs", runs)
	}
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("weekly full did not complete: %#v", p)
	}
	state := readCatalogSchedule(t, f)
	if state.Weekly != shanghaiTestTime(2030, time.January, 13, 3, 0).UTC().Unix() ||
		state.Daily != shanghaiTestTime(2030, time.January, 7, 3, 0).UTC().Unix() {
		t.Fatalf("simultaneous due advanced to %+v", state)
	}
	dispatchCatalog(t, f)
	if jobs := countQueuedFullJobs(t, f); jobs != 0 {
		t.Fatalf("second poll repeated the weekly full: %d jobs", jobs)
	}
}

// A weekly boundary that came due while a sync held the lease leaves one
// pending bit. The next dispatch creates the full and records its run id but
// keeps the bit until that run succeeds; only then does the weekly boundary
// advance and the pending state clear.
func TestCatalogScheduleResumeFullPendingWinsAndClears(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/", catalogSyncTestEntry{Name: "Partition A", IsDir: true, Modified: "p1"})
	tree.set("/Partition A", catalogSyncTestEntry{Name: "Work 001", IsDir: true, Modified: "w1"})
	tree.set("/Partition A/Work 001", catalogSyncTestEntry{Name: "01.mp3", Size: 100, Modified: "f1"})
	f := newCatalogSyncFixture(t, tree)
	now := shanghaiTestTime(2030, time.January, 6, 10, 0)
	f.server.catalogNow = func() time.Time { return now }
	if p := f.runFullScan(t, ""); p["status"] != "complete" {
		t.Fatalf("full scan did not complete: %#v", p)
	}
	var before int64
	if err := f.server.db.QueryRow(`SELECT workflow_run_id FROM source_catalog_scan WHERE file_source_id=?`, f.sourceID(t)).Scan(&before); err != nil {
		t.Fatal(err)
	}
	// Daily is still ahead: only the pending bit may start work.
	setCatalogScheduleTestState(t, f, catalogScheduleState{
		Daily:      now.Add(24 * time.Hour).Unix(),
		Weekly:     now.Add(48 * time.Hour).Unix(),
		ResumeFull: true,
	})
	dispatchCatalog(t, f)
	if jobs := countQueuedFullJobs(t, f); jobs != 1 {
		t.Fatalf("resume_full_pending queued %d full jobs, want 1", jobs)
	}
	if runs := countCatalogSyncRuns(t, f); runs != 0 {
		t.Fatalf("resume_full_pending started %d syncs, want a weekly full", runs)
	}
	state := readCatalogSchedule(t, f)
	if !state.ResumeFull || !state.PendingFullRunID.Valid {
		t.Fatalf("weekly full creation cleared its own pending state: %+v", state)
	}
	if state.Weekly != now.Add(48*time.Hour).Unix() {
		t.Fatalf("weekly due moved before the full succeeded: %+v", state)
	}
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("resumed full did not complete: %#v", p)
	}
	dispatchCatalog(t, f)
	state = readCatalogSchedule(t, f)
	if state.ResumeFull || state.PendingFullRunID.Valid {
		t.Fatalf("succeeded full did not clear resume state: %+v", state)
	}
	if want := shanghaiTestTime(2030, time.January, 13, 3, 0).UTC().Unix(); state.Weekly != want {
		t.Fatalf("weekly due after resume = %d, want %d", state.Weekly, want)
	}
	var after int64
	if err := f.server.db.QueryRow(`SELECT workflow_run_id FROM source_catalog_scan WHERE file_source_id=?`, f.sourceID(t)).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after == before {
		t.Fatal("resume_full_pending reused the completed generation")
	}
	dispatchCatalog(t, f)
	if jobs := countQueuedFullJobs(t, f); jobs != 0 {
		t.Fatalf("full completion re-queued itself: %d jobs", jobs)
	}
}

// A resume full that fails or is cancelled must not advance the weekly clock.
// The run id is forgotten, the pending bit survives, and the next tick creates
// exactly one replacement.
func TestCatalogScheduleFailedResumeFullRetriesWithoutMovingWeekly(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/", catalogSyncTestEntry{Name: "Partition A", IsDir: true, Modified: "p1"})
	tree.set("/Partition A", catalogSyncTestEntry{Name: "Work 001", IsDir: true, Modified: "w1"})
	tree.set("/Partition A/Work 001", catalogSyncTestEntry{Name: "01.mp3", Size: 100, Modified: "f1"})
	f := newCatalogSyncFixture(t, tree)
	now := shanghaiTestTime(2030, time.January, 6, 10, 0)
	f.server.catalogNow = func() time.Time { return now }
	if p := f.runFullScan(t, ""); p["status"] != "complete" {
		t.Fatalf("full scan did not complete: %#v", p)
	}
	weekly := now.Add(48 * time.Hour).Unix()
	setCatalogScheduleTestState(t, f, catalogScheduleState{
		Daily:      now.Add(24 * time.Hour).Unix(),
		Weekly:     weekly,
		ResumeFull: true,
	})
	dispatchCatalog(t, f)
	state := readCatalogSchedule(t, f)
	if !state.PendingFullRunID.Valid {
		t.Fatal("resume full did not record its run id")
	}
	failedID := state.PendingFullRunID.Int64
	if _, err := f.server.db.Exec(`UPDATE workflow_run SET status='failed',finished_at=CURRENT_TIMESTAMP WHERE id=?`, failedID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.db.Exec(`UPDATE workflow_job SET status='failed',locked_by='',locked_at=NULL,heartbeat_at=NULL WHERE workflow_run_id=?`, failedID); err != nil {
		t.Fatal(err)
	}
	dispatchCatalog(t, f)
	state = readCatalogSchedule(t, f)
	if !state.ResumeFull || state.PendingFullRunID.Valid {
		t.Fatalf("failed resume full did not return to a retryable state: %+v", state)
	}
	if state.Weekly != weekly {
		t.Fatalf("failed resume full moved the weekly boundary: %+v", state)
	}
	if jobs := countQueuedFullJobs(t, f); jobs != 0 {
		t.Fatalf("failed run was immediately replaced in the same tick: %d jobs", jobs)
	}
	dispatchCatalog(t, f)
	state = readCatalogSchedule(t, f)
	if !state.PendingFullRunID.Valid || state.PendingFullRunID.Int64 == failedID {
		t.Fatalf("retry did not create a replacement full: %+v", state)
	}
	if jobs := countQueuedFullJobs(t, f); jobs != 1 {
		t.Fatalf("retry queued %d full jobs, want 1", jobs)
	}
}

// A manual request that arrived while work was running is consumed once and
// never moves the daily or weekly boundary.
func TestCatalogScheduleManualPendingConsumedOnce(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/", catalogSyncTestEntry{Name: "Partition A", IsDir: true, Modified: "p1"})
	tree.set("/Partition A", catalogSyncTestEntry{Name: "Work 001", IsDir: true, Modified: "w1"})
	tree.set("/Partition A/Work 001", catalogSyncTestEntry{Name: "01.mp3", Size: 100, Modified: "f1"})
	f := newCatalogSyncFixture(t, tree)
	now := shanghaiTestTime(2030, time.January, 2, 10, 0)
	f.server.catalogNow = func() time.Time { return now }
	if p := f.runFullScan(t, ""); p["status"] != "complete" {
		t.Fatalf("full scan did not complete: %#v", p)
	}
	daily := now.Add(24 * time.Hour).Unix()
	weekly := now.Add(72 * time.Hour).Unix()
	setCatalogScheduleTestState(t, f, catalogScheduleState{Daily: daily, Weekly: weekly, ManualPending: true})
	dispatchCatalog(t, f)
	waitCatalogSyncSettled(t, f)
	if runs := countCatalogSyncRuns(t, f); runs != 1 {
		t.Fatalf("manual request produced %d sync runs, want 1", runs)
	}
	state := readCatalogSchedule(t, f)
	if state.ManualPending {
		t.Fatal("manual request was not consumed")
	}
	if state.Daily != daily || state.Weekly != weekly {
		t.Fatalf("manual request moved the boundaries: %+v", state)
	}
	dispatchCatalog(t, f)
	if runs := countCatalogSyncRuns(t, f); runs != 1 {
		t.Fatalf("manual request ran %d times, want once", runs)
	}
}

// While a sync holds the writer the dispatcher must not enqueue a full. It
// records the weekly boundary as pending and leaves every due value untouched.
func TestCatalogScheduleRunningSyncDefersFullAndKeepsDue(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/", catalogSyncTestEntry{Name: "Partition A", IsDir: true, Modified: "p1"})
	tree.set("/Partition A", catalogSyncTestEntry{Name: "Work 001", IsDir: true, Modified: "w1"})
	tree.set("/Partition A/Work 001", catalogSyncTestEntry{Name: "01.mp3", Size: 100, Modified: "f1"})
	f := newCatalogSyncFixture(t, tree)
	now := shanghaiTestTime(2030, time.January, 6, 3, 0)
	f.server.catalogNow = func() time.Time { return now }
	if p := f.runFullScan(t, ""); p["status"] != "complete" {
		t.Fatalf("full scan did not complete: %#v", p)
	}
	setCatalogScheduleTestState(t, f, catalogScheduleState{Daily: now.Unix(), Weekly: now.Unix()})
	if _, err := f.server.db.Exec(`INSERT INTO source_catalog_sync_run(file_source_id,status,note) VALUES(?,'running','')`, f.sourceID(t)); err != nil {
		t.Fatal(err)
	}
	dispatchCatalog(t, f)
	state := readCatalogSchedule(t, f)
	if !state.ResumeFull {
		t.Fatal("weekly boundary during a running sync was not remembered")
	}
	if state.Daily != now.Unix() || state.Weekly != now.Unix() {
		t.Fatalf("running sync advanced the schedule: %+v", state)
	}
	if jobs := countQueuedFullJobs(t, f); jobs != 0 {
		t.Fatalf("running sync created %d overlapping full jobs", jobs)
	}
	if runs := countCatalogSyncRuns(t, f); runs != 1 {
		t.Fatalf("dispatcher created %d additional syncs, want none", runs-1)
	}
}

// A running full that crosses the weekly boundary is registered as the pending
// run. Its own success then advances the weekly boundary; a second full must not
// be queued on the next tick.
func TestCatalogScheduleRunningFullRegistersPendingRun(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/", catalogSyncTestEntry{Name: "Partition A", IsDir: true, Modified: "p1"})
	tree.set("/Partition A", catalogSyncTestEntry{Name: "Work 001", IsDir: true, Modified: "w1"})
	tree.set("/Partition A/Work 001", catalogSyncTestEntry{Name: "01.mp3", Size: 100, Modified: "f1"})
	f := newCatalogSyncFixture(t, tree)
	now := shanghaiTestTime(2030, time.January, 6, 3, 0)
	f.server.catalogNow = func() time.Time { return now }
	// Start a full and leave it queued instead of draining it.
	f.request(t, http.MethodPost, "/catalog/index", `{}`, http.StatusAccepted)
	var runID int64
	if err := f.server.db.QueryRow(`SELECT workflow_run_id FROM source_catalog_scan WHERE file_source_id=?`, f.sourceID(t)).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	setCatalogScheduleTestState(t, f, catalogScheduleState{Daily: now.Unix(), Weekly: now.Unix()})

	dispatchCatalog(t, f)
	state := readCatalogSchedule(t, f)
	if !state.ResumeFull {
		t.Fatal("weekly boundary during a running full was not remembered")
	}
	if !state.PendingFullRunID.Valid || state.PendingFullRunID.Int64 != runID {
		t.Fatalf("pending full run = %+v, want the running generation %d", state.PendingFullRunID, runID)
	}
	if state.Daily != now.Unix() || state.Weekly != now.Unix() {
		t.Fatalf("running full advanced the schedule: %+v", state)
	}
	if jobs := countQueuedFullJobs(t, f); jobs != 1 {
		t.Fatalf("running full produced %d jobs, want only the existing one", jobs)
	}
	dispatchCatalog(t, f)
	if jobs := countQueuedFullJobs(t, f); jobs != 1 {
		t.Fatalf("second tick queued %d full jobs, want no duplicate", jobs)
	}
}

// The daily boundary must stay due when the sync cannot be created because a
// full generation already holds the lease. Advancing first would lose the day
// and leave no task behind.
func TestCatalogScheduleDailyDueWaitsForSyncCreation(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/", catalogSyncTestEntry{Name: "Partition A", IsDir: true, Modified: "p1"})
	tree.set("/Partition A", catalogSyncTestEntry{Name: "Work 001", IsDir: true, Modified: "w1"})
	tree.set("/Partition A/Work 001", catalogSyncTestEntry{Name: "01.mp3", Size: 100, Modified: "f1"})
	f := newCatalogSyncFixture(t, tree)
	now := shanghaiTestTime(2030, time.January, 2, 3, 0)
	f.server.catalogNow = func() time.Time { return now }
	if p := f.runFullScan(t, ""); p["status"] != "complete" {
		t.Fatalf("full scan did not complete: %#v", p)
	}
	// A queued full that is not this completed generation makes the dispatcher
	// take the daily sync branch and fail its enqueue with errCatalogIndexRunning.
	if _, err := f.server.db.Exec(`UPDATE workflow_run SET status='queued' WHERE id=(
		SELECT workflow_run_id FROM source_catalog_scan WHERE file_source_id=?)`, f.sourceID(t)); err != nil {
		t.Fatal(err)
	}
	daily := now.Unix()
	weekly := now.Add(72 * time.Hour).Unix()
	setCatalogScheduleTestState(t, f, catalogScheduleState{Daily: daily, Weekly: weekly})

	dispatchCatalog(t, f)
	state := readCatalogSchedule(t, f)
	if state.Daily != daily {
		t.Fatalf("daily due moved without a created task: %+v", state)
	}
	if state.Weekly != weekly {
		t.Fatalf("daily race changed the weekly boundary: %+v", state)
	}
	if runs := countCatalogSyncRuns(t, f); runs != 0 {
		t.Fatalf("daily race created %d sync runs, want none", runs)
	}
}

// Advancing the weekly boundary also clears a stale deferral reason; leaving the
// reason behind with deferred_until=0 describes a pause that is no longer real.
func TestCatalogScheduleWeeklyAdvanceClearsDeferral(t *testing.T) {
	db := openMigratedTestDB(t)
	metadataReviewExec(t, db, `INSERT INTO file_source(id,code,display_name,source_type) VALUES(74,'weekly-deferral','Weekly deferral','openlist');
		INSERT INTO source_catalog_schedule(file_source_id,daily_due_at,weekly_due_at,deferred_until,deferred_reason)
		VALUES(74,1,1,9999999999,'low_disk')`)
	now := shanghaiTestTime(2030, time.January, 6, 3, 0)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = advanceCatalogScheduleTx(context.Background(), tx, 74, "weekly", now); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var state catalogScheduleState
	var manual, resume int
	if err = db.QueryRow(`SELECT daily_due_at,weekly_due_at,deferred_until,deferred_reason,manual_pending,resume_full_pending,pending_full_run_id
		FROM source_catalog_schedule WHERE file_source_id=74`).
		Scan(&state.Daily, &state.Weekly, &state.Deferred, &state.DeferredReason, &manual, &resume, &state.PendingFullRunID); err != nil {
		t.Fatal(err)
	}
	if state.Deferred != 0 || state.DeferredReason != "" {
		t.Fatalf("weekly advance left deferral %d/%q", state.Deferred, state.DeferredReason)
	}
	if state.Weekly != nextCatalogWeeklyDue(now).Unix() {
		t.Fatalf("weekly due = %d, want %d", state.Weekly, nextCatalogWeeklyDue(now).Unix())
	}
}

// A resource hold writes only deferred_until. The deferred source stays quiet
// until the deadline, then catches up exactly once.
func TestCatalogScheduleDeferredCatchesUpOnce(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/", catalogSyncTestEntry{Name: "Partition A", IsDir: true, Modified: "p1"})
	tree.set("/Partition A", catalogSyncTestEntry{Name: "Work 001", IsDir: true, Modified: "w1"})
	tree.set("/Partition A/Work 001", catalogSyncTestEntry{Name: "01.mp3", Size: 100, Modified: "f1"})
	f := newCatalogSyncFixture(t, tree)
	now := shanghaiTestTime(2030, time.January, 2, 3, 0)
	f.server.catalogNow = func() time.Time { return now }
	if p := f.runFullScan(t, ""); p["status"] != "complete" {
		t.Fatalf("full scan did not complete: %#v", p)
	}
	daily := now.Unix()
	weekly := now.Add(72 * time.Hour).Unix()
	setCatalogScheduleTestState(t, f, catalogScheduleState{Daily: daily, Weekly: weekly, Deferred: now.Add(time.Hour).Unix()})
	dispatchCatalog(t, f)
	if runs := countCatalogSyncRuns(t, f); runs != 0 {
		t.Fatalf("deferred source started %d syncs", runs)
	}
	state := readCatalogSchedule(t, f)
	if state.Daily != daily || state.Weekly != weekly || state.Deferred != now.Add(time.Hour).Unix() {
		t.Fatalf("waiting tick changed the schedule: %+v", state)
	}

	now = now.Add(time.Hour)
	f.server.catalogNow = func() time.Time { return now }
	dispatchCatalog(t, f)
	waitCatalogSyncSettled(t, f)
	if runs := countCatalogSyncRuns(t, f); runs != 1 {
		t.Fatalf("deferred recovery produced %d sync runs, want 1", runs)
	}
	state = readCatalogSchedule(t, f)
	if state.Deferred != 0 || state.DeferredReason != "" {
		t.Fatalf("recovery left deferral %d/%q", state.Deferred, state.DeferredReason)
	}
	dispatchCatalog(t, f)
	if runs := countCatalogSyncRuns(t, f); runs != 1 {
		t.Fatalf("deferred recovery replayed: %d sync runs", runs)
	}
}

// A disabled source keeps its row but is skipped, and enabling it again starts
// from the next local boundary instead of inheriting the old one.
func TestCatalogScheduleDisabledSourceIsSkippedAndResetOnEnable(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/", catalogSyncTestEntry{Name: "Partition A", IsDir: true, Modified: "p1"})
	tree.set("/Partition A", catalogSyncTestEntry{Name: "Work 001", IsDir: true, Modified: "w1"})
	tree.set("/Partition A/Work 001", catalogSyncTestEntry{Name: "01.mp3", Size: 100, Modified: "f1"})
	f := newCatalogSyncFixture(t, tree)
	now := shanghaiTestTime(2030, time.January, 2, 3, 0)
	f.server.catalogNow = func() time.Time { return now }
	if p := f.runFullScan(t, ""); p["status"] != "complete" {
		t.Fatalf("full scan did not complete: %#v", p)
	}
	setCatalogScheduleTestState(t, f, catalogScheduleState{Daily: now.Unix(), Weekly: now.Add(48 * time.Hour).Unix()})
	sourceID := f.sourceID(t)
	if _, err := f.server.db.Exec(`UPDATE file_source SET enabled=0 WHERE id=?`, sourceID); err != nil {
		t.Fatal(err)
	}
	dispatchCatalog(t, f)
	if runs := countCatalogSyncRuns(t, f); runs != 0 {
		t.Fatalf("disabled source started %d syncs", runs)
	}
	now = shanghaiTestTime(2030, time.January, 3, 3, 0)
	f.server.catalogNow = func() time.Time { return now }
	dispatchCatalog(t, f)
	if runs := countCatalogSyncRuns(t, f); runs != 0 {
		t.Fatalf("disabled source started %d syncs after a later tick", runs)
	}

	// Re-enable through the real update route so the reset is part of the same
	// transaction as the source change.
	var apiURL string
	if err := f.server.db.QueryRow(`SELECT api_url FROM file_source_endpoint WHERE file_source_id=?`, sourceID).Scan(&apiURL); err != nil {
		t.Fatal(err)
	}
	reviewRequest(t, f.handler, f.cookie, http.MethodPatch, "/api/file-sources/"+strconv.FormatInt(sourceID, 10),
		`{"displayName":"Example Remote A","sourceType":"openlist","enabled":true,"endpoint":{"apiUrl":"`+apiURL+`"}}`, 200)
	state := readCatalogSchedule(t, f)
	if state.Daily != shanghaiTestTime(2030, time.January, 4, 3, 0).UTC().Unix() ||
		state.Weekly != shanghaiTestTime(2030, time.January, 6, 3, 0).UTC().Unix() ||
		state.Deferred != 0 || state.ManualPending || state.ResumeFull {
		t.Fatalf("re-enabled source inherited a schedule: %+v", state)
	}
}

// A terminal partial generation keeps its checkpoints, but the daily update is
// still a directory-first sync: it retries the failed branch and independently
// discovers changes in the branches that completed.
func TestCatalogSchedulePartialGenerationRunsIncrementalDiscovery(t *testing.T) {
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
	now = shanghaiTestTime(2030, time.January, 2, 3, 0)
	f.server.catalogNow = func() time.Time { return now }
	setCatalogScheduleTestState(t, f, catalogScheduleState{Daily: now.Unix(), Weekly: now.Add(96 * time.Hour).Unix()})
	tree.addFile("/Partition A/Work A", "a2.mp3", 200, "fa2")
	tree.setModified("/Partition A", "Work A", "wa2")
	dispatchCatalog(t, f)
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
	var missing int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_entry WHERE file_source_id=? AND availability_status='missing'`, f.sourceID(t)).Scan(&missing); err != nil {
		t.Fatal(err)
	}
	if missing != 0 {
		t.Fatalf("failed branch produced %d missing cards", missing)
	}

	// Once the source recovers, the next maintenance sync retries the failed
	// range and publishes its card without a new full generation.
	tree.mu.Lock()
	delete(tree.listFail, "/Partition B")
	tree.mu.Unlock()
	now = shanghaiTestTime(2030, time.January, 3, 3, 0)
	f.server.catalogNow = func() time.Time { return now }
	dispatchCatalog(t, f)
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

// A partial scan that stopped before the end of the tree retries its pending
// range through the same directory-first sync and still probes the completed
// branches. A full resume must not freeze the successful branches.
func TestCatalogSchedulePartialWithPendingRangesStillProbesCompletedBranches(t *testing.T) {
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
	setCatalogScheduleTestState(t, f, catalogScheduleState{Daily: now.Unix(), Weekly: now.Add(96 * time.Hour).Unix()})
	dispatchCatalog(t, f)
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
}

// A successful dirs response proves which direct child folders exist only when
// the same directory's file listing is complete. If the listing fails, a folder
// missing from dirs keeps its card and files until a later complete listing.
func TestCatalogScheduleParentListingFailureKeepsChildrenUntilListingSucceeds(t *testing.T) {
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
