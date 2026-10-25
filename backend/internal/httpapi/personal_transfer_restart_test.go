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
	"github.com/yexca/kikoto/backend/internal/testfixture"
)

func openTransferApplication(t *testing.T, root string, first bool) (*sql.DB, *httptest.Server, *http.Cookie, *http.Cookie, func()) {
	t.Helper()
	db, err := storage.Open(filepath.Join(root, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	migrationDir, err := testfixture.MigrationDir(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	if err = storage.MigrateFS(db, os.DirFS(migrationDir), buildinfo.Version); err != nil {
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

// A library can know a numberless catalog entry, its series and an episode
// before any work is registered. Restoring personal records must register the
// minimum work identity for each known source object, keep the exact source
// references, and remain idempotent across a real application restart.
func TestPersonalTransferRestoresKnownUnregisteredCatalogSeriesAndEpisodeAcrossRestart(t *testing.T) {
	root := t.TempDir()
	db, source, owner, listener, closeSource := openTransferApplication(t, root, true)
	metadataReviewExec(t, db, `INSERT INTO source_catalog_entry(id,root_path,title,primary_code) VALUES
			(101,'/Numberless','Numberless Work',''),
			(102,'/Numberless/Episode 1','Episode One','');
		INSERT INTO catalog_series(id,title) VALUES(201,'Numberless Series');
		INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence)
			VALUES(201,102,1,'manual');`)
	var standaloneRef, episodeRef, seriesRef string
	if err := db.QueryRow(`SELECT ref FROM personal_transfer_ref WHERE catalog_entry_id=101`).Scan(&standaloneRef); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT ref FROM personal_transfer_ref WHERE catalog_entry_id=102`).Scan(&episodeRef); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT ref FROM personal_transfer_ref WHERE series_id=201`).Scan(&seriesRef); err != nil {
		t.Fatal(err)
	}
	rating4, rating5, rating3 := 4, 5, 3
	backup := personal.Backup{Format: "kikoto-user-data", Version: 2, Works: []personal.BackupWork{
		{Identity: &personal.BackupIdentity{Ref: standaloneRef, Kind: "work", Title: "Numberless Work"}, ListeningStatus: "finished", Rating: &rating4, Note: "Standalone note"},
		{Identity: &personal.BackupIdentity{Ref: episodeRef, Kind: "episode", Title: "Episode One",
			Parent: &personal.BackupIdentity{Ref: seriesRef, Kind: "series", Title: "Numberless Series"}}, ListeningStatus: "listening", Rating: &rating5, Note: "Episode note"},
		{Identity: &personal.BackupIdentity{Ref: seriesRef, Kind: "series", Title: "Numberless Series"}, ListeningStatus: "paused", Rating: &rating3, Note: "Series note"},
	}}
	body := mustJSON(map[string]any{"format": "kikoto", "data": backup, "conflict": "keep"})

	var preview personal.ImportPreview
	workReferenceRequest(t, source, listener, "POST", "/api/user-data/import/preview", body, 200, &preview)
	if preview.NewWorks != 3 || preview.MatchedWorks != 0 || preview.Conflicts != 0 || len(preview.Issues) != 0 {
		t.Fatalf("preview = %+v, want three known-but-unregistered identities", preview)
	}
	workReferenceRequest(t, source, listener, "POST", "/api/user-data/import", body, 403, nil)
	var works int
	if err := db.QueryRow(`SELECT count(*) FROM work`).Scan(&works); err != nil {
		t.Fatal(err)
	}
	if works != 0 {
		t.Fatalf("unauthorized import registered %d works", works)
	}
	var result personal.ImportResult
	workReferenceRequest(t, source, owner, "POST", "/api/user-data/import", body, 200, &result)
	if result.ImportedWorks != 3 || result.SkippedWorks != 0 {
		t.Fatalf("restore = %+v, want all three known source objects imported", result)
	}
	workReferenceRequest(t, source, owner, "POST", "/api/user-data/import", body, 200, &result)
	if result.ImportedWorks != 3 || result.SkippedWorks != 0 {
		t.Fatalf("duplicate restore = %+v", result)
	}

	var ownerID int64
	if err := db.QueryRow(`SELECT id FROM user_account WHERE username='synthetic-user'`).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	var standaloneWork, episodeWork, seriesWork sql.NullInt64
	if err := db.QueryRow(`SELECT work_id FROM source_catalog_entry WHERE id=101`).Scan(&standaloneWork); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT work_id FROM source_catalog_entry WHERE id=102`).Scan(&episodeWork); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT work_id FROM catalog_series WHERE id=201`).Scan(&seriesWork); err != nil {
		t.Fatal(err)
	}
	if !standaloneWork.Valid || !episodeWork.Valid || !seriesWork.Valid ||
		standaloneWork.Int64 == episodeWork.Int64 || standaloneWork.Int64 == seriesWork.Int64 || episodeWork.Int64 == seriesWork.Int64 {
		t.Fatalf("registered identities = standalone %v episode %v series %v, want three distinct works", standaloneWork, episodeWork, seriesWork)
	}
	if err := db.QueryRow(`SELECT count(*) FROM work`).Scan(&works); err != nil {
		t.Fatal(err)
	}
	if works != 3 {
		t.Fatalf("works=%d, want one per known evaluation object", works)
	}
	for workID, want := range map[int64]struct {
		rating int
		note   string
	}{
		standaloneWork.Int64: {4, "Standalone note"},
		episodeWork.Int64:    {5, "Episode note"},
		seriesWork.Int64:     {3, "Series note"},
	} {
		var rating int
		var note string
		if err := db.QueryRow(`SELECT rating,note FROM user_work_state WHERE user_id=? AND work_id=?`, ownerID, workID).Scan(&rating, &note); err != nil {
			t.Fatal(err)
		}
		if rating != want.rating || note != want.note {
			t.Fatalf("work %d review = %d/%q, want %d/%q", workID, rating, note, want.rating, want.note)
		}
	}
	var sharedMembers, workflows, media int
	if err := db.QueryRow(`SELECT count(*) FROM shared_identity_member`).Scan(&sharedMembers); err != nil {
		t.Fatal(err)
	}
	var seriesRows, seriesMembers int
	if err := db.QueryRow(`SELECT count(*) FROM catalog_series`).Scan(&seriesRows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM catalog_series_member WHERE series_id=201 AND catalog_entry_id=102`).Scan(&seriesMembers); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM workflow_run`).Scan(&workflows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM media_item`).Scan(&media); err != nil {
		t.Fatal(err)
	}
	if sharedMembers != 0 || seriesRows != 1 || seriesMembers != 1 || workflows != 0 || media != 0 {
		t.Fatalf("import changed shared relations or media: shared=%d series=%d members=%d workflows=%d media=%d", sharedMembers, seriesRows, seriesMembers, workflows, media)
	}
	if files, err := os.ReadDir(filepath.Join(root, "data")); err != nil || len(files) != 0 {
		t.Fatalf("import wrote media files: %v %v", files, err)
	}
	closeSource()

	restartedDB, restarted, admin, other, closeRestarted := openTransferApplication(t, root, false)
	var restored personal.Backup
	workReferenceRequest(t, restarted, admin, "GET", "/api/user-data/export", "", 200, &restored)
	if len(restored.Works) != 3 {
		t.Fatalf("restarted export = %+v, want three works", restored)
	}
	kinds := map[string]int{}
	notes := map[string]string{}
	for _, w := range restored.Works {
		kinds[w.Identity.Kind]++
		notes[w.Identity.Kind] = w.Note
	}
	if kinds["work"] != 1 || kinds["episode"] != 1 || kinds["series"] != 1 ||
		notes["work"] != "Standalone note" || notes["episode"] != "Episode note" || notes["series"] != "Series note" {
		t.Fatalf("restarted records = kinds %#v notes %#v", kinds, notes)
	}
	var otherBackup personal.Backup
	workReferenceRequest(t, restarted, other, "GET", "/api/user-data/export", "", 200, &otherBackup)
	if len(otherBackup.Works) != 0 {
		t.Fatal("restart exposed another user's records")
	}
	var afterRestart int
	if err := restartedDB.QueryRow(`SELECT count(*) FROM work`).Scan(&afterRestart); err != nil {
		t.Fatal(err)
	}
	if afterRestart != 3 {
		t.Fatalf("restart created duplicate works: %d", afterRestart)
	}
	var linkedEntries, linkedSeries int
	if err := restartedDB.QueryRow(`SELECT count(*) FROM source_catalog_entry WHERE id IN (101,102) AND work_id IS NOT NULL`).Scan(&linkedEntries); err != nil {
		t.Fatal(err)
	}
	if err := restartedDB.QueryRow(`SELECT count(*) FROM catalog_series WHERE id=201 AND work_id IS NOT NULL`).Scan(&linkedSeries); err != nil {
		t.Fatal(err)
	}
	if linkedEntries != 2 || linkedSeries != 1 {
		t.Fatalf("restart lost source registrations: entries=%d series=%d", linkedEntries, linkedSeries)
	}
	closeRestarted()
}
