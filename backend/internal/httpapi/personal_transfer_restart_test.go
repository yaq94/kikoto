package httpapi

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yexca/kikoto/backend/internal/buildinfo"
	"github.com/yexca/kikoto/backend/internal/config"
	"github.com/yexca/kikoto/backend/internal/personal"
	"github.com/yexca/kikoto/backend/internal/storage"
	"github.com/yexca/kikoto/backend/migrations"
)

func openTransferApplication(t *testing.T, root string, first bool) (*sql.DB, *httptest.Server, *http.Cookie, *http.Cookie, func()) {
	t.Helper()
	db, err := storage.Open(filepath.Join(root, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err = storage.MigrateFS(db, migrations.Files, buildinfo.Version); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", DataRoot: filepath.Join(root, "data"), CacheRoot: filepath.Join(root, "cache")}
	if err = os.MkdirAll(cfg.DataRoot, 0700); err != nil {
		t.Fatal(err)
	}
	s := NewServer(db, cfg)
	if first {
		createTestAdministrator(t, s, "synthetic-user", "synthetic-password")
		createAccountManagedTestUser(t, s, "synthetic-listener", "synthetic-password")
	}
	app := httptest.NewServer(s.Routes())
	closed := false
	closeApp := func() {
		if closed {
			return
		}
		closed = true
		app.Close()
		if err := s.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(closeApp)
	return db, app, workReferenceLogin(t, app, "synthetic-user"), workReferenceLogin(t, app, "synthetic-listener"), closeApp
}

func TestPersonalTransferEmptyLibraryRoundTripAndRestart(t *testing.T) {
	db, source, owner, _, closeSource := openTransferApplication(t, t.TempDir(), true)
	metadataReviewExec(t, db, `INSERT INTO work(id,primary_code,title) VALUES(10,'RJ00000000','Example Numbered');
	 INSERT INTO source_catalog_entry(id,root_path,title) VALUES(1,'/Example Standalone','Example Standalone'),(2,'/Example Episode','Example Episode');
	 INSERT INTO catalog_series(id,title) VALUES(1,'Example Series');
	 INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence) VALUES(1,2,1,'manual');`)
	for _, path := range []string{"/api/works/10", "/api/source-catalog/1", "/api/source-catalog/2", "/api/catalog-series/1"} {
		workReferenceRequest(t, source, owner, "PATCH", path+"/user-state", `{"rating":3,"note":"Example private review"}`, 200, nil)
	}
	var backup personal.Backup
	// Persisted metadata uses the transfer's file budget, not a new short-title
	// limit. A backup produced by this server must remain consumable unchanged.
	if _, err := db.Exec(`UPDATE work SET title=? WHERE id=10`, strings.Repeat("Example ", 700)); err != nil {
		t.Fatal(err)
	}
	workReferenceRequest(t, source, owner, "GET", "/api/user-data/export", "", 200, &backup)
	closeSource()
	// A supplied file exercises all existing portable record classes, including
	// an unavailable audio target that must remain exportable in the empty library.
	backup.Tags = []personal.BackupTag{{Scope: "work", Name: "Example tag", Color: "blue"}}
	list := personal.BackupPlaylist{Name: "Example list", Icon: "moon", Description: "Example description"}
	for index := range backup.Works {
		w := &backup.Works[index]
		w.ListeningStatus = "paused"
		w.Tags = []string{"Example tag"}
		w.Statistics = &personal.BackupStatistics{ListenedSeconds: 90, ListenCount: 2, LastPlayedAt: "2026-01-01 00:00:00"}
		w.Progress = &personal.BackupProgress{MediaWorkCode: w.PrimaryCode, MediaIdentity: w.Identity, MediaTitle: "Example audio", PositionSeconds: 12, LastPlayedAt: "2026-01-01 00:00:00"}
		list.Items = append(list.Items, personal.BackupPlaylistItem{PrimaryCode: w.PrimaryCode, WorkRef: w.Identity.Ref, Note: "Example list note", SortOrder: int64(index)})
	}
	backup.Playlists = []personal.BackupPlaylist{list}
	body := mustJSON(map[string]any{"format": "kikoto", "data": backup, "conflict": "keep"})
	root := t.TempDir()
	for boot := 0; boot < 2; boot++ {
		_, app, admin, listener, closeApp := openTransferApplication(t, root, boot == 0)
		if boot == 0 {
			var preview personal.ImportPreview
			workReferenceRequest(t, app, listener, "POST", "/api/user-data/import/preview", body, 200, &preview)
			if preview.NewWorks != 4 || preview.MatchedWorks != 0 || preview.UnmatchedProgress != 4 {
				t.Fatalf("empty preview=%+v", preview)
			}
			workReferenceRequest(t, app, listener, "POST", "/api/user-data/import", body, 403, nil)
			var empty map[string]any
			workReferenceRequest(t, app, admin, "GET", "/api/works", "", 200, &empty)
			if empty["total"] != float64(0) {
				t.Fatal("denied import registered objects")
			}
			var result personal.ImportResult
			workReferenceRequest(t, app, admin, "POST", "/api/user-data/import", body, 200, &result)
			if result.ImportedWorks != 4 || result.SkippedWorks != 0 || result.SkippedProgress != 4 {
				t.Fatalf("restore=%+v", result)
			}
			workReferenceRequest(t, app, admin, "POST", "/api/user-data/import", body, 200, &result)
			// A repeated identical import is idempotent. It re-applies equal
			// values instead of reporting a conflict, and must not create a
			// second object.
			if result.ImportedWorks != 4 || result.SkippedWorks != 0 {
				t.Fatalf("duplicate=%+v", result)
			}
		}
		var restored personal.Backup
		workReferenceRequest(t, app, admin, "GET", "/api/user-data/export", "", 200, &restored)
		if len(restored.Works) != 4 || len(restored.Playlists) != 1 || len(restored.Playlists[0].Items) != 4 || restored.Playlists[0].Icon != "moon" {
			t.Fatalf("restored counts=%+v", restored)
		}
		kinds := map[string]int{}
		for _, w := range restored.Works {
			kinds[w.Identity.Kind]++
			if w.Rating == nil || *w.Rating != 3 || w.Note != "Example private review" || w.ListeningStatus != "paused" || len(w.Tags) != 1 || w.Progress == nil || w.Progress.PositionSeconds != 12 || w.Statistics == nil || w.Statistics.ListenedSeconds != 90 || w.Statistics.ListenCount != 2 {
				t.Fatalf("lost private fields boot %d: %+v", boot, w)
			}
		}
		if kinds["series"] != 1 || kinds["episode"] != 1 || kinds["work"] != 2 {
			t.Fatalf("lost evaluation levels: %#v", kinds)
		}
		var other personal.Backup
		workReferenceRequest(t, app, listener, "GET", "/api/user-data/export", "", 200, &other)
		if len(other.Works) != 0 || len(other.Playlists) != 0 {
			t.Fatal("export leaked owner records")
		}
		var library, runs map[string]any
		workReferenceRequest(t, app, admin, "GET", "/api/works", "", 200, &library)
		if library["total"] != float64(4) {
			t.Fatalf("duplicate registration: %#v", library)
		}
		workReferenceRequest(t, app, admin, "GET", "/api/workflow-runs", "", 200, &runs)
		if runs["total"] != float64(0) {
			t.Fatal("import started media work")
		}
		files, err := os.ReadDir(filepath.Join(root, "data"))
		if err != nil || len(files) != 0 {
			t.Fatalf("import wrote media: %v %v", files, err)
		}
		if strings.Contains(mustJSON(restored), "/Example Standalone") {
			t.Fatal("source path entered backup")
		}
		closeApp()
	}
}
