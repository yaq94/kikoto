package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yexca/kikoto/backend/internal/config"
	"github.com/yexca/kikoto/backend/internal/storage"
)

// The administrator's HTTP operation must not hold the writer for an entire
// subtree. SQLite hooks include publication, admission, search and FTS writes.
func TestCatalogExclusionHTTPBoundsMultiRootWrites(t *testing.T) {
	audit := &catalogWriteAudit{}
	db := openCatalogWriteAuditedDB(t, audit)
	seedExclusionSource(t, db, 7, sourceTypeOpenList)
	for id := int64(1); id <= 3; id++ {
		root := fmt.Sprintf("/Example/Part %d", id)
		if id == 3 {
			root = "/Example boundary"
		}
		metadataReviewExec(t, db, `INSERT INTO source_catalog_entry(id,file_source_id,root_path,title) VALUES(?,7,?,'Example Work')`, id, root)
		seedAdmissionRows(t, db, id, "track", ".mp3", "audio", 150, 0)
	}
	s := NewServer(db, config.Config{CacheRoot: t.TempDir()})
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	createTestAdministrator(t, s, "synthetic-user", "synthetic-password")
	handler := s.Routes()
	cookie := loginTestSession(t, handler, "synthetic-user", "synthetic-password")
	audit.start()
	response := exclusionRequest(t, handler, cookie, http.MethodPost, "/api/remote-sources/7/catalog/exclusions", `{"path":"/Example"}`)
	audit.stop()
	if response.Code != http.StatusCreated {
		t.Fatalf("create=%d %s", response.Code, response.Body.String())
	}
	peak, _, files, _ := audit.details()
	total, transactions, allPeak, tables := audit.allDetails()
	t.Logf("business peak=%d files peak=%d all rows=%d transactions=%d all peak=%d tables=%v", peak, files, total, transactions, allPeak, tables)
	if peak > 100 || files > 64 {
		t.Errorf("unbounded exclusion: business=%d files=%d", peak, files)
	}
	for _, id := range []int64{1, 2} {
		response = exclusionRequest(t, handler, cookie, http.MethodGet, fmt.Sprintf("/api/source-catalog/%d/tracks", id), "")
		if response.Code != http.StatusNotFound {
			t.Errorf("excluded entry %d tracks=%d %s", id, response.Code, response.Body.String())
		}
	}
	response = exclusionRequest(t, handler, cookie, http.MethodGet, "/api/source-catalog/3/tracks", "")
	if response.Code != http.StatusOK {
		t.Fatalf("boundary entry tracks=%d %s", response.Code, response.Body.String())
	}
}

func seedExclusionRecovery(t *testing.T, db *sql.DB) {
	t.Helper()
	seedExclusionSource(t, db, 7, sourceTypeOpenList)
	metadataReviewExec(t, db, `INSERT INTO work(id,primary_code,title) VALUES(1,'RJ00000000','Example Work');
	 INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,work_id) VALUES(1,7,'/Example/Drop','Example Work',1),(2,7,'/Example/Keep','Example Keep',NULL);
	 INSERT INTO source_catalog_exclusion(file_source_id,path,mode) VALUES(7,'/Example/Keep','whitelist');
	 INSERT INTO media_item(id,work_id,kind,title,fingerprint) VALUES(1,1,'audio','Example Track','catalog:1:track1.mp3');
	 INSERT INTO media_file_location(id,media_item_id,file_source_id,location_type,path,availability) VALUES(1,1,7,'catalog_stream','track1.mp3','available');
	 INSERT INTO work_source_presence(work_id,file_source_id,presence_type,availability) VALUES(1,7,'source','available');`)
	seedAdmissionRows(t, db, 1, "track", ".mp3", "audio", 150, 0)
	seedAdmissionRows(t, db, 2, "keep", ".mp3", "audio", 1, 0)
}

