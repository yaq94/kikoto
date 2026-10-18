package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yexca/kikoto/backend/internal/buildinfo"
	"github.com/yexca/kikoto/backend/internal/config"
	"github.com/yexca/kikoto/backend/internal/storage"
	"github.com/yexca/kikoto/backend/migrations"
)

func TestSourceCatalogDailyStartsOnlyAfterCompleteAndSurvivesRestart(t *testing.T) {
	var now atomic.Int64
	now.Store(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC).Unix())
	var added atomic.Bool
	var low atomic.Bool
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&input)
		items := []map[string]any{{"name": "track.mp3", "size": 100}}
		if input.Path == "/" {
			items = []map[string]any{{"name": "Example A", "is_dir": true}}
			if added.Load() {
				items = append(items, map[string]any{"name": "Example B", "is_dir": true})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": len(items)}})
	}))
	defer remote.Close()
	filename := filepath.Join(t.TempDir(), "library.db")
	cfg := config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", DatabasePath: filename, DataRoot: t.TempDir(), CacheRoot: t.TempDir()}
	boot := func(first bool) (dailyCatalogFixture, func()) {
		db, err := storage.Open(filename)
		if err != nil {
			t.Fatal(err)
		}
		if err = storage.MigrateFS(db, migrations.Files, buildinfo.Version); err != nil {
			t.Fatal(err)
		}
		s := NewServer(db, cfg)
		s.catalogNow = func() time.Time { return time.Unix(now.Load(), 0).UTC() }
		s.catalogDiskSpace = func(string) (diskSpaceSnapshot, error) {
			if low.Load() {
				return diskSpaceSnapshot{Available: 0}, nil
			}
			return diskSpaceSnapshot{Available: 4 << 30}, nil
		}
		if first {
			createTestAdministrator(t, s, "synthetic-user", "synthetic-password")
		}
		h := s.Routes()
		app := httptest.NewServer(h)
		cookie := workReferenceLogin(t, app, "synthetic-user")
		if first {
			input := mustJSON(map[string]any{"displayName": "Example Remote A", "sourceType": "openlist", "enabled": true, "endpoint": map[string]any{"apiUrl": remote.URL, "baseUrl": remote.URL}})
			var result map[string]any
			workReferenceRequest(t, app, cookie, http.MethodPost, "/api/file-sources", input, 201, &result)
			if result["id"] != float64(1) {
				t.Fatalf("unexpected fixture source: %#v", result)
			}
		}
		f := dailyCatalogFixture{catalogIndexFixture{s, h, cookie, "/api/remote-sources/1"}, app}
		closed := false
		closeApp := func() {
			if closed {
				return
			}
			closed = true
			app.Close()
			if err := s.Shutdown(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
		}
		t.Cleanup(closeApp)
		return f, closeApp
	}
	f, closeApp := boot(true)
	f.server.Go(f.server.StartJobRunner)
	// Advancing time alone cannot authorize a first full crawl.
	now.Add(int64(48 * time.Hour / time.Second))
	time.Sleep(2200 * time.Millisecond)
	if p := f.request(t, http.MethodGet, "/catalog/index", "", 200); p["status"] != "idle" {
		t.Fatalf("unrequested initial scan: %#v", p)
	}
	first := f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"pagesPerBatch":8}`, 202)["runId"]
	waitCatalogDaily(t, f, func(p map[string]any) bool { return p["status"] == "complete" })
	closeApp()
	// A stopped application misses its due time. Startup must dispatch exactly
	// one persisted update, without creating a second initial scan.
	now.Add(int64(24 * time.Hour / time.Second))
	added.Store(true)
	f, closeApp = boot(false)
	f.server.Go(f.server.StartJobRunner)
	p := waitCatalogDaily(t, f, func(p map[string]any) bool { return p["runId"] != first && p["status"] == "complete" })
	if p["indexedEntries"] != float64(2) {
		t.Fatalf("daily scan missed newly added audio: %#v", p)
	}
	second := p["runId"]
	list := f.request(t, http.MethodGet, "/works?q=Example%20B", "", 200)
	if len(list["works"].([]any)) != 1 {
		t.Fatal("daily discovery is not searchable")
	}
	now.Add(int64(23 * time.Hour / time.Second))
	time.Sleep(2200 * time.Millisecond)
	if p = f.request(t, http.MethodGet, "/catalog/index", "", 200); p["runId"] != second {
		t.Fatal("update repeated before the next day")
	}
	entryID := strconv.Itoa(int(list["works"].([]any)[0].(map[string]any)["catalogEntryId"].(float64)))
	workReferenceRequest(t, f.app, f.cookie, http.MethodPatch, "/api/source-catalog/"+entryID+"/user-state", `{"rating":3,"note":"Example retained note"}`, 200, nil)
	low.Store(true)
	now.Add(int64(time.Hour / time.Second))
	p = waitCatalogDaily(t, f, func(p map[string]any) bool { return p["runId"] != second && p["status"] == "paused" })
	third := p["runId"]
	if p["requests"] != float64(0) || p["pauseReason"] != "low_disk_space" {
		t.Fatalf("scheduled scan ignored disk limit: %#v", p)
	}
	closeApp()
	now.Add(int64(24 * time.Hour / time.Second))
	f, closeApp = boot(false)
	f.server.Go(f.server.StartJobRunner)
	waitCatalogDaily(t, f, func(p map[string]any) bool { return p["status"] == "paused" && p["runId"] == third })
	// Repeated manual starts and a due timer all keep the paused generation.
	for range 4 {
		joined := f.request(t, http.MethodPost, "/catalog/index", `{}`, 202)
		if joined["runId"] != third || joined["existing"] != true {
			t.Fatal("paused scan was duplicated")
		}
	}
	var detail map[string]any
	workReferenceRequest(t, f.app, f.cookie, http.MethodGet, "/api/source-catalog/"+entryID, "", 200, &detail)
	assertReview(t, detail, float64(3), "Example retained note")
	low.Store(false)
	f.request(t, http.MethodPost, "/catalog/index", `{}`, 202)
	p = waitCatalogDaily(t, f, func(p map[string]any) bool { return p["status"] == "complete" })
	if p["runId"] != third || p["indexedEntries"] != float64(2) {
		t.Fatal("paused checkpoint did not resume")
	}
	closeApp()
}

type dailyCatalogFixture struct {
	catalogIndexFixture
	app *httptest.Server
}

func (f dailyCatalogFixture) request(t *testing.T, method, suffix, body string, code int) map[string]any {
	t.Helper()
	var result map[string]any
	workReferenceRequest(t, f.app, f.cookie, method, f.base+suffix, body, code, &result)
	return result
}

func waitCatalogDaily(t *testing.T, f interface {
	request(*testing.T, string, string, string, int) map[string]any
}, ready func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var p map[string]any
	for time.Now().Before(deadline) {
		p = f.request(t, http.MethodGet, "/catalog/index", "", 200)
		if ready(p) {
			return p
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("catalog state did not settle: run=%v status=%v requests=%v", p["runId"], p["status"], p["requests"])
	return nil
}

func TestSourceCatalogDailyJoinsQueuedRunningAndCancelledScansWithSavedBudget(t *testing.T) {
	var now atomic.Int64
	now.Store(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC).Unix())
	var block atomic.Bool
	started := make(chan struct{}, 1)
	var mu sync.Mutex
	var starts []time.Time
	active, maximum := 0, 0
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&input)
		mu.Lock()
		starts = append(starts, time.Now())
		active++
		maximum = max(maximum, active)
		mu.Unlock()
		defer func() { mu.Lock(); active--; mu.Unlock() }()
		if block.Load() {
			select {
			case started <- struct{}{}:
			default:
			}
			select {
			case <-r.Context().Done():
			case <-time.After(10 * time.Second):
				t.Error("cancel did not reach the upstream boundary")
			}
			return
		}
		items := []map[string]any{{"name": "track.mp3", "size": 100}}
		if input.Path == "/" {
			items = []map[string]any{{"name": "Example Work", "is_dir": true}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": len(items)}})
	}))
	f.server.catalogNow = func() time.Time { return time.Unix(now.Load(), 0).UTC() }
	createAccountManagedTestUser(t, f.server, "synthetic-listener", "synthetic-password")
	other := loginTestSession(t, f.handler, "synthetic-listener", "synthetic-password")
	reviewRequest(t, f.handler, other, http.MethodPost, f.base+"/catalog/index", `{}`, 403)
	reviewRequest(t, f.handler, other, http.MethodGet, f.base+"/catalog/index", "", 403)
	f.request(t, http.MethodPost, "/catalog/index", `{}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("initial scan: %#v", p)
	}
	queued := f.request(t, http.MethodPost, "/catalog/index", `{}`, 202)["runId"]
	for range 3 {
		if p := f.request(t, http.MethodPost, "/catalog/index", `{}`, 202); p["runId"] != queued {
			t.Fatal("queued scan duplicated")
		}
	}
	block.Store(true)
	now.Add(int64(24 * time.Hour / time.Second))
	f.server.Go(f.server.StartJobRunner)
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("queued scan did not start")
	}
	time.Sleep(2200 * time.Millisecond)
	for range 3 {
		if p := f.request(t, http.MethodPost, "/catalog/index", `{}`, 202); p["runId"] != queued {
			t.Fatal("running scan duplicated")
		}
	}
	id := strconv.Itoa(int(queued.(float64)))
	reviewRequest(t, f.handler, f.cookie, http.MethodPost, "/api/workflow-runs/"+id+"/cancel", `{}`, 200)
	waitCatalogDaily(t, f, func(p map[string]any) bool { return p["status"] == "cancelled" })
	block.Store(false)
	now.Add(int64(24 * time.Hour / time.Second))
	p := waitCatalogDaily(t, f, func(p map[string]any) bool { return p["status"] == "complete" })
	if p["runId"] != queued {
		t.Fatal("daily recovery replaced cancelled generation")
	}
	options := p["options"].(map[string]any)
	if options["concurrency"] != float64(1) || options["intervalMs"] != float64(1000) {
		t.Fatal("scheduled update discarded default budget")
	}
	mu.Lock()
	defer mu.Unlock()
	if maximum != 1 {
		t.Fatalf("overlapping metadata requests: %d", maximum)
	}
	for i := 1; i < len(starts); i++ {
		if starts[i].Sub(starts[i-1]) < 950*time.Millisecond {
			t.Fatal("daily update exceeded one metadata request per second")
		}
	}
}

