package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSourceCatalogIndexRootlessConfigurationPermissionsAndLimits(t *testing.T) {
	f := newCatalogIndexFixture(t, http.NotFoundHandler())
	createAccountManagedTestUser(t, f.server, "synthetic-listener", "synthetic-password")
	listener := loginTestSession(t, f.handler, "synthetic-listener", "synthetic-password")
	for _, method := range []string{http.MethodPost, http.MethodGet} {
		reviewRequest(t, f.handler, listener, method, f.base+"/catalog/index", `{}`, 403)
		reviewRequest(t, f.handler, nil, method, f.base+"/catalog/index", `{}`, 401)
	}
	for _, body := range []string{`{"concurrency":0}`, `{"concurrency":5}`, `{"intervalMs":0}`, `{"maxResponseBytes":999}`, `{"pageSize":501}`, `{"pagesPerBatch":33}`, `{"minFreeBytes":-1}`, `{"maxPagesPerDirectory":0}`, `{"unexpected":true}`, `{} {}`} {
		f.request(t, http.MethodPost, "/catalog/index", body, 400)
	}
	if p := f.request(t, http.MethodGet, "/catalog/index", "", 200); p["status"] != "idle" {
		t.Fatal("invalid options created a run")
	}
	reviewRequest(t, f.handler, f.cookie, http.MethodPost, "/api/file-sources", `{"displayName":"Example Remote B","sourceType":"openlist","enabled":true,"config":{},"endpoint":{"apiUrl":"https://source.example.invalid","baseUrl":"https://source.example.invalid"}}`, 201)
	// Demo's global mutation boundary must also cover the new route.
	f.server.cfg.Mode = "demo"
	reviewRequest(t, f.server.Routes(), f.cookie, http.MethodPost, f.base+"/catalog/index", `{}`, 401)
}

func TestSourceCatalogIndexConcurrentStartsJoinOneDurableRun(t *testing.T) {
	f := newCatalogIndexFixture(t, http.NotFoundHandler())
	app := httptest.NewServer(f.handler)
	defer app.Close()
	type result struct {
		code  int
		runID float64
		err   error
	}
	start := make(chan struct{})
	results := make(chan result, 6)
	for range 6 {
		go func() {
			<-start
			request, err := http.NewRequest(http.MethodPost, app.URL+f.base+"/catalog/index", strings.NewReader(`{}`))
			if err != nil {
				results <- result{err: err}
				return
			}
			request.Header.Set("Content-Type", "application/json")
			request.AddCookie(f.cookie)
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				results <- result{err: err}
				return
			}
			defer func() { _ = response.Body.Close() }()
			var body struct {
				RunID float64 `json:"runId"`
			}
			err = json.NewDecoder(response.Body).Decode(&body)
			results <- result{response.StatusCode, body.RunID, err}
		}()
	}
	close(start)
	var id float64
	for range 6 {
		r := <-results
		if r.err != nil || r.code != 202 {
			t.Fatalf("concurrent start: status=%d err=%v", r.code, r.err)
		}
		if id == 0 {
			id = r.runID
		}
		if r.runID != id {
			t.Fatal("concurrent starts created separate runs")
		}
	}
}

func TestSourceCatalogIndexCheckpointSurvivesWorkflowHistoryCleanup(t *testing.T) {
	f := newCatalogIndexFixture(t, http.NotFoundHandler())
	queued := f.request(t, http.MethodPost, "/catalog/index", `{}`, 202)
	runID := int64(queued["runId"].(float64))
	reviewRequest(t, f.handler, f.cookie, http.MethodPost, "/api/workflow-runs/"+strconv.FormatInt(runID, 10)+"/cancel", `{}`, 200)
	metadataReviewExec(t, f.server.db, `UPDATE workflow_run SET finished_at='2000-01-01 00:00:00' WHERE id=?;
	INSERT INTO workflow_run(workflow_definition_id,workflow_code,display_name,status,trigger_type) SELECT workflow_definition_id,workflow_code,'Example recent run','succeeded','manual' FROM workflow_run WHERE id=?;`, runID, runID)
	reviewRequest(t, f.handler, f.cookie, http.MethodPost, "/api/maintenance/database/cleanup", `{"tasks":["old_runs"]}`, 200)
	p := f.request(t, http.MethodGet, "/catalog/index", "", 200)
	if p["runId"] != float64(runID) || p["status"] != "cancelled" {
		t.Fatal("history cleanup removed the source checkpoint")
	}
	resumed := f.request(t, http.MethodPost, "/catalog/index", `{}`, 202)
	if resumed["runId"] != float64(runID) {
		t.Fatal("history cleanup lost the resumable run")
	}
}

