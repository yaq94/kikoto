package httpapi

import (
	"context"
	"database/sql"
	"testing"
)

// A series conflict is a review outcome, not a committed retirement. The work
// and personal state must stay on the old card when the series cannot be
// folded.
func TestRetireCatalogCardRollsBackWhenSeriesConflict(t *testing.T) {
	db := openMigratedTestDB(t)
	metadataReviewExec(t, db, `
		INSERT INTO file_source(id,code,display_name,source_type)
			VALUES(7,'series_retire','Series retire','openlist');
		INSERT INTO user_account(id,username,display_name,role) VALUES(1,'retire-user','Retire User','user');
		INSERT INTO work(id,primary_code,title) VALUES(501,'','Old work'),(502,'','Target work');
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,work_id,availability_status) VALUES
			(100,7,'/Old Work','Old work',501,'available'),
			(200,7,'/New Work','New work',502,'available');
		INSERT INTO catalog_series(id,title,source_id,source_key) VALUES
			(31,'Old series',7,'/Creator
old series'),
			(32,'New series',7,'/Creator
new series');
		INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence) VALUES
			(31,100,1,'directory'),(32,200,1,'directory');
		INSERT INTO user_work_state(user_id,work_id,rating,note) VALUES(1,501,5,'keep me');
	`)

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	retired, err := retireCatalogCardTx(context.Background(), tx, 100, 200)
	if err != nil {
		t.Fatal(err)
	}
	if retired {
		t.Fatal("retired a card whose series conflicts")
	}

	var staleWork, targetWork sql.NullInt64
	if err = tx.QueryRow(`SELECT work_id FROM source_catalog_entry WHERE id=100`).Scan(&staleWork); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(`SELECT work_id FROM source_catalog_entry WHERE id=200`).Scan(&targetWork); err != nil {
		t.Fatal(err)
	}
	if !staleWork.Valid || staleWork.Int64 != 501 || !targetWork.Valid || targetWork.Int64 != 502 {
		t.Fatalf("work links after conflict: stale=%v target=%v, want 501/502", staleWork, targetWork)
	}
	var rating int
	var note string
	if err = tx.QueryRow(`SELECT rating,note FROM user_work_state WHERE user_id=1 AND work_id=501`).Scan(&rating, &note); err != nil {
		t.Fatalf("personal state was moved by a rejected retirement: %v", err)
	}
	var targetState int
	if err = tx.QueryRow(`SELECT count(*) FROM user_work_state WHERE user_id=1 AND work_id=502`).Scan(&targetState); err != nil {
		t.Fatal(err)
	}
	if rating != 5 || note != "keep me" || targetState != 0 {
		t.Fatalf("personal state after conflict: rating=%d note=%q target=%d", rating, note, targetState)
	}
}

// A directory rename keeps the established series and adopts the new episode
// path. The rename evidence must compare the old series against the new one.
func TestMergeCatalogSeriesMembershipFoldsRenamedDirectory(t *testing.T) {
	db := openMigratedTestDB(t)
	metadataReviewExec(t, db, `
		INSERT INTO file_source(id,code,display_name,source_type)
			VALUES(7,'series_rename','Series rename','openlist');
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,content_fingerprint,layout_fingerprint,last_seen_generation) VALUES
			(1,7,'/Creator/Old Show/Episode 1','Episode 1','same-fp','same-layout',10),
			(2,7,'/Creator/New Show/Episode 1','Episode 1','same-fp','same-layout',11);
		INSERT INTO catalog_series(id,title,source_id,source_key) VALUES
			(11,'Old Show',7,'/Creator/Old Show
old show'),
			(12,'New Show',7,'/Creator/New Show
new show');
		INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence) VALUES
			(11,1,1,'directory'),(12,2,1,'directory');
	`)

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, pair := range [][2]int64{{11, 12}, {12, 11}} {
		renamed, err := catalogSeriesRenameEvidenceTx(context.Background(), tx, pair[0], pair[1])
		if err != nil {
			t.Fatal(err)
		}
		if !renamed {
			t.Fatalf("rename evidence failed for series order %v", pair)
		}
	}
	if err = mergeCatalogSeriesMembershipTx(context.Background(), tx, 2, 1); err != nil {
		t.Fatalf("merge renamed series: %v", err)
	}
	var series, survivorMembers, staleMembers int
	if err = tx.QueryRow(`SELECT count(*) FROM catalog_series`).Scan(&series); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(`SELECT count(*) FROM catalog_series_member WHERE series_id=11`).Scan(&survivorMembers); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(`SELECT count(*) FROM catalog_series_member WHERE catalog_entry_id=2`).Scan(&staleMembers); err != nil {
		t.Fatal(err)
	}
	if series != 1 || survivorMembers != 1 || staleMembers != 0 {
		t.Fatalf("rename fold: series=%d survivor members=%d retired members=%d, want 1/1/0", series, survivorMembers, staleMembers)
	}
}
