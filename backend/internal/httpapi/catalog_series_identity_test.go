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

// A missing old series can be followed by an unrelated series under another
// parent with the same episode name and content. A generation change and a
// fingerprint match cannot replace the parent-directory identity check.
func TestCatalogSeriesRenameEvidenceRejectsStaleSeriesFromDifferentParent(t *testing.T) {
	db := openMigratedTestDB(t)
	metadataReviewExec(t, db, `INSERT INTO file_source(id,code,display_name,source_type) VALUES(7,'series-parent-source','Series Parent Source','openlist');
		INSERT INTO catalog_series(id,title,source_id,source_key) VALUES(11,'Series A',7,'/Series A'),(12,'Series B',7,'/Series B');
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,content_fingerprint,layout_fingerprint,discovered_generation,last_seen_generation)
			VALUES(101,7,'/Series A/Episode 1','Episode 1','same-content','same-layout',1,1),
			      (102,7,'/Series B/Episode 1','Episode 1','same-content','same-layout',2,2);
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
		t.Fatal("stale series from a different parent was accepted as a rename")
	}
}

// A nested series rename is only credible when both series share the directory
// above the series. Equal episode names and content under two libraries are not
// enough, even when the old series is already stale.
func TestCatalogSeriesRenameEvidenceRejectsNestedDifferentParents(t *testing.T) {
	db := openMigratedTestDB(t)
	metadataReviewExec(t, db, `INSERT INTO file_source(id,code,display_name,source_type) VALUES(7,'series-nested-source','Series Nested Source','openlist');
		INSERT INTO catalog_series(id,title,source_id,source_key) VALUES
			(11,'Series',7,'/Library A/Series'),(12,'Series',7,'/Library B/Series');
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,content_fingerprint,layout_fingerprint,discovered_generation,last_seen_generation)
			VALUES(101,7,'/Library A/Series/Episode 1','Episode 1','same-content','same-layout',1,1),
			      (102,7,'/Library B/Series/Episode 1','Episode 1','same-content','same-layout',2,2);
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
		t.Fatal("series under different parent directories were accepted as a rename")
	}
}

// Old rows without a source_key can derive the series directory from their
// members, but a series directly below the source root still has no parent that
// can distinguish a rename from an unrelated sibling.
func TestCatalogSeriesRenameEvidenceRejectsRootLevelSeries(t *testing.T) {
	db := openMigratedTestDB(t)
	metadataReviewExec(t, db, `INSERT INTO file_source(id,code,display_name,source_type) VALUES(7,'series-root-source','Series Root Source','openlist');
		INSERT INTO catalog_series(id,title,source_id,source_key) VALUES
			(11,'Series',7,NULL),(12,'Series',7,'/Series\nseries');
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,source_root,title,content_fingerprint,layout_fingerprint,discovered_generation,last_seen_generation)
			VALUES(101,7,'/Alias/Episode 1','/Series/Episode 1','Episode 1','same-content','same-layout',1,1),
			      (102,7,'/Series/Episode 1','','Episode 1','same-content','same-layout',2,2),
			      (103,7,'/Series/Episode 2','','Episode 2','other-content','other-layout',2,2);
		INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence) VALUES
			(11,101,1,'directory'),(12,102,1,'directory'),(12,103,2,'directory');`)

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
		t.Fatal("root-level series were accepted as a rename without a comparable parent")
	}
}

// Rows created before source_key was populated can still recover a rename: the
// member's effective root supplies the series directory, and its parent matches
// the renamed series under the same creator.
func TestCatalogSeriesRenameEvidenceDerivesMissingParent(t *testing.T) {
	db := openMigratedTestDB(t)
	metadataReviewExec(t, db, `INSERT INTO file_source(id,code,display_name,source_type) VALUES(7,'series-derived-source','Series Derived Source','openlist');
		INSERT INTO catalog_series(id,title,source_id,source_key) VALUES
			(11,'Series A',7,NULL),(12,'Series B',7,'/Creator/Series B\nseries b');
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,source_root,title,content_fingerprint,layout_fingerprint,discovered_generation,last_seen_generation)
			VALUES(101,7,'/Alias/Episode 1','/Creator/Series A/Episode 1','Episode 1','same-content','same-layout',1,1),
			      (102,7,'/Creator/Series B/Episode 1','','Episode 1','same-content','same-layout',2,2),
			      (103,7,'/Creator/Series B/Episode 2','','Episode 2','other-content','other-layout',2,2);
		INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence) VALUES
			(11,101,1,'directory'),(12,102,1,'directory'),(12,103,2,'directory');`)

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	ok, err := catalogSeriesRenameEvidenceTx(context.Background(), tx, 11, 12)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("rename with a missing source_key was not recovered from member roots")
	}
}
