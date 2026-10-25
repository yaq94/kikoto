package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yexca/kikoto/backend/internal/openlist"
)

type catalogSyncTestEntry struct {
	Name     string
	IsDir    bool
	Size     int64
	Modified string
}

func TestManualFullIndexRefusesWhileIncrementalSyncRuns(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/", catalogSyncTestEntry{Name: "Partition A", IsDir: true, Modified: "p1"})
	tree.set("/Partition A", catalogSyncTestEntry{Name: "Work 001", IsDir: true, Modified: "w1"})
	tree.set("/Partition A/Work 001", catalogSyncTestEntry{Name: "01.mp3", Size: 100, Modified: "f1"})
	f := newCatalogSyncFixture(t, tree)
	if p := f.runFullScan(t, ""); p["status"] != "complete" {
		t.Fatalf("full scan did not complete: %#v", p)
	}
	sourceID := f.sourceID(t)
	var generation int64
	if err := f.server.db.QueryRow(`SELECT workflow_run_id FROM source_catalog_scan WHERE file_source_id=?`, sourceID).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.db.Exec(`INSERT INTO source_catalog_sync_run(file_source_id,status,note) VALUES(?,'running','')`, sourceID); err != nil {
		t.Fatal(err)
	}
	resp := f.request(t, http.MethodPost, "/catalog/index", `{}`, http.StatusConflict)
	if resp["code"] != "catalog_sync_running" {
		t.Fatalf("manual full response = %#v", resp)
	}
	var after int64
	if err := f.server.db.QueryRow(`SELECT workflow_run_id FROM source_catalog_scan WHERE file_source_id=?`, sourceID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != generation {
		t.Fatalf("manual full replaced generation %d with %d", generation, after)
	}
}

func TestIncrementalSyncRefusesWhileFullGenerationRuns(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/", catalogSyncTestEntry{Name: "Partition A", IsDir: true, Modified: "p1"})
	tree.set("/Partition A", catalogSyncTestEntry{Name: "Work 001", IsDir: true, Modified: "w1"})
	tree.set("/Partition A/Work 001", catalogSyncTestEntry{Name: "01.mp3", Size: 100, Modified: "f1"})
	f := newCatalogSyncFixture(t, tree)
	f.request(t, http.MethodPost, "/catalog/index", `{}`, http.StatusAccepted)
	ctx := context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true)
	if _, err := f.server.runCatalogIncrementalSync(ctx, f.sourceID(t), false); !errors.Is(err, errCatalogIndexRunning) {
		t.Fatalf("incremental sync error = %v, want errCatalogIndexRunning", err)
	}
	var runs int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_sync_run WHERE file_source_id=?`, f.sourceID(t)).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 0 {
		t.Fatalf("incremental sync started %d run(s) while the full generation was queued", runs)
	}
}

type catalogSyncTestTree struct {
	mu       sync.Mutex
	entries  map[string][]catalogSyncTestEntry
	dirsFail map[string]bool
	listFail map[string]bool
	listHits map[string]int
	dirsHits map[string]int
}

func newCatalogSyncTestTree() *catalogSyncTestTree {
	return &catalogSyncTestTree{entries: map[string][]catalogSyncTestEntry{}, dirsFail: map[string]bool{}, listFail: map[string]bool{}, listHits: map[string]int{}, dirsHits: map[string]int{}}
}

func (tree *catalogSyncTestTree) set(path string, entries ...catalogSyncTestEntry) {
	tree.mu.Lock()
	defer tree.mu.Unlock()
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	tree.entries[path] = entries
}

func (tree *catalogSyncTestTree) addFile(path, name string, size int64, modified string) {
	tree.mu.Lock()
	defer tree.mu.Unlock()
	entries := append([]catalogSyncTestEntry{}, tree.entries[path]...)
	entries = append(entries, catalogSyncTestEntry{Name: name, Size: size, Modified: modified})
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	tree.entries[path] = entries
}

func (tree *catalogSyncTestTree) setModified(path, name, modified string) {
	tree.mu.Lock()
	defer tree.mu.Unlock()
	entries := tree.entries[path]
	for i := range entries {
		if entries[i].Name == name {
			entries[i].Modified = modified
		}
	}
	tree.entries[path] = entries
}

func (tree *catalogSyncTestTree) resetHits() {
	tree.mu.Lock()
	defer tree.mu.Unlock()
	tree.listHits = map[string]int{}
	tree.dirsHits = map[string]int{}
}

func (tree *catalogSyncTestTree) listCount(path string) int {
	tree.mu.Lock()
	defer tree.mu.Unlock()
	return tree.listHits[path]
}

func (tree *catalogSyncTestTree) totalListHits() int {
	tree.mu.Lock()
	defer tree.mu.Unlock()
	total := 0
	for _, count := range tree.listHits {
		total += count
	}
	return total
}

func (tree *catalogSyncTestTree) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Path    string `json:"path"`
			Page    int    `json:"page"`
			PerPage int    `json:"per_page"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Errorf("decode %s: %v", r.URL.Path, err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		tree.mu.Lock()
		switch r.URL.Path {
		case "/api/fs/dirs":
			tree.dirsHits[input.Path]++
			if tree.dirsFail[input.Path] {
				tree.mu.Unlock()
				http.Error(w, "synthetic subtree failure", http.StatusInternalServerError)
				return
			}
			items := []map[string]any{}
			for _, entry := range tree.entries[input.Path] {
				if entry.IsDir {
					items = append(items, map[string]any{"name": entry.Name, "is_dir": true, "modified": entry.Modified})
				}
			}
			tree.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": items})
		case "/api/fs/list":
			tree.listHits[input.Path]++
			if tree.listFail[input.Path] {
				tree.mu.Unlock()
				http.Error(w, "synthetic listing failure", http.StatusInternalServerError)
				return
			}
			entries := append([]catalogSyncTestEntry{}, tree.entries[input.Path]...)
			tree.mu.Unlock()
			items := make([]map[string]any, 0, len(entries))
			for _, entry := range entries {
				item := map[string]any{"name": entry.Name, "size": entry.Size, "modified": entry.Modified}
				if entry.IsDir {
					item["is_dir"] = true
				}
				items = append(items, item)
			}
			page, perPage := max(input.Page, 1), max(input.PerPage, 1)
			start := min(len(items), (page-1)*perPage)
			end := min(len(items), start+perPage)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items[start:end], "total": len(items)}})
		default:
			tree.mu.Unlock()
			http.NotFound(w, r)
		}
	})
}

