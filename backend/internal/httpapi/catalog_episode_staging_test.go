package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/yexca/kikoto/backend/internal/config"
	"github.com/yexca/kikoto/backend/internal/openlist"
	"github.com/yexca/kikoto/backend/internal/storage"
	"modernc.org/sqlite"
)

type catalogWriteAudit struct {
	mu           sync.Mutex
	active       bool
	rows         int
	entries      int
	files        int
	maxRows      int
	maxEntries   int
	maxFiles     int
	transactions int
	history      []string
}

func (a *catalogWriteAudit) start() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.active = true
	a.rows = 0
	a.entries = 0
	a.files = 0
	a.maxRows = 0
	a.maxEntries = 0
	a.maxFiles = 0
	a.transactions = 0
	a.history = nil
}

func (a *catalogWriteAudit) stop() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.active = false
}

func (a *catalogWriteAudit) details() (maxRows, maxEntries, maxFiles, transactions int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.maxRows, a.maxEntries, a.maxFiles, a.transactions
}

func (a *catalogWriteAudit) transactionsDetail() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.history...)
}

func (a *catalogWriteAudit) rowChanged(data sqlite.SQLitePreUpdateData) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.active {
		return
	}
	switch data.TableName {
	case "source_catalog_entry":
		a.entries++
		a.rows++
	case "source_catalog_file":
		a.files++
		a.rows++
	}
}

func (a *catalogWriteAudit) committed() int32 {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.active && a.rows > 0 {
		a.transactions++
		if a.rows > a.maxRows {
			a.maxRows = a.rows
			a.maxEntries = a.entries
			a.maxFiles = a.files
		}
		if len(a.history) < 12 {
			a.history = append(a.history, fmt.Sprintf("entries=%d files=%d", a.entries, a.files))
		}
	}
	a.rows = 0
	a.entries = 0
	a.files = 0
	return 0
}

func (a *catalogWriteAudit) rolledBack() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.rows = 0
	a.entries = 0
	a.files = 0
}

