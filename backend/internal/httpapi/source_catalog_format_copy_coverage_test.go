package httpapi

import (
	"context"
	"testing"
)

// A format copy publishes its files into the sibling episode card, but its
// directory row keeps the episode as catalog_root. Root completeness and
// uncertainty checks must therefore follow catalog_root as well as the path
// subtree; otherwise a failed copy looks fully observed and lifecycle cleanup
// can delete the only playable rows.
func TestCatalogRootCoverageIncludesSiblingFormatCopies(t *testing.T) {
	db := openMigratedTestDB(t)
	metadataReviewExec(t, db, `INSERT INTO file_source(id,code,display_name,source_type)
		VALUES(107,'format-copy-coverage','Format copy coverage','openlist');
		INSERT INTO source_catalog_directory
			(file_source_id,path,generation,revision,partition_path,catalog_root,status,error_code)
		VALUES(107,'/',9,1,'','','complete',''),
		      (107,'/Show',9,1,'/Show','/Show','complete',''),
		      (107,'/Show/Episode 1',9,1,'/Show/Episode 1','/Show/Episode 1','complete',''),
		      (107,'/Show/Episode 1 (MP3)',9,1,'/Show/Episode 1 (MP3)','/Show/Episode 1','failed','')`)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	ctx := context.Background()

	incomplete, err := catalogRootIncomplete(ctx, tx, 107, 9, "/Show/Episode 1")
	if err != nil {
		t.Fatal(err)
	}
	if !incomplete {
		t.Fatal("a failed sibling format copy was treated as complete")
	}
	uncertain, err := catalogRangeUncertain(ctx, tx, 107, 9, "/Show/Episode 1")
	if err != nil {
		t.Fatal(err)
	}
	if !uncertain {
		t.Fatal("a failed sibling format copy did not make the card range uncertain")
	}
	batch, err := catalogRangeUncertainSetTx(ctx, tx, 107, 9, []string{"/Show/Episode 1"})
	if err != nil {
		t.Fatal(err)
	}
	if !batch["/Show/Episode 1"] {
		t.Fatal("the batched lifecycle verdict missed the failed sibling format copy")
	}

	// A complete directory with an unpublished audio row is still an
	// observation gap. This arm also has to follow catalog_root when the
	// directory is a sibling of the card root.
	if _, err = tx.Exec(`UPDATE source_catalog_directory SET status='complete',error_code=''
		WHERE file_source_id=107 AND path='/Show/Episode 1 (MP3)'`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO source_catalog_observation
		(file_source_id,directory_path,name,revision,kind,size_bytes,publish_pending)
		VALUES(107,'/Show/Episode 1 (MP3)','track.mp3',1,'audio',100,1)`); err != nil {
		t.Fatal(err)
	}
	incomplete, err = catalogRootIncomplete(ctx, tx, 107, 9, "/Show/Episode 1")
	if err != nil {
		t.Fatal(err)
	}
	if !incomplete {
		t.Fatal("a pending audio row in a sibling format copy was ignored")
	}
}

// Cleanup must keep rows from the previous generation when the sibling format
// copy failed. Deleting them would hide playable tracks until the copy recovers.
func TestCatalogCleanupKeepsRowsWhenSiblingFormatCopyFails(t *testing.T) {
	db := openMigratedTestDB(t)
	server := &Server{db: db}
	const sourceID, runID = int64(108), int64(908)
	job := seedCatalogCleanupJob(t, db, sourceID, runID)
	ctx := context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true)

	for _, statement := range []string{
		`INSERT INTO source_catalog_directory
			(file_source_id,path,generation,revision,partition_path,catalog_root,status)
		 VALUES(108,'/Show',908,1,'/Show','/Show','complete'),
		       (108,'/Show/Episode 1',908,1,'/Show/Episode 1','/Show/Episode 1','complete'),
		       (108,'/Show/Episode 1 (MP3)',908,1,'/Show/Episode 1 (MP3)','/Show/Episode 1','failed')`,
		`INSERT INTO source_catalog_entry
			(id,file_source_id,root_path,title,last_seen_generation,discovered_generation)
		 VALUES(1,108,'/Show/Episode 1','Episode 1',908,908)`,
		`INSERT INTO source_catalog_file
			(catalog_entry_id,relative_path,title,kind,size_bytes,observed_generation)
		 VALUES(1,'old.mp3','old.mp3','audio',100,907),
		       (1,'new.mp3','new.mp3','audio',100,908)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}

	scanned, removed, err := server.cleanupCatalogPublishBatch(ctx, job, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if scanned != 1 || removed != 0 {
		t.Fatalf("cleanup scanned=%d removed=%d, want 1 and 0", scanned, removed)
	}
	var oldRows int
	if err = db.QueryRow(`SELECT count(*) FROM source_catalog_file
		WHERE catalog_entry_id=1 AND observed_generation=907`).Scan(&oldRows); err != nil {
		t.Fatal(err)
	}
	if oldRows != 1 {
		t.Fatalf("cleanup deleted %d rows from the unobserved format copy", 1-oldRows)
	}
}