func assertExclusionTracks(t *testing.T, handler http.Handler, cookie *http.Cookie, id int64, want int) {
	t.Helper()
	response := exclusionRequest(t, handler, cookie, http.MethodGet, fmt.Sprintf("/api/source-catalog/%d/tracks", id), "")
	if want == 0 {
		if response.Code != http.StatusNotFound {
			t.Fatalf("entry %d tracks=%d %s", id, response.Code, response.Body.String())
		}
		return
	}
	var result struct {
		Tracks []any `json:"tracks"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || len(result.Tracks) != want {
		t.Fatalf("entry %d tracks=%d count=%d want=%d", id, response.Code, len(result.Tracks), want)
	}
}

func exclusionRun(t *testing.T, db *sql.DB) (run, operation int64) {
	t.Helper()
	if err := db.QueryRow(`SELECT workflow_run_id,json_extract(payload_json,'$.operation') FROM workflow_job WHERE worker_type='source_catalog_exclusion' ORDER BY id DESC LIMIT 1`).Scan(&run, &operation); err != nil {
		t.Fatal(err)
	}
	return
}

func TestCatalogExclusionBatchFailureRestartAndHTTPRetry(t *testing.T) {
	db, databasePath := openPublicationRetryDB(t)
	seedExclusionRecovery(t, db)
	s := NewServer(db, config.Config{DatabasePath: databasePath})
	createTestAdministrator(t, s, "synthetic-user", "synthetic-password")
	handler := s.Routes()
	cookie := loginTestSession(t, handler, "synthetic-user", "synthetic-password")
	metadataReviewExec(t, db, `CREATE TRIGGER synthetic_exclusion_failure BEFORE DELETE ON source_catalog_file
	 WHEN OLD.catalog_entry_id=1 AND (SELECT count(*) FROM source_catalog_file WHERE catalog_entry_id=1)=86
	 BEGIN SELECT RAISE(ABORT,'synthetic batch failure'); END`)
	response := exclusionRequest(t, handler, cookie, http.MethodPost, "/api/remote-sources/7/catalog/exclusions", `{"path":"/Example"}`)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("fault=%d %s", response.Code, response.Body.String())
	}
	var remaining int
	if err := db.QueryRow(`SELECT count(*) FROM source_catalog_file WHERE catalog_entry_id=1`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 86 {
		t.Fatalf("committed first batch left %d files, want 86", remaining)
	}
	run, operation := exclusionRun(t, db)
	assertExclusionTracks(t, handler, cookie, 1, 150)
	assertExclusionTracks(t, handler, cookie, 2, 1)
	// An ordinary catalogue mutation cannot steal a failed operation's source.
	response = exclusionRequest(t, handler, cookie, http.MethodPost, "/api/remote-sources/7/catalog/exclusions", `{"path":"/Other"}`)
	if response.Code != http.StatusConflict {
		t.Fatalf("competing exclusion=%d %s", response.Code, response.Body.String())
	}
	// Personal state remains live while the original source snapshot is readable.
	reviewRequest(t, handler, cookie, http.MethodPatch, "/api/source-catalog/1/user-state", `{"rating":5,"note":"synthetic retained note"}`, http.StatusOK)
	metadataReviewExec(t, db, `UPDATE workflow_job SET checkpoint_json=json_set(checkpoint_json,'$.unrelated','preserved') WHERE workflow_run_id=?`, run)
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	db, err = storage.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s = NewServer(db, config.Config{DatabasePath: databasePath})
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	handler = s.Routes()
	assertExclusionTracks(t, handler, cookie, 1, 150)
	metadataReviewExec(t, db, `DROP TRIGGER synthetic_exclusion_failure`)
	response = exclusionRequest(t, handler, cookie, http.MethodPost, fmt.Sprintf("/api/workflow-runs/%d/retry", run), "")
	if response.Code != http.StatusAccepted {
		t.Fatalf("retry=%d %s", response.Code, response.Body.String())
	}
	if err = s.runNextQueuedWorkflowJob(context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true)); err != nil {
		t.Fatal(err)
	}
	assertExclusionTracks(t, handler, cookie, 1, 0)
	assertExclusionTracks(t, handler, cookie, 2, 1)
	gotRun, gotOperation := exclusionRun(t, db)
	if gotRun != run || gotOperation != operation {
		t.Fatalf("resume allocated another operation %d/%d != %d/%d", gotRun, gotOperation, run, operation)
	}
	var jobs, identities, personal int
	var status, unrelated, media, presence string
	if err = db.QueryRow(`SELECT count(*),status,json_extract(checkpoint_json,'$.unrelated') FROM workflow_job WHERE worker_type='source_catalog_exclusion'`).Scan(&jobs, &status, &unrelated); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 || status != "succeeded" || unrelated != "preserved" {
		t.Fatalf("resumed job=%d/%s/%s", jobs, status, unrelated)
	}
	if err = db.QueryRow(`SELECT count(*) FROM source_catalog_entry WHERE id=1 AND work_id=1`).Scan(&identities); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM user_work_state WHERE work_id=1 AND rating=5 AND note='synthetic retained note'`).Scan(&personal); err != nil {
		t.Fatal(err)
	}
	if identities != 1 || personal != 1 {
		t.Fatalf("stable identity/personal=%d/%d", identities, personal)
	}
	if err = db.QueryRow(`SELECT availability FROM published_media_file_location WHERE id=1`).Scan(&media); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT availability FROM published_work_source_presence WHERE work_id=1 AND file_source_id=7 AND presence_type='source'`).Scan(&presence); err != nil {
		t.Fatal(err)
	}
	if media != "unavailable" || presence != "unavailable" {
		t.Fatalf("excluded source projection media=%s presence=%s", media, presence)
	}
}

// Request cancellation must leave the durable cursor and old catalog intact.
// The runner, rather than a new HTTP rule, resumes the same saved decision.
func TestCatalogExclusionCancelledRequestRetainsPublishedSet(t *testing.T) {
	db := openMigratedTestDB(t)
	seedExclusionRecovery(t, db)
	s := NewServer(db, config.Config{CacheRoot: t.TempDir()})
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	createTestAdministrator(t, s, "synthetic-user", "synthetic-password")
	handler := s.Routes()
	cookie := loginTestSession(t, handler, "synthetic-user", "synthetic-password")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	commits := 0
	ctx = context.WithValue(ctx, catalogWriteTimingKey{}, func(_ time.Duration) {
		commits++
		if commits == 3 {
			cancel()
		}
	})
	req := httptest.NewRequest(http.MethodPost, "/api/remote-sources/7/catalog/exclusions", strings.NewReader(`{"path":"/Example"}`)).WithContext(ctx)
	req.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("cancel=%d %s", response.Code, response.Body.String())
	}
	assertExclusionTracks(t, handler, cookie, 1, 150)
	run, _ := exclusionRun(t, db)
	response = exclusionRequest(t, handler, cookie, http.MethodPost, fmt.Sprintf("/api/workflow-runs/%d/retry", run), "")
	if response.Code != http.StatusAccepted {
		t.Fatalf("retry=%d %s", response.Code, response.Body.String())
	}
	if err := s.runNextQueuedWorkflowJob(context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true)); err != nil {
		t.Fatal(err)
	}
	assertExclusionTracks(t, handler, cookie, 1, 0)
}

func TestCatalogExclusionCancelOrdersWithPublicationMarker(t *testing.T) {
	for _, markerFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("marker-first-%v", markerFirst), func(t *testing.T) {
			db := openMigratedTestDB(t)
			seedExclusionRecovery(t, db)
			s := NewServer(db, config.Config{CacheRoot: t.TempDir()})
			t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
			createTestAdministrator(t, s, "synthetic-user", "synthetic-password")
			handler := s.Routes()
			cookie := loginTestSession(t, handler, "synthetic-user", "synthetic-password")
			if markerFirst {
				metadataReviewExec(t, db, `CREATE TRIGGER synthetic_cleanup_failure BEFORE DELETE ON source_catalog_file_publication_before BEGIN SELECT RAISE(ABORT,'synthetic cleanup failure'); END`)
			}
			cancelStatus := 0
			ctx := context.WithValue(context.Background(), catalogWriteTimingKey{}, func(_ time.Duration) {
				if cancelStatus != 0 {
					return
				}
				var phase, jobStatus string
				var run int64
				err := db.QueryRow(`SELECT workflow_run_id,status,json_extract(checkpoint_json,'$.exclusion.phase') FROM workflow_job WHERE worker_type='source_catalog_exclusion' ORDER BY id DESC LIMIT 1`).Scan(&run, &jobStatus, &phase)
				if err != nil || phase != "publish" || (markerFirst && jobStatus != "succeeded") {
					return
				}
				// Start a competing real HTTP cancellation only once the previous
				// write has committed and released its SQLite writer connection.
				result := make(chan int, 1)
				go func() {
					response := exclusionRequest(t, handler, cookie, http.MethodPost, fmt.Sprintf("/api/workflow-runs/%d/cancel", run), "")
					result <- response.Code
				}()
				cancelStatus = <-result
			})
			req := httptest.NewRequest(http.MethodPost, "/api/remote-sources/7/catalog/exclusions", strings.NewReader(`{"path":"/Example"}`)).WithContext(ctx)
			req.AddCookie(cookie)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			var status string
			if err := db.QueryRow(`SELECT status FROM workflow_run WHERE workflow_code='source_catalog_exclusion'`).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if markerFirst {
				if cancelStatus != http.StatusConflict || status != "succeeded" || response.Code != http.StatusCreated {
					t.Fatalf("after marker cancel=%d job=%s create=%d %s", cancelStatus, status, response.Code, response.Body.String())
				}
				assertExclusionTracks(t, handler, cookie, 1, 0)
				metadataReviewExec(t, db, `DROP TRIGGER synthetic_cleanup_failure`)
				if err := s.cleanupCatalogPublications(context.Background()); err != nil {
					t.Fatal(err)
				}
			} else {
				if cancelStatus != http.StatusOK || status != "cancelled" {
					t.Fatalf("before marker cancel=%d job=%s", cancelStatus, status)
				}
				assertExclusionTracks(t, handler, cookie, 1, 150)
				var pending int
				if err := db.QueryRow(`SELECT count(*) FROM source_catalog_publication WHERE state='preparing'`).Scan(&pending); err != nil || pending != 1 {
					t.Fatalf("cancelled publication=%d %v", pending, err)
				}
			}
		})
	}
}

func TestCatalogExclusionMarkerAndJobRollbackTogether(t *testing.T) {
	db := openMigratedTestDB(t)
	seedExclusionRecovery(t, db)
	s := NewServer(db, config.Config{CacheRoot: t.TempDir()})
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	createTestAdministrator(t, s, "synthetic-user", "synthetic-password")
	handler := s.Routes()
	cookie := loginTestSession(t, handler, "synthetic-user", "synthetic-password")
	metadataReviewExec(t, db, `CREATE TRIGGER synthetic_settlement_failure BEFORE UPDATE OF status ON workflow_job WHEN NEW.worker_type='source_catalog_exclusion' AND NEW.status='succeeded' BEGIN SELECT RAISE(ABORT,'synthetic settlement failure'); END`)
	response := exclusionRequest(t, handler, cookie, http.MethodPost, "/api/remote-sources/7/catalog/exclusions", `{"path":"/Example"}`)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("settlement fault=%d %s", response.Code, response.Body.String())
	}
	assertExclusionTracks(t, handler, cookie, 1, 150)
	var media string
	if err := db.QueryRow(`SELECT availability FROM published_media_file_location WHERE id=1`).Scan(&media); err != nil || media != "available" {
		t.Fatalf("old media=%s %v", media, err)
	}
	metadataReviewExec(t, db, `DROP TRIGGER synthetic_settlement_failure`)
	run, _ := exclusionRun(t, db)
	response = exclusionRequest(t, handler, cookie, http.MethodPost, fmt.Sprintf("/api/workflow-runs/%d/retry", run), "")
	if response.Code != http.StatusAccepted {
		t.Fatalf("retry=%d %s", response.Code, response.Body.String())
	}
	if err := s.runNextQueuedWorkflowJob(context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true)); err != nil {
		t.Fatal(err)
	}
	assertExclusionTracks(t, handler, cookie, 1, 0)
}

func TestCatalogExclusionLowSpaceRestartResumesSavedOperation(t *testing.T) {
	db, databasePath := openPublicationRetryDB(t)
	seedExclusionRecovery(t, db)
	s := NewServer(db, config.Config{DatabasePath: databasePath})
	createTestAdministrator(t, s, "synthetic-user", "synthetic-password")
	handler := s.Routes()
	cookie := loginTestSession(t, handler, "synthetic-user", "synthetic-password")
	s.catalogDiskSpace = func(string) (diskSpaceSnapshot, error) {
		var remaining int
		err := db.QueryRow(`SELECT count(*) FROM source_catalog_file WHERE catalog_entry_id=1`).Scan(&remaining)
		if remaining < 150 {
			return diskSpaceSnapshot{}, err
		}
		return diskSpaceSnapshot{Available: 1 << 40}, err
	}
	response := exclusionRequest(t, handler, cookie, http.MethodPost, "/api/remote-sources/7/catalog/exclusions", `{"path":"/Example"}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("low space create=%d %s", response.Code, response.Body.String())
	}
	run, operation := exclusionRun(t, db)
	var status, reason string
	var remaining int
	if err := db.QueryRow(`SELECT status,error_message FROM workflow_job WHERE workflow_run_id=?`, run).Scan(&status, &reason); err != nil {
		t.Fatal(err)
	}
	if status != "queued" || reason != "low_disk_space" {
		t.Fatalf("pause=%s/%s", status, reason)
	}
	if err := db.QueryRow(`SELECT count(*) FROM source_catalog_file WHERE catalog_entry_id=1`).Scan(&remaining); err != nil || remaining != 86 {
		t.Fatalf("low space working rows=%d %v", remaining, err)
	}
	assertExclusionTracks(t, handler, cookie, 1, 150)
	metadataReviewExec(t, db, `UPDATE file_source SET enabled=0 WHERE id=7`)
	response = exclusionRequest(t, handler, cookie, http.MethodPost, "/api/source-catalog/1/playback", `{"path":"track1.mp3"}`)
	if response.Code == http.StatusOK {
		t.Fatal("old publication restored disabled source access")
	}
	metadataReviewExec(t, db, `UPDATE file_source SET enabled=1 WHERE id=7`)
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	db, err = storage.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s = NewServer(db, config.Config{DatabasePath: databasePath})
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	s.catalogDiskSpace = func(string) (diskSpaceSnapshot, error) { return diskSpaceSnapshot{Available: 1 << 40}, nil }
	handler = s.Routes()
	// Advance only the persisted retry deadline; the real runner consumes the
	// queued job from its saved checkpoint after the database has reopened.
	metadataReviewExec(t, db, `UPDATE workflow_job SET available_at=datetime('now','-1 second') WHERE workflow_run_id=?`, run)
	if err = s.runNextQueuedWorkflowJob(context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true)); err != nil {
		t.Fatal(err)
	}
	assertExclusionTracks(t, handler, cookie, 1, 0)
	gotRun, gotOperation := exclusionRun(t, db)
	if gotRun != run || gotOperation != operation {
		t.Fatalf("low space resume changed operation %d/%d", gotRun, gotOperation)
	}
}