func TestSourceCatalogIndexNewGenerationReclaimsStagingButRetainsCards(t *testing.T) {
	var generation atomic.Int32
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&input)
		items := []map[string]any{{"name": "track.mp3"}}
		if input.Path == "/" {
			name := "Example A"
			if generation.Load() > 0 {
				name = "Example B"
			}
			items = []map[string]any{{"name": name, "is_dir": true}}
		}
		if input.Path == "/Example A" {
			items = nil
			for n := 0; n < 201; n++ {
				items = append(items, map[string]any{"name": "track " + strconv.Itoa(n) + ".mp3"})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": len(items)}})
	}))
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"pagesPerBatch":1,"pageSize":500}`, 202)
	first := f.drain(t)
	list := f.request(t, http.MethodGet, "/works", "", 200)
	entry := list["works"].([]any)[0].(map[string]any)
	id := strconv.Itoa(int(entry["catalogEntryId"].(float64)))
	reviewRequest(t, f.handler, f.cookie, http.MethodPatch, "/api/source-catalog/"+id+"/user-state", `{"rating":4,"note":"Example retained note"}`, 200)
	generation.Store(1)
	f.request(t, http.MethodPost, "/catalog/index", `{}`, 202)
	p := first
	for n := 0; n < 30; n++ {
		before := p["stagingObservations"].(float64)
		if err := f.server.runNextQueuedWorkflowJob(context.Background()); err != nil {
			t.Fatal(err)
		}
		p = f.request(t, http.MethodGet, "/catalog/index", "", 200)
		if before-p["stagingObservations"].(float64) > 100 {
			t.Fatal("staging cleanup exceeded its batch bound")
		}
		if p["status"] == "complete" {
			break
		}
	}
	if p["status"] != "complete" || p["stagingDirectories"] != float64(2) || p["stagingObservations"] != float64(2) {
		t.Fatalf("obsolete staging retained: %#v", p)
	}
	if list = f.request(t, http.MethodGet, "/works", "", 200); list["total"] != float64(2) {
		t.Fatal("staging cleanup removed retained cards")
	}
	assertReview(t, reviewRequest(t, f.handler, f.cookie, http.MethodGet, "/api/source-catalog/"+id, "", 200), float64(4), "Example retained note")
	metadataReviewExec(t, f.server.db, `UPDATE workflow_run SET finished_at='2000-01-01 00:00:00' WHERE id=?`, first["runId"])
	reviewRequest(t, f.handler, f.cookie, http.MethodPost, "/api/maintenance/database/cleanup", `{"tasks":["old_runs"]}`, 200)
	reviewRequest(t, f.handler, f.cookie, http.MethodGet, "/api/workflow-runs/"+strconv.Itoa(int(first["runId"].(float64))), "", 404)
}

func TestSourceCatalogIndexRefusesMetadataRedirectAndKeepsFailureVisible(t *testing.T) {
	var redirected atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1); w.WriteHeader(200) }))
	defer other.Close()
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	p := f.drain(t)
	if redirected.Load() != 0 || p["status"] != "partial" || p["failedDirectories"] != float64(1) {
		t.Fatalf("redirect accepted or hidden: %#v", p)
	}
}

func TestSourceCatalogIndexPageLimitCanBeRaisedWithoutFalseCompletion(t *testing.T) {
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Path string `json:"path"`
			Page int    `json:"page"`
		}
		_ = json.NewDecoder(r.Body).Decode(&input)
		items := []map[string]any{{"name": "track.mp3"}}
		total := 1
		if input.Path == "/" {
			total = 2
			items = []map[string]any{{"name": []string{"Example A", "Example B"}[input.Page-1], "is_dir": true}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": total}})
	}))
	f.request(t, http.MethodPost, "/catalog/index", `{"pageSize":1,"maxPagesPerDirectory":1,"intervalMs":100}`, 202)
	p := f.drain(t)
	if p["status"] != "partial" || p["failedDirectories"] != float64(1) {
		t.Fatalf("limit counted as complete: %#v", p)
	}
	details := p["details"].([]any)
	if details[0].(map[string]any)["errorCode"] != "page_limit" {
		t.Fatal("page limit not explained")
	}
	f.request(t, http.MethodPost, "/catalog/index", `{"maxPagesPerDirectory":10}`, 202)
	p = f.drain(t)
	if p["status"] != "complete" || p["indexedEntries"] != float64(2) {
		t.Fatal("raised limit did not recover the range")
	}
	if err := f.server.runNextQueuedWorkflowJob(context.Background()); err != nil {
		t.Fatal(err)
	}
}
