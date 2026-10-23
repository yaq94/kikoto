package httpapi

import (
	"context"
	"testing"
)

// A historical relocation can leave the only series membership on an alias.
// Moving the card must normalize that row onto the live target, otherwise all
// live members disappear and a later rename has no evidence to compare.
func TestCatalogSeriesAliasMembershipMovesToEmptyTarget(t *testing.T) {
	db := openMigratedTestDB(t)
	metadataReviewExec(t, db, `INSERT INTO file_source(id,code,display_name,source_type)
			VALUES(7,'alias-series-source','Alias Series Source','openlist');
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,relocated_to_id,content_fingerprint,layout_fingerprint,last_seen_generation)
			VALUES(1,NULL,'/Creator/Old Show/Episode 1','Episode 1',2,'same-content','same-layout',10),
			      (2,7,'/Catalog/Old Show/Episode 1','Episode 1',NULL,'','',10),
			      (3,7,'/Archive/Episode 1','Episode 1',NULL,'same-content','same-layout',10),
			      (4,7,'/Creator/New Show/Episode 1','Episode 1',NULL,'same-content','same-layout',11);
		INSERT INTO catalog_series(id,title,source_id,source_key)
			VALUES(11,'Old Show',7,'/Creator/Old Show
old show'),(12,'New Show',7,'/Creator/New Show
new show');
		INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence)
			VALUES(11,1,1,'directory'),(12,4,1,'directory');`)

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err = mergeCatalogSeriesMembershipTx(context.Background(), tx, 2, 3); err != nil {
		t.Fatal(err)
	}

	live, err := catalogSeriesLiveMembersTx(context.Background(), tx, 11)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 || live[0].root != "/Archive/Episode 1" {
		t.Fatalf("live members after merge = %#v, want the target entry", live)
	}
	var members, entryID, position int64
	if err = tx.QueryRow(`SELECT count(*),COALESCE(MAX(catalog_entry_id),0),COALESCE(MAX(position),0)
			FROM catalog_series_member WHERE series_id=11`).Scan(&members, &entryID, &position); err != nil {
		t.Fatal(err)
	}
	if members != 1 || entryID != 3 || position != 1 {
		t.Fatalf("normalized membership = %d rows entry %d position %d, want entry 3 position 1", members, entryID, position)
	}

	renamed, err := catalogSeriesRenameEvidenceTx(context.Background(), tx, 11, 12)
	if err != nil {
		t.Fatal(err)
	}
	if !renamed {
		t.Fatal("rename evidence no longer sees the normalized live member")
	}
}

// Memberships on both sides of a same-series merge can also sit on aliases.
// The target position stays authoritative and every duplicate alias row is
// removed so the series has one live member for the episode.
func TestCatalogSeriesAliasMembershipCollapsesSameSeries(t *testing.T) {
	db := openMigratedTestDB(t)
	metadataReviewExec(t, db, `INSERT INTO file_source(id,code,display_name,source_type)
			VALUES(7,'same-series-source','Same Series Source','openlist');
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,relocated_to_id)
			VALUES(1,NULL,'/Series/Old/Episode 2','Episode 2',2),
			      (2,7,'/Catalog/Old/Episode 2','Episode 2',NULL),
			      (3,NULL,'/Series/New/Episode 2','Episode 2',4),
			      (4,7,'/Catalog/New/Episode 2','Episode 2',NULL);
		INSERT INTO catalog_series(id,title,source_id,source_key)
			VALUES(11,'Series',7,'/Series');
		INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence)
			VALUES(11,1,1,'directory'),(11,3,2,'directory');`)

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err = mergeCatalogSeriesMembershipTx(context.Background(), tx, 2, 4); err != nil {
		t.Fatal(err)
	}

	var members, entryID, position int64
	if err = tx.QueryRow(`SELECT count(*),COALESCE(MAX(catalog_entry_id),0),COALESCE(MAX(position),0)
			FROM catalog_series_member WHERE series_id=11`).Scan(&members, &entryID, &position); err != nil {
		t.Fatal(err)
	}
	if members != 1 || entryID != 4 || position != 2 {
		t.Fatalf("same-series merge = members %d entry %d position %d, want one live member at entry 4 position 2", members, entryID, position)
	}
	live, err := catalogSeriesLiveMembersTx(context.Background(), tx, 11)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 || live[0].root != "/Catalog/New/Episode 2" {
		t.Fatalf("live members after same-series merge = %#v, want the target entry", live)
	}
}