func newCatalogSyncFixture(t *testing.T, tree *catalogSyncTestTree) catalogIndexFixture {
	t.Helper()
	return newCatalogIndexFixture(t, tree.handler(t))
}

func (f catalogIndexFixture) runCatalogSync(t *testing.T, force bool) catalogSyncRunState {
	t.Helper()
	ctx := context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true)
	runID, err := f.server.runCatalogIncrementalSync(ctx, f.sourceID(t), force)
	if err != nil {
		t.Fatalf("run incremental sync: %v", err)
	}
	var state catalogSyncRunState
	if err = f.server.db.QueryRow(`SELECT status,dirs_probed,dirs_changed,dirs_listed,entries_published,requests,note FROM source_catalog_sync_run WHERE id=?`, runID).Scan(&state.Status, &state.DirsProbed, &state.DirsChanged, &state.DirsListed, &state.EntriesPublished, &state.Requests, &state.Note); err != nil {
		t.Fatal(err)
	}
	state.ID = runID
	return state
}

func (f catalogIndexFixture) runFullScan(t *testing.T, body string) map[string]any {
	t.Helper()
	if body == "" {
		body = `{"intervalMs":100,"concurrency":1}`
	}
	f.request(t, http.MethodPost, "/catalog/index", body, http.StatusAccepted)
	return f.drain(t)
}

func catalogSyncTestObservationCount(t *testing.T, f catalogIndexFixture, sourceID int64) int {
	t.Helper()
	var count int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_observation WHERE file_source_id=?`, sourceID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestCatalogSyncNoChangeSecondRunListsNothing(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/", catalogSyncTestEntry{Name: "Partition A", IsDir: true, Modified: "p1"})
	tree.set("/Partition A", catalogSyncTestEntry{Name: "Work 001", IsDir: true, Modified: "w1"})
	tree.set("/Partition A/Work 001",
		catalogSyncTestEntry{Name: "01.mp3", Size: 100, Modified: "f1"},
		catalogSyncTestEntry{Name: "MP3", IsDir: true, Modified: "m1"})
	tree.set("/Partition A/Work 001/MP3", catalogSyncTestEntry{Name: "01.mp3", Size: 200, Modified: "f2"})
	f := newCatalogSyncFixture(t, tree)
	if p := f.runFullScan(t, ""); p["status"] != "complete" {
		t.Fatalf("full scan did not complete: %#v", p)
	}
	sourceID := f.sourceID(t)
	before := catalogSyncTestObservationCount(t, f, sourceID)
	tree.resetHits()

	first := f.runCatalogSync(t, false)
	if first.Status != "succeeded" || first.DirsProbed == 0 || first.DirsListed != 1 || first.EntriesPublished != 0 {
		t.Fatalf("baseline sync did not adopt mtimes: %#v", first)
	}
	tree.resetHits()
	second := f.runCatalogSync(t, false)
	t.Logf("no-change sync requests=%d dirs_probed=%d dirs_changed=%d dirs_listed=%d entries_published=%d list_requests=%d", second.Requests, second.DirsProbed, second.DirsChanged, second.DirsListed, second.EntriesPublished, tree.totalListHits())
	if second.Status != "succeeded" || second.DirsListed != 1 || second.EntriesPublished != 0 {
		t.Fatalf("unchanged sync published work: %#v", second)
	}
	if got := tree.totalListHits(); got != 1 || tree.listCount("/") != 1 {
		t.Fatalf("unchanged sync issued %d list requests, dirs=%v list=%v", got, tree.dirsHits, tree.listHits)
	}
	if after := catalogSyncTestObservationCount(t, f, sourceID); after != before {
		t.Fatalf("unchanged sync changed observation rows: before=%d after=%d", before, after)
	}
}

func TestCatalogSyncDeepDirectoryAddsOnlyOneRow(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/", catalogSyncTestEntry{Name: "Partition A", IsDir: true, Modified: "p1"})
	tree.set("/Partition A", catalogSyncTestEntry{Name: "Work 001", IsDir: true, Modified: "w1"})
	tree.set("/Partition A/Work 001",
		catalogSyncTestEntry{Name: "01.mp3", Size: 100, Modified: "f1"},
		catalogSyncTestEntry{Name: "MP3", IsDir: true, Modified: "m1"})
	tree.set("/Partition A/Work 001/MP3", catalogSyncTestEntry{Name: "01.mp3", Size: 200, Modified: "f2"})
	f := newCatalogSyncFixture(t, tree)
	if p := f.runFullScan(t, ""); p["status"] != "complete" {
		t.Fatalf("full scan did not complete: %#v", p)
	}
	_ = f.runCatalogSync(t, false)
	tree.addFile("/Partition A/Work 001/MP3", "02.mp3", 300, "f3")
	tree.setModified("/Partition A/Work 001", "MP3", "m2")
	tree.resetHits()
	state := f.runCatalogSync(t, false)
	t.Logf("deep-add sync requests=%d dirs_probed=%d dirs_changed=%d dirs_listed=%d entries_published=%d target_list_requests=%d", state.Requests, state.DirsProbed, state.DirsChanged, state.DirsListed, state.EntriesPublished, tree.listCount("/Partition A/Work 001/MP3"))
	if state.Status != "succeeded" || state.DirsListed != 2 || state.EntriesPublished != 1 {
		t.Fatalf("deep sync state = %#v", state)
	}
	if got := tree.listCount("/Partition A/Work 001/MP3"); got != 1 {
		t.Fatalf("changed leaf list requests = %d, want 1; all=%v", got, tree.listHits)
	}
	if got := tree.listCount("/"); got != 1 {
		t.Fatalf("root relist requests = %d, want 1; all=%v", got, tree.listHits)
	}
	for _, path := range []string{"/", "/Partition A", "/Partition A/Work 001"} {
		if path == "/" {
			continue
		}
		if got := tree.listCount(path); got != 0 {
			t.Fatalf("unrelated directory %s issued %d list requests", path, got)
		}
	}
	var files int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_file f JOIN source_catalog_entry e ON e.id=f.catalog_entry_id WHERE e.file_source_id=? AND f.kind='audio'`, f.sourceID(t)).Scan(&files); err != nil {
		t.Fatal(err)
	}
	if files != 3 {
		t.Fatalf("published audio files = %d, want 3", files)
	}
}