func TestSourceCatalogDailyDoesNotRetryIncompleteFirstCoverage(t *testing.T) {
	var now atomic.Int64
	now.Store(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC).Unix())
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": []any{}, "total": 0}})
	}))
	f.server.catalogNow = func() time.Time { return time.Unix(now.Load(), 0).UTC() }
	// file_source ids can be reused after deletion. Model the old source's
	// retained setting; it must not enable a replacement source's first scan.
	metadataReviewExec(t, f.server.db, `INSERT INTO app_setting(key,value_json) VALUES('source_catalog_daily:1','1')`)
	first := f.request(t, http.MethodPost, "/catalog/index", `{}`, 202)["runId"]
	p := f.drain(t)
	if p["status"] != "partial" || p["unconfirmedEmptyDirectories"] != float64(1) {
		t.Fatal("empty root was treated as complete")
	}
	now.Add(int64(48 * time.Hour / time.Second))
	f.server.Go(f.server.StartJobRunner)
	time.Sleep(2200 * time.Millisecond)
	p = f.request(t, http.MethodGet, "/catalog/index", "", 200)
	if p["runId"] != first || p["status"] != "partial" {
		t.Fatal("daily timer replaced the first generation")
	}
	// The run reached the end of the tree. Its only gap is an unconfirmed empty
	// root, so the next day must observe the source again in the same
	// generation; otherwise new content could never be discovered.
	if p["requests"] == float64(1) {
		t.Fatal("daily update did not re-observe a finished partial generation")
	}
}
