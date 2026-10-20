package httpapi

import (
	"context"
	"testing"
)

// The configured root is the whole tree. A failed child must keep the root
// card's older file rows; a confirmed 404 is complete coverage and must not
// block retirement forever.
func TestCatalogRangeUncertainAtConfiguredRoot(t *testing.T) {
	db := openMigratedTestDB(t)
	metadataReviewExec(t, db, `INSERT INTO file_source(id,code,display_name,source_type)
		VALUES(7,'example_remote_a','Example Remote A','openlist')`)
	metadataReviewExec(t, db, `INSERT INTO source_catalog_directory
		(file_source_id,path,generation,partition_path,catalog_root,status,error_code)
		VALUES(7,'/',9,'','','complete',''),
		      (7,'/Example Work',9,'','','failed',''),
		      (7,'/Example Work 2',9,'','','failed','')`)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	ctx := context.Background()

	var uncertain bool
	for _, base := range []string{"/", ""} {
		uncertain, err = catalogRangeUncertain(ctx, tx, 7, 9, base)
		if err != nil {
			t.Fatal(err)
		}
		if !uncertain {
			t.Fatalf("base %q ignored a failed child", base)
		}
	}
	// The path check is boundary-aware: /Example Work 2 is not below
	// /Example Work and must not keep that card's rows alive.
	if _, err = tx.Exec(`UPDATE source_catalog_directory SET status='complete' WHERE path='/Example Work'`); err != nil {
		t.Fatal(err)
	}
	uncertain, err = catalogRangeUncertain(ctx, tx, 7, 9, "/Example Work")
	if err != nil {
		t.Fatal(err)
	}
	if uncertain {
		t.Fatal("a failed sibling with a different path prefix must not be uncertain")
	}

	if _, err = tx.Exec(`UPDATE source_catalog_directory SET status='empty',error_code='not_found'
		WHERE path='/Example Work'`); err != nil {
		t.Fatal(err)
	}
	for _, base := range []string{"/", ""} {
		uncertain, err = catalogRangeUncertain(ctx, tx, 7, 9, base)
		if err != nil {
			t.Fatal(err)
		}
		if !uncertain {
			// /Example Work 2 is still failed, so the root remains uncertain.
			t.Fatalf("base %q lost the remaining failed sibling", base)
		}
	}
	uncertain, err = catalogRangeUncertain(ctx, tx, 7, 9, "/Example Work")
	if err != nil {
		t.Fatal(err)
	}
	if uncertain {
		t.Fatal("a confirmed 404 is complete coverage for that path")
	}
	incomplete, err := catalogRootIncomplete(ctx, tx, 7, 9, "/Example Work")
	if err != nil {
		t.Fatal(err)
	}
	if incomplete {
		t.Fatal("a confirmed 404 must not keep a format card incomplete forever")
	}

	if _, err = tx.Exec(`UPDATE source_catalog_directory SET status='empty',error_code=''
		WHERE path='/Example Work'`); err != nil {
		t.Fatal(err)
	}
	incomplete, err = catalogRootIncomplete(ctx, tx, 7, 9, "/Example Work")
	if err != nil {
		t.Fatal(err)
	}
	if !incomplete {
		t.Fatal("an unconfirmed empty directory must keep the card incomplete")
	}

	// A complete directory with a staged but unpublished observation is still
	// an observation gap: publishing may add files the card must not lose.
	if _, err = tx.Exec(`UPDATE source_catalog_directory SET status='complete',error_code=''
		WHERE path='/Example Work'`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO source_catalog_observation
		(file_source_id,directory_path,name,revision,kind,size_bytes,publish_pending)
		VALUES(7,'/Example Work','track.mp3',1,'audio',100,1)`); err != nil {
		t.Fatal(err)
	}
	incomplete, err = catalogRootIncomplete(ctx, tx, 7, 9, "/Example Work")
	if err != nil {
		t.Fatal(err)
	}
	if !incomplete {
		t.Fatal("a pending observation must keep the card incomplete")
	}
	if _, err = tx.Exec(`UPDATE source_catalog_observation SET publish_pending=0
		WHERE file_source_id=7 AND directory_path='/Example Work'`); err != nil {
		t.Fatal(err)
	}
	incomplete, err = catalogRootIncomplete(ctx, tx, 7, 9, "/Example Work")
	if err != nil {
		t.Fatal(err)
	}
	if incomplete {
		t.Fatal("a published observation closes the gap")
	}
}

func TestCatalogFormatDirectoryAcceptsMultipleQualitySuffixes(t *testing.T) {
	// "/" is not allowed in a directory name, so the slash spelling is not a
	// case: the scan splits it into two levels before this check runs.
	for _, name := range []string{"FLAC 24bit 96kHz", "WAV 24-96", "MP3 320kbps", "FLAC24bit", "FLAC 44.1kHz", "Lossless", "MP3 VBR"} {
		if !catalogFormatDirectory(name) {
			t.Errorf("%q should be a format copy", name)
		}
	}
	for _, name := range []string{"MP3 Collection", "Audio Drama", "Example Release 2"} {
		if catalogFormatDirectory(name) {
			t.Errorf("%q should stay a title", name)
		}
	}
	// A name that starts with a format and quality tokens is folded into the
	// release even when it ends with another word: creating a work from it
	// would be the worse error.
	if !catalogWorkPartDirectory("FLAC 24bit 96kHz Extra") {
		t.Error("a format-led directory must stay a work part")
	}
}