func TestCatalogSyncMissesContentOnlyChangeAndFullReconcileFindsIt(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/", catalogSyncTestEntry{Name: "Partition A", IsDir: true, Modified: "p1"})
	tree.set("/Partition A", catalogSyncTestEntry{Name: "Work 001", IsDir: true, Modified: "w1"})
	tree.set("/Partition A/Work 001", catalogSyncTestEntry{Name: "01.mp3", Size: 100, Modified: "f1"})
	f := newCatalogSyncFixture(t, tree)
	if p := f.runFullScan(t, ""); p["status"] != "complete" {
		t.Fatalf("full scan did not complete: %#v", p)
	}
	_ = f.runCatalogSync(t, false)
	// The file changes in place while the directory mtime stays put. The
	// incremental probe cannot see it by design.
	tree.addFile("/Partition A/Work 001", "02.mp3", 999, "f2")
	tree.resetHits()
	state := f.runCatalogSync(t, false)
	if state.DirsListed != 1 || state.EntriesPublished != 0 {
		t.Fatalf("directory mtime did not move but sync listed: %#v", state)
	}
	if tree.totalListHits() != 1 || tree.listCount("/") != 1 {
		t.Fatalf("unchanged mtime still issued unexpected list requests: %v", tree.listHits)
	}
	// The weekly full reconcile is the backstop.
	if p := f.runFullScan(t, `{"force":true,"intervalMs":100,"concurrency":1}`); p["status"] != "complete" {
		t.Fatalf("forced full reconcile did not complete: %#v", p)
	}
	var files int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_file f JOIN source_catalog_entry e ON e.id=f.catalog_entry_id WHERE e.file_source_id=? AND f.kind='audio'`, f.sourceID(t)).Scan(&files); err != nil {
		t.Fatal(err)
	}
	if files != 2 {
		t.Fatalf("full reconcile files = %d, want 2", files)
	}
}

func TestCatalogSyncFailedSubtreeKeepsOtherBranchesAndAdvancesLifecycle(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/",
		catalogSyncTestEntry{Name: "Partition A", IsDir: true, Modified: "p1"},
		catalogSyncTestEntry{Name: "Partition B", IsDir: true, Modified: "q1"})
	tree.set("/Partition A", catalogSyncTestEntry{Name: "Work A", IsDir: true, Modified: "wa1"})
	tree.set("/Partition A/Work A", catalogSyncTestEntry{Name: "a.mp3", Size: 100, Modified: "fa1"})
	tree.set("/Partition B", catalogSyncTestEntry{Name: "Work B", IsDir: true, Modified: "wb1"})
	tree.set("/Partition B/Work B", catalogSyncTestEntry{Name: "b.mp3", Size: 100, Modified: "fb1"})
	f := newCatalogSyncFixture(t, tree)
	if p := f.runFullScan(t, ""); p["status"] != "complete" {
		t.Fatalf("full scan did not complete: %#v", p)
	}
	_ = f.runCatalogSync(t, false)
	tree.addFile("/Partition A/Work A", "a2.mp3", 200, "fa2")
	tree.setModified("/Partition A", "Work A", "wa2")
	tree.mu.Lock()
	tree.dirsFail["/Partition B"] = true
	tree.mu.Unlock()
	state := f.runCatalogSync(t, false)
	if state.Status != "partial" {
		t.Fatalf("sync status = %q, want partial; note=%q", state.Status, state.Note)
	}
	if state.EntriesPublished != 1 {
		t.Fatalf("published in healthy branch = %d, want 1", state.EntriesPublished)
	}
	var stage string
	if err := f.server.db.QueryRow(`SELECT lifecycle_stage FROM source_catalog_scan WHERE file_source_id=?`, f.sourceID(t)).Scan(&stage); err != nil {
		t.Fatal(err)
	}
	if stage != "done" {
		t.Fatalf("lifecycle stage = %q, want done", stage)
	}
}

func TestCatalogSyncDoesNotBumpGeneration(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/", catalogSyncTestEntry{Name: "Partition A", IsDir: true, Modified: "p1"})
	tree.set("/Partition A", catalogSyncTestEntry{Name: "Work 001", IsDir: true, Modified: "w1"})
	tree.set("/Partition A/Work 001", catalogSyncTestEntry{Name: "01.mp3", Size: 100, Modified: "f1"})
	f := newCatalogSyncFixture(t, tree)
	if p := f.runFullScan(t, ""); p["status"] != "complete" {
		t.Fatalf("full scan did not complete: %#v", p)
	}
	var before int64
	if err := f.server.db.QueryRow(`SELECT workflow_run_id FROM source_catalog_scan WHERE file_source_id=?`, f.sourceID(t)).Scan(&before); err != nil {
		t.Fatal(err)
	}
	_ = f.runCatalogSync(t, false)
	tree.addFile("/Partition A/Work 001", "02.mp3", 200, "f2")
	tree.setModified("/Partition A", "Work 001", "w2")
	_ = f.runCatalogSync(t, false)
	var after int64
	if err := f.server.db.QueryRow(`SELECT workflow_run_id FROM source_catalog_scan WHERE file_source_id=?`, f.sourceID(t)).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("incremental sync bumped generation: before=%d after=%d", before, after)
	}
}

// waitCatalogSyncSettled waits for the incremental run the daily dispatcher
// started in the background. The catalog status alone cannot describe it: a
// sync keeps the existing generation, so its runId never moves.
func waitCatalogSyncSettled(t *testing.T, f catalogIndexFixture) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var p map[string]any
	for time.Now().Before(deadline) {
		p = f.request(t, http.MethodGet, "/catalog/index", "", 200)
		if sync, ok := p["lastSync"].(map[string]any); ok && sync["status"] != "running" {
			return p
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("incremental sync did not settle: %#v", p["lastSync"])
	return nil
}

// A completed generation is updated by directory mtime. The update must reuse
// the same generation, publish the new card and remain searchable.
func TestCatalogDailyDispatchesIncrementalRunAfterComplete(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/", catalogSyncTestEntry{Name: "Partition A", IsDir: true, Modified: "p1"})
	tree.set("/Partition A", catalogSyncTestEntry{Name: "Work 001", IsDir: true, Modified: "w1"})
	tree.set("/Partition A/Work 001", catalogSyncTestEntry{Name: "01.mp3", Size: 100, Modified: "f1"})
	f := newCatalogSyncFixture(t, tree)
	now := time.Date(2030, time.January, 1, 10, 0, 0, 0, catalogScheduleLocation).UTC()
	f.server.catalogNow = func() time.Time { return now }
	seedCatalogScheduleAt(t, f, now)
	if p := f.runFullScan(t, ""); p["status"] != "complete" {
		t.Fatalf("full scan did not complete: %#v", p)
	}
	var generation int64
	if err := f.server.db.QueryRow(`SELECT workflow_run_id FROM source_catalog_scan WHERE file_source_id=?`, f.sourceID(t)).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	schedule := readCatalogSchedule(t, f)
	if schedule.Weekly != nextCatalogWeeklyDue(now).Unix() {
		t.Fatalf("next full due = %d, want %d", schedule.Weekly, nextCatalogWeeklyDue(now).Unix())
	}
	fullMarker := schedule.Weekly
	tree.set("/", catalogSyncTestEntry{Name: "Partition A", IsDir: true, Modified: "p1"},
		catalogSyncTestEntry{Name: "Partition B", IsDir: true, Modified: "q1"})
	tree.set("/Partition B", catalogSyncTestEntry{Name: "Work B", IsDir: true, Modified: "wb1"})
	tree.set("/Partition B/Work B", catalogSyncTestEntry{Name: "b.mp3", Size: 100, Modified: "fb1"})
	tomorrow := now.Add(17 * time.Hour) // next local 03:00
	f.server.catalogNow = func() time.Time { return tomorrow }
	if err := f.server.dispatchDueCatalogUpdates(context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true)); err != nil {
		t.Fatal(err)
	}
	p := waitCatalogSyncSettled(t, f)
	if p["runId"] != float64(generation) {
		t.Fatalf("daily incremental changed generation: before=%d after=%v", generation, p["runId"])
	}
	sync := p["lastSync"].(map[string]any)
	for _, key := range []string{"id", "status", "startedAt", "finishedAt", "dirsProbed", "dirsChanged", "dirsListed", "entriesPublished", "requests", "note"} {
		if _, ok := sync[key]; !ok {
			t.Fatalf("sync summary lacks %q: %#v", key, sync)
		}
	}
	if sync["status"] != "succeeded" || sync["entriesPublished"] != float64(1) || sync["dirsProbed"].(float64) == 0 {
		t.Fatalf("daily sync = %#v", sync)
	}
	if after := readCatalogSchedule(t, f).Weekly; after != fullMarker {
		t.Fatalf("incremental sync reset the weekly marker: before=%d after=%d", fullMarker, after)
	}
	list := f.request(t, http.MethodGet, "/works?q=Work%20B", "", 200)
	if len(list["works"].([]any)) != 1 {
		t.Fatal("incremental discovery is not searchable")
	}
}

// A partial generation that reached the end of the tree keeps its checkpoints
// and receives a directory-first sync. The sync retries the failed ranges and
// still probes the branches that succeeded; it does not start another full
// crawl.
func TestCatalogDailyResumesPartialGenerationWithFailures(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/", catalogSyncTestEntry{Name: "Partition A", IsDir: true, Modified: "p1"})
	tree.set("/Partition A", catalogSyncTestEntry{Name: "Work 001", IsDir: true, Modified: "w1"})
	tree.set("/Partition A/Work 001", catalogSyncTestEntry{Name: "01.mp3", Size: 100, Modified: "f1"})
	f := newCatalogSyncFixture(t, tree)
	now := time.Date(2030, time.January, 1, 10, 0, 0, 0, catalogScheduleLocation).UTC()
	f.server.catalogNow = func() time.Time { return now }
	if p := f.runFullScan(t, ""); p["status"] != "complete" {
		t.Fatalf("full scan did not complete: %#v", p)
	}
	var generation int64
	if err := f.server.db.QueryRow(`SELECT workflow_run_id FROM source_catalog_scan WHERE file_source_id=?`, f.sourceID(t)).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.db.Exec(`UPDATE workflow_run SET status='partial' WHERE id=?`, generation); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.db.Exec(`UPDATE source_catalog_scan SET failed=1 WHERE file_source_id=?`, f.sourceID(t)); err != nil {
		t.Fatal(err)
	}
	fullMarker := readCatalogSchedule(t, f).Weekly
	tomorrow := now.Add(17 * time.Hour) // next local 03:00
	f.server.catalogNow = func() time.Time { return tomorrow }
	if err := f.server.dispatchDueCatalogUpdates(context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true)); err != nil {
		t.Fatal(err)
	}
	var syncRuns int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_sync_run WHERE file_source_id=?`, f.sourceID(t)).Scan(&syncRuns); err != nil {
		t.Fatal(err)
	}
	if syncRuns != 1 {
		t.Fatalf("partial generation produced %d sync runs, want 1", syncRuns)
	}
	var after int64
	if err := f.server.db.QueryRow(`SELECT workflow_run_id FROM source_catalog_scan WHERE file_source_id=?`, f.sourceID(t)).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != generation {
		t.Fatalf("daily resume changed generation: before=%d after=%d", generation, after)
	}
	if marker := readCatalogSchedule(t, f).Weekly; marker != fullMarker {
		t.Fatalf("resume reset the weekly marker: before=%d after=%d", fullMarker, marker)
	}
	state := waitCatalogSyncSettled(t, f)
	sync := state["lastSync"].(map[string]any)
	if sync["status"] != "succeeded" {
		t.Fatalf("partial maintenance sync = %#v", sync)
	}
}

