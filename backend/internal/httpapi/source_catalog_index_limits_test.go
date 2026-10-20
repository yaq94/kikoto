package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yexca/kikoto/backend/internal/config"
)

type catalogIndexFixture struct {
	server  *Server
	handler http.Handler
	cookie  *http.Cookie
	base    string
}

func TestSourceCatalogIndexLowDiskPausesBeforePublishingAndResumes(t *testing.T) {
	var low atomic.Bool
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&input)
		items := []map[string]any{{"name": "Example A", "is_dir": true}}
		if input.Path != "/" {
			items = []map[string]any{{"name": "track.mp3"}}
			low.Store(true)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": 1}})
	}))
	f.server.catalogDiskSpace = func(string) (diskSpaceSnapshot, error) {
		if low.Load() {
			return diskSpaceSnapshot{Available: 0}, nil
		}
		return diskSpaceSnapshot{Available: 4 << 30}, nil
	}
	metadataReviewExec(t, f.server.db, `INSERT INTO source_catalog_entry(id,file_source_id,root_path,title) VALUES(91,1,'/Retained','Example retained card');
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES(91,'track.mp3','Example track','audio',100);`)
	reviewRequest(t, f.handler, f.cookie, http.MethodPatch, "/api/source-catalog/91/user-state", `{"rating":0,"note":"Example retained note"}`, 200)
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	p := f.drain(t)
	if p["status"] != "paused" || p["pauseReason"] != "low_disk_space" || p["indexedEntries"] != float64(0) || p["observedEntries"] != float64(1) {
		t.Fatalf("low space admitted new publication: %#v", p)
	}
	if list := f.request(t, http.MethodGet, "/works", "", 200); list["total"] != float64(1) {
		t.Fatal("low space created new catalog entries")
	}
	assertReview(t, reviewRequest(t, f.handler, f.cookie, http.MethodGet, "/api/source-catalog/91", "", 200), float64(0), "Example retained note")
	f.server.catalogDiskSpace = func(string) (diskSpaceSnapshot, error) { return diskSpaceSnapshot{Available: 4 << 30}, nil }
	f.request(t, http.MethodPost, "/catalog/index", `{}`, 202)
	p = f.drain(t)
	if p["status"] != "complete" || p["indexedEntries"] != float64(1) {
		t.Fatalf("low-space resume failed: %#v", p)
	}
}

func TestSourceCatalogIndexCancelStopsConcurrentRequestsAndResumes(t *testing.T) {
	started := make(chan struct{}, 2)
	var ready atomic.Bool
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&input)
		items := []map[string]any{{"name": "Example A", "is_dir": true}, {"name": "Example B", "is_dir": true}}
		if input.Path != "/" {
			if !ready.Load() {
				started <- struct{}{}
				<-r.Context().Done()
				return
			}
			items = []map[string]any{{"name": "track.mp3"}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": len(items)}})
	}))
	queued := f.request(t, http.MethodPost, "/catalog/index", `{"concurrency":2,"intervalMs":100}`, 202)
	done := make(chan error, 1)
	go func() { done <- f.server.runNextQueuedWorkflowJob(context.Background()) }()
	for range 2 {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("parallel requests did not start")
		}
	}
	id := strconv.Itoa(int(queued["runId"].(float64)))
	reviewRequest(t, f.handler, f.cookie, http.MethodPost, "/api/workflow-runs/"+id+"/cancel", `{}`, 200)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not release requests")
	}
	p := f.request(t, http.MethodGet, "/catalog/index", "", 200)
	if p["status"] != "cancelled" || p["completedDirectories"] != float64(1) {
		t.Fatalf("cancel lost committed directory: %#v", p)
	}
	ready.Store(true)
	f.request(t, http.MethodPost, "/catalog/index", `{}`, 202)
	p = f.drain(t)
	if p["status"] != "complete" || p["indexedEntries"] != float64(2) {
		t.Fatalf("cancel resume lost entries: %#v", p)
	}
}

func newCatalogIndexFixture(t *testing.T, upstream http.Handler) catalogIndexFixture {
	t.Helper()
	remote := httptest.NewServer(upstream)
	t.Cleanup(remote.Close)
	db := openMigratedTestDB(t)
	s := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	createTestAdministrator(t, s, "synthetic-user", "synthetic-password")
	h := s.Routes()
	cookie := loginTestSession(t, h, "synthetic-user", "synthetic-password")
	input := mustJSON(map[string]any{"displayName": "Example Remote A", "sourceType": "openlist", "enabled": true, "config": map[string]any{"openListRoots": []string{"/Example"}}, "endpoint": map[string]any{"apiUrl": remote.URL, "baseUrl": remote.URL}})
	source := reviewRequest(t, h, cookie, http.MethodPost, "/api/file-sources", input, 201)
	return catalogIndexFixture{s, h, cookie, "/api/remote-sources/" + strconv.Itoa(int(source["id"].(float64)))}
}

