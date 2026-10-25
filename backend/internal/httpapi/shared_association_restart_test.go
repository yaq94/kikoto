package httpapi

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/yexca/kikoto/backend/internal/buildinfo"
	"github.com/yexca/kikoto/backend/internal/config"
	"github.com/yexca/kikoto/backend/internal/storage"
)

func TestSharedAssociationRestartStaleReviewsAndPlaybackIsolation(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "library.db")
	cfg := config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-editor", DataRoot: t.TempDir(), CacheRoot: t.TempDir()}
	var op map[string]any
	var url string
	for boot := 0; boot < 3; boot++ {
		db, err := storage.Open(filename)
		if err != nil {
			t.Fatal(err)
		}
		if err = storage.MigrateFS(db, completeMigrationFS(t), buildinfo.Version); err != nil {
			t.Fatal(err)
		}
		s := NewServer(db, cfg)
		app := httptest.NewServer(s.Routes())
		t.Cleanup(func() { app.Close(); _ = s.Shutdown(context.Background()); _ = db.Close() })
		if boot == 0 {
			createTestAdministrator(t, s, "synthetic-editor", "synthetic-password")
			createAccountManagedTestUser(t, s, "synthetic-a", "synthetic-password")
			createAccountManagedTestUser(t, s, "synthetic-b", "synthetic-password")
			metadataReviewExec(t, db, `INSERT INTO work(id,primary_code,title) VALUES(11,'RJ00000000','Example A'),(12,'RJ00000001','Example B');
			INSERT INTO file_source(id,code,display_name,source_type) VALUES(7,'example_local','Example Local','local');
			INSERT INTO media_item(id,work_id,kind,title,fingerprint) VALUES(1,11,'audio','Example audio','synthetic-a');
			INSERT INTO media_file_location(id,media_item_id,file_source_id,location_type,path,availability) VALUES(1,1,7,'local','RJ00000000/audio.wav','available');`)
		}
		editor := workReferenceLogin(t, app, "synthetic-editor")
		a := workReferenceLogin(t, app, "synthetic-a")
		b := workReferenceLogin(t, app, "synthetic-b")
		input := `{"left":{"kind":"work","id":11},"right":{"kind":"work","id":12}}`
		switch boot {
		case 0:
			for _, id := range []string{"11", "12"} {
				workReferenceRequest(t, app, a, "PATCH", "/api/works/"+id+"/user-state", `{"rating":1,"note":"A `+id+`"}`, 200, nil)
				workReferenceRequest(t, app, b, "PATCH", "/api/works/"+id+"/user-state", `{"rating":2,"note":"B `+id+`"}`, 200, nil)
			}
			workReferenceRequest(t, app, editor, "POST", "/api/shared-associations/previews", input, 201, &op)
			url = "/api/shared-associations/" + strconv.Itoa(int(op["id"].(float64)))
			workReferenceRequest(t, app, editor, "POST", url+"/start", mustJSON(map[string]any{"version": op["version"]}), 200, &op)
			workReferenceRequest(t, app, a, "POST", url+"/decision", mustJSON(map[string]any{"version": op["version"], "note": "A chosen"}), 200, &op)
		case 1:
			workReferenceRequest(t, app, a, "GET", url, "", 200, &op)
			if op["status"] != "pending" || op["myDecision"] == nil {
				t.Fatal("restart lost pending choice")
			}
			// Updating listening state leaves the review decision valid.
			workReferenceRequest(t, app, a, "PATCH", "/api/media-items/1/progress", `{"locationId":1,"positionSeconds":37,"durationSeconds":180}`, 200, nil)
			workReferenceRequest(t, app, a, "PATCH", "/api/works/11/user-state", `{"listeningStatus":"listening"}`, 200, nil)
			workReferenceRequest(t, app, a, "GET", url, "", 200, &op)
			if op["status"] != "pending" {
				t.Fatal("unrelated listening change expired review")
			}
			// A real record change, including change and restoration, expires all choices.
			workReferenceRequest(t, app, b, "PATCH", "/api/works/12/user-state", `{"note":"Changed"}`, 200, nil)
			workReferenceRequest(t, app, b, "PATCH", "/api/works/12/user-state", `{"note":"B 12"}`, 200, nil)
			workReferenceRequest(t, app, b, "POST", url+"/decision", mustJSON(map[string]any{"version": op["version"], "note": "B chosen"}), 409, nil)
			workReferenceRequest(t, app, a, "GET", url, "", 200, &op)
			if op["status"] != "stale" || op["myDecision"] != nil {
				t.Fatal("stale decision remains actionable")
			}
			workReferenceRequest(t, app, editor, "POST", "/api/shared-associations/previews", input, 201, &op)
			url = "/api/shared-associations/" + strconv.Itoa(int(op["id"].(float64)))
			workReferenceRequest(t, app, editor, "POST", url+"/start", mustJSON(map[string]any{"version": op["version"]}), 200, &op)
			workReferenceRequest(t, app, a, "POST", url+"/decision", mustJSON(map[string]any{"version": op["version"], "note": "A final"}), 200, nil)
			workReferenceRequest(t, app, b, "POST", url+"/decision", mustJSON(map[string]any{"version": op["version"], "note": "B final"}), 200, &op)
			if op["status"] != "completed" {
				t.Fatal("refreshed association not complete")
			}
			var cursor map[string]any
			workReferenceRequest(t, app, a, "GET", "/api/works/11/playback-cursor", "", 200, &cursor)
			if cursor["cursor"].(map[string]any)["positionSeconds"] != float64(37) {
				t.Fatal("association changed playback cursor")
			}
			workReferenceRequest(t, app, a, "GET", "/api/works/12/playback-cursor", "", 200, &cursor)
			if cursor["cursor"] != nil {
				t.Fatal("association copied a timeline")
			}
			workReferenceRequest(t, app, a, "PATCH", "/api/works/12/user-state", `{"rating":null,"note":""}`, 200, nil)
		default:
			for _, id := range []string{"11", "12"} {
				var work map[string]any
				workReferenceRequest(t, app, a, "GET", "/api/works/"+id, "", 200, &work)
				assertReview(t, work, nil, "")
				workReferenceRequest(t, app, b, "GET", "/api/works/"+id, "", 200, &work)
				assertReview(t, work, float64(2), "B final")
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