func TestCatalogExclusionRuleRemovalKeepsIdentityAndRemainingRules(t *testing.T) {
	db := openMigratedTestDB(t)
	seedExclusionRecovery(t, db)
	s := NewServer(db, config.Config{CacheRoot: t.TempDir()})
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	createTestAdministrator(t, s, "synthetic-user", "synthetic-password")
	handler := s.Routes()
	cookie := loginTestSession(t, handler, "synthetic-user", "synthetic-password")
	var keepRef string
	if err := db.QueryRow(`SELECT ref FROM personal_transfer_ref WHERE catalog_entry_id=2`).Scan(&keepRef); err != nil {
		t.Fatal(err)
	}
	response := exclusionRequest(t, handler, cookie, http.MethodPost, "/api/remote-sources/7/catalog/exclusions", `{"path":"/Example"}`)
	if response.Code != http.StatusCreated {
		t.Fatal(response.Body.String())
	}
	var whole catalogExclusion
	if err := json.Unmarshal(response.Body.Bytes(), &whole); err != nil {
		t.Fatal(err)
	}
	assertExclusionTracks(t, handler, cookie, 2, 1)
	metadataReviewExec(t, db, `CREATE TRIGGER synthetic_reexclude_failure BEFORE UPDATE OF state ON source_catalog_publication WHEN NEW.state='committed' BEGIN SELECT RAISE(ABORT,'synthetic removal failure'); END`)
	response = exclusionRequest(t, handler, cookie, http.MethodDelete, "/api/remote-sources/7/catalog/exclusions/1", "")
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("delete fault=%d %s", response.Code, response.Body.String())
	}
	assertExclusionTracks(t, handler, cookie, 2, 1)
	run, _ := exclusionRun(t, db)
	metadataReviewExec(t, db, `DROP TRIGGER synthetic_reexclude_failure`)
	response = exclusionRequest(t, handler, cookie, http.MethodPost, fmt.Sprintf("/api/workflow-runs/%d/retry", run), "")
	if response.Code != http.StatusAccepted {
		t.Fatal(response.Body.String())
	}
	if err := s.runNextQueuedWorkflowJob(context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true)); err != nil {
		t.Fatal(err)
	}
	assertExclusionTracks(t, handler, cookie, 2, 0)
	response = exclusionRequest(t, handler, cookie, http.MethodPost, "/api/remote-sources/7/catalog/exclusions", `{"path":"/Example/Drop"}`)
	if response.Code != http.StatusCreated {
		t.Fatal(response.Body.String())
	}
	response = exclusionRequest(t, handler, cookie, http.MethodDelete, fmt.Sprintf("/api/remote-sources/7/catalog/exclusions/%d", whole.ID), "")
	if response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	var drop, keep, gotRef string
	if err := db.QueryRow(`SELECT availability_status FROM published_source_catalog_entry WHERE id=1`).Scan(&drop); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT availability_status FROM published_source_catalog_entry WHERE id=2`).Scan(&keep); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT ref FROM personal_transfer_ref WHERE catalog_entry_id=2`).Scan(&gotRef); err != nil {
		t.Fatal(err)
	}
	if drop != "excluded" || keep != "available" || gotRef != keepRef {
		t.Fatalf("revoke drop=%s keep=%s stableRef=%v", drop, keep, gotRef == keepRef)
	}
	var files int
	if err := db.QueryRow(`SELECT count(*) FROM source_catalog_file`).Scan(&files); err != nil || files != 0 {
		t.Fatalf("revocation fabricated files=%d %v", files, err)
	}
}