// The weekly boundary records the next Sunday 03:00. Neither a daily
// incremental sync nor a resume may restart that wall-clock schedule.
func TestCatalogSyncWeeklyFullMarkerOnlyMovesWithANewGeneration(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/", catalogSyncTestEntry{Name: "Partition A", IsDir: true, Modified: "p1"})
	tree.set("/Partition A", catalogSyncTestEntry{Name: "Work 001", IsDir: true, Modified: "w1"})
	tree.set("/Partition A/Work 001", catalogSyncTestEntry{Name: "01.mp3", Size: 100, Modified: "f1"})
	f := newCatalogSyncFixture(t, tree)
	now := time.Date(2030, time.January, 1, 10, 0, 0, 0, catalogScheduleLocation).UTC()
	f.server.catalogNow = func() time.Time { return now }
	seedCatalogScheduleAt(t, f, now)
	if p := f.runFullScan(t, ""); p["status"] != "complete" {
		t.Fatalf("full scan did not complete: %#v", p)
	}
	var generation int64
	if err := f.server.db.QueryRow(`SELECT workflow_run_id FROM source_catalog_scan WHERE file_source_id=?`, f.sourceID(t)).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	enqueued := readCatalogSchedule(t, f).Weekly
	wantFull := nextCatalogWeeklyDue(now)
	if enqueued != wantFull.Unix() {
		t.Fatalf("next full due = %d, want %d", enqueued, wantFull.Unix())
	}
	// Incremental sync keeps the marker.
	now = now.Add(17 * time.Hour) // next local 03:00
	f.server.catalogNow = func() time.Time { return now }
	if err := f.server.dispatchDueCatalogUpdates(context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true)); err != nil {
		t.Fatal(err)
	}
	waitCatalogSyncSettled(t, f)
	if marker := readCatalogSchedule(t, f).Weekly; marker != enqueued {
		t.Fatalf("incremental sync reset the weekly marker: before=%d after=%d", enqueued, marker)
	}
	// A full resume keeps it too. Force a failed mid-tree run so the daily
	// dispatcher chooses the full checkpoint rather than a sync.
	if _, err := f.server.db.Exec(`UPDATE workflow_run SET status='failed' WHERE id=?`, generation); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.db.Exec(`UPDATE source_catalog_scan SET failed=1,pending=1 WHERE file_source_id=?`, f.sourceID(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.db.Exec(`UPDATE source_catalog_directory SET status='pending',error_code='' WHERE file_source_id=? AND path='/'`, f.sourceID(t)); err != nil {
		t.Fatal(err)
	}
	now = time.Date(2030, time.January, 3, 3, 0, 0, 0, catalogScheduleLocation).UTC()
	f.server.catalogNow = func() time.Time { return now }
	if err := f.server.dispatchDueCatalogUpdates(context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true)); err != nil {
		t.Fatal(err)
	}
	if marker := readCatalogSchedule(t, f).Weekly; marker != enqueued {
		t.Fatalf("resume reset the weekly marker: before=%d after=%d", enqueued, marker)
	}
	// A failed generation is repaired by a maintenance sync, not a full resume.
	waitCatalogSyncSettled(t, f)
	var resumed int64
	if err := f.server.db.QueryRow(`SELECT workflow_run_id FROM source_catalog_scan WHERE file_source_id=?`, f.sourceID(t)).Scan(&resumed); err != nil {
		t.Fatal(err)
	}
	if resumed != generation {
		t.Fatalf("maintenance sync replaced generation: before=%d after=%d", generation, resumed)
	}
	if marker := readCatalogSchedule(t, f).Weekly; marker != enqueued {
		t.Fatalf("maintenance sync reset the weekly marker: before=%d after=%d", enqueued, marker)
	}
	// Only the Sunday boundary starts a new full generation, and that enqueue is
	// what moves the marker to the following Sunday.
	now = time.Date(2030, time.January, 6, 3, 0, 0, 0, catalogScheduleLocation).UTC()
	f.server.catalogNow = func() time.Time { return now }
	if err := f.server.dispatchDueCatalogUpdates(context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true)); err != nil {
		t.Fatal(err)
	}
	// The missed daily boundary is earlier than the weekly one, so this tick
	// repairs the generation with a maintenance sync and records the weekly
	// boundary for the next tick. The full only advances the marker after it
	// actually succeeds.
	waitCatalogSyncSettled(t, f)
	state := readCatalogSchedule(t, f)
	if !state.ResumeFull {
		t.Fatal("weekly boundary during maintenance sync was not remembered")
	}
	if state.Weekly != enqueued {
		t.Fatalf("maintenance sync moved the weekly marker: before=%d after=%d", enqueued, state.Weekly)
	}
	dispatchCatalog(t, f)
	nextFull := nextCatalogWeeklyDue(now)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("weekly full did not complete: %#v", p)
	}
	dispatchCatalog(t, f)
	if marker := readCatalogSchedule(t, f).Weekly; marker != nextFull.Unix() {
		t.Fatalf("new generation did not move the weekly marker: got %d want %d", marker, nextFull.Unix())
	}
	var after int64
	if err := f.server.db.QueryRow(`SELECT workflow_run_id FROM source_catalog_scan WHERE file_source_id=?`, f.sourceID(t)).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after == generation {
		t.Fatal("expired weekly marker did not enqueue a new full generation")
	}
}

