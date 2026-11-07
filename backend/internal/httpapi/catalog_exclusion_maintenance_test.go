package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/yexca/kikoto/backend/internal/config"
)

func TestCatalogExclusionRetrySurvivesOldRunCleanup(t *testing.T) {
	for _, state := range []string{"failed", "cancelled"} {
		t.Run(state, func(t *testing.T) {
			db := openMigratedTestDB(t)
			seedExclusionRecovery(t, db)
			seedExclusionSource(t, db, 8, sourceTypeOpenList)
			s := NewServer(db, config.Config{CacheRoot: t.TempDir()})
			t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
			createTestAdministrator(t, s, "synthetic-user", "synthetic-password")
			handler := s.Routes()
			cookie := loginTestSession(t, handler, "synthetic-user", "synthetic-password")
			metadataReviewExec(t, db, `CREATE TRIGGER synthetic_cleanup_failure BEFORE DELETE ON source_catalog_file
			 WHEN OLD.catalog_entry_id=1 AND (SELECT count(*) FROM source_catalog_file WHERE catalog_entry_id=1)=86
			 BEGIN SELECT RAISE(ABORT,'synthetic failure'); END`)
			response := exclusionRequest(t, handler, cookie, http.MethodPost, "/api/remote-sources/7/catalog/exclusions", `{"path":"/Example"}`)
			if response.Code != http.StatusInternalServerError {
				t.Fatalf("fault=%d %s", response.Code, response.Body.String())
			}
			run, _ := exclusionRun(t, db)
			metadataReviewExec(t, db, `UPDATE workflow_run SET status=?,finished_at='2000-01-01 00:00:00' WHERE id=?`, state, run)
			metadataReviewExec(t, db, `UPDATE workflow_job SET status=? WHERE workflow_run_id=?`, state, run)
			response = exclusionRequest(t, handler, cookie, http.MethodPost, "/api/remote-sources/8/catalog/exclusions", `{"path":"/Example"}`)
			if response.Code != http.StatusCreated {
				t.Fatalf("second source=%d %s", response.Code, response.Body.String())
			}
			response = exclusionRequest(t, handler, cookie, http.MethodPost, "/api/maintenance/database/cleanup", `{"tasks":["old_runs"]}`)
			if response.Code != http.StatusOK {
				t.Fatalf("cleanup=%d %s", response.Code, response.Body.String())
			}
			assertExclusionTracks(t, handler, cookie, 1, 150)
			metadataReviewExec(t, db, `DROP TRIGGER synthetic_cleanup_failure`)
			response = exclusionRequest(t, handler, cookie, http.MethodPost, fmt.Sprintf("/api/workflow-runs/%d/retry", run), "")
			if response.Code != http.StatusAccepted {
				t.Fatalf("retry after cleanup=%d %s", response.Code, response.Body.String())
			}
			if err := s.runNextQueuedWorkflowJob(context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true)); err != nil {
				t.Fatal(err)
			}
			assertExclusionTracks(t, handler, cookie, 1, 0)
			assertExclusionTracks(t, handler, cookie, 2, 1)
		})
	}
}