func (f catalogIndexFixture) request(t *testing.T, method, suffix, body string, code int) map[string]any {
	t.Helper()
	return reviewRequest(t, f.handler, f.cookie, method, f.base+suffix, body, code)
}
func (f catalogIndexFixture) drain(t *testing.T) map[string]any {
	t.Helper()
	for range 100 {
		if err := f.server.runNextQueuedWorkflowJob(context.Background()); err != nil {
			t.Fatal(err)
		}
		p := f.request(t, http.MethodGet, "/catalog/index", "", 200)
		if p["status"] != "queued" && p["status"] != "running" {
			return p
		}
	}
	t.Fatal("scan did not settle")
	return nil
}

func TestSourceCatalogIndexConfiguredConcurrencyAndSharedRate(t *testing.T) {
	for _, concurrency := range []int{1, 2} {
		t.Run(strconv.Itoa(concurrency), func(t *testing.T) {
			var mu sync.Mutex
			active, maximum := 0, 0
			starts := []time.Time{}
			f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/fs/list" {
					t.Error("unexpected request")
					http.NotFound(w, r)
					return
				}
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
				objects := []map[string]any{}
				if input.Path == "/" {
					for _, name := range []string{"Example A", "Example B", "Example C", "Example D"} {
						objects = append(objects, map[string]any{"name": name, "is_dir": true})
					}
				} else {
					delay := 350 * time.Millisecond
					if input.Path == "/Example B" {
						delay = 200 * time.Millisecond
					}
					select {
					case <-r.Context().Done():
						return
					case <-time.After(delay):
					}
					objects = append(objects, map[string]any{"name": "track.mp3", "size": 100})
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": objects, "total": len(objects)}})
			}))
			f.request(t, http.MethodPost, "/catalog/index", mustJSON(map[string]any{"concurrency": concurrency, "intervalMs": 100}), 202)
			p := f.drain(t)
			if p["status"] != "complete" || p["indexedEntries"] != float64(4) {
				t.Fatalf("out-of-order publication: %#v", p)
			}
			mu.Lock()
			defer mu.Unlock()
			if maximum != concurrency {
				t.Fatalf("maximum active requests=%d, configured=%d", maximum, concurrency)
			}
			for i := 1; i < len(starts); i++ {
				if starts[i].Sub(starts[i-1]) < 90*time.Millisecond {
					t.Fatalf("rate gap=%s", starts[i].Sub(starts[i-1]))
				}
			}
		})
	}
}

// An unconfirmed empty child is not proof that the whole generation is
// complete. Only a confirmed 404/410 range may be treated as missing.
func TestSourceCatalogIndexNonRootUnconfirmedEmptyStaysPartial(t *testing.T) {
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&input)
		items := []map[string]any{}
		switch input.Path {
		case "/":
			items = []map[string]any{{"name": "Work", "is_dir": true}, {"name": "Empty", "is_dir": true}}
		case "/Work":
			items = []map[string]any{{"name": "track.mp3", "size": 100}}
		case "/Empty":
			items = []map[string]any{}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": len(items)}})
	}))
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	p := f.drain(t)
	if p["status"] != "partial" || p["unconfirmedEmptyDirectories"] != float64(1) {
		t.Fatalf("unconfirmed empty child was reported as complete: %#v", p)
	}
}

// A resumed run with a publish backlog must not publish anything when the
// disk is already below the floor. The check precedes the first publish call.
func TestSourceCatalogIndexLowDiskBlocksTheFirstPublishOfAResumedRun(t *testing.T) {
	f := newCatalogIndexFixture(t, emptyCatalogUpstream(t))
	f.server.catalogDiskSpace = func(string) (diskSpaceSnapshot, error) { return diskSpaceSnapshot{Available: 0}, nil }
	started := f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"minFreeBytes":0}`, 202)
	runID := int64(started["runId"].(float64))
	metadataReviewExec(t, f.server.db, `INSERT INTO source_catalog_audio_root(file_source_id,generation,catalog_root)
		VALUES(1,?,'/Example Work');
	INSERT INTO source_catalog_directory(file_source_id,path,generation,partition_path,catalog_root,status,revision,next_page,expected_total)
		VALUES(1,'/Example Work',?,'/Example Work','/Example Work','complete',1,2,1);
	INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes,publish_pending)
		VALUES(1,'/Example Work','track.mp3',1,'audio',100,1);`, runID, runID)
	if err := f.server.runNextQueuedWorkflowJob(context.Background()); err != nil {
		t.Fatal(err)
	}
	p := f.request(t, http.MethodGet, "/catalog/index", "", 200)
	if p["status"] != "paused" && p["status"] != "partial" || p["pauseReason"] != "low_disk_space" {
		t.Fatalf("low disk did not stop the first publish: %#v", p)
	}
	var pending, entries int
	if err := f.server.db.QueryRow(`SELECT publish_pending FROM source_catalog_observation WHERE file_source_id=1 AND name='track.mp3'`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_entry`).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if pending != 1 || entries != 0 {
		t.Fatalf("low space published a backlog row: pending=%d entries=%d", pending, entries)
	}
}