func TestCatalogExclusionResourceScale(t *testing.T) {
	for _, roots := range []int{32, 128} {
		t.Run(fmt.Sprintf("roots-%d", roots), func(t *testing.T) {
			audit := &catalogWriteAudit{}
			db := openCatalogWriteAuditedDB(t, audit)
			seedExclusionSource(t, db, 7, sourceTypeOpenList)
			for id := 1; id <= roots; id++ {
				root := fmt.Sprintf("/Example/Root %06d", id)
				metadataReviewExec(t, db, `INSERT INTO source_catalog_entry(id,file_source_id,root_path,title) VALUES(?,7,?,'Example Work');`, id, root)
				seedAdmissionRows(t, db, int64(id), "track", ".mp3", "audio", 65, 0)
				metadataReviewExec(t, db, `INSERT INTO source_catalog_directory(file_source_id,path,generation,partition_path,catalog_root,status) VALUES(7,?,1,'/Example',?,'complete');`, root, root)
				metadataReviewExec(t, db, `INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes) SELECT 7,?,relative_path,1,kind,size_bytes FROM source_catalog_file WHERE catalog_entry_id=?`, root, id)
				metadataReviewExec(t, db, `INSERT INTO source_catalog_audio_root(file_source_id,generation,catalog_root) VALUES(7,1,?)`, root)
			}
			s := NewServer(db, config.Config{CacheRoot: t.TempDir()})
			t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
			createTestAdministrator(t, s, "synthetic-user", "synthetic-password")
			handler := s.Routes()
			cookie := loginTestSession(t, handler, "synthetic-user", "synthetic-password")
			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)
			peakHeap := before.HeapAlloc
			ctx := context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true)
			ctx = context.WithValue(ctx, catalogWriteTimingKey{}, func(elapsed time.Duration) {
				audit.recordTiming(elapsed)
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				peakHeap = max(peakHeap, m.HeapAlloc)
			})
			req := httptest.NewRequest(http.MethodPost, "/api/remote-sources/7/catalog/exclusions", strings.NewReader(`{"path":"/Example"}`)).WithContext(ctx)
			req.AddCookie(cookie)
			response := httptest.NewRecorder()
			audit.start()
			started := time.Now()
			handler.ServeHTTP(response, req)
			responseElapsed := time.Since(started)
			if response.Code != http.StatusCreated {
				t.Fatalf("scale=%d %s", response.Code, response.Body.String())
			}
			var rule struct {
				ID    int64 `json:"id"`
				RunID int64 `json:"runId"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &rule); err != nil {
				t.Fatal(err)
			}
			view := reviewRequest(t, handler, cookie, http.MethodGet, fmt.Sprintf("/api/workflow-runs/%d", rule.RunID), "", http.StatusOK)
			if view["status"] != "queued" {
				t.Fatalf("large operation was not yielded: %v", view["status"])
			}
			assertExclusionTracks(t, handler, cookie, 1, 65)
			if roots == 32 {
				var sequence int
				var name, databasePath string
				if err := db.QueryRow(`PRAGMA database_list`).Scan(&sequence, &name, &databasePath); err != nil {
					t.Fatal(err)
				}
				if err := s.Shutdown(context.Background()); err != nil {
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				var err error
				db, err = storage.Open(filepath.FromSlash(databasePath))
				if err != nil {
					t.Fatal(err)
				}
				db.SetMaxOpenConns(1)
				t.Cleanup(func() { _ = db.Close() })
				s = NewServer(db, config.Config{DatabasePath: databasePath})
				handler = s.Routes()
				assertExclusionTracks(t, handler, cookie, 1, 65)
			}
			drainExclusionRun(t, s, handler, cookie, ctx, rule.RunID)
			elapsed := time.Since(started)
			audit.stop()
			runtime.ReadMemStats(&after)
			peak, _, filePeak, _ := audit.details()
			total, transactions, allPeak, tables := audit.allDetails()
			files, fileTransactions := audit.fileWork()
			t.Logf("roots=%d files=%d fileTx=%d businessPeak=%d filesAtBusinessPeak=%d allRows=%d allTx=%d allPeak=%d response=%s duration=%s maxWrite=%s allocBytes=%d sampledHeapGrowth=%d peakTables=%v", roots, files, fileTransactions, peak, filePeak, total, transactions, allPeak, responseElapsed, elapsed, audit.duration(), after.TotalAlloc-before.TotalAlloc, peakHeap-before.HeapAlloc, tables)
			if peak > 100 || filePeak > 64 || files != roots*65 {
				t.Fatalf("write bounds/coverage=%d/%d/%d", peak, filePeak, files)
			}
			for _, table := range []string{"source_catalog_file", "source_catalog_observation", "source_catalog_directory", "source_catalog_audio_root"} {
				var count int
				if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
					t.Fatalf("remaining %s=%d %v", table, count, err)
				}
			}
			var visited int
			if err := db.QueryRow(`SELECT json_extract(checkpoint_json,'$.exclusion.visited') FROM workflow_job WHERE worker_type='source_catalog_exclusion'`).Scan(&visited); err != nil {
				t.Fatal(err)
			}
			if visited != roots*133 {
				t.Fatalf("visited=%d want=%d (entry + 65 files + 65 observations + directory + root)", visited, roots*133)
			}
			// Revoke traverses all ids in bounded pages and preserves their identity.
			audit.start()
			response = exclusionRequest(t, handler, cookie, http.MethodDelete, fmt.Sprintf("/api/remote-sources/7/catalog/exclusions/%d", rule.ID), "")
			if response.Code != http.StatusOK {
				t.Fatal(response.Body.String())
			}
			var deleted struct {
				RunID int64 `json:"runId"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &deleted); err != nil {
				t.Fatal(err)
			}
			drainExclusionRun(t, s, handler, cookie, ctx, deleted.RunID)
			audit.stop()
			peak, _, _, _ = audit.details()
			if peak > 64 {
				t.Fatalf("revoke business peak=%d", peak)
			}
			var identities int
			if err := db.QueryRow(`SELECT count(*) FROM source_catalog_entry WHERE file_source_id=7 AND availability_status='available'`).Scan(&identities); err != nil || identities != roots {
				t.Fatalf("revoke identities=%d %v", identities, err)
			}
		})
	}
}

