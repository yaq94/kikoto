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
}

func TestCatalogFormatDirectoryAcceptsMultipleQualitySuffixes(t *testing.T) {
	for _, name := range []string{"FLAC 24bit 96kHz", "FLAC 24bit/96kHz", "WAV 24-96", "MP3 320kbps", "FLAC24bit"} {
		if !catalogFormatDirectory(name) {
			t.Errorf("%q should be a format copy", name)
		}
	}
	for _, name := range []string{"MP3 Collection", "FLAC 24bit 96kHz Extra", "Audio Drama"} {
		if catalogFormatDirectory(name) {
			t.Errorf("%q should stay a title", name)
		}
	}
}
