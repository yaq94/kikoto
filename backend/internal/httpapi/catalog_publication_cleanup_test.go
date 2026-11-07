package httpapi

import (
	"context"
	"testing"
)

func TestCatalogPublicationCleanupPreservesPendingListingCheckpoints(t *testing.T) {
	db := openMigratedTestDB(t)
	seedCatalogPlanSource(t, db)
	s := &Server{db: db}
	ctx, op, err := s.beginCatalogPublication(context.Background(), 7, "synthetic:pending")
	if err != nil {
		t.Fatal(err)
	}
	metadataReviewExec(t, db, `WITH RECURSIVE n(i) AS(SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<130)
		INSERT INTO source_catalog_listing(file_source_id,directory_path,generation,base_revision,observed_version,options_json,state,apply_cursor)
		SELECT 7,'/Example '||i,?,0,'complete','{}','applied','track.mp3' FROM n`, -op)
	metadataReviewExec(t, db, `INSERT INTO source_catalog_listing_item(listing_id,directory_path,name,kind,size_bytes,content_hash,modified_at)
		SELECT id,directory_path,'track.mp3','audio',1,'','' FROM source_catalog_listing`)
	// Cleanup can run when a different source publishes while this operation is
	// interrupted after applying its files but before publishing its marker.
	if err = s.cleanupCatalogPublications(context.Background()); err != nil {
		t.Fatal(err)
	}
	var headers, items int
	if err = db.QueryRow(`SELECT count(*),(SELECT count(*) FROM source_catalog_listing_item) FROM source_catalog_listing`).Scan(&headers, &items); err != nil {
		t.Fatal(err)
	}
	if headers != 130 || items != 130 {
		t.Fatalf("unpublished recovery checkpoints removed: headers=%d items=%d", headers, items)
	}
	if err = s.commitCatalogPublication(ctx, op); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*),(SELECT count(*) FROM source_catalog_listing_item) FROM source_catalog_listing`).Scan(&headers, &items); err != nil {
		t.Fatal(err)
	}
	if headers != 0 || items != 0 {
		t.Fatalf("committed cleanup did not drain its bounded batches: headers=%d items=%d", headers, items)
	}
}
