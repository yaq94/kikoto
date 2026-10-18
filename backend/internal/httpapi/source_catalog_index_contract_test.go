package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/yexca/kikoto/backend/internal/config"
)

// The contract suite runs the recoverable index over a synthetic tree through
// the real HTTP routes, the real worker and a migrated SQLite database. It
// asserts the published set, the accounting of every directory, the split of a
// creator folder and idempotency. The throughput experiment stays opt-in in
// source_catalog_index_calibration_test.go; this file owns the correctness
// contract and a wall-clock budget that a regression cannot hide behind.
const (
	contractPartitions        = 4
	contractWorksPerPartition = 124
	contractPageSize          = 500
	contractPagesPerBatch     = 32
	contractConcurrency       = 4
	contractIntervalMS        = 100
	// contractBudget is the wall-clock ceiling for the correctness contract.
	// The suite disables upstream pacing because that policy has its own test;
	// keeping the pacing delay here would make a 1,000-directory tree cost at
	// least 100 s by construction and the ceiling could never fail on writing.
	contractBudget = 60 * time.Second
)

type contractTree struct {
	tree        map[string][]map[string]any
	directories int
	entries     int
	files       int
}

// contractTree builds four partitions, each holding work directories with one
// format subdirectory. Every work directory publishes exactly one entry, and
// the format subdirectory attaches to it.
func buildContractTree() contractTree {
	dir := func(name string) map[string]any { return map[string]any{"name": name, "is_dir": true} }
	audio := func(name string) map[string]any { return map[string]any{"name": name, "size": 1024} }
	tree := map[string][]map[string]any{}
	directories := 1
	root := []map[string]any{}
	for partition := range contractPartitions {
		partitionName := fmt.Sprintf("Partition %c", 'A'+partition)
		partitionPath := "/" + partitionName
		root = append(root, dir(partitionName))
		directories++
		entries := []map[string]any{}
		for work := range contractWorksPerPartition {
			workName := fmt.Sprintf("Example Work %c%03d", 'A'+partition, work)
			workPath := partitionPath + "/" + workName
			entries = append(entries, dir(workName))
			directories++
			tree[workPath] = []map[string]any{audio("01.mp3"), audio("02.flac"), dir("MP3")}
			tree[workPath+"/MP3"] = []map[string]any{audio("01.mp3"), audio("02.mp3")}
			directories++
		}
		tree[partitionPath] = entries
	}
	tree["/"] = root
	return contractTree{
		tree:        tree,
		directories: directories,
		entries:     contractPartitions * contractWorksPerPartition,
		// Two direct audio, the format folder and its two audio per entry.
		files: contractPartitions * contractWorksPerPartition * 5,
	}
}

