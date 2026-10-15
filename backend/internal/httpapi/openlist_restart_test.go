package httpapi

import (
	"context"
	"io/fs"
	"net/http"
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

func TestOpenListUpgradeRetainsReferencesFavoritesPlaybackAndReviewsAcrossRestart(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "library.db")
	db, err := storage.Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	oldFiles := fstest.MapFS{}
	entries, err := fs.ReadDir(migrations.Files, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") || entry.Name()[:3] > "068" {
			continue
		}
		raw, err := fs.ReadFile(migrations.Files, entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		oldFiles[entry.Name()] = &fstest.MapFile{Data: raw}
	}
	baseline, err := fs.ReadFile(migrations.Files, "baseline/068_v0.8.3.sql")
	if err != nil {
		t.Fatal(err)
	}
	oldFiles["baseline/068_v0.8.3.sql"] = &fstest.MapFile{Data: baseline}
	if err = storage.MigrateFS(db, oldFiles, "v0.8.3"); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", DataRoot: t.TempDir(), CacheRoot: t.TempDir()}
	old := NewServer(db, cfg)
	createTestAdministrator(t, old, "synthetic-user", "synthetic-password")
	createAccountManagedTestUser(t, old, "synthetic-listener", "synthetic-password")
	metadataReviewExec(t, db, `INSERT INTO work(id,primary_code,title,rating_average) VALUES(1,'RJ00000000','Example Work',4.5),(2,'RJ00000001','Example Edition',4);
		INSERT INTO logical_work(id,canonical_work_id,canonical_code) VALUES(1,1,'RJ00000000');
		INSERT INTO work_edition(work_id,logical_work_id,primary_code,is_canonical) VALUES(1,1,'RJ00000000',1),(2,1,'RJ00000001',0);
		INSERT INTO work_code_alias(logical_work_id,provider_id,primary_code) VALUES(1,2,'RJ00000004');
		INSERT INTO media_item(id,work_id,kind,title,fingerprint) VALUES(1,2,'audio','Example Track','example-track');
		INSERT INTO file_source(id,code,display_name,source_type) VALUES(7,'example_remote_a','Example Remote A','openlist');
		INSERT INTO file_source_endpoint(file_source_id,api_url,base_url) VALUES(7,'https://source.example.invalid','https://source.example.invalid');
		INSERT INTO media_file_location(id,media_item_id,file_source_id,location_type,path,availability) VALUES(1,1,7,'remote_stream','Example/track.mp3','available');
		INSERT INTO favorite_list(id,user_id,name) SELECT 91,id,'Example favorites' FROM user_account WHERE username='synthetic-user';
		INSERT INTO favorite_list_item(list_id,work_id) VALUES(91,1);
		INSERT INTO user_work_playback_cursor(user_id,work_id,media_item_id,file_source_id,location_id,position_seconds,duration_seconds) SELECT id,1,1,7,1,13.5,60 FROM user_account WHERE username='synthetic-user';
		INSERT INTO user_work_state(user_id,work_id,rating,note,favorite) SELECT id,1,0,'Example original note',1 FROM user_account WHERE username='synthetic-user';
		INSERT INTO user_work_state(user_id,work_id,rating,note) SELECT id,1,5,'Example other note' FROM user_account WHERE username='synthetic-listener';`)
	if err = old.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	var workID int64
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
		other := workReferenceLogin(t, app, "synthetic-listener")
		for _, ref := range []string{"1", "RJ00000000", "RJ00000001", "RJ00000004"} {
			var detail workDetail
			workReferenceRequest(t, app, admin, http.MethodGet, "/api/works/"+ref+"?includeMedia=false", "", 200, &detail)
			if detail.ID != 1 || detail.PersonalRating == nil || *detail.PersonalRating != 0 || detail.PersonalNote != "Example original note" || !detail.Favorite || len(detail.Translations) != 2 {
				t.Fatalf("boot %d lost numbered relations/state: %+v", boot, detail)
			}
		}
		var detail workDetail
		workReferenceRequest(t, app, other, http.MethodGet, "/api/works/1?includeMedia=false", "", 200, &detail)
		if detail.PersonalRating == nil || *detail.PersonalRating != 5 || detail.PersonalNote != "Example other note" {
			t.Fatal("other user's review lost")
		}
		var cursor map[string]any
		workReferenceRequest(t, app, admin, http.MethodGet, "/api/works/1/playback-cursor", "", 200, &cursor)
		storedCursor, ok := cursor["cursor"].(map[string]any)
		if !ok || storedCursor["positionSeconds"] != 13.5 || storedCursor["mediaWorkId"] != float64(2) {
			t.Fatalf("playback lost: %#v", cursor)
		}
		var favorites []map[string]any
		workReferenceRequest(t, app, admin, http.MethodGet, "/api/works/1/favorite-lists", "", 200, &favorites)
		if !strings.Contains(mustJSON(favorites), "Example favorites") {
			t.Fatalf("favorites lost: %#v", favorites)
		}
		if boot == 0 {
			metadataReviewExec(t, db, `INSERT INTO source_catalog_entry(id,file_source_id,root_path,title) VALUES(1,7,'/Example Unnumbered','Example Unnumbered')`)
			var saved workUserStateResponse
			workReferenceRequest(t, app, admin, http.MethodPatch, "/api/source-catalog/1/user-state", `{"rating":0,"note":"Example catalog note"}`, 200, &saved)
			workID = saved.WorkID
			workReferenceRequest(t, app, other, http.MethodPatch, "/api/source-catalog/1/user-state", `{"rating":4,"note":"Other catalog note"}`, 200, nil)
		}
		var catalog map[string]any
		workReferenceRequest(t, app, admin, http.MethodGet, "/api/source-catalog/1", "", 200, &catalog)
		if catalog["workId"] != float64(workID) || catalog["primaryCode"] != "" {
			t.Fatalf("numberless identity changed: %#v", catalog)
		}
		assertReview(t, catalog, float64(0), "Example catalog note")
		workReferenceRequest(t, app, other, http.MethodGet, "/api/works/"+strconv.FormatInt(workID, 10)+"?includeMedia=false", "", 200, &detail)
		if detail.PersonalNote != "Other catalog note" || detail.PrimaryCode != "" {
			t.Fatalf("numberless review lost: %+v", detail)
		}
		if _, err = db.Exec("INSERT INTO work(primary_code,title) VALUES('RJ00000000','Duplicate')"); err == nil {
			t.Fatal("numbered uniqueness lost")
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
