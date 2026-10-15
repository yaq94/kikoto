package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yexca/kikoto/backend/internal/config"
)

func TestWorkMatchingBudgetDoesNotHideLaterCandidates(t *testing.T) {
	db := openMigratedTestDB(t)
	s := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-editor", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	defer func() { _ = s.Shutdown(context.Background()) }()
	createTestAdministrator(t, s, "synthetic-editor", "synthetic-password")
	metadataReviewExec(t, db, `INSERT INTO work(id,primary_code,title,release_date) VALUES(11,'','Example Work','2020-01-02');
	WITH RECURSIVE n(x) AS (VALUES(12) UNION ALL SELECT x+1 FROM n WHERE x<113) INSERT INTO work(id,primary_code,title,release_date) SELECT x,'','Example Work','2020-01-02' FROM n;
	INSERT INTO person(id,display_name) VALUES(1,'Example Voice'); INSERT INTO work_credit(work_id,person_id,role) VALUES(11,1,'voice_actor'),(12,1,'voice_actor'),(113,1,'voice_actor');`)
	app := httptest.NewServer(s.Routes())
	defer app.Close()
	cookie := workReferenceLogin(t, app, "synthetic-editor")
	var result map[string]any
	workReferenceRequest(t, app, cookie, "POST", "/api/work-matching", `{"kind":"work","id":11}`, 200, &result)
	if result["status"] != "paused" || result["rule"] != "candidate_limit" {
		t.Fatalf("truncated range became unique: %#v", result)
	}
	workReferenceRequest(t, app, cookie, "PUT", "/api/work-matching/settings", `{"candidateLimit":200}`, 200, nil)
	workReferenceRequest(t, app, cookie, "POST", "/api/work-matching", `{"kind":"work","id":11}`, 200, &result)
	if result["status"] != "review" || result["rule"] != "ambiguous_evidence" || result["candidateCount"] != float64(102) {
		t.Fatalf("later candidate disappeared: %#v", result)
	}
	var ops []any
	workReferenceRequest(t, app, cookie, "GET", "/api/shared-associations?kind=work&id=11", "", 200, &ops)
	if len(ops) != 0 {
		t.Fatal("budget pause created a partial association")
	}
}

func TestWorkMatchingConcurrentRequestsShareOnePendingOperation(t *testing.T) {
	db := openMigratedTestDB(t)
	s := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-editor", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	defer func() { _ = s.Shutdown(context.Background()) }()
	createTestAdministrator(t, s, "synthetic-editor", "synthetic-password")
	metadataReviewExec(t, db, `INSERT INTO work(id,primary_code,title,release_date) VALUES(11,'','Example Work','2020-01-02'),(12,'','Example Work','2020-01-02'); INSERT INTO person(id,display_name) VALUES(1,'Example Voice'); INSERT INTO work_credit(work_id,person_id,role) VALUES(11,1,'voice_actor'),(12,1,'voice_actor');`)
	app := httptest.NewServer(s.Routes())
	defer app.Close()
	cookie := workReferenceLogin(t, app, "synthetic-editor")
	workReferenceRequest(t, app, cookie, "PATCH", "/api/works/11/user-state", `{"note":"Example left"}`, 200, nil)
	workReferenceRequest(t, app, cookie, "PATCH", "/api/works/12/user-state", `{"note":"Example right"}`, 200, nil)
	results := make(chan int, 8)
	for range 8 {
		go func() {
			req, _ := http.NewRequest("POST", app.URL+"/api/work-matching", strings.NewReader(`{"kind":"work","id":11}`))
			req.Header.Set("Content-Type", "application/json")
			req.AddCookie(cookie)
			res, err := app.Client().Do(req)
			if err != nil {
				results <- 0
				return
			}
			_, _ = io.Copy(io.Discard, res.Body)
			_ = res.Body.Close()
			results <- res.StatusCode
		}()
	}
	for range 8 {
		if status := <-results; status != 200 && status != 429 {
			t.Fatalf("unexpected concurrent status %d", status)
		}
	}
	var result map[string]any
	workReferenceRequest(t, app, cookie, "POST", "/api/work-matching", `{"kind":"work","id":12}`, 200, &result)
	if result["status"] != "pending" {
		t.Fatal("reverse refresh bypassed owner conflict")
	}
	var ops []any
	workReferenceRequest(t, app, cookie, "GET", "/api/shared-associations?kind=work&id=11", "", 200, &ops)
	if len(ops) != 1 {
		t.Fatalf("duplicate automatic operations: %#v", ops)
	}
}