// A deployment hold stops a sync before it spends a request, records why, and
// moves the daily tick to the hold end instead of a full day later.
func TestCatalogSyncPausesForMaintenanceAndRetriesAfterTheHold(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/", catalogSyncTestEntry{Name: "Partition A", IsDir: true, Modified: "p1"})
	tree.set("/Partition A", catalogSyncTestEntry{Name: "Work 001", IsDir: true, Modified: "w1"})
	tree.set("/Partition A/Work 001", catalogSyncTestEntry{Name: "01.mp3", Size: 100, Modified: "f1"})
	f := newCatalogSyncFixture(t, tree)
	if p := f.runFullScan(t, ""); p["status"] != "complete" {
		t.Fatalf("full scan did not complete: %#v", p)
	}
	if _, err := f.server.db.Exec(`INSERT INTO app_setting(key,value_json) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value_json=excluded.value_json`, catalogMaintenanceSetting,
		mustJSON(catalogMaintenanceState{Until: time.Now().Add(5 * time.Minute), Reason: "deploy"})); err != nil {
		t.Fatal(err)
	}
	state := f.runCatalogSync(t, false)
	if state.Status != "partial" || state.Note != "maintenance" {
		t.Fatalf("held sync = %q/%q, want partial/maintenance", state.Status, state.Note)
	}
	if state.Requests != 0 || state.DirsProbed != 0 {
		t.Fatalf("held sync spent %d requests on %d probes", state.Requests, state.DirsProbed)
	}
	if tree.dirsHits["/"] != 0 {
		t.Fatalf("held sync probed the tree %d times", tree.dirsHits["/"])
	}
	var deferred int64
	if err := f.server.db.QueryRow(`SELECT deferred_until FROM source_catalog_schedule WHERE file_source_id=?`, f.sourceID(t)).Scan(&deferred); err != nil {
		t.Fatal(err)
	}
	if remaining := deferred - time.Now().Unix(); remaining < 4*60 || remaining > 6*60 {
		t.Fatalf("deferral in %d seconds, want the hold end", remaining)
	}
}