func drainExclusionRun(t *testing.T, s *Server, handler http.Handler, cookie *http.Cookie, ctx context.Context, run int64) {
	t.Helper()
	for step := 0; step < 1000; step++ {
		view := reviewRequest(t, handler, cookie, http.MethodGet, fmt.Sprintf("/api/workflow-runs/%d", run), "", http.StatusOK)
		if view["status"] == "succeeded" {
			return
		}
		if view["status"] != "queued" {
			t.Fatalf("reconciliation state=%v", view["status"])
		}
		if err := s.runNextQueuedWorkflowJob(ctx); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("reconciliation did not reach a terminal state")
}

func TestCatalogExclusionBlocksCompetingIndexAndSync(t *testing.T) {
	db := openMigratedTestDB(t)
	seedExclusionRecovery(t, db)
	s := NewServer(db, config.Config{CacheRoot: t.TempDir()})
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	createTestAdministrator(t, s, "synthetic-user", "synthetic-password")
	handler := s.Routes()
	cookie := loginTestSession(t, handler, "synthetic-user", "synthetic-password")
	s.catalogDiskSpace = func(string) (diskSpaceSnapshot, error) { return diskSpaceSnapshot{}, nil }
	response := exclusionRequest(t, handler, cookie, http.MethodPost, "/api/remote-sources/7/catalog/exclusions", `{"path":"/Example"}`)
	if response.Code != http.StatusCreated {
		t.Fatal(response.Body.String())
	}
	response = exclusionRequest(t, handler, cookie, http.MethodPost, "/api/remote-sources/7/catalog/index", `{"force":true}`)
	if response.Code != http.StatusConflict {
		t.Fatalf("competing index=%d %s", response.Code, response.Body.String())
	}
	if run, err := s.queueCatalogIncrementalSync(context.Background(), 7, false); err == nil || run != 0 {
		t.Fatalf("competing sync=%d %v", run, err)
	}
	var scans, syncs int
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM source_catalog_scan),(SELECT count(*) FROM source_catalog_sync_run)`).Scan(&scans, &syncs); err != nil || scans != 0 || syncs != 0 {
		t.Fatalf("competing operations changed checkpoints=%d/%d %v", scans, syncs, err)
	}
	assertExclusionTracks(t, handler, cookie, 1, 150)
}

func TestCatalogExclusionKeysetPlansDoNotRescanPrefixes(t *testing.T) {
	db := openMigratedTestDB(t)
	for _, tc := range []struct {
		query string
		args  []any
	}{
		{`SELECT id,COALESCE(NULLIF(source_root,''),root_path) FROM source_catalog_entry NOT INDEXED WHERE file_source_id=? AND id>? ORDER BY id LIMIT 64`, []any{7, 9000}},
		{`SELECT relative_path,COALESCE(NULLIF(source_root,''),?) FROM source_catalog_file WHERE catalog_entry_id=? AND relative_path>? ORDER BY relative_path LIMIT 64`, []any{"/Example", 1, "track9000"}},
		{`SELECT directory_path,name FROM source_catalog_observation WHERE file_source_id=? AND (directory_path,name)>(?,?) ORDER BY directory_path,name LIMIT 64`, []any{7, "/Example/9000", "track9000"}},
		{`SELECT path FROM source_catalog_directory WHERE file_source_id=? AND path>? ORDER BY path LIMIT 64`, []any{7, "/Example/9000"}},
		{`SELECT generation,catalog_root FROM source_catalog_audio_root WHERE file_source_id=? AND (generation,catalog_root)>(?,?) ORDER BY generation,catalog_root LIMIT 64`, []any{7, 3, "/Example/9000"}},
	} {
		rows, err := db.Query("EXPLAIN QUERY PLAN "+tc.query, tc.args...)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err = rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			t.Log(detail)
			if strings.Contains(detail, "SEARCH") && strings.Contains(detail, ">") {
				found = true
			}
			if strings.Contains(detail, "TEMP B-TREE") || strings.Contains(detail, "SCAN ") {
				t.Errorf("unbounded prefix plan: %s", detail)
			}
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		_ = rows.Close()
		if !found {
			t.Errorf("missing keyset range: %s", tc.query)
		}
	}
}

func TestCatalogExclusionCountersResumeAfterFailure(t *testing.T) {
	db, databasePath := openPublicationRetryDB(t)
	seedExclusionRecovery(t, db)
	metadataReviewExec(t, db, `INSERT INTO workflow_run(id,workflow_code,display_name,status,trigger_type) VALUES(90,'source_catalog_index','Example index','succeeded','manual');
	 INSERT INTO source_catalog_scan(file_source_id,workflow_run_id,options_json) VALUES(7,90,'{}');
	 UPDATE source_catalog_entry SET last_seen_generation=90;
	 UPDATE source_catalog_file SET observed_generation=90;
	 INSERT INTO source_catalog_directory(file_source_id,path,generation,partition_path,catalog_root,status) VALUES(7,'/Example/Drop',90,'/Example','/Example/Drop','complete');
	 INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes,publish_pending) VALUES(7,'/Example/Drop','drop.mp3',1,'audio',1,1);
	 INSERT INTO source_catalog_audio_root(file_source_id,generation,catalog_root) VALUES(7,90,'/Example/Drop');`)
	// The unaffected prefix is deliberately larger than a page. Counting it
	// must survive interruption without skipping or counting committed pages twice.
	for i := 0; i < 193; i++ {
		p := fmt.Sprintf("/A Prefix/%06d", i)
		metadataReviewExec(t, db, `INSERT INTO source_catalog_directory(file_source_id,path,generation,partition_path,catalog_root,status) VALUES(7,?,90,'/A Prefix',?,'complete')`, p, p)
		metadataReviewExec(t, db, `INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes,publish_pending) VALUES(7,?,'current.mp3',1,'audio',1,1),(7,?,'old.mp3',0,'audio',1,1)`, p, p)
		metadataReviewExec(t, db, `INSERT INTO source_catalog_audio_root(file_source_id,generation,catalog_root) VALUES(7,90,?)`, p)
	}
	s := NewServer(db, config.Config{DatabasePath: databasePath})
	createTestAdministrator(t, s, "synthetic-user", "synthetic-password")
	handler := s.Routes()
	cookie := loginTestSession(t, handler, "synthetic-user", "synthetic-password")
	metadataReviewExec(t, db, `CREATE TRIGGER synthetic_counter_failure BEFORE UPDATE OF directories,observed ON source_catalog_scan BEGIN SELECT RAISE(ABORT,'synthetic counter publication failure'); END`)
	response := exclusionRequest(t, handler, cookie, http.MethodPost, "/api/remote-sources/7/catalog/exclusions", `{"path":"/Example/Drop"}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("enqueue=%d %s", response.Code, response.Body.String())
	}
	run, op := exclusionRun(t, db)
	for step := 0; step < 20; step++ {
		view := reviewRequest(t, handler, cookie, http.MethodGet, fmt.Sprintf("/api/workflow-runs/%d", run), "", http.StatusOK)
		if view["status"] == "failed" {
			break
		}
		_ = s.runNextQueuedWorkflowJob(context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true))
	}
	view := reviewRequest(t, handler, cookie, http.MethodGet, fmt.Sprintf("/api/workflow-runs/%d", run), "", http.StatusOK)
	if view["status"] != "failed" {
		t.Fatalf("counter failure=%v", view["status"])
	}
	assertExclusionTracks(t, handler, cookie, 1, 150)
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	db, err = storage.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s = NewServer(db, config.Config{DatabasePath: databasePath})
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	handler = s.Routes()
	metadataReviewExec(t, db, `DROP TRIGGER synthetic_counter_failure`)
	metadataReviewExec(t, db, `UPDATE source_catalog_scan SET workflow_run_id=? WHERE file_source_id=7`, run)
	response = exclusionRequest(t, handler, cookie, http.MethodPost, fmt.Sprintf("/api/workflow-runs/%d/retry", run), "")
	if response.Code != http.StatusAccepted {
		t.Fatal(response.Body.String())
	}
	if err = s.runNextQueuedWorkflowJob(context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true)); err == nil || !strings.Contains(err.Error(), "generation changed") {
		t.Fatalf("stale generation=%v", err)
	}
	assertExclusionTracks(t, handler, cookie, 1, 150)
	metadataReviewExec(t, db, `UPDATE source_catalog_scan SET workflow_run_id=90 WHERE file_source_id=7`)
	response = exclusionRequest(t, handler, cookie, http.MethodPost, fmt.Sprintf("/api/workflow-runs/%d/retry", run), "")
	if response.Code != http.StatusAccepted {
		t.Fatal(response.Body.String())
	}
	drainExclusionRun(t, s, handler, cookie, context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true), run)
	progress := reviewRequest(t, handler, cookie, http.MethodGet, "/api/remote-sources/7/catalog/index", "", http.StatusOK)
	for key, want := range map[string]int{"directories": 193, "completedDirectories": 193, "pendingDirectories": 0, "observedEntries": 193, "indexedEntries": 2} {
		if progress[key] != float64(want) {
			t.Errorf("progress %s=%v want=%d", key, progress[key], want)
		}
	}
	var pending int
	if err = db.QueryRow(`SELECT publish_pending_remaining FROM source_catalog_scan WHERE file_source_id=7`).Scan(&pending); err != nil || pending != 193 {
		t.Fatalf("pending projection=%d %v", pending, err)
	}
	gotRun, gotOp := exclusionRun(t, db)
	if gotRun != run || gotOp != op {
		t.Fatal("counter retry replaced operation")
	}
	assertExclusionTracks(t, handler, cookie, 1, 0)
}