func TestWorkMatchingWorkerBacksOffAndRecoversFailedTransaction(t *testing.T) {
	db := openMigratedTestDB(t)
	s := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-editor", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	defer func() { _ = s.Shutdown(context.Background()) }()
	createTestAdministrator(t, s, "synthetic-editor", "synthetic-password")
	metadataReviewExec(t, db, `INSERT INTO work(id,primary_code,title,release_date) VALUES(11,'','Example Work','2020-01-02'),(12,'','Example Work','2020-01-02'); INSERT INTO person(id,display_name) VALUES(1,'Example Voice'); INSERT INTO work_credit(work_id,person_id,role) VALUES(11,1,'voice_actor'),(12,1,'voice_actor');
	CREATE TRIGGER synthetic_matching_failure BEFORE INSERT ON shared_association BEGIN SELECT RAISE(ABORT,'synthetic failure'); END;`)
	var ticks atomic.Int64
	ticks.Store(time.Now().Unix())
	s.catalogNow = func() time.Time { return time.Unix(ticks.Load(), 0) }
	app := httptest.NewServer(s.Routes())
	defer app.Close()
	cookie := workReferenceLogin(t, app, "synthetic-editor")
	workReferenceRequest(t, app, cookie, "PUT", "/api/work-matching/settings", `{"intervalMs":100}`, 200, nil)
	s.Go(s.RunWorkMatchingWorker)
	wait := func(expected string) map[string]any {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			var status map[string]any
			workReferenceRequest(t, app, cookie, "GET", "/api/work-matching/status", "", 200, &status)
			if status["status"] == expected {
				return status
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("worker did not reach %s", expected)
		return nil
	}
	status := wait("retrying")
	if status["retryAfter"].(float64) <= float64(ticks.Load()) {
		t.Fatal("retry did not back off")
	}
	var ops []any
	workReferenceRequest(t, app, cookie, "GET", "/api/shared-associations?kind=work&id=11", "", 200, &ops)
	if len(ops) != 0 {
		t.Fatal("failed transaction published association")
	}
	metadataReviewExec(t, db, `DROP TRIGGER synthetic_matching_failure`)
	ticks.Add(31)
	wait("complete")
	workReferenceRequest(t, app, cookie, "GET", "/api/shared-associations?kind=work&id=11", "", 200, &ops)
	if len(ops) != 1 {
		t.Fatal("retry did not complete unique association")
	}
}

func TestWorkMatchingLowDiskRetainsReviewsAndResumes(t *testing.T) {
	db := openMigratedTestDB(t)
	s := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-editor", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	defer func() { _ = s.Shutdown(context.Background()) }()
	createTestAdministrator(t, s, "synthetic-editor", "synthetic-password")
	var low atomic.Bool
	low.Store(true)
	s.catalogDiskSpace = func(string) (diskSpaceSnapshot, error) {
		if low.Load() {
			return diskSpaceSnapshot{Available: 0}, nil
		}
		return diskSpaceSnapshot{Available: 4 << 30}, nil
	}
	metadataReviewExec(t, db, `INSERT INTO work(id,primary_code,title,release_date) VALUES(11,'','Example Work','2020-01-02'),(12,'','Example Work','2020-01-02'); INSERT INTO person(id,display_name) VALUES(1,'Example Voice'); INSERT INTO work_credit(work_id,person_id,role) VALUES(11,1,'voice_actor'),(12,1,'voice_actor');`)
	app := httptest.NewServer(s.Routes())
	defer app.Close()
	cookie := workReferenceLogin(t, app, "synthetic-editor")
	workReferenceRequest(t, app, cookie, "PATCH", "/api/works/11/user-state", `{"note":"Example retained review"}`, 200, nil)
	var result map[string]any
	workReferenceRequest(t, app, cookie, "POST", "/api/work-matching", `{"kind":"work","id":11}`, 200, &result)
	if result["status"] != "paused" || result["rule"] != "low_disk_space" {
		t.Fatalf("low disk not paused: %#v", result)
	}
	workReferenceRequest(t, app, cookie, "PATCH", "/api/works/11/user-state", `{"rating":3}`, 200, nil)
	var work map[string]any
	workReferenceRequest(t, app, cookie, "GET", "/api/works/11", "", 200, &work)
	assertReview(t, work, float64(3), "Example retained review")
	low.Store(false)
	workReferenceRequest(t, app, cookie, "POST", "/api/work-matching", `{"kind":"work","id":11}`, 200, &result)
	if result["status"] != "completed" {
		t.Fatal("matching did not resume")
	}
}
