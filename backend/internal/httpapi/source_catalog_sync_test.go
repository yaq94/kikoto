package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
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

type catalogSyncTestTree struct {
	mu       sync.Mutex
	entries  map[string][]catalogSyncTestEntry
	dirsFail map[string]bool
	listHits map[string]int
	dirsHits map[string]int
}

func newCatalogSyncTestTree() *catalogSyncTestTree {
	return &catalogSyncTestTree{entries: map[string][]catalogSyncTestEntry{}, dirsFail: map[string]bool{}, listHits: map[string]int{}, dirsHits: map[string]int{}}
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
	if first.Status != "succeeded" || first.DirsProbed == 0 || first.DirsListed != 0 || first.EntriesPublished != 0 {
		t.Fatalf("baseline sync did not adopt mtimes: %#v", first)
	}
	tree.resetHits()
	second := f.runCatalogSync(t, false)
	t.Logf("no-change sync requests=%d dirs_probed=%d dirs_changed=%d dirs_listed=%d entries_published=%d list_requests=%d", second.Requests, second.DirsProbed, second.DirsChanged, second.DirsListed, second.EntriesPublished, tree.totalListHits())
	if second.Status != "succeeded" || second.DirsListed != 0 || second.EntriesPublished != 0 {
		t.Fatalf("unchanged sync published work: %#v", second)
	}
	if got := tree.totalListHits(); got != 0 {
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
	if state.Status != "succeeded" || state.DirsListed != 1 || state.EntriesPublished != 1 {
		t.Fatalf("deep sync state = %#v", state)
	}
	if got := tree.listCount("/Partition A/Work 001/MP3"); got != 1 {
		t.Fatalf("changed leaf list requests = %d, want 1; all=%v", got, tree.listHits)
	}
	for _, path := range []string{"/", "/Partition A", "/Partition A/Work 001"} {
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
	if state.DirsListed != 0 || state.EntriesPublished != 0 {
		t.Fatalf("directory mtime did not move but sync listed: %#v", state)
	}
	if tree.totalListHits() != 0 {
		t.Fatal("unchanged mtime still issued list requests")
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

func TestCatalogDailyDispatchesIncrementalRunWithoutReusingPartialGeneration(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/", catalogSyncTestEntry{Name: "Partition A", IsDir: true, Modified: "p1"})
	tree.set("/Partition A", catalogSyncTestEntry{Name: "Work 001", IsDir: true, Modified: "w1"})
	tree.set("/Partition A/Work 001", catalogSyncTestEntry{Name: "01.mp3", Size: 100, Modified: "f1"})
	f := newCatalogSyncFixture(t, tree)
	now := time.Now().UTC().Truncate(time.Second)
	f.server.catalogNow = func() time.Time { return now }
	if p := f.runFullScan(t, ""); p["status"] != "complete" {
		t.Fatalf("full scan did not complete: %#v", p)
	}
	var generation int64
	if err := f.server.db.QueryRow(`SELECT workflow_run_id FROM source_catalog_scan WHERE file_source_id=?`, f.sourceID(t)).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	// Model the one partial shape the incremental path owns: the full
	// generation reached the tree but at least one range failed or was
	// restricted. Unconfirmed-empty and cancelled generations keep their old
	// resume behavior.
	if _, err := f.server.db.Exec(`UPDATE workflow_run SET status='partial' WHERE id=?`, generation); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.db.Exec(`UPDATE source_catalog_scan SET failed=1 WHERE file_source_id=?`, f.sourceID(t)); err != nil {
		t.Fatal(err)
	}
	tomorrow := now.Add(25 * time.Hour)
	f.server.catalogNow = func() time.Time { return tomorrow }
	if err := f.server.dispatchDueCatalogUpdates(context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	var runID int64
	var status string
	for time.Now().Before(deadline) {
		err := f.server.db.QueryRow(`SELECT id,status FROM source_catalog_sync_run WHERE file_source_id=? ORDER BY id DESC LIMIT 1`, f.sourceID(t)).Scan(&runID, &status)
		if err == nil && status != "running" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if runID == 0 || status == "running" {
		t.Fatalf("daily did not start an incremental run: id=%d status=%q", runID, status)
	}
	var after int64
	if err := f.server.db.QueryRow(`SELECT workflow_run_id FROM source_catalog_scan WHERE file_source_id=?`, f.sourceID(t)).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != generation {
		t.Fatalf("daily incremental changed generation: before=%d after=%d", generation, after)
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