func openCatalogWriteAuditedDB(t *testing.T, audit *catalogWriteAudit) *sql.DB {
	t.Helper()
	image, err := migratedTestDatabaseImage()
	if err != nil {
		t.Fatalf("prepare test database: %v", err)
	}
	path := filepath.Join(t.TempDir(), "write-audit.db")
	if err := os.WriteFile(path, image, 0o600); err != nil {
		t.Fatalf("copy test database: %v", err)
	}
	sqlite.RegisterConnectionHook(func(conn sqlite.ExecQuerierContext, dsn string) error {
		if !strings.Contains(dsn, path) {
			return nil
		}
		hooker, ok := conn.(sqlite.HookRegisterer)
		if !ok {
			return errors.New("write audit connection does not expose SQLite hooks")
		}
		hooker.RegisterPreUpdateHook(audit.rowChanged)
		hooker.RegisterCommitHook(audit.committed)
		hooker.RegisterRollbackHook(audit.rolledBack)
		return nil
	})
	db, err := storage.Open(path)
	if err != nil {
		t.Fatalf("open audited test database: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func episodeStagingFiles(t *testing.T, fileCount int) []openlist.File {
	t.Helper()
	f := newLooseWorkFixture(t, episodeStagingTree(fileCount))
	return mustOpenListFiles(t, f, "/Example")
}

func persistEpisodeStagingFiles(t *testing.T, f *looseWorkFixture, files []openlist.File) error {
	t.Helper()
	source, err := f.server.loadRemoteSourceForUse(context.Background(), f.sourceID(t))
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.server.persistOpenListRoot(context.Background(), source, "/Example", files)
	return err
}

func episodeStagingTree(episodeFiles int) map[string][]map[string]any {
	files := make([]map[string]any, 0, episodeFiles)
	for index := 0; index < episodeFiles; index++ {
		files = append(files, looseAudio(fmt.Sprintf("track-%03d.mp3", index+1)))
	}
	return map[string][]map[string]any{
		"/Example":           {looseDir("Episode 1"), looseDir("Episode 2")},
		"/Example/Episode 1": files,
		"/Example/Episode 2": {looseAudio("final.mp3")},
	}
}

func looseEpisodeTree(episodeCount int) map[string][]map[string]any {
	files := make([]map[string]any, 0, episodeCount)
	for index := 0; index < episodeCount; index++ {
		files = append(files, looseAudio(fmt.Sprintf("Episode %d.mp3", index+1)))
	}
	return map[string][]map[string]any{
		"/":        {looseDir("Example")},
		"/Example": files,
	}
}

func episodePendingCount(t *testing.T, f *looseWorkFixture) int {
	t.Helper()
	var count int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_episode_pending WHERE source_id=?`, f.sourceID(t)).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func episodePublications(t *testing.T, f *looseWorkFixture) map[string]int64 {
	t.Helper()
	rows, err := f.server.db.Query(`SELECT root_path,id FROM source_catalog_entry
		WHERE file_source_id IS NOT NULL AND root_path LIKE '/Example%' AND relocated_to_id IS NULL ORDER BY root_path`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	result := map[string]int64{}
	for rows.Next() {
		var root string
		var id int64
		if err = rows.Scan(&root, &id); err != nil {
			t.Fatal(err)
		}
		result[root] = id
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestCatalogEpisodeLargeSnapshotStagesAtMost100RowsPerBatch(t *testing.T) {
	const files = 205
	f := newLooseWorkFixture(t, episodeStagingTree(files))
	filesToPublish := episodeStagingFiles(t, files)
	metadataReviewExec(t, f.server.db, `
		CREATE TRIGGER fail_second_episode_file_batch BEFORE INSERT ON source_catalog_file
		WHEN NEW.relative_path='track-100.mp3'
		BEGIN SELECT RAISE(ABORT,'synthetic second file batch failure'); END;
		CREATE TRIGGER hold_episode_file_batch BEFORE DELETE ON catalog_series_member
		BEGIN SELECT RAISE(ABORT,'synthetic held staging state'); END;
	`)
	if err := persistEpisodeStagingFiles(t, f, filesToPublish); err == nil {
		t.Fatal("second staged file batch succeeded despite the SQLite trigger")
	}
	var stagedFiles, stagedRoots int
	if err := f.server.db.QueryRow(`SELECT count(*),
			(SELECT count(DISTINCT catalog_entry_id) FROM source_catalog_episode_pending WHERE source_id=?)
		FROM source_catalog_file WHERE catalog_entry_id IN (
			SELECT catalog_entry_id FROM source_catalog_episode_pending WHERE source_id=?
		)`, f.sourceID(t), f.sourceID(t)).Scan(&stagedFiles, &stagedRoots); err != nil {
		t.Fatal(err)
	}
	if stagedFiles != 99 || stagedRoots != 1 {
		t.Fatalf("first stage committed %d files for %d roots, want 99 files plus one entry row (100 entry/file writes)", stagedFiles, stagedRoots)
	}
	var stagedID int64
	if err := f.server.db.QueryRow(`SELECT catalog_entry_id FROM source_catalog_episode_pending WHERE source_id=?`, f.sourceID(t)).Scan(&stagedID); err != nil {
		t.Fatal(err)
	}
	if got := len(episodePublications(t, f)); got != 0 {
		t.Fatalf("public episode entries=%d while staging is incomplete, want zero", got)
	}
	metadataReviewExec(t, f.server.db, `DROP TRIGGER fail_second_episode_file_batch; DROP TRIGGER hold_episode_file_batch`)
	if err := persistEpisodeStagingFiles(t, f, filesToPublish); err != nil {
		t.Fatal(err)
	}
	published := episodePublications(t, f)
	if len(published) != 2 || published["/Example/Episode 1"] == 0 || published["/Example/Episode 2"] == 0 {
		t.Fatalf("published roots=%v, want two independently addressable episodes", published)
	}
	var storedFiles int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_file f
		JOIN source_catalog_entry e ON e.id=f.catalog_entry_id WHERE e.file_source_id=?`, f.sourceID(t)).Scan(&storedFiles); err != nil {
		t.Fatal(err)
	}
	if storedFiles != files+1 {
		t.Fatalf("stored files=%d, want %d", storedFiles, files+1)
	}
	if published["/Example/Episode 1"] != stagedID {
		t.Fatalf("pending entry id=%d activated as %d, want durable identity reuse", stagedID, published["/Example/Episode 1"])
	}
	ids := map[int64]bool{}
	for _, id := range published {
		if ids[id] {
			t.Fatalf("duplicate catalog id %d: %v", id, published)
		}
		ids[id] = true
	}
	if got := episodePendingCount(t, f); got != 0 {
		t.Fatalf("pending mappings=%d after activation, want zero", got)
	}
}

func TestCatalogEpisodeInterruptedStagingLeavesNoPublicSplitAndRecoversAfterRestart(t *testing.T) {
	f := newLooseWorkFixture(t, episodeStagingTree(130))
	metadataReviewExec(t, f.server.db, `
		CREATE TRIGGER fail_episode_staged_member BEFORE INSERT ON catalog_series_member
		WHEN NEW.position=2
		BEGIN SELECT RAISE(ABORT,'synthetic staged member failure'); END;
		CREATE TRIGGER fail_episode_compensation BEFORE DELETE ON catalog_series_member
		WHEN OLD.catalog_entry_id IN (
			SELECT catalog_entry_id FROM source_catalog_episode_pending
		)
		BEGIN SELECT RAISE(ABORT,'synthetic compensation failure'); END;
	`)
	f.request(t, http.MethodPost, "/catalog/scan", `{}`, http.StatusInternalServerError)
	if got := len(episodePublications(t, f)); got != 0 {
		t.Fatalf("public episode entries=%d after interrupted initial split, want zero", got)
	}
	if got := episodePendingCount(t, f); got == 0 {
		t.Fatal("failed compensation discarded the durable pending recovery records")
	}
	if cards := f.request(t, http.MethodGet, "/works", ``, http.StatusOK); cards["total"] != float64(0) {
		t.Fatalf("public cards after interrupted split=%v, want none", cards)
	}

	metadataReviewExec(t, f.server.db, `DROP TRIGGER fail_episode_staged_member; DROP TRIGGER fail_episode_compensation;`)
	if err := f.server.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	server := NewServer(f.server.db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	f.server = server
	f.handler = server.Routes()
	f.cookie = loginTestSession(t, f.handler, "synthetic-user", "synthetic-password")
	f.request(t, http.MethodPost, "/catalog/scan", `{}`, http.StatusOK)

	published := episodePublications(t, f)
	if len(published) != 2 {
		t.Fatalf("published roots after restart=%v, want two", published)
	}
	var files, duplicateRoots int
	if err := f.server.db.QueryRow(`SELECT count(*),
		(SELECT count(*) FROM (SELECT file_source_id,root_path FROM source_catalog_entry
			WHERE file_source_id=? GROUP BY file_source_id,root_path HAVING count(*)>1))
		FROM source_catalog_file f JOIN source_catalog_entry e ON e.id=f.catalog_entry_id
		WHERE e.file_source_id=?`, f.sourceID(t), f.sourceID(t)).Scan(&files, &duplicateRoots); err != nil {
		t.Fatal(err)
	}
	if files != 131 || duplicateRoots != 0 {
		t.Fatalf("files=%d duplicate roots=%d after restart, want 131/0", files, duplicateRoots)
	}
	if got := episodePendingCount(t, f); got != 0 {
		t.Fatalf("pending mappings=%d after restart recovery, want zero", got)
	}
}

func TestCatalogEpisodePendingEntryIsHiddenFromReadSearchAndReview(t *testing.T) {
	f := newLooseWorkFixture(t, looseEpisodeTree(101))
	sourceID := f.sourceID(t)
	metadataReviewExec(t, f.server.db, fmt.Sprintf(`
		CREATE TRIGGER fail_pending_activation BEFORE UPDATE OF file_source_id ON source_catalog_entry
		WHEN OLD.file_source_id IS NULL AND NEW.file_source_id IS NOT NULL
			AND NEW.root_path=(
				SELECT e.root_path FROM source_catalog_episode_pending p
				JOIN source_catalog_entry e ON e.id=p.catalog_entry_id
				WHERE p.source_id=%d AND p.parent_root='/Example'
				ORDER BY e.root_path DESC LIMIT 1
			)
		BEGIN SELECT RAISE(ABORT,'synthetic late pending activation failure'); END;
		CREATE TRIGGER hold_pending_cleanup BEFORE DELETE ON source_catalog_episode_pending
		BEGIN SELECT RAISE(ABORT,'synthetic pending cleanup failure'); END;
	`, sourceID))
	f.request(t, http.MethodPost, "/catalog/scan", `{}`, http.StatusInternalServerError)

	var pendingID int64
	if err := f.server.db.QueryRow(`SELECT p.catalog_entry_id FROM source_catalog_episode_pending p
		JOIN source_catalog_entry e ON e.id=p.catalog_entry_id
		WHERE p.source_id=? AND e.file_source_id=? ORDER BY p.catalog_entry_id LIMIT 1`,
		sourceID, sourceID).Scan(&pendingID); err != nil {
		t.Fatalf("load interrupted pending entry: %v", err)
	}
	reviewRequest(t, f.handler, f.cookie, http.MethodGet,
		"/api/source-catalog/"+strconv.FormatInt(pendingID, 10), "", http.StatusNotFound)
	reviewRequest(t, f.handler, f.cookie, http.MethodGet,
		"/api/source-catalog/"+strconv.FormatInt(pendingID, 10)+"/tracks", "", http.StatusNotFound)
	reviewRequest(t, f.handler, f.cookie, http.MethodPatch,
		"/api/source-catalog/"+strconv.FormatInt(pendingID, 10)+"/user-state", `{"rating":5}`, http.StatusNotFound)
	if works := f.request(t, http.MethodGet, "/works?q=Episode+1", "", http.StatusOK); works["total"] != float64(0) {
		t.Fatalf("search exposed a pending entry: %#v", works)
	}
	var pendingWork, stateRows int
	if err := f.server.db.QueryRow(`SELECT
			COALESCE((SELECT work_id FROM source_catalog_entry WHERE id=?),0),
			(SELECT count(*) FROM user_work_state WHERE work_id IN (
				SELECT work_id FROM source_catalog_entry WHERE id=? AND work_id IS NOT NULL))`,
		pendingID, pendingID).Scan(&pendingWork, &stateRows); err != nil {
		t.Fatal(err)
	}
	if pendingWork != 0 || stateRows != 0 {
		t.Fatalf("pending entry work=%d personalRows=%d, want no registration", pendingWork, stateRows)
	}

	metadataReviewExec(t, f.server.db, `INSERT INTO source_catalog_entry(
			id,file_source_id,root_path,title,availability_status)
			VALUES(950,NULL,'/Retained missing','Retained missing','missing')`)
	retained := reviewRequest(t, f.handler, f.cookie, http.MethodGet, "/api/source-catalog/950", "", http.StatusOK)
	if retained["status"] != "missing" || retained["title"] != "Retained missing" {
		t.Fatalf("retained missing entry=%v, want readable missing record", retained)
	}
}

func TestCatalogEpisodeActivationFailureCompensatesWithoutPublicSplit(t *testing.T) {
	f := newLooseWorkFixture(t, episodeStagingTree(110))
	metadataReviewExec(t, f.server.db, `
		CREATE TRIGGER fail_episode_activation BEFORE UPDATE OF file_source_id ON source_catalog_entry
		WHEN OLD.file_source_id IS NULL AND NEW.file_source_id IS NOT NULL
		BEGIN SELECT RAISE(ABORT,'synthetic activation failure'); END;
	`)
	f.request(t, http.MethodPost, "/catalog/scan", `{}`, http.StatusInternalServerError)
	if got := len(episodePublications(t, f)); got != 0 {
		t.Fatalf("public episode entries=%d after failed activation, want zero", got)
	}
	if got := episodePendingCount(t, f); got != 0 {
		t.Fatalf("pending mappings=%d after successful compensation, want zero", got)
	}
	metadataReviewExec(t, f.server.db, `DROP TRIGGER fail_episode_activation`)
	f.request(t, http.MethodPost, "/catalog/scan", `{}`, http.StatusOK)
	if got := len(episodePublications(t, f)); got != 2 {
		t.Fatalf("published roots after retry=%d, want two", got)
	}
}

// A retry after a successful split must never treat published files as staged.
// The publication row is the durable boundary: even when a legacy build left
// pending mappings behind, the next staging pass keeps every published file and
// the retry either reuses the identity or stops before clearing it.
func TestCatalogEpisodePublishedFilesSurviveAStalePendingMappingRetry(t *testing.T) {
	f := newLooseWorkFixture(t, episodeStagingTree(110))
	files := mustOpenListFiles(t, f, "/Example")
	if err := persistEpisodeStagingFiles(t, f, files); err != nil {
		t.Fatal(err)
	}
	published := episodePublications(t, f)
	if len(published) != 2 {
		t.Fatalf("published roots=%v, want two", published)
	}
	var before int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_file
		WHERE catalog_entry_id IN (SELECT id FROM source_catalog_entry WHERE file_source_id=?)`,
		f.sourceID(t)).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before == 0 {
		t.Fatal("published split has no files to protect")
	}
	// Model the legacy leftover: the split is published but a pending mapping
	// still points at a published entry.
	metadataReviewExec(t, f.server.db, `INSERT INTO source_catalog_episode_pending(
			source_id,parent_root,catalog_entry_root,catalog_entry_id)
		SELECT file_source_id,'/Example',root_path,id FROM source_catalog_entry
		WHERE root_path='/Example/Episode 1' AND file_source_id IS NOT NULL`)
	if got := episodePendingCount(t, f); got != 1 {
		t.Fatalf("stale pending mappings=%d, want one", got)
	}

	// Fault injection: the mapping cleanup that normally runs in the
	// publication transaction fails. The whole publication rolls back, so the
	// published state and its files remain untouched.
	metadataReviewExec(t, f.server.db, `CREATE TRIGGER fail_published_mapping_cleanup
		BEFORE DELETE ON source_catalog_episode_pending
		WHEN EXISTS(SELECT 1 FROM source_catalog_episode_publication publication
			WHERE publication.source_id=OLD.source_id AND publication.parent_root=OLD.parent_root
				AND publication.state='published')
		BEGIN SELECT RAISE(ABORT,'synthetic mapping cleanup failure'); END;`)
	if err := persistEpisodeStagingFiles(t, f, files); err == nil {
		t.Fatal("retry succeeded despite the injected mapping cleanup failure")
	}
	metadataReviewExec(t, f.server.db, `DROP TRIGGER fail_published_mapping_cleanup`)

	var state string
	if err := f.server.db.QueryRow(`SELECT state FROM source_catalog_episode_publication
		WHERE source_id=? AND parent_root='/Example'`, f.sourceID(t)).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "published" {
		t.Fatalf("retry rolled the publication state back to %q", state)
	}
	var after int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_file
		WHERE catalog_entry_id IN (SELECT id FROM source_catalog_entry WHERE file_source_id=?)`,
		f.sourceID(t)).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("published files changed across retry: before=%d after=%d", before, after)
	}
	if got := len(episodePublications(t, f)); got != len(published) {
		t.Fatalf("published identities changed across retry: before=%v after=%v", published, episodePublications(t, f))
	}

	// Once the fault is gone the retry completes without losing a published
	// file or changing the published identities.
	if err := persistEpisodeStagingFiles(t, f, files); err != nil {
		t.Fatalf("recovered retry: %v", err)
	}
	var recovered int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_file
		WHERE catalog_entry_id IN (SELECT id FROM source_catalog_entry WHERE file_source_id=?)`,
		f.sourceID(t)).Scan(&recovered); err != nil {
		t.Fatal(err)
	}
	if recovered != before {
		t.Fatalf("recovered retry changed published files: before=%d after=%d", before, recovered)
	}
	if got := len(episodePublications(t, f)); got != len(published) {
		t.Fatalf("recovered retry changed published identities: before=%v after=%v", published, episodePublications(t, f))
	}
}

// A new episode staged below an already published split is the row the final
// transaction deletes after flipping the parent publication row. Injecting a
// failure on that DELETE must roll the flip back and must not touch the
// published episodes or their files; only the not-yet-public episode is
// compensated.
func TestCatalogEpisodeFinalizeCleanupFailureKeepsPublishedSplit(t *testing.T) {
	f := newLooseWorkFixture(t, episodeStagingTree(110))
	files := mustOpenListFiles(t, f, "/Example")
	if err := persistEpisodeStagingFiles(t, f, files); err != nil {
		t.Fatal(err)
	}
	published := episodePublications(t, f)
	if len(published) != 2 {
		t.Fatalf("published roots=%v, want the initial two", published)
	}
	// The next scan adds one episode below the already published split. Its
	// pending mapping is the only row the finalize transaction has to delete.
	tree := episodeStagingTree(110)
	tree["/Example"] = append(tree["/Example"], looseDir("Episode 3"))
	tree["/Example/Episode 3"] = []map[string]any{looseAudio("bonus.mp3")}
	f.updates(tree)
	files = mustOpenListFiles(t, f, "/Example")
	type cardState struct {
		id    int64
		files int
	}
	before := map[string]cardState{}
	rows, err := f.server.db.Query(`SELECT e.root_path,e.id,
			(SELECT count(*) FROM source_catalog_file f WHERE f.catalog_entry_id=e.id)
		FROM source_catalog_entry e WHERE e.file_source_id=? AND e.root_path LIKE '/Example%'`,
		f.sourceID(t))
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var root string
		var state cardState
		if err = rows.Scan(&root, &state.id, &state.files); err != nil {
			t.Fatal(err)
		}
		before[root] = state
	}
	readErr := rows.Err()
	_ = rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(before) != 2 {
		t.Fatalf("published cards=%v, want two", before)
	}

	// The failure targets only the DELETE that runs after the publication row
	// became published in the same transaction. The compensation path sees the
	// rolled-back pending state and is not affected.
	metadataReviewExec(t, f.server.db, `CREATE TRIGGER fail_finalize_mapping_cleanup
		BEFORE DELETE ON source_catalog_episode_pending
		WHEN EXISTS(SELECT 1 FROM source_catalog_episode_publication publication
			WHERE publication.source_id=OLD.source_id AND publication.parent_root=OLD.parent_root
				AND publication.state='published')
		BEGIN SELECT RAISE(ABORT,'synthetic finalize mapping cleanup failure'); END;`)
	err = persistEpisodeStagingFiles(t, f, files)
	if err == nil || !strings.Contains(err.Error(), "synthetic finalize mapping cleanup failure") {
		t.Fatalf("persist error=%v, want the injected finalize cleanup failure", err)
	}
	metadataReviewExec(t, f.server.db, `DROP TRIGGER fail_finalize_mapping_cleanup`)

	for root, want := range before {
		var got cardState
		if err = f.server.db.QueryRow(`SELECT id,
				(SELECT count(*) FROM source_catalog_file f WHERE f.catalog_entry_id=e.id)
			FROM source_catalog_entry e WHERE e.file_source_id=? AND e.root_path=?`,
			f.sourceID(t), root).Scan(&got.id, &got.files); err != nil {
			t.Fatalf("published card %s after the failed finalize: %v", root, err)
		}
		if got != want {
			t.Fatalf("published card %s changed across the failed finalize: before=%+v after=%+v", root, want, got)
		}
	}
	var newEpisode int
	if err = f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_entry WHERE root_path='/Example/Episode 3'`).Scan(&newEpisode); err != nil {
		t.Fatal(err)
	}
	if newEpisode != 0 {
		t.Fatalf("unpublished episode survived compensation: %d rows", newEpisode)
	}
	if got := episodePendingCount(t, f); got != 0 {
		t.Fatalf("pending mappings=%d after compensation, want zero", got)
	}
}