func newContractIndexFixture(t *testing.T, tree map[string][]map[string]any) (catalogIndexFixture, func()) {
	t.Helper()
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/fs/list" {
			t.Errorf("contract index requested %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		var input struct {
			Path    string `json:"path"`
			Page    int    `json:"page"`
			PerPage int    `json:"per_page"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Errorf("decode listing request: %v", err)
			return
		}
		items, ok := tree[input.Path]
		if !ok {
			t.Errorf("contract index requested unknown directory %q", input.Path)
			http.NotFound(w, r)
			return
		}
		start := min(len(items), max((input.Page-1)*input.PerPage, 0))
		end := min(len(items), start+input.PerPage)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200,
			"data": map[string]any{"content": items[start:end], "total": len(items)},
		})
	}))
	db := openMigratedTestDBWithProductionPool(t)
	server := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	createTestAdministrator(t, server, "synthetic-user", "synthetic-password")
	handler := server.Routes()
	cookie := loginTestSession(t, handler, "synthetic-user", "synthetic-password")
	input := mustJSON(map[string]any{
		"displayName": "Example Remote A", "sourceType": "openlist", "enabled": true,
		"config":   map[string]any{"openListRoots": []string{}},
		"endpoint": map[string]any{"apiUrl": stub.URL, "baseUrl": stub.URL},
	})
	source := reviewRequest(t, handler, cookie, http.MethodPost, "/api/file-sources", input, http.StatusCreated)
	fixture := catalogIndexFixture{
		server:  server,
		handler: handler,
		cookie:  cookie,
		base:    "/api/remote-sources/" + strconv.Itoa(int(source["id"].(float64))),
	}
	return fixture, stub.Close
}

func (f catalogIndexFixture) drainIndex(t *testing.T) map[string]any {
	t.Helper()
	ctx := context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true)
	for step := range 200 {
		if err := f.server.runNextQueuedWorkflowJob(ctx); err != nil {
			t.Fatalf("contract index job failed at step %d: %v", step, err)
		}
		progress := f.request(t, http.MethodGet, "/catalog/index", "", http.StatusOK)
		if progress["status"] != "queued" && progress["status"] != "running" {
			return progress
		}
	}
	t.Fatal("contract index did not settle")
	return nil
}

func TestSourceCatalogIndexContract(t *testing.T) {
	built := buildContractTree()
	fixture, closeStub := newContractIndexFixture(t, built.tree)
	defer closeStub()

	fixture.request(t, http.MethodPost, "/catalog/index", mustJSON(map[string]any{
		"pageSize": contractPageSize, "pagesPerBatch": contractPagesPerBatch,
		"concurrency": contractConcurrency, "intervalMs": contractIntervalMS,
	}), http.StatusAccepted)

	started := time.Now()
	progress := fixture.drainIndex(t)
	elapsed := time.Since(started)
	t.Logf("contract index directories=%d entries=%d elapsed=%s", built.directories, built.entries, elapsed)

	if progress["status"] != "complete" {
		t.Fatalf("contract index ended %v: %#v", progress["status"], progress)
	}
	if got := progress["completedDirectories"]; got != float64(built.directories) {
		t.Fatalf("completed directories = %v, want %d", got, built.directories)
	}
	if got := progress["indexedEntries"]; got != float64(built.entries) {
		t.Fatalf("indexed entries = %v, want %d", got, built.entries)
	}
	if failed, restricted, empty := progress["failedDirectories"], progress["restrictedDirectories"], progress["unconfirmedEmptyDirectories"]; failed != float64(0) || restricted != float64(0) || empty != float64(0) {
		t.Fatalf("contract index did not cover the tree: failed=%v restricted=%v empty=%v", failed, restricted, empty)
	}

	var entries, files, pending int
	if err := fixture.server.db.QueryRow(`SELECT
		(SELECT COUNT(*) FROM source_catalog_entry WHERE file_source_id=1),
		(SELECT COUNT(*) FROM source_catalog_file WHERE catalog_entry_id IN (SELECT id FROM source_catalog_entry WHERE file_source_id=1)),
		(SELECT COUNT(*) FROM source_catalog_observation o
			JOIN source_catalog_directory d ON d.file_source_id=o.file_source_id AND d.path=o.directory_path
			WHERE o.file_source_id=1 AND o.publish_pending=1 AND d.status='complete'
				AND d.catalog_root IN (SELECT catalog_root FROM source_catalog_audio_root WHERE file_source_id=1))`).Scan(&entries, &files, &pending); err != nil {
		t.Fatal(err)
	}
	if entries != built.entries {
		t.Fatalf("published entries = %d, want %d", entries, built.entries)
	}
	if files != built.files {
		t.Fatalf("published files = %d, want %d", files, built.files)
	}
	if pending != 0 {
		// Rows in directories without an audio root stay in staging by design;
		// a row that belongs to a published root but is still pending means the
		// run claimed complete coverage while search misses that file.
		t.Fatalf("publish queue left %d publishable rows after a complete run", pending)
	}

	if elapsed > contractBudget {
		t.Fatalf("contract index took %s, budget %s", elapsed.Round(time.Millisecond), contractBudget)
	}
}

// A creator folder with repeated work prefixes must publish one card per work
// and retire the container card. Running the same tree again must not duplicate
// entries, files or cards.
func TestSourceCatalogIndexContractSplitAndIdempotency(t *testing.T) {
	dir := func(name string) map[string]any { return map[string]any{"name": name, "is_dir": true} }
	audio := func(name string) map[string]any { return map[string]any{"name": name, "size": 2048} }
	tree := map[string][]map[string]any{
		"/":            {dir("Partition A")},
		"/Partition A": {dir("Example Creator")},
		"/Partition A/Example Creator": {
			audio("N001 Example Work-1.mp3"),
			audio("N001 Example Work-2.mp3"),
			audio("N002 Another Work-1.mp3"),
			audio("N002 Another Work-2.mp3"),
			dir("MP3"),
		},
		"/Partition A/Example Creator/MP3": {audio("N001 Example Work-1.mp3")},
	}
	fixture, closeStub := newContractIndexFixture(t, tree)
	defer closeStub()

	run := func() map[string]any {
		t.Helper()
		fixture.request(t, http.MethodPost, "/catalog/index", mustJSON(map[string]any{
			"pageSize": contractPageSize, "pagesPerBatch": contractPagesPerBatch,
			"concurrency": contractConcurrency, "intervalMs": contractIntervalMS,
			"force": true,
		}), http.StatusAccepted)
		return fixture.drainIndex(t)
	}

	first := run()
	if first["status"] != "complete" || first["indexedEntries"] != float64(2) {
		t.Fatalf("split run published %v entries: %#v", first["indexedEntries"], first)
	}
	rows, err := fixture.server.db.Query(`SELECT root_path, source_root, title FROM source_catalog_entry WHERE file_source_id=1 ORDER BY root_path`)
	if err != nil {
		t.Fatal(err)
	}
	type card struct{ root, sourceRoot, title string }
	cards := []card{}
	for rows.Next() {
		var c card
		if err = rows.Scan(&c.root, &c.sourceRoot, &c.title); err != nil {
			t.Fatal(err)
		}
		cards = append(cards, c)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()
	if len(cards) != 2 {
		t.Fatalf("split published %d cards, want 2: %#v", len(cards), cards)
	}
	for _, c := range cards {
		if c.sourceRoot != "/Partition A/Example Creator" {
			t.Fatalf("split card %q lost its source root: %#v", c.root, c)
		}
	}
	if cards[0].title != "N001 Example Work-1" || cards[1].title != "N002 Another Work-1" {
		t.Fatalf("split card titles: %#v", cards)
	}
	var container int
	if err = fixture.server.db.QueryRow(`SELECT COUNT(*) FROM source_catalog_entry WHERE file_source_id=1 AND root_path='/Partition A/Example Creator'`).Scan(&container); err != nil {
		t.Fatal(err)
	}
	if container != 0 {
		t.Fatalf("creator container card survived the split")
	}

	second := run()
	if second["status"] != "complete" || second["indexedEntries"] != float64(2) {
		t.Fatalf("second run published %v entries: %#v", second["indexedEntries"], second)
	}
	var entries, files int
	if err = fixture.server.db.QueryRow(`SELECT
		(SELECT COUNT(*) FROM source_catalog_entry WHERE file_source_id=1),
		(SELECT COUNT(*) FROM source_catalog_file)`).Scan(&entries, &files); err != nil {
		t.Fatal(err)
	}
	if entries != 2 || files != 5 {
		t.Fatalf("second run was not idempotent: entries=%d files=%d, want 2 and 5", entries, files)
	}
}
