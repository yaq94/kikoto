package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
)

// A rescan that only resumes unresolved directories keeps the admission
// decisions of the failed generation: a directory that was completed under the
// old rules is never observed again. An explicit force starts a new generation
// so every directory is read and re-admitted, while an active run still wins so
// one source never runs two full crawls at once.
func TestSourceCatalogForceRescanObservesCompletedDirectoriesAgain(t *testing.T) {
	var legacy atomic.Bool
	legacy.Store(true)
	var requests atomic.Int64
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var in struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		items := []map[string]any{}
		switch in.Path {
		case "/":
			items = []map[string]any{{"name": "Example Voice", "is_dir": true}}
		case "/Example Voice":
			items = []map[string]any{{"name": "loose.mp3", "size": 100}}
			if !legacy.Load() {
				items = append(items, map[string]any{"name": "[RJ00000045]Example Work 7", "is_dir": true})
			}
		case "/Example Voice/[RJ00000045]Example Work 7":
			items = []map[string]any{{"name": "coded.mp3", "size": 100}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": len(items)}})
	}))

	first := f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if first["existing"] != false {
		t.Fatalf("first scan joined an existing run: %#v", first)
	}
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("first scan = %#v", p)
	}
	if cards := f.request(t, http.MethodGet, "/works?q=RJ00000045", "", 200); cards["total"] != float64(0) {
		t.Fatalf("legacy scan published the coded child: %#v", cards)
	}

	legacy.Store(false)
	requests.Store(0)
	second := f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"force":true}`, 202)
	if second["runId"] == first["runId"] {
		t.Fatalf("force rescan reused generation %v: %#v", first["runId"], second)
	}
	if second["existing"] != false {
		t.Fatalf("force rescan reported existing: %#v", second)
	}
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("force rescan = %#v", p)
	}
	if requests.Load() == 0 {
		t.Fatal("force rescan did not observe the tree")
	}
	cards := f.request(t, http.MethodGet, "/works?q=RJ00000045", "", 200)
	if cards["total"] != float64(1) {
		t.Fatalf("force rescan search=%v, want the coded child: %s", cards["total"], mustJSON(cards))
	}
	if titles := cardTitles(cards); !containsString(titles, "[RJ00000045]Example Work 7") {
		t.Fatalf("force rescan titles=%v, want the coded child card", titles)
	}
}

// An active run is never duplicated, even when the caller asks for a forced
// generation: the response names the running generation and says it was joined.
func TestSourceCatalogForceJoinsActiveRun(t *testing.T) {
	var holdFirst atomic.Bool
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	done := make(chan error, 1)
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if holdFirst.CompareAndSwap(true, false) {
			select {
			case started <- struct{}{}:
			default:
			}
			<-release
		}
		items := []map[string]any{{"name": "track.mp3", "size": 100}}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": len(items)}})
	}))
	first := f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	holdFirst.Store(true)
	go func() { done <- f.server.runNextQueuedWorkflowJob(context.Background()) }()
	<-started
	joined := f.request(t, http.MethodPost, "/catalog/index", `{"force":true}`, 202)
	if joined["runId"] != first["runId"] || joined["existing"] != true {
		t.Fatalf("force duplicated an active run: %#v", joined)
	}
	// Let the joined run finish so the fixture tears down cleanly.
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if p := f.request(t, http.MethodGet, "/catalog/index", "", 200); p["status"] != "complete" {
		t.Fatalf("joined run did not finish: %#v", p)
	}
}

func cardTitles(cards map[string]any) []string {
	titles := []string{}
	for _, item := range cards["works"].([]any) {
		titles = append(titles, item.(map[string]any)["title"].(string))
	}
	return titles
}