// Between the activation batches an entry already has its source identity but
// the parent publication row is still pending. Series detail and tracks must
// hide it until that one row flips, then expose it without rewriting the entry.
func TestCatalogSeriesHidesPendingPublicationUntilItIsPublished(t *testing.T) {
	db := openMigratedTestDB(t)
	metadataReviewExec(t, db, `INSERT INTO file_source(id,code,display_name,source_type,enabled)
		VALUES(86,'pending-series','Pending Series','openlist',1);
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,availability_status) VALUES
			(8601,86,'/series/ready','Ready','available'),
			(8602,86,'/series/staged','Staged','available');
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES
			(8601,'a.mp3','a','audio',1),(8602,'a.mp3','a','audio',1);
		INSERT INTO catalog_series(id,title) VALUES(1,'Pending series');
		INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence) VALUES
			(1,8601,1,'manual'),(1,8602,2,'manual');
		INSERT INTO source_catalog_episode_pending(source_id,parent_root,catalog_entry_root,catalog_entry_id)
			VALUES(86,'/series','/series/staged',8602);
		INSERT INTO source_catalog_episode_publication(source_id,parent_root,state)
			VALUES(86,'/series','pending')`)
	server := NewServer(db, config.Config{})
	request := httptest.NewRequest(http.MethodGet, "/api/catalog-series/1", nil)
	request.SetPathValue("id", "1")
	response := httptest.NewRecorder()
	server.getCatalogSeries(response, request)
	var view catalogSeriesView
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Episodes) != 1 || view.Episodes[0].CatalogEntryID != 8601 {
		t.Fatalf("pending activation leaked into the series: %+v", view.Episodes)
	}
	tracks := httptest.NewRequest(http.MethodGet, "/api/source-catalog/8602/tracks", nil)
	tracks.SetPathValue("id", "8602")
	response = httptest.NewRecorder()
	server.getSourceCatalogTracks(response, tracks)
	if response.Code != http.StatusNotFound {
		t.Fatalf("pending member tracks status = %d", response.Code)
	}

	metadataReviewExec(t, db, `UPDATE source_catalog_episode_publication SET state='published'
		WHERE source_id=86 AND parent_root='/series'`)
	request = httptest.NewRequest(http.MethodGet, "/api/catalog-series/1", nil)
	request.SetPathValue("id", "1")
	response = httptest.NewRecorder()
	server.getCatalogSeries(response, request)
	view = catalogSeriesView{}
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Episodes) != 2 {
		t.Fatalf("published member stayed hidden: %+v", view.Episodes)
	}
	tracks = httptest.NewRequest(http.MethodGet, "/api/source-catalog/8602/tracks", nil)
	tracks.SetPathValue("id", "8602")
	response = httptest.NewRecorder()
	server.getSourceCatalogTracks(response, tracks)
	if response.Code != http.StatusOK {
		t.Fatalf("published member tracks status = %d", response.Code)
	}
}

