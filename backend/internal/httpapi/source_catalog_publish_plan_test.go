package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"path"
	"strconv"
	"testing"
)

// A publish batch must be driven by this generation's directories. Rows left
// pending by an older generation sit in the same index and can sort before the
// current ones; publishing must not depend on the purge having reached them
// first.
func TestSourceCatalogPublishSkipsStalePendingRowsAheadOfCurrent(t *testing.T) {
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
	for i := 0; i < 200; i++ {
		dir := fmt.Sprintf("/AAA Stale/Work %05d", i)
		if _, err = f.server.db.Exec(`INSERT INTO source_catalog_directory(file_source_id,path,generation,partition_path,catalog_root,status)
			VALUES(?,?,?,'/Partition',?,'complete')`, sourceID, dir, runID-1, dir); err != nil {
			t.Fatal(err)
		}
		if _, err = f.server.db.Exec(`INSERT INTO source_catalog_audio_root(file_source_id,generation,catalog_root) VALUES(?,?,?)`,
			sourceID, runID-1, dir); err != nil {
			t.Fatal(err)
		}
		if _, err = f.server.db.Exec(`INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes,published_generation,publish_pending)
			VALUES(?,?,'stale.mp3',1,'audio',100,?,1)`, sourceID, dir, runID-1); err != nil {
			t.Fatal(err)
		}
	}
	seedLargeCatalog(t, f.server.db, sourceID, runID, 1, catalogPublishRows)
	if _, err = f.server.db.Exec(`UPDATE source_catalog_observation SET publish_pending=1 WHERE file_source_id=? AND published_generation=?`, sourceID, runID); err != nil {
		t.Fatal(err)
	}
	current := path.Join("/Partition", fmt.Sprintf("Work %05d", 0))
	job := runningCatalogJob(t, f.server.db, sourceID, runID)
	published, err := f.server.publishCatalogBatch(context.Background(), job, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if published != catalogPublishRows {
		t.Fatalf("published=%d want %d", published, catalogPublishRows)
	}
	var stalePublished int
	if err = f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_observation WHERE file_source_id=? AND publish_pending=0 AND directory_path LIKE '/AAA Stale/%'`, sourceID).Scan(&stalePublished); err != nil {
		t.Fatal(err)
	}
	if stalePublished != 0 {
		t.Fatalf("stale generation rows were published (%d)", stalePublished)
	}
	if err = f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_observation WHERE file_source_id=? AND directory_path=? AND publish_pending=0`, sourceID, current).Scan(&stalePublished); err != nil {
		t.Fatal(err)
	}
	if stalePublished != catalogPublishRows {
		t.Fatalf("current rows published=%d want %d", stalePublished, catalogPublishRows)
	}
}
