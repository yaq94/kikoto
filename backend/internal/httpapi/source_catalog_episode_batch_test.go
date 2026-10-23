package httpapi

import (
	"fmt"
	"net/http"
	"testing"
)

// Episode discovery writes in bounded batches, but every episode in one series
// must still become an independently addressable member.
func TestSourceCatalogEpisodeDiscoveryPersistsAcrossBoundedBatches(t *testing.T) {
	const episodes = catalogEpisodeRootsPerBatch + 1
	files := make([]map[string]any, 0, episodes)
	for index := 0; index < episodes; index++ {
		files = append(files, looseAudio(fmt.Sprintf("Episode %d.mp3", index+1)))
	}
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/":             {looseDir("Creator")},
		"/Creator":      {looseDir("Show")},
		"/Creator/Show": files,
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"pageSize":500}`, http.StatusAccepted)
	if progress := f.drain(t); progress["status"] != "complete" {
		t.Fatalf("scan = %#v", progress)
	}
	var members int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM catalog_series_member m
		JOIN catalog_series s ON s.id=m.series_id
		JOIN source_catalog_entry e ON e.id=m.catalog_entry_id
		WHERE e.file_source_id=? AND e.root_path LIKE '/Creator/Show::episode:%'`,
		f.sourceID(t)).Scan(&members); err != nil {
		t.Fatal(err)
	}
	if members != episodes {
		t.Fatalf("series members=%d, want %d", members, episodes)
	}
}