func TestCatalogEpisodePinnedParentKeepsIdentityAndPersonalState(t *testing.T) {
	tree := map[string][]map[string]any{"/Example": {looseAudio("track.mp3"), looseAudio("notes.txt")}}
	f := newLooseWorkFixture(t, tree)
	result := f.request(t, http.MethodPost, "/catalog/scan", `{}`, http.StatusOK)
	root := result["roots"].([]any)[0].(map[string]any)
	parentID := int64(root["catalogEntryId"].(float64))
	if parentID == 0 {
		t.Fatal("initial parent scan returned no catalog identity")
	}
	reviewRequest(t, f.handler, f.cookie, http.MethodPatch,
		"/api/source-catalog/"+strconv.FormatInt(parentID, 10)+"/user-state", `{"rating":4,"note":"Keep this parent"}`, http.StatusOK)
	if _, err := f.server.db.Exec(`
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,availability_status)
		VALUES(900,NULL,'/Example/Episode 1','Episode 1','excluded');
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes)
		VALUES(900,'stale.mp3','Stale','audio',100);
		INSERT INTO source_catalog_episode_pending(source_id,parent_root,catalog_entry_root,catalog_entry_id)
		VALUES(?,'/Example','/Example/Episode 1',900)`, f.sourceID(t)); err != nil {
		t.Fatal(err)
	}
	tree["/Example"] = []map[string]any{looseDir("Episode 1"), looseDir("Episode 2"), looseAudio("notes.txt")}
	tree["/Example/Episode 1"] = []map[string]any{looseAudio("one.mp3")}
	tree["/Example/Episode 2"] = []map[string]any{looseAudio("two.mp3")}
	f.updates(tree)

	result = f.request(t, http.MethodPost, "/catalog/scan", `{}`, http.StatusOK)
	root = result["roots"].([]any)[0].(map[string]any)
	returnedID, _ := root["catalogEntryId"].(float64)
	if got := int64(returnedID); got != parentID {
		t.Fatalf("pinned scan returned catalog id=%d, want parent %d (result=%v)", got, parentID, result)
	}
	var workID, sourceID int64
	var status string
	if err := f.server.db.QueryRow(`SELECT work_id,file_source_id,availability_status FROM source_catalog_entry WHERE id=?`, parentID).Scan(&workID, &sourceID, &status); err != nil {
		t.Fatal(err)
	}
	if workID == 0 || sourceID != f.sourceID(t) || status != "available" {
		t.Fatalf("parent work/source/status=%d/%d/%s, want retained available identity", workID, sourceID, status)
	}
	var personal string
	if err := f.server.db.QueryRow(`SELECT note FROM user_work_state WHERE work_id=?`, workID).Scan(&personal); err != nil {
		t.Fatal(err)
	}
	if personal != "Keep this parent" {
		t.Fatalf("personal note=%q", personal)
	}
	if got := episodePendingCount(t, f); got != 0 {
		t.Fatalf("pending mappings=%d for pinned parent, want zero", got)
	}
	if cards := f.request(t, http.MethodGet, "/works", ``, http.StatusOK); cards["total"] != float64(1) {
		t.Fatalf("cards=%v, want the retained parent only", cards)
	}
	if got := len(episodePublications(t, f)); got != 1 {
		t.Fatalf("public entries=%d for pinned parent, want one", got)
	}
	var directFiles int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_file
		WHERE catalog_entry_id=? AND relative_path IN ('Episode 1/one.mp3','Episode 2/two.mp3','notes.txt')`, parentID).Scan(&directFiles); err != nil {
		t.Fatal(err)
	}
	if directFiles != 3 {
		t.Fatalf("pinned parent direct files=%d, want the complete ordinary snapshot", directFiles)
	}
	var garbage int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_entry WHERE id=900`).Scan(&garbage); err != nil {
		t.Fatal(err)
	}
	if garbage != 0 {
		t.Fatal("pinned parent left its stale pending entry behind")
	}
	if _, err := f.server.db.Exec(`
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,availability_status)
		VALUES(901,NULL,'/Example/Removed Episode','Removed Episode','excluded');
		INSERT INTO source_catalog_episode_pending(source_id,parent_root,catalog_entry_root,catalog_entry_id)
		VALUES(?,'/Example','/Example/Removed Episode',901)`, f.sourceID(t)); err != nil {
		t.Fatal(err)
	}
	result = f.request(t, http.MethodPost, "/catalog/scan", `{}`, http.StatusOK)
	if got := episodePendingCount(t, f); got != 0 {
		t.Fatalf("pending mappings=%d after pinned rescan, want all parent staging reclaimed", got)
	}
}

