package httpapi

import (
	"context"
	"testing"
)

// Identical episode names and even identical episode content are not enough to
// fold two live series. A rename leaves the old membership behind in an older
// generation; two unrelated series under different directories remain current.
func TestCatalogSeriesRenameEvidenceRejectsUnrelatedLiveSeries(t *testing.T) {
	db := openMigratedTestDB(t)
	metadataReviewExec(t, db, `INSERT INTO file_source(id,code,display_name,source_type) VALUES(7,'series-source','Series Source','openlist');
		INSERT INTO catalog_series(id,title,source_id,source_key) VALUES(11,'Series A',7,'/Series A'),(12,'Series B',7,'/Series B');
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,content_fingerprint,layout_fingerprint,discovered_generation,last_seen_generation)
			VALUES(101,7,'/Series A/Episode 1','Episode 1','same-content','same-layout',5,5),
			      (102,7,'/Series B/Episode 1','Episode 1','same-content','same-layout',5,5);
		INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence) VALUES(11,101,1,'directory'),(12,102,1,'directory');`)

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	ok, err := catalogSeriesRenameEvidenceTx(context.Background(), tx, 11, 12)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("two live series with different directory keys were accepted as a rename")
	}
}

// The same episode layout under two providers is not a directory rename. A
// source change has to stay separate even when the names and fingerprints match.
func TestCatalogSeriesRenameEvidenceRejectsDifferentSources(t *testing.T) {
	db := openMigratedTestDB(t)
	metadataReviewExec(t, db, `INSERT INTO file_source(id,code,display_name,source_type) VALUES
			(7,'series-source-a','Series Source A','openlist'),
			(8,'series-source-b','Series Source B','openlist');
		INSERT INTO catalog_series(id,title,source_id,source_key) VALUES
			(11,'Series A',7,'/Series/Episode 1'),(12,'Series A',8,'/Series/Episode 1');
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,content_fingerprint,layout_fingerprint,discovered_generation,last_seen_generation)
			VALUES(101,7,'/Series/Episode 1','Episode 1','same-content','same-layout',1,1),
			      (102,8,'/Series/Episode 1','Episode 1','same-content','same-layout',2,2);
		INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence) VALUES
			(11,101,1,'directory'),(12,102,1,'directory');`)

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	ok, err := catalogSeriesRenameEvidenceTx(context.Background(), tx, 11, 12)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("series from different sources were accepted as a rename")
	}
}
