package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/yexca/kikoto/backend/internal/buildinfo"
	"github.com/yexca/kikoto/backend/internal/config"
	"github.com/yexca/kikoto/backend/internal/storage"
	"github.com/yexca/kikoto/backend/migrations"
)

func TestSourceCatalogIndexBoundsDirectoryEnqueueBatch(t *testing.T) {
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items := []map[string]any{}
		for n := 0; n < 201; n++ {
			items = append(items, map[string]any{"name": "Example " + strconv.Itoa(n), "is_dir": true})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": len(items)}})
	}))
	f.request(t, http.MethodPost, "/catalog/index", `{"pageSize":500,"pagesPerBatch":1,"intervalMs":100}`, 202)
	if err := f.server.runNextQueuedWorkflowJob(context.Background()); err != nil {
		t.Fatal(err)
	}
	p := f.request(t, http.MethodGet, "/catalog/index", "", 200)
	if p["directories"].(float64) > 101 || p["status"] == "complete" {
		t.Fatalf("one page expanded an unbounded child queue: %#v", p)
	}
}

func TestSourceCatalogIndexPartialRangesRetryWithoutLosingCompletedEntries(t *testing.T) {
	var mu sync.Mutex
	recovered := false
	changed := false
	reads := map[string]int{}
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Path string `json:"path"`
			Page int    `json:"page"`
			Size int    `json:"per_page"`
		}
		_ = json.NewDecoder(r.Body).Decode(&input)
		mu.Lock()
		defer mu.Unlock()
		reads[input.Path]++
		items := []map[string]any{}
		total := 0
		switch input.Path {
		case "/":
			for _, name := range []string{"A Ready", "B Restricted", "C Empty", "D Changing", "E Repeated", "F Oversized", "G Failed"} {
				items = append(items, map[string]any{"name": name, "is_dir": true})
			}
			total = len(items)
			start := min(total, (input.Page-1)*input.Size)
			items = items[start:min(total, start+input.Size)]
		case "/B Restricted":
			if !recovered {
				w.WriteHeader(403)
				return
			}
		case "/C Empty":
			if !recovered {
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": 0}})
				return
			}
		case "/F Oversized":
			if !recovered {
				_, _ = w.Write([]byte(strings.Repeat("x", 2048)))
				return
			}
		case "/G Failed":
			if !recovered {
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 500})
				return
			}
		case "/D Changing":
			total = 3
			if changed || input.Page > 1 {
				total = 4
				changed = true
			}
			if input.Page == 1 {
				items = []map[string]any{{"name": "01.mp3"}, {"name": "02.mp3"}}
			} else {
				items = []map[string]any{{"name": "03.mp3"}, {"name": "04.mp3"}}
			}
		case "/E Repeated":
			total = 3
			if input.Page == 1 || !recovered {
				items = []map[string]any{{"name": "01.mp3"}, {"name": "02.mp3"}}
			} else {
				items = []map[string]any{{"name": "03.mp3"}}
			}
		}
		if total == 0 {
			items = []map[string]any{{"name": "track.mp3"}}
			total = 1
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": total}})
	}))
	first := f.request(t, http.MethodPost, "/catalog/index", `{"pageSize":2,"concurrency":2,"intervalMs":100,"maxResponseBytes":1024}`, 202)
	p := f.drain(t)
	if p["status"] != "partial" || p["restrictedDirectories"] != float64(1) || p["unconfirmedEmptyDirectories"] != float64(1) || p["failedDirectories"] != float64(3) {
		t.Fatalf("failed coverage was hidden: %#v", p)
	}
	list := f.request(t, http.MethodGet, "/works?q=A%20Ready", "", 200)
	entry := list["works"].([]any)[0].(map[string]any)
	id := strconv.Itoa(int(entry["catalogEntryId"].(float64)))
	reviewRequest(t, f.handler, f.cookie, http.MethodPatch, "/api/source-catalog/"+id+"/user-state", `{"rating":3,"note":"Example retained note"}`, 200)
	mu.Lock()
	recovered = true
	before := reads["/A Ready"]
	mu.Unlock()
	resumed := f.request(t, http.MethodPost, "/catalog/index", `{}`, 202)
	if resumed["runId"] != first["runId"] {
		t.Fatal("retry discarded the checkpoint generation")
	}
	p = f.drain(t)
	if p["status"] != "complete" || p["indexedEntries"] != float64(7) {
		t.Fatalf("retry missed qualified entries: %#v", p)
	}
	mu.Lock()
	after := reads["/A Ready"]
	mu.Unlock()
	if after != before {
		t.Fatal("retry reread a complete directory")
	}
	assertReview(t, reviewRequest(t, f.handler, f.cookie, http.MethodGet, "/api/source-catalog/"+id, "", 200), float64(3), "Example retained note")
}

func TestSourceCatalogIndexCheckpointSurvivesRealDatabaseCloseAndReopen(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]int{}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Path string `json:"path"`
			Page int    `json:"page"`
		}
		_ = json.NewDecoder(r.Body).Decode(&input)
		mu.Lock()
		seen[input.Path+strconv.Itoa(input.Page)]++
		mu.Unlock()
		items := []map[string]any{{"name": "track.mp3"}}
		total := 1
		if input.Path == "/" {
			total = 2
			items = []map[string]any{{"name": []string{"Example A", "Example B"}[input.Page-1], "is_dir": true}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": total}})
	}))
	defer remote.Close()
	filename := filepath.Join(t.TempDir(), "library.db")
	cfg := config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", DatabasePath: filename, DataRoot: t.TempDir(), CacheRoot: t.TempDir()}
	var sourceID, runID float64
	for boot := 0; boot < 2; boot++ {
		db, err := storage.Open(filename)
		if err != nil {
			t.Fatal(err)
		}
		if err = storage.MigrateFS(db, migrations.Files, buildinfo.Version); err != nil {
			t.Fatal(err)
		}
		s := NewServer(db, cfg)
		if boot == 0 {
			createTestAdministrator(t, s, "synthetic-user", "synthetic-password")
		}
		h := s.Routes()
		cookie := loginTestSession(t, h, "synthetic-user", "synthetic-password")
		if boot == 0 {
			input := mustJSON(map[string]any{"displayName": "Example Remote A", "sourceType": "openlist", "enabled": true, "config": map[string]any{"openListRoots": []string{"/Example"}}, "endpoint": map[string]any{"apiUrl": remote.URL, "baseUrl": remote.URL}})
			sourceID = reviewRequest(t, h, cookie, http.MethodPost, "/api/file-sources", input, 201)["id"].(float64)
		}
		f := catalogIndexFixture{s, h, cookie, "/api/remote-sources/" + strconv.Itoa(int(sourceID))}
		if boot == 0 {
			runID = f.request(t, http.MethodPost, "/catalog/index", `{"pageSize":1,"pagesPerBatch":1,"intervalMs":100}`, 202)["runId"].(float64)
			if err = s.runNextQueuedWorkflowJob(context.Background()); err != nil {
				t.Fatal(err)
			}
		} else {
			p := f.request(t, http.MethodGet, "/catalog/index", "", 200)
			if p["runId"] != runID || p["requests"] != float64(1) {
				t.Fatalf("checkpoint disappeared: %#v", p)
			}
			p = f.drain(t)
			if p["status"] != "complete" || p["indexedEntries"] != float64(2) {
				t.Fatalf("restart missed entries: %#v", p)
			}
		}
		if err = s.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err = db.Close(); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if seen["/1"] != 1 {
		t.Fatal("restart repeated the completed page")
	}
}
