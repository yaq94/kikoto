package httpapi

import (
	"fmt"
	"net/http"
	"testing"
)

// Episode discovery writes in bounded batches, but every episode in one series
// must still become an independently addressable member.
func TestSourceCatalogEpisodeDiscoveryPublishes101WithinWriteBound(t *testing.T) {
	const episodes = 101
	audit := &catalogWriteAudit{}
	f := newLooseWorkFixtureWithDB(t, looseEpisodeTree(episodes), openCatalogWriteAuditedDB(t, audit))
	audit.start()
	f.request(t, http.MethodPost, "/catalog/scan", `{}`, http.StatusOK)
	audit.stop()
	maxRows, maxEntries, maxFiles, transactions := audit.details()
	if transactions == 0 {
		t.Fatal("write audit saw no catalog entry/file transaction")
	}
	if maxRows > 100 {
		t.Fatalf("largest catalog transaction wrote %d entry rows and %d file rows, want at most 100 total; first transactions=%v",
			maxEntries, maxFiles, audit.transactionsDetail())
	}
	t.Logf("maximum transaction: %d entry rows + %d file rows", maxEntries, maxFiles)
	var publicationState string
	if err := f.server.db.QueryRow(`SELECT state FROM source_catalog_episode_publication
		WHERE source_id=? AND parent_root='/Example'`, f.sourceID(t)).Scan(&publicationState); err != nil {
		t.Fatal(err)
	}
	if publicationState != "published" {
		t.Fatalf("parent publication state=%q, want published", publicationState)
	}
	var seriesID int64
	if err := f.server.db.QueryRow(`SELECT m.series_id FROM catalog_series_member m
		JOIN source_catalog_entry e ON e.id=m.catalog_entry_id
		WHERE e.file_source_id=? AND e.root_path LIKE '/Example::episode:%' LIMIT 1`,
		f.sourceID(t)).Scan(&seriesID); err != nil {
		t.Fatal(err)
	}
	view := reviewRequest(t, f.handler, f.cookie, http.MethodGet,
		fmt.Sprintf("/api/catalog-series/%d", seriesID), "", http.StatusOK)
	members := view["episodes"].([]any)
	if len(members) != episodes {
		t.Fatalf("published series members=%d, want %d", len(members), episodes)
	}
}

func TestCatalogEpisodeFinalPublicationCompensatesAfterFirstActivationBatch(t *testing.T) {
	const episodes = 101
	audit := &catalogWriteAudit{}
	f := newLooseWorkFixtureWithDB(t, looseEpisodeTree(episodes), openCatalogWriteAuditedDB(t, audit))
	metadataReviewExec(t, f.server.db, fmt.Sprintf(`
		CREATE TRIGGER fail_late_episode_activation BEFORE UPDATE OF file_source_id ON source_catalog_entry
		WHEN OLD.file_source_id IS NULL AND NEW.file_source_id IS NOT NULL
			AND NEW.root_path=(
				SELECT e.root_path FROM source_catalog_episode_pending p
				JOIN source_catalog_entry e ON e.id=p.catalog_entry_id
				WHERE p.source_id=%d AND p.parent_root='/Example'
				ORDER BY e.root_path DESC LIMIT 1
			)
		BEGIN SELECT RAISE(ABORT,'synthetic late activation failure'); END;
	`, f.sourceID(t)))

	audit.start()
	f.request(t, http.MethodPost, "/catalog/scan", `{}`, http.StatusInternalServerError)
	audit.stop()
	if got := len(episodePublications(t, f)); got != 0 {
		t.Fatalf("public entries=%d after late activation failure, want zero", got)
	}
	if got := episodePendingCount(t, f); got != 0 {
		t.Fatalf("pending mappings=%d after compensated late failure, want zero", got)
	}
	if cards := f.request(t, http.MethodGet, "/works", "", http.StatusOK); cards["total"] != float64(0) {
		t.Fatalf("works=%v after late activation failure, want none", cards)
	}
	maxRows, maxEntries, maxFiles, transactions := audit.details()
	if transactions == 0 {
		t.Fatal("write audit saw no catalog entry/file transaction")
	}
	if maxRows > 100 {
		t.Fatalf("largest failed-publication transaction wrote %d entry rows and %d file rows, want at most 100 total",
			maxEntries, maxFiles)
	}
	t.Logf("maximum failed-publication transaction: %d entry rows + %d file rows", maxEntries, maxFiles)

	metadataReviewExec(t, f.server.db, `DROP TRIGGER fail_late_episode_activation`)
	f.request(t, http.MethodPost, "/catalog/scan", `{}`, http.StatusOK)
	var seriesID int64
	if err := f.server.db.QueryRow(`SELECT m.series_id FROM catalog_series_member m
		JOIN source_catalog_entry e ON e.id=m.catalog_entry_id
		WHERE e.file_source_id=? AND e.root_path LIKE '/Example::episode:%' LIMIT 1`,
		f.sourceID(t)).Scan(&seriesID); err != nil {
		t.Fatal(err)
	}
	view := reviewRequest(t, f.handler, f.cookie, http.MethodGet,
		fmt.Sprintf("/api/catalog-series/%d", seriesID), "", http.StatusOK)
	if members := view["episodes"].([]any); len(members) != episodes {
		t.Fatalf("series members after retry=%d, want %d", len(members), episodes)
	}
}

func TestCatalogEpisodeParentMetadataPublishesWithinWriteBound(t *testing.T) {
	const episodes = 101
	tree := looseEpisodeTree(episodes)
	for index := 0; index < episodes; index++ {
		tree["/Example"] = append(tree["/Example"], map[string]any{
			"name": fmt.Sprintf("note-%03d.txt", index+1),
			"size": 8,
		})
	}
	audit := &catalogWriteAudit{}
	f := newLooseWorkFixtureWithDB(t, tree, openCatalogWriteAuditedDB(t, audit))
	audit.start()
	f.request(t, http.MethodPost, "/catalog/scan", `{}`, http.StatusOK)
	audit.stop()
	maxRows, maxEntries, maxFiles, transactions := audit.details()
	if transactions == 0 {
		t.Fatal("write audit saw no catalog entry/file transaction")
	}
	if maxRows > 100 {
		t.Fatalf("largest catalog transaction wrote %d entry rows and %d file rows, want at most 100 total",
			maxEntries, maxFiles)
	}
	t.Logf("maximum parent-publication transaction: %d entry rows + %d file rows", maxEntries, maxFiles)
	var parentID, sidecars, audio int
	if err := f.server.db.QueryRow(`SELECT id,
			(SELECT count(*) FROM source_catalog_file f WHERE f.catalog_entry_id=e.id),
			(SELECT count(*) FROM source_catalog_file f WHERE f.catalog_entry_id=e.id AND f.kind='audio')
		FROM source_catalog_entry e WHERE e.file_source_id=? AND e.root_path='/Example'`,
		f.sourceID(t)).Scan(&parentID, &sidecars, &audio); err != nil {
		t.Fatal(err)
	}
	if sidecars != episodes || audio != 0 {
		t.Fatalf("parent files=%d audio=%d, want %d metadata files and no audio", sidecars, audio, episodes)
	}
	reviewRequest(t, f.handler, f.cookie, http.MethodGet,
		fmt.Sprintf("/api/source-catalog/%d", parentID), "", http.StatusNotFound)
}
