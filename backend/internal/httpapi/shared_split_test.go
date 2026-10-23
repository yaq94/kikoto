package httpapi

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/yexca/kikoto/backend/internal/buildinfo"
	"github.com/yexca/kikoto/backend/internal/config"
	"github.com/yexca/kikoto/backend/internal/storage"
	"github.com/yexca/kikoto/backend/migrations"
)

func TestSharedSplitOwnerAllocationsSurviveApplicationRestart(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "library.db")
	cfg := config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-editor", DataRoot: t.TempDir(), CacheRoot: t.TempDir()}
	var split map[string]any
	var url string
	for boot := 0; boot < 3; boot++ {
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
			for _, name := range []string{"synthetic-a", "synthetic-b", "synthetic-c"} {
				createAccountManagedTestUser(t, s, name, "synthetic-password")
			}
			metadataReviewExec(t, db, `INSERT INTO work(id,primary_code,title) VALUES(11,'','Example A'),(12,'RJ00000000','Example B');
			INSERT INTO file_source(id,code,display_name,source_type) VALUES(7,'example_remote_a','Example Remote A','openlist'),(8,'example_remote_b','Example Remote B','openlist');
			INSERT INTO file_source_endpoint(file_source_id,api_url,base_url) VALUES(7,'https://a.example.invalid','https://a.example.invalid'),(8,'https://b.example.invalid','https://b.example.invalid');
			INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,work_id) VALUES(1,7,'/A','Example A',11),(2,8,'/B','Example B',12);`)
		}
		editor := workReferenceLogin(t, app, "synthetic-editor")
		a := workReferenceLogin(t, app, "synthetic-a")
		b := workReferenceLogin(t, app, "synthetic-b")
		c := workReferenceLogin(t, app, "synthetic-c")
		switch boot {
		case 0:
			workReferenceRequest(t, app, a, "PATCH", "/api/source-catalog/1/user-state", `{"rating":1,"note":"A before"}`, 200, nil)
			workReferenceRequest(t, app, b, "PATCH", "/api/source-catalog/2/user-state", `{"rating":2,"note":"B before"}`, 200, nil)
			var link map[string]any
			workReferenceRequest(t, app, editor, "POST", "/api/shared-associations/previews", `{"left":{"kind":"catalog","id":1},"right":{"kind":"catalog","id":2}}`, 201, &link)
			linkURL := "/api/shared-associations/" + strconv.Itoa(int(link["id"].(float64)))
			workReferenceRequest(t, app, editor, "POST", linkURL+"/start", mustJSON(map[string]any{"version": link["version"]}), 200, &link)
			workReferenceRequest(t, app, a, "PATCH", "/api/source-catalog/2/user-state", `{"rating":4,"note":"A after"}`, 200, nil)
			workReferenceRequest(t, app, b, "PATCH", "/api/source-catalog/1/user-state", `{"note":"B after"}`, 200, nil)
			workReferenceRequest(t, app, c, "PATCH", "/api/source-catalog/1/user-state", `{"rating":5,"note":"C new"}`, 200, nil)
			workReferenceRequest(t, app, editor, "POST", linkURL+"/split-preview", "{}", 201, &split)
			if strings.Contains(mustJSON(split), "A before") || strings.Contains(mustJSON(split), "B before") || strings.Contains(mustJSON(split), "C new") {
				t.Fatal("editor split preview leaked private records")
			}
			url = "/api/shared-splits/" + strconv.Itoa(int(split["id"].(float64)))
			workReferenceRequest(t, app, a, "POST", url+"/start", mustJSON(map[string]any{"version": split["version"]}), 403, nil)
			workReferenceRequest(t, app, editor, "POST", url+"/start", mustJSON(map[string]any{"version": split["version"]}), 200, &split)
			if split["status"] != "pending" {
				t.Fatal("split completed before owners chose")
			}
			workReferenceRequest(t, app, editor, "POST", url+"/decision", mustJSON(map[string]any{"version": split["version"], "allocation": "both"}), 403, nil)
			workReferenceRequest(t, app, a, "GET", url, "", 200, &split)
			if split["myRecordChange"] != "modified" || split["myBeforeLeft"].(map[string]any)["note"] != "A before" || split["myCurrentReview"].(map[string]any)["note"] != "A after" {
				t.Fatal("split preview does not distinguish before/current")
			}
			if preview := mustJSON(split); strings.Contains(preview, "B before") || strings.Contains(preview, "B after") || strings.Contains(preview, "C new") {
				t.Fatalf("owner %q lost their own record while seeing another user's record", "synthetic-a")
			}
			workReferenceRequest(t, app, a, "POST", url+"/decision", mustJSON(map[string]any{"version": split["version"], "allocation": "left"}), 200, &split)
		case 1:
			workReferenceRequest(t, app, a, "GET", url, "", 200, &split)
			if split["status"] != "pending" || split["myAllocation"] != "left" {
				t.Fatal("pending allocation lost after restart")
			}
			var stillShared map[string]any
			workReferenceRequest(t, app, a, "GET", "/api/source-catalog/2", "", 200, &stillShared)
			assertReview(t, stillShared, float64(4), "A after")
			workReferenceRequest(t, app, b, "POST", url+"/decision", mustJSON(map[string]any{"version": split["version"], "allocation": "right"}), 200, &split)
			if split["status"] != "pending" {
				t.Fatal("unanswered new record was discarded")
			}
			workReferenceRequest(t, app, c, "GET", url, "", 200, &split)
			if split["myRecordChange"] != "added" {
				t.Fatal("new record not identified")
			}
			workReferenceRequest(t, app, c, "POST", url+"/decision", mustJSON(map[string]any{"version": split["version"], "allocation": "both"}), 200, &split)
			if split["status"] != "completed" {
				t.Fatal("final owner did not complete split")
			}
		case 2:
			for _, row := range []struct {
				user   string
				id     string
				rating any
				note   string
			}{{"synthetic-a", "1", float64(4), "A after"}, {"synthetic-a", "2", nil, ""}, {"synthetic-b", "1", nil, ""}, {"synthetic-b", "2", float64(2), "B after"}, {"synthetic-c", "1", float64(5), "C new"}, {"synthetic-c", "2", float64(5), "C new"}} {
				cookie := workReferenceLogin(t, app, row.user)
				var entry map[string]any
				workReferenceRequest(t, app, cookie, "GET", "/api/source-catalog/"+row.id, "", 200, &entry)
				assertReview(t, entry, row.rating, row.note)
			}
			workReferenceRequest(t, app, c, "PATCH", "/api/source-catalog/2/user-state", `{"note":"Independent after split"}`, 200, nil)
			var entry map[string]any
			workReferenceRequest(t, app, c, "GET", "/api/source-catalog/1", "", 200, &entry)
			assertReview(t, entry, float64(5), "C new")
			var library map[string]any
			workReferenceRequest(t, app, editor, "GET", "/api/works", "", 200, &library)
			if library["total"] != float64(2) {
				t.Fatal("split did not restore separate cards")
			}
			for _, id := range []string{"11", "12"} {
				var dirs map[string]any
				workReferenceRequest(t, app, editor, "GET", "/api/works/"+id+"/catalog-directories", "", 200, &dirs)
				if len(dirs["directories"].([]any)) != 1 {
					t.Fatal("split did not restore original source ownership")
				}
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