func TestCatalogEpisodeActiveRootConsumesOnlyUnownedPendingDuplicate(t *testing.T) {
	f := newLooseWorkFixture(t, episodeStagingTree(110))
	files := mustOpenListFiles(t, f, "/Example")
	sourceID := f.sourceID(t)
	if _, err := f.server.db.Exec(`
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title)
		VALUES(800,?,'/Example/Episode 1','Episode 1');
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,availability_status)
		VALUES(801,NULL,'/Example/Episode 1','Episode 1','excluded'),
		      (802,NULL,'/Example/Episode 2','Episode 2','excluded');
		INSERT INTO source_catalog_episode_pending(source_id,parent_root,catalog_entry_root,catalog_entry_id)
		VALUES(?,'/Example','/Example/Episode 1',801),(?,'/Example','/Example/Episode 2',802)`, sourceID, sourceID, sourceID); err != nil {
		t.Fatal(err)
	}
	if err := persistEpisodeStagingFiles(t, f, files); err != nil {
		t.Fatal(err)
	}
	published := episodePublications(t, f)
	if published["/Example/Episode 1"] != 800 || published["/Example/Episode 2"] != 802 {
		t.Fatalf("published identities=%v, want existing active 800 and staged 802", published)
	}
	var duplicate int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_entry WHERE id=801`).Scan(&duplicate); err != nil {
		t.Fatal(err)
	}
	if duplicate != 0 || episodePendingCount(t, f) != 0 {
		t.Fatalf("duplicate=%d pending=%d, want safe duplicate reclaimed", duplicate, episodePendingCount(t, f))
	}
}

func TestCatalogEpisodeActivationDoesNotDeletePendingWithWork(t *testing.T) {
	f := newLooseWorkFixture(t, episodeStagingTree(110))
	files := mustOpenListFiles(t, f, "/Example")
	sourceID := f.sourceID(t)
	if _, err := f.server.db.Exec(`
		INSERT INTO work(id,primary_code,title) VALUES(900,'','Pending work');
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,work_id,availability_status)
			VALUES(800,NULL,'/Example/Episode 1','Episode 1',900,'excluded'),
		      (801,?,'/Example/Episode 1','Episode 1',NULL,'available');
		INSERT INTO source_catalog_episode_pending(source_id,parent_root,catalog_entry_root,catalog_entry_id)
		VALUES(?,'/Example','/Example/Episode 1',800)`, sourceID, sourceID); err != nil {
		t.Fatal(err)
	}
	err := persistEpisodeStagingFiles(t, f, files)
	if !errors.Is(err, errCatalogEpisodePendingConflict) {
		t.Fatalf("persist error=%v, want pending identity conflict", err)
	}
	var workID int64
	if queryErr := f.server.db.QueryRow(`SELECT work_id FROM source_catalog_entry WHERE id=800`).Scan(&workID); queryErr != nil {
		t.Fatal(queryErr)
	}
	if workID != 900 || episodePendingCount(t, f) != 1 {
		t.Fatalf("work=%d pending=%d, want the conflicting pending identity retained", workID, episodePendingCount(t, f))
	}
}

func TestCatalogEpisodeFinalParentPinReturnsOrdinarySnapshot(t *testing.T) {
	f := newLooseWorkFixture(t, episodeStagingTree(110))
	sourceID := f.sourceID(t)
	if _, err := f.server.db.Exec(`
		INSERT INTO work(id,primary_code,title) VALUES(900,'','Parent work');
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title)
		VALUES(800,?,'/Example','Example');
		INSERT INTO user_work_state(user_id,work_id,rating,note)
		SELECT id,900,5,'Parent review' FROM user_account WHERE username='synthetic-user';
		CREATE TRIGGER pin_episode_parent BEFORE UPDATE OF file_source_id ON source_catalog_entry
		WHEN OLD.root_path LIKE '/Example/Episode %' AND OLD.file_source_id IS NULL
		BEGIN
			UPDATE source_catalog_entry SET work_id=900 WHERE id=800;
		END`, sourceID); err != nil {
		t.Fatal(err)
	}
	result := f.request(t, http.MethodPost, "/catalog/scan", `{}`, http.StatusOK)
	root := result["roots"].([]any)[0].(map[string]any)
	if got := int64(root["catalogEntryId"].(float64)); got != 800 {
		t.Fatalf("final pinned root returned %d, want parent 800", got)
	}
	var personal string
	if err := f.server.db.QueryRow(`SELECT note FROM user_work_state WHERE work_id=900`).Scan(&personal); err != nil {
		t.Fatal(err)
	}
	if personal != "Parent review" || episodePendingCount(t, f) != 0 {
		t.Fatalf("personal=%q pending=%d, want retained parent state and no staging residue", personal, episodePendingCount(t, f))
	}
}

func TestCatalogEpisodeCompensationReportsBothFailures(t *testing.T) {
	f := newLooseWorkFixture(t, episodeStagingTree(110))
	metadataReviewExec(t, f.server.db, `
		CREATE TRIGGER fail_episode_staged_member BEFORE INSERT ON catalog_series_member
		WHEN NEW.position=2
		BEGIN SELECT RAISE(ABORT,'synthetic staged member failure'); END;
		CREATE TRIGGER fail_episode_compensation BEFORE DELETE ON catalog_series_member
		WHEN OLD.catalog_entry_id IN (SELECT catalog_entry_id FROM source_catalog_episode_pending)
		BEGIN SELECT RAISE(ABORT,'synthetic compensation failure'); END;
	`)
	source, err := f.server.loadRemoteSourceForUse(context.Background(), f.sourceID(t))
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.server.persistOpenListRoot(context.Background(), source, "/Example", mustOpenListFiles(t, f, "/Example"))
	if err == nil || !strings.Contains(err.Error(), "synthetic staged member failure") || !strings.Contains(err.Error(), "synthetic compensation failure") {
		t.Fatalf("persist error=%v, want both staged-write and compensation failures", err)
	}
}

func mustOpenListFiles(t *testing.T, f *looseWorkFixture, root string) []openlist.File {
	t.Helper()
	source, err := f.server.loadRemoteSourceForUse(context.Background(), f.sourceID(t))
	if err != nil {
		t.Fatal(err)
	}
	files, err := f.server.openListClient(source).ScanRoot(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	return files
}