// The sync honours the same disk floor as the full crawl. A shortfall is
// retried after an hour, not after the daily interval.
func TestCatalogSyncPausesOnLowDiskAndDoesNotSpendRequests(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/", catalogSyncTestEntry{Name: "Partition A", IsDir: true, Modified: "p1"})
	tree.set("/Partition A", catalogSyncTestEntry{Name: "Work 001", IsDir: true, Modified: "w1"})
	tree.set("/Partition A/Work 001", catalogSyncTestEntry{Name: "01.mp3", Size: 100, Modified: "f1"})
	f := newCatalogSyncFixture(t, tree)
	if p := f.runFullScan(t, ""); p["status"] != "complete" {
		t.Fatalf("full scan did not complete: %#v", p)
	}
	f.server.catalogDiskSpace = func(string) (diskSpaceSnapshot, error) { return diskSpaceSnapshot{Available: 0}, nil }
	state := f.runCatalogSync(t, false)
	if state.Status != "partial" || state.Note != "low_disk_space" {
		t.Fatalf("low-disk sync = %q/%q, want partial/low_disk_space", state.Status, state.Note)
	}
	if state.Requests != 0 || state.DirsProbed != 0 {
		t.Fatalf("low-disk sync spent %d requests on %d probes", state.Requests, state.DirsProbed)
	}
	var deferred int64
	if err := f.server.db.QueryRow(`SELECT deferred_until FROM source_catalog_schedule WHERE file_source_id=?`, f.sourceID(t)).Scan(&deferred); err != nil {
		t.Fatal(err)
	}
	if remaining := deferred - time.Now().Unix(); remaining < 50*60 || remaining > 70*60 {
		t.Fatalf("deferral in %d seconds, want about an hour", remaining)
	}
}

