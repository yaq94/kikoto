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
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	p := f.drain(t)
	if p["status"] != "paused" || p["pauseReason"] != "low_disk_space" || p["indexedEntries"] != float64(0) || p["observedEntries"] != float64(1) {
		t.Fatalf("low space admitted new publication: %#v", p)
	}
	if list := f.request(t, http.MethodGet, "/works", "", 200); list["total"] != float64(0) {
		t.Fatal("low space created new catalog entries")
	}
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
