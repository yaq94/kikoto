package httpapi

import (
	"context"
	"io/fs"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/yexca/kikoto/backend/internal/buildinfo"
	"github.com/yexca/kikoto/backend/internal/config"
	"github.com/yexca/kikoto/backend/internal/storage"
	"github.com/yexca/kikoto/backend/migrations"
)

func TestCatalogSeriesUpgradePreviewRestartAndStaleConfirmation(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "library.db")
	db, err := storage.Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	old := fstest.MapFS{}
	entries, err := fs.ReadDir(migrations.Files, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") || entry.Name()[:3] > "070" {
			continue
		}
		data, err := fs.ReadFile(migrations.Files, entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		old[entry.Name()] = &fstest.MapFile{Data: data}
	}
	baseline, err := fs.ReadFile(migrations.Files, "baseline/068_v0.8.3.sql")
	if err != nil {
		t.Fatal(err)
	}
	old["baseline/068_v0.8.3.sql"] = &fstest.MapFile{Data: baseline}
	if err = storage.MigrateFS(db, old, buildinfo.Version); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", DataRoot: t.TempDir(), CacheRoot: t.TempDir()}
	server := NewServer(db, cfg)
	createTestAdministrator(t, server, "synthetic-user", "synthetic-password")
	createAccountManagedTestUser(t, server, "synthetic-listener", "synthetic-password")
	metadataReviewExec(t, db, `INSERT INTO file_source(id,code,display_name,source_type) VALUES(7,'example_remote_a','Example Remote A','openlist');
		INSERT INTO file_source_endpoint(file_source_id,api_url,base_url) VALUES(7,'https://source.example.invalid','https://source.example.invalid');
		INSERT INTO work(id,primary_code,title) VALUES(11,'','Example A'),(12,'','Example B');
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,work_id) VALUES(1,7,'/Example A','Example A',11),(2,7,'/Example B','Example B',12);
		INSERT INTO user_work_state(user_id,work_id,rating,note) SELECT id,11,4,'Private A' FROM user_account WHERE username='synthetic-user';
		INSERT INTO user_work_state(user_id,work_id,rating,note) SELECT id,12,1,'Private B' FROM user_account WHERE username='synthetic-user';
		INSERT INTO user_work_state(user_id,work_id,rating,note) SELECT id,11,2,'Other private A' FROM user_account WHERE username='synthetic-listener';`)
	if err = server.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	var previewID, seriesID int64
	for boot := 0; boot < 3; boot++ {
		db, err := storage.Open(filename)
		if err != nil {
			t.Fatal(err)
		}
		if err = storage.MigrateFS(db, migrations.Files, buildinfo.Version); err != nil {
			t.Fatal(err)
		}
		server := NewServer(db, cfg)
		app := httptest.NewServer(server.Routes())
		t.Cleanup(func() { app.Close(); _ = server.Shutdown(context.Background()); _ = db.Close() })
		admin := workReferenceLogin(t, app, "synthetic-user")
		listener := workReferenceLogin(t, app, "synthetic-listener")
		var preview catalogGroupingPreview
		body := `{"title":"Confirmed series","catalogEntryIds":[1,2]}`
		switch boot {
		case 0:
			workReferenceRequest(t, app, admin, "POST", "/api/catalog-series/previews", body, 201, &preview)
			previewID = preview.ID
		case 1:
			url := "/api/catalog-series/previews/" + strconv.FormatInt(previewID, 10)
			workReferenceRequest(t, app, admin, "GET", url, "", 200, &preview)
			if preview.Status != "pending" || len(preview.Episodes) != 2 {
				t.Fatalf("pending preview lost: %+v", preview)
			}
			workReferenceRequest(t, app, listener, "PATCH", "/api/source-catalog/1/user-state", `{"note":"Changed privately"}`, 200, nil)
			workReferenceRequest(t, app, admin, "POST", url+"/confirm", "{}", 409, nil)
			var catalog map[string]any
			workReferenceRequest(t, app, admin, "GET", "/api/source-catalog/1", "", 200, &catalog)
			if catalog["seriesId"] != nil || catalog["workId"] != float64(11) {
				t.Fatalf("stale preview changed identity: %#v", catalog)
			}
			workReferenceRequest(t, app, admin, "POST", "/api/catalog-series/previews", body, 201, &preview)
			previewID = preview.ID
			workReferenceRequest(t, app, admin, "POST", "/api/catalog-series/previews/"+strconv.FormatInt(previewID, 10)+"/confirm", "{}", 200, &preview)
			seriesID = *preview.SeriesID
		default:
			workReferenceRequest(t, app, admin, "POST", "/api/catalog-series/previews/"+strconv.FormatInt(previewID, 10)+"/confirm", "{}", 200, &preview)
			if preview.SeriesID == nil || *preview.SeriesID != seriesID {
				t.Fatal("repeated confirmation changed series identity")
			}
			var series catalogSeriesView
			workReferenceRequest(t, app, listener, "GET", "/api/catalog-series/"+strconv.FormatInt(seriesID, 10), "", 200, &series)
			if series.Title != "Confirmed series" || len(series.Episodes) != 2 || series.Episodes[0].CatalogEntryID != 1 || series.Episodes[1].CatalogEntryID != 2 {
				t.Fatalf("series lost after restart: %+v", series)
			}
		}
		for _, member := range []struct {
			id     string
			rating float64
			note   string
		}{{"1", 4, "Private A"}, {"2", 1, "Private B"}} {
			var current map[string]any
			workReferenceRequest(t, app, admin, "GET", "/api/source-catalog/"+member.id, "", 200, &current)
			assertReview(t, current, member.rating, member.note)
		}
		var check string
		if err = db.QueryRow("PRAGMA integrity_check").Scan(&check); err != nil || check != "ok" {
			t.Fatalf("integrity %q %v", check, err)
		}
		app.Close()
		if err = server.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err = db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