// A failed listing must not stop the directories behind it. The path cursor
// moves on, so one broken directory costs one request instead of hiding the
// rest of the tree.
func TestCatalogSyncContinuesPastAFailedListing(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/", catalogSyncTestEntry{Name: "P1", IsDir: true, Modified: "a"})
	works := make([]catalogSyncTestEntry, 0, 40)
	for i := range 40 {
		name := fmt.Sprintf("W%02d", i)
		works = append(works, catalogSyncTestEntry{Name: name, IsDir: true, Modified: "w"})
		tree.set("/P1/"+name, catalogSyncTestEntry{Name: "01.mp3", Size: 100, Modified: "f"})
	}
	tree.set("/P1", works...)
	f := newCatalogSyncFixture(t, tree)
	if p := f.runFullScan(t, ""); p["status"] != "complete" {
		t.Fatalf("full scan did not complete: %#v", p)
	}
	// Force a second pass over the whole tree with one early failure.
	tree.mu.Lock()
	tree.listFail["/P1/W00"] = true
	tree.mu.Unlock()
	state := f.runCatalogSync(t, true)
	if state.Status != "partial" {
		t.Fatalf("sync with one failed listing = %q/%q", state.Status, state.Note)
	}
	if !strings.Contains(state.Note, "/P1/W00") {
		t.Fatalf("note does not name the failed directory: %q", state.Note)
	}
	if tree.listCount("/P1/W39") == 0 {
		t.Fatal("a failed listing hid the directories behind it")
	}
}

// Phase timings describe the sync itself, not the full crawl it reuses.
func TestCatalogSyncRecordsPhaseTimingsOnItsOwnRun(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/", catalogSyncTestEntry{Name: "Partition A", IsDir: true, Modified: "p1"})
	tree.set("/Partition A", catalogSyncTestEntry{Name: "Work 001", IsDir: true, Modified: "w1"})
	tree.set("/Partition A/Work 001", catalogSyncTestEntry{Name: "01.mp3", Size: 100, Modified: "f1"})
	f := newCatalogSyncFixture(t, tree)
	if p := f.runFullScan(t, ""); p["status"] != "complete" {
		t.Fatalf("full scan did not complete: %#v", p)
	}
	tree.set("/", catalogSyncTestEntry{Name: "Partition A", IsDir: true, Modified: "p1"},
		catalogSyncTestEntry{Name: "Partition B", IsDir: true, Modified: "q1"})
	tree.set("/Partition B", catalogSyncTestEntry{Name: "Work B", IsDir: true, Modified: "wb1"})
	tree.set("/Partition B/Work B", catalogSyncTestEntry{Name: "b.mp3", Size: 100, Modified: "fb1"})
	state := f.runCatalogSync(t, false)
	if state.Status != "succeeded" {
		t.Fatalf("sync = %#v", state)
	}
	var generation int64
	if err := f.server.db.QueryRow(`SELECT workflow_run_id FROM source_catalog_scan WHERE file_source_id=?`, f.sourceID(t)).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	var syncRun int64
	if err := f.server.db.QueryRow(`SELECT r.id FROM workflow_run r JOIN workflow_job j ON j.workflow_run_id=r.id
		WHERE j.worker_type=? ORDER BY r.id DESC LIMIT 1`, sourceCatalogSyncWorkerType).Scan(&syncRun); err != nil {
		t.Fatal(err)
	}
	if syncRun == generation {
		t.Fatalf("sync reused the full generation run %d", generation)
	}
	var phases int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_phase_timing WHERE workflow_run_id=? AND phase='scan'`, syncRun).Scan(&phases); err != nil {
		t.Fatal(err)
	}
	if phases != 1 {
		t.Fatalf("sync scan phases = %d, want 1", phases)
	}
	var generationScans int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_phase_timing WHERE workflow_run_id=? AND phase='scan'`, generation).Scan(&generationScans); err != nil {
		t.Fatal(err)
	}
	if generationScans != 1 {
		t.Fatalf("full generation scan phases = %d, want the single full crawl phase", generationScans)
	}
}

