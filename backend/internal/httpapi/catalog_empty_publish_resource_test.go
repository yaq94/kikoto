package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path"
	"strconv"
	"testing"
	"time"
)

// An empty publication pass must not reread historical file observations while
// holding SQLite's writer. A source can retain those observations after its
// current generation has drained; login and personal writes share that writer.
func TestCatalogEmptyPublishBoundsHistoricalReads(t *testing.T) {
	f := newCatalogIndexFixture(t, http.NotFoundHandler())
	sourceID, err := strconv.ParseInt(path.Base(f.base), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	started := f.request(t, http.MethodPost, "/catalog/index", `{"pagesPerBatch":32}`, http.StatusAccepted)
	runID := int64(started["runId"].(float64))
	metadataReviewExec(t, f.server.db, `DELETE FROM source_catalog_directory WHERE file_source_id=?`, sourceID)
	directories, observations, pageBudget := 1500, 64, 8000
	if os.Getenv("KIKOTO_CATALOG_RESOURCE_SCALE") == "1" {
		directories, observations, pageBudget = 42000, 32, 500000
	}
	seedLargeCatalog(t, f.server.db, sourceID, runID, directories, observations)
	// Old-generation pending rows must neither lead the batch nor be consumed.
	metadataReviewExec(t, f.server.db, fmt.Sprintf(`WITH RECURSIVE n(i) AS(SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<%d)
		INSERT INTO source_catalog_directory(file_source_id,path,generation,revision,partition_path,catalog_root,status)
		SELECT ?, '/Old example/'||i,? ,1,'/Old example','/Old example/'||i,'complete' FROM n`, directories), sourceID, runID-1)
	metadataReviewExec(t, f.server.db, `INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes,publish_pending)
		SELECT file_source_id,path,'old-'||n.i,1,'audio',100,1
		FROM source_catalog_directory CROSS JOIN (WITH RECURSIVE n(i) AS(SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<8) SELECT i FROM n) n
		WHERE file_source_id=? AND generation=?`, sourceID, runID-1)
	metadataReviewExec(t, f.server.db, `ANALYZE`)
	// A partial/stale analysis underestimates the source fanout after a crawl.
	// Approximate statistics can make a whole-source observation scan look cheap.
	metadataReviewExec(t, f.server.db, `UPDATE sqlite_stat1 SET stat='16000 400 400 400 400' WHERE idx='source_catalog_directory_root'`)
	metadataReviewExec(t, f.server.db, `UPDATE sqlite_stat1 SET stat='320000 400 64 64 48 1' WHERE idx='source_catalog_observation_revision'`)
	metadataReviewExec(t, f.server.db, `UPDATE sqlite_stat1 SET stat='180000 400 24 1' WHERE idx='source_catalog_observation_pending'`)
	metadataReviewExec(t, f.server.db, `DELETE FROM sqlite_stat4`)
	metadataReviewExec(t, f.server.db, `ANALYZE sqlite_schema`)
	job := runningCatalogJob(t, f.server.db, sourceID, runID)
	catalogSearchPageAccesses(t, f.server.db)
	clock := time.Now()
	published, err := f.server.publishCatalogBatch(context.Background(), job, sourceID)
	elapsed := time.Since(clock)
	pages := catalogSearchPageAccesses(t, f.server.db)
	t.Logf("directories=%d current_observations=%d stale_pending=%d published=%d elapsed=%s sqlite_pages=%d", directories, directories*observations, directories*8, published, elapsed, pages)
	if err != nil || published != 0 {
		t.Fatalf("empty current publication = %d, %v", published, err)
	}
	// The CI budget allows headroom for directory probes but excludes scanning
	// the retained observations. The larger opt-in fixture has a separate page
	// budget; neither budget depends on elapsed time.
	if pages > pageBudget {
		t.Fatalf("empty publication read %d SQLite pages; budget=%d", pages, pageBudget)
	}
	var stale int
	if err = f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_observation o JOIN source_catalog_directory d
		ON d.file_source_id=o.file_source_id AND d.path=o.directory_path
		WHERE d.file_source_id=? AND d.generation=? AND o.publish_pending=1`, sourceID, runID-1).Scan(&stale); err != nil {
		t.Fatal(err)
	}
	if stale != directories*8 {
		t.Fatalf("historical pending observations changed: %d", stale)
	}
}

func TestCatalogAdmissionPrepassKeepsUnfinishedAndStaleRevisionObservations(t *testing.T) {
	f := newCatalogIndexFixture(t, http.NotFoundHandler())
	sourceID, err := strconv.ParseInt(path.Base(f.base), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	started := f.request(t, http.MethodPost, "/catalog/index", `{"pagesPerBatch":32}`, http.StatusAccepted)
	runID := int64(started["runId"].(float64))
	metadataReviewExec(t, f.server.db, `DELETE FROM source_catalog_directory WHERE file_source_id=?`, sourceID)
	for _, dir := range []struct {
		path, root, status string
		revision           int
	}{
		{"/Complete example", "/Complete example", "complete", 1},
		{"/Partial example", "/Partial example", "complete", 1},
		{"/Partial example/Child", "/Partial example", "pending", 1},
		{"/Revised example", "/Revised example", "complete", 2},
	} {
		metadataReviewExec(t, f.server.db, `INSERT INTO source_catalog_directory
			(file_source_id,path,generation,revision,partition_path,catalog_root,status)
			VALUES(?,?,?,?,?,?,?)`, sourceID, dir.path, runID, dir.revision, "/", dir.root, dir.status)
	}
	for _, dir := range []string{"/Complete example", "/Partial example", "/Revised example"} {
		metadataReviewExec(t, f.server.db, `INSERT INTO source_catalog_observation
			(file_source_id,directory_path,name,revision,kind,size_bytes,publish_pending)
			VALUES(?,?,'notes.txt',1,'text',100,1)`, sourceID, dir)
	}
	job := runningCatalogJob(t, f.server.db, sourceID, runID)
	if published, err := f.server.publishCatalogBatch(context.Background(), job, sourceID); err != nil || published != 0 {
		t.Fatalf("non-audio publication = %d, %v", published, err)
	}
	for _, item := range []struct {
		directory string
		pending   int
	}{
		{"/Complete example", 0},
		{"/Partial example", 1},
		{"/Revised example", 1},
	} {
		var pending int
		if err := f.server.db.QueryRow(`SELECT publish_pending FROM source_catalog_observation
			WHERE file_source_id=? AND directory_path=? AND name='notes.txt'`, sourceID, item.directory).Scan(&pending); err != nil {
			t.Fatal(err)
		}
		if pending != item.pending {
			t.Errorf("%s pending=%d, want %d", item.directory, pending, item.pending)
		}
	}
}
