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

// Two retries are bounded. The third failure becomes a durable failed result
// instead of an endless retry loop, and no association is published.
func TestWorkMatchingWorkerGivesUpAfterThirdFailure(t *testing.T) {
	db := openMigratedTestDB(t)
	s := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-editor", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	defer func() { _ = s.Shutdown(context.Background()) }()
	createTestAdministrator(t, s, "synthetic-editor", "synthetic-password")
	metadataReviewExec(t, db, `INSERT INTO work(id,primary_code,title,release_date) VALUES(11,'','Example Work','2020-01-02'),(12,'','Example Work','2020-01-02'); INSERT INTO person(id,display_name) VALUES(1,'Example Voice'); INSERT INTO work_credit(work_id,person_id,role) VALUES(11,1,'voice_actor'),(12,1,'voice_actor');
	CREATE TRIGGER synthetic_matching_failure BEFORE INSERT ON shared_association BEGIN SELECT RAISE(ABORT,'synthetic failure'); END;`)
	var now atomic.Int64
	now.Store(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC).Unix())
	s.catalogNow = func() time.Time { return time.Unix(now.Load(), 0) }
	options := workMatchingOptions{IntervalMS: 100, CandidateLimit: 100, MinFreeBytes: 0}
	var status string
	for pass := 0; pass < 20; pass++ {
		if err := s.advanceWorkMatching(context.Background(), options); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT status FROM work_matching_control WHERE id=1`).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status == "failed" {
			break
		}
		now.Add(121)
	}
	if status != "failed" {
		t.Fatalf("worker never reached a terminal failed state; last status=%s", status)
	}
	var cursorKind string
	var cursorID int64
	var attempts int
	if err := db.QueryRow(`SELECT cursor_kind,cursor_id,attempts FROM work_matching_control WHERE id=1`).Scan(&cursorKind, &cursorID, &attempts); err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ {
		now.Add(121)
		if err := s.advanceWorkMatching(context.Background(), options); err != nil {
			t.Fatal(err)
		}
		var nextStatus, nextKind string
		var nextCursor int64
		var nextAttempts int
		if err := db.QueryRow(`SELECT status,cursor_kind,cursor_id,attempts FROM work_matching_control WHERE id=1`).Scan(&nextStatus, &nextKind, &nextCursor, &nextAttempts); err != nil {
			t.Fatal(err)
		}
		if nextStatus != "failed" || nextKind != cursorKind || nextCursor != cursorID || nextAttempts != attempts {
			t.Fatalf("failed state retried on pass %d: status=%s cursor=%s/%d attempts=%d", pass, nextStatus, nextKind, nextCursor, nextAttempts)
		}
	}
	for _, id := range []int64{11, 12} {
		var raw string
		if err := db.QueryRow(`SELECT result_json FROM work_matching_result WHERE kind='work' AND entity_id=?`, id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(raw, `"status":"failed"`) {
			t.Fatalf("third failure for work %d did not become terminal: %s", id, raw)
		}
	}
	var ops int
	if err := db.QueryRow(`SELECT count(*) FROM shared_association`).Scan(&ops); err != nil || ops != 0 {
		t.Fatalf("failed retries published an association: %d %v", ops, err)
	}
}

// The daily automatic-operation budget is a hard pause, not a partial match.
func TestWorkMatchingPausesAfterDailyOperationBudget(t *testing.T) {
	db := openMigratedTestDB(t)
	s := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-editor", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	defer func() { _ = s.Shutdown(context.Background()) }()
	createTestAdministrator(t, s, "synthetic-editor", "synthetic-password")
	metadataReviewExec(t, db, `INSERT INTO work(id,primary_code,title,release_date) VALUES(11,'','Example Work','2020-01-02'),(12,'','Example Work','2020-01-02');
	INSERT INTO person(id,display_name) VALUES(1,'Example Voice'); INSERT INTO work_credit(work_id,person_id,role) VALUES(11,1,'voice_actor'),(12,1,'voice_actor');
	WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<1000)
	INSERT INTO shared_association(id,operation,status,version,fingerprint,snapshot_json,created_at)
	SELECT x,'link','completed','v','f','{}',datetime('now') FROM n;
	INSERT INTO work_matching_operation(operation_id,subject_kind,subject_id,evidence_fingerprint,rule)
	SELECT id,'work',1,'f','product_code' FROM shared_association;`)
	app := httptest.NewServer(s.Routes())
	defer app.Close()
	cookie := workReferenceLogin(t, app, "synthetic-editor")
	var result map[string]any
	workReferenceRequest(t, app, cookie, "POST", "/api/work-matching", `{"kind":"work","id":11}`, 200, &result)
	if result["status"] != "paused" || result["rule"] != "operation_limit" {
		t.Fatalf("daily budget was not enforced: %#v", result)
	}
}

// Serialized candidate evidence is bounded at 2 MiB. A range that would exceed
// it pauses instead of choosing a unique candidate from a truncated set. The
// fixture crosses the ceiling with few, large candidates so the scan stays
// inside the five-second evaluation budget even under the race detector.
func TestWorkMatchingPausesAtEvidenceByteLimit(t *testing.T) {
	db := openMigratedTestDB(t)
	s := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-editor", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	defer func() { _ = s.Shutdown(context.Background()) }()
	createTestAdministrator(t, s, "synthetic-editor", "synthetic-password")
	longTitle := strings.Repeat("x", 40<<10)
	metadataReviewExec(t, db, `INSERT INTO work(id,primary_code,title,release_date) VALUES(11,'','`+longTitle+`','2020-01-02');
	INSERT INTO person(id,display_name) VALUES(1,'Example Voice'); INSERT INTO work_credit(work_id,person_id,role) VALUES(11,1,'voice_actor');
	WITH RECURSIVE n(x) AS (VALUES(12) UNION ALL SELECT x+1 FROM n WHERE x<71)
	INSERT INTO work(id,primary_code,title,release_date) SELECT x,'','`+longTitle+`','2020-01-02' FROM n;
	INSERT INTO work_credit(work_id,person_id,role) SELECT id,1,'voice_actor' FROM work WHERE id BETWEEN 12 AND 71;`)
	app := httptest.NewServer(s.Routes())
	defer app.Close()
	cookie := workReferenceLogin(t, app, "synthetic-editor")
	workReferenceRequest(t, app, cookie, "PUT", "/api/work-matching/settings", `{"candidateLimit":300}`, 200, nil)
	var result map[string]any
	workReferenceRequest(t, app, cookie, "POST", "/api/work-matching", `{"kind":"work","id":11}`, 200, &result)
	if result["status"] != "paused" || result["rule"] != "metadata_limit" {
		t.Fatalf("evidence byte limit was not enforced: %#v", result)
	}
}