// A process that stopped mid-sync leaves a running row. Startup clears it so
// the same day can run instead of waiting for the six-hour stale window.
func TestCatalogSyncStartupClearsAnInterruptedRun(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/", catalogSyncTestEntry{Name: "Partition A", IsDir: true, Modified: "p1"})
	tree.set("/Partition A", catalogSyncTestEntry{Name: "Work 001", IsDir: true, Modified: "w1"})
	tree.set("/Partition A/Work 001", catalogSyncTestEntry{Name: "01.mp3", Size: 100, Modified: "f1"})
	f := newCatalogSyncFixture(t, tree)
	if p := f.runFullScan(t, ""); p["status"] != "complete" {
		t.Fatalf("full scan did not complete: %#v", p)
	}
	if _, err := f.server.db.Exec(`INSERT INTO source_catalog_sync_run(file_source_id,status,note) VALUES(?,'running','')`, f.sourceID(t)); err != nil {
		t.Fatal(err)
	}
	if err := f.server.settleInterruptedCatalogSyncTiming(context.Background()); err != nil {
		t.Fatal(err)
	}
	var status, note string
	if err := f.server.db.QueryRow(`SELECT status,note FROM source_catalog_sync_run ORDER BY id DESC LIMIT 1`).Scan(&status, &note); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || !strings.Contains(note, "startup interrupted") {
		t.Fatalf("interrupted sync = %q/%q", status, note)
	}
	state := f.runCatalogSync(t, false)
	if state.Status != "succeeded" {
		t.Fatalf("next sync after startup cleanup = %q/%q", state.Status, state.Note)
	}
}

func TestCatalogSyncDirsEndpointAcceptsArrayAndWrappedShapes(t *testing.T) {
	for _, body := range []string{
		`{"code":200,"data":[{"name":"A","is_dir":true,"modified":"m1"}]}`,
		`{"code":200,"data":{"content":[{"name":"A","is_dir":true,"modified":"m1"}]}}`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		client := openlist.NewClient(server.URL, server.Client())
		dirs, err := client.Dirs(context.Background(), "/")
		server.Close()
		if err != nil {
			t.Fatalf("dirs %s: %v", body, err)
		}
		if len(dirs) != 1 || dirs[0].Name != "A" || !dirs[0].IsDir || dirs[0].Modified != "m1" {
			t.Fatalf("dirs %s = %#v", body, dirs)
		}
	}
}

// An exclusion added after the first index must also stop the incremental
// path. A root probe that re-lists the tree cannot recreate the excluded
// directory row, otherwise the next publish pass would admit it again.
func TestCatalogSyncDoesNotRecreateAnExcludedSubtree(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/", catalogSyncTestEntry{Name: "Partition A", IsDir: true, Modified: "p1"})
	tree.set("/Partition A",
		catalogSyncTestEntry{Name: "Keep", IsDir: true, Modified: "k1"},
		catalogSyncTestEntry{Name: "Drop", IsDir: true, Modified: "d1"})
	tree.set("/Partition A/Keep", catalogSyncTestEntry{Name: "01.mp3", Size: 100, Modified: "kf"})
	tree.set("/Partition A/Drop", catalogSyncTestEntry{Name: "02.mp3", Size: 100, Modified: "df"})
	f := newCatalogSyncFixture(t, tree)
	if p := f.runFullScan(t, ""); p["status"] != "complete" {
		t.Fatalf("full scan did not complete: %#v", p)
	}
	sourceID := f.sourceID(t)
	if _, err := f.server.db.Exec(`INSERT INTO source_catalog_exclusion(file_source_id,path) VALUES(?, '/Partition A/Drop')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.db.Exec(`DELETE FROM source_catalog_directory WHERE file_source_id=? AND path='/Partition A/Drop'`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.db.Exec(`UPDATE source_catalog_directory SET observed_version='' WHERE file_source_id=?`, sourceID); err != nil {
		t.Fatal(err)
	}
	tree.resetHits()
	state := f.runCatalogSync(t, true)
	if state.Status != "succeeded" {
		t.Fatalf("sync did not succeed: %#v", state)
	}
	var recreated int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_directory WHERE file_source_id=? AND path='/Partition A/Drop'`, sourceID).Scan(&recreated); err != nil {
		t.Fatal(err)
	}
	if recreated != 0 {
		t.Fatalf("incremental sync recreated the excluded directory")
	}
	var kept int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_directory WHERE file_source_id=? AND path='/Partition A/Keep'`, sourceID).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if kept != 1 {
		t.Fatalf("incremental sync dropped an admitted directory")
	}
}
