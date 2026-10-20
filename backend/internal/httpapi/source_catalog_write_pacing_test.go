package httpapi

import (
	"context"
	"errors"
	"net/http"
	"path"
	"strconv"
	"testing"
	"time"
)

func TestCatalogWritePauseHonoursContext(t *testing.T) {
	started := time.Now()
	if err := pauseCatalogWriter(context.Background()); err != nil {
		t.Fatalf("pauseCatalogWriter: %v", err)
	}
	if elapsed := time.Since(started); elapsed < catalogWritePause {
		t.Fatalf("write pause returned after %s, want at least %s", elapsed, catalogWritePause)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started = time.Now()
	err := pauseCatalogWriter(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled context returned %v, want context.Canceled", err)
	}
	if elapsed := time.Since(started); elapsed >= catalogWritePause {
		t.Fatalf("cancelled write pause waited %s", elapsed)
	}

	quiet := context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true)
	started = time.Now()
	if err = pauseCatalogWriter(quiet); err != nil {
		t.Fatalf("no-pacing pause: %v", err)
	}
	if elapsed := time.Since(started); elapsed >= catalogWritePause {
		t.Fatalf("no-pacing context still waited %s", elapsed)
	}
}

// TestPublishCatalogQueueSentinelMatchesBatchRows locks the publish LIMIT to
// the sentinel that decides whether a pass may have left work behind. A full
// batch is not proof of exhaustion; a short batch is.
func TestPublishCatalogQueueSentinelMatchesBatchRows(t *testing.T) {
	if !catalogPublishBatchHasMore(catalogPublishRows) {
		t.Fatalf("a full %d-row batch must report more work", catalogPublishRows)
	}
	if catalogPublishBatchHasMore(catalogPublishRows - 1) {
		t.Fatalf("a short batch must not report more work")
	}

	for _, rows := range []int{catalogPublishRows, catalogPublishRows - 1} {
		t.Run(strconv.Itoa(rows), func(t *testing.T) {
			f := newCatalogIndexFixture(t, http.NotFoundHandler())
			sourceID, err := strconv.ParseInt(path.Base(f.base), 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			started := f.request(t, http.MethodPost, "/catalog/index", `{"pagesPerBatch":32}`, http.StatusAccepted)
			runID := int64(started["runId"].(float64))
			if _, err = f.server.db.Exec(`DELETE FROM source_catalog_directory WHERE file_source_id=?`, sourceID); err != nil {
				t.Fatal(err)
			}
			seedLargeCatalog(t, f.server.db, sourceID, runID, 1, rows)
			if _, err = f.server.db.Exec(`UPDATE source_catalog_observation SET published_generation=0,publish_pending=1 WHERE file_source_id=?`, sourceID); err != nil {
				t.Fatal(err)
			}
			job := runningCatalogJob(t, f.server.db, sourceID, runID)
			published, err := f.server.publishCatalogBatch(context.Background(), job, sourceID)
			if err != nil {
				t.Fatal(err)
			}
			if published != rows {
				t.Fatalf("published=%d want %d; LIMIT must match catalogPublishRows", published, rows)
			}
			more := catalogPublishBatchHasMore(published)
			if want := rows == catalogPublishRows; more != want {
				t.Fatalf("more=%v want %v for %d rows", more, want, rows)
			}
		})
	}
}

// TestPublishCatalogBatchLimitMatchesConstant seeds twice the batch size. A
// literal LIMIT larger than catalogPublishRows would publish all of them and
// break the per-pass budget, so the row count itself is the assertion.
func TestPublishCatalogBatchLimitMatchesConstant(t *testing.T) {
	f := newCatalogIndexFixture(t, http.NotFoundHandler())
	sourceID, err := strconv.ParseInt(path.Base(f.base), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	started := f.request(t, http.MethodPost, "/catalog/index", `{"pagesPerBatch":32}`, http.StatusAccepted)
	runID := int64(started["runId"].(float64))
	if _, err = f.server.db.Exec(`DELETE FROM source_catalog_directory WHERE file_source_id=?`, sourceID); err != nil {
		t.Fatal(err)
	}
	seedLargeCatalog(t, f.server.db, sourceID, runID, 1, 2*catalogPublishRows)
	if _, err = f.server.db.Exec(`UPDATE source_catalog_observation SET published_generation=0,publish_pending=1 WHERE file_source_id=?`, sourceID); err != nil {
		t.Fatal(err)
	}
	job := runningCatalogJob(t, f.server.db, sourceID, runID)
	published, err := f.server.publishCatalogBatch(context.Background(), job, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if published != catalogPublishRows {
		t.Fatalf("published=%d want catalogPublishRows=%d", published, catalogPublishRows)
	}
	if !catalogPublishBatchHasMore(published) {
		t.Fatal("a full batch with a remaining backlog must report more work")
	}
}
