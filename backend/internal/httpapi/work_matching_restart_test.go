package httpapi

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/yexca/kikoto/backend/internal/buildinfo"
	"github.com/yexca/kikoto/backend/internal/config"
	"github.com/yexca/kikoto/backend/internal/storage"
	"github.com/yexca/kikoto/backend/migrations"
)

func TestWorkMatchingWorkerResumesAndRetainsEveryOwnerDecision(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "library.db")
	cfg := config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-editor", DatabasePath: filename, DataRoot: t.TempDir(), CacheRoot: t.TempDir()}
	var operationID int
	var version any
	for boot := 0; boot < 2; boot++ {
		db, err := storage.Open(filename)
		if err != nil {
			t.Fatal(err)
		}
		if err = storage.MigrateFS(db, migrations.Files, buildinfo.Version); err != nil {
			t.Fatal(err)
		}
		s := NewServer(db, cfg)
		app := httptest.NewServer(s.Routes())
		t.Cleanup(func() { app.Close(); _ = s.Shutdown(context.Background()); _ = db.Close() })
		if boot == 0 {
			createTestAdministrator(t, s, "synthetic-editor", "synthetic-password")
			createAccountManagedTestUser(t, s, "synthetic-a", "synthetic-password")
			createAccountManagedTestUser(t, s, "synthetic-b", "synthetic-password")
			metadataReviewExec(t, db, `INSERT INTO work(id,primary_code,title,release_date) VALUES(11,'','Example Work','2020-01-02'),(12,'','Example Work','2020-01-02'); INSERT INTO person(id,display_name) VALUES(1,'Example Voice'); INSERT INTO work_credit(work_id,person_id,role) VALUES(11,1,'voice_actor'),(12,1,'voice_actor');`)
		}
		editor := workReferenceLogin(t, app, "synthetic-editor")
		a := workReferenceLogin(t, app, "synthetic-a")
		b := workReferenceLogin(t, app, "synthetic-b")
		if boot == 0 {
			for _, ref := range []string{"11", "12"} {
				workReferenceRequest(t, app, a, "PATCH", "/api/works/"+ref+"/user-state", `{"note":"Private A `+ref+`"}`, 200, nil)
				workReferenceRequest(t, app, b, "PATCH", "/api/works/"+ref+"/user-state", `{"note":"Private B `+ref+`"}`, 200, nil)
			}
			workReferenceRequest(t, app, editor, "PUT", "/api/work-matching/settings", `{"intervalMs":100}`, 200, nil)
			s.Go(s.RunWorkMatchingWorker)
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				var result map[string]any
				workReferenceRequest(t, app, editor, "GET", "/api/work-matching?kind=work&id=11", "", 200, &result)
				if result["status"] == "pending" {
					operationID = int(result["operationId"].(float64))
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if operationID == 0 {
				t.Fatal("worker did not automatically propose unique match")
			}
		}
		url := "/api/shared-associations/" + strconv.Itoa(operationID)
		var op map[string]any
		workReferenceRequest(t, app, a, "GET", url, "", 200, &op)
		if boot == 0 {
			version = op["version"]
			workReferenceRequest(t, app, a, "POST", url+"/decision", mustJSON(map[string]any{"version": version, "note": "A chosen"}), 200, &op)
			if op["status"] != "pending" {
				t.Fatal("first owner committed automatically")
			}
		} else {
			if op["status"] != "pending" || op["myDecision"] == nil {
				t.Fatal("restart lost private choice")
			}
			s.Go(s.RunWorkMatchingWorker)
			workReferenceRequest(t, app, b, "POST", url+"/decision", mustJSON(map[string]any{"version": version, "note": "B chosen"}), 200, &op)
			if op["status"] != "completed" {
				t.Fatal("last owner did not complete")
			}
			for _, ref := range []string{"11", "12"} {
				var work map[string]any
				workReferenceRequest(t, app, a, "GET", "/api/works/"+ref, "", 200, &work)
				assertReview(t, work, nil, "A chosen")
				workReferenceRequest(t, app, b, "GET", "/api/works/"+ref, "", 200, &work)
				assertReview(t, work, nil, "B chosen")
			}
		}
		app.Close()
		if err = s.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err = db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
