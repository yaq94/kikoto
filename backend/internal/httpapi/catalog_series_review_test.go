package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/yexca/kikoto/backend/internal/buildinfo"
	"github.com/yexca/kikoto/backend/internal/config"
	"github.com/yexca/kikoto/backend/internal/storage"
)

func TestCatalogSeriesPersonalReviewsRestartAndExistingSourceRelations(t *testing.T) {
	root := t.TempDir()
	filename := filepath.Join(root, "library.db")
	cfg := config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", DataRoot: filepath.Join(root, "data"), CacheRoot: filepath.Join(root, "cache")}
	if err := os.Mkdir(cfg.DataRoot, 0700); err != nil {
		t.Fatal(err)
	}
	var identities [3]float64
	for boot := 0; boot < 2; boot++ {
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
			createTestAdministrator(t, s, "synthetic-user", "synthetic-password")
			createAccountManagedTestUser(t, s, "synthetic-listener", "synthetic-password")
			metadataReviewExec(t, db, `INSERT INTO file_source(id,code,display_name,source_type) VALUES(7,'example_remote_a','Example Remote A','openlist'),(8,'example_remote_b','Example Remote B','openlist');
    INSERT INTO file_source_endpoint(file_source_id,api_url,base_url) VALUES(7,'https://a.example.invalid','https://a.example.invalid'),(8,'https://b.example.invalid','https://b.example.invalid');
    INSERT INTO source_catalog_entry(id,file_source_id,root_path,title) VALUES(1,7,'/Episode A','Episode A'),(2,8,'/Episode B','Episode B');
    INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES(1,'track.mp3','Track A','audio',128),(2,'track.mp3','Track B','audio',128);
    INSERT INTO catalog_series(id,title) VALUES(1,'Example series');
    INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence) VALUES(1,1,1,'manual'),(1,2,2,'manual');`)
		}
		admin := workReferenceLogin(t, app, "synthetic-user")
		listener := workReferenceLogin(t, app, "synthetic-listener")
		if boot == 0 {
			workReferenceRequest(t, app, listener, "PATCH", "/api/catalog-series/1/user-state", `{"rating":5}`, 403, nil)
			var untouched map[string]any
			workReferenceRequest(t, app, listener, "GET", "/api/catalog-series/1", "", 200, &untouched)
			if untouched["workId"] != nil {
				t.Fatal("denied registration created a work")
			}
		}
		routes := []string{"/api/catalog-series/1", "/api/source-catalog/1", "/api/source-catalog/2"}
		for i, route := range routes {
			if boot == 0 {
				var saved map[string]any
				workReferenceRequest(t, app, admin, "PATCH", route+"/user-state", mustJSON(map[string]any{"rating": i + 1, "note": "Owner " + strconv.Itoa(i)}), 200, &saved)
				identities[i] = saved["workId"].(float64)
				workReferenceRequest(t, app, listener, "PATCH", route+"/user-state", mustJSON(map[string]any{"rating": 5 - i, "note": "Listener " + strconv.Itoa(i)}), 200, nil)
			}
			for _, actor := range []struct {
				cookie *http.Cookie
				rating int
				note   string
			}{{admin, i + 1, "Owner " + strconv.Itoa(i)}, {listener, 5 - i, "Listener " + strconv.Itoa(i)}} {
				var current map[string]any
				workReferenceRequest(t, app, actor.cookie, "GET", route, "", 200, &current)
				assertReview(t, current, float64(actor.rating), actor.note)
				if current["title"] != []string{"Example series", "Episode A", "Episode B"}[i] {
					t.Fatal("personal review changed public metadata")
				}
				if current["workId"] != identities[i] {
					t.Fatalf("identity changed: %#v", current)
				}
			}
		}
		if identities[0] == identities[1] || identities[1] == identities[2] || identities[0] == identities[2] {
			t.Fatal("series and episode evaluations share identity")
		}
		if boot == 0 {
			// Existing source relation fixture; admission/matching is owned by later tickets.
			if _, err = db.Exec(`INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,work_id) VALUES(3,8,'/Alternative A','Alternative A',?)`, identities[1]); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(`INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES(3,'track.mp3','Track A copy','audio',128)`); err != nil {
				t.Fatal(err)
			}
			for _, change := range []struct {
				patch  string
				rating any
				note   string
			}{
				{`{"rating":4}`, float64(4), "Owner 0"},
				{`{"note":"Updated series"}`, float64(4), "Updated series"},
				{`{"rating":null}`, nil, "Updated series"},
				{`{"note":""}`, nil, ""},
			} {
				workReferenceRequest(t, app, admin, "PATCH", "/api/catalog-series/1/user-state", change.patch, 200, nil)
				var partial map[string]any
				workReferenceRequest(t, app, admin, "GET", "/api/catalog-series/1", "", 200, &partial)
				assertReview(t, partial, change.rating, change.note)
			}
			var cleared map[string]any
			workReferenceRequest(t, app, admin, "GET", "/api/catalog-series/1", "", 200, &cleared)
			assertReview(t, cleared, nil, "")
			workReferenceRequest(t, app, admin, "PATCH", "/api/catalog-series/1/user-state", `{"rating":1,"note":"Owner 0"}`, 200, nil)
			workReferenceRequest(t, app, admin, "PATCH", "/api/source-catalog/3/user-state", `{"note":"Source change"}`, 200, nil)
			var same map[string]any
			workReferenceRequest(t, app, admin, "GET", "/api/source-catalog/1", "", 200, &same)
			assertReview(t, same, float64(2), "Source change")
			workReferenceRequest(t, app, admin, "PATCH", "/api/source-catalog/3/user-state", `{"note":"Owner 1"}`, 200, nil)
			for _, body := range []string{`{"rating":6}`, `{"rating":1.5}`, `{"note":null}`, `{}`, `{"rating":1,"userId":999}`} {
				workReferenceRequest(t, app, admin, "PATCH", "/api/catalog-series/1/user-state", body, 400, nil)
			}
			workReferenceRequest(t, app, nil, "PATCH", "/api/catalog-series/1/user-state", `{"rating":1}`, 401, nil)
			workReferenceRequest(t, app, admin, "PATCH", "/api/access-policy", `{"anonymousAccessEnabled":true}`, 200, nil)
			var public map[string]any
			workReferenceRequest(t, app, nil, "GET", "/api/catalog-series/1", "", 200, &public)
			assertReview(t, public, nil, "")
		}
		for _, sid := range []string{"7", "8"} {
			var cards map[string]any
			workReferenceRequest(t, app, admin, "GET", "/api/remote-sources/"+sid+"/works", "", 200, &cards)
			found := false
			for _, raw := range cards["works"].([]any) {
				if raw.(map[string]any)["seriesId"] == float64(1) {
					found = true
				}
			}
			if !found {
				t.Fatalf("existing series not shared across source %s", sid)
			}
		}
		var library, runs map[string]any
		workReferenceRequest(t, app, admin, "GET", "/api/works", "", 200, &library)
		if library["total"] != float64(3) {
			t.Fatalf("registration did not create exactly three objects: %#v", library)
		}
		workReferenceRequest(t, app, admin, "GET", "/api/workflow-runs", "", 200, &runs)
		if runs["total"] != float64(0) {
			t.Fatal("saving reviews queued media work")
		}
		files, err := os.ReadDir(cfg.DataRoot)
		if err != nil || len(files) != 0 {
			t.Fatalf("review wrote managed media: %v %v", files, err)
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
