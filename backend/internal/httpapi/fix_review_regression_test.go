package httpapi

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yexca/kikoto/backend/internal/openlist"
)

// A no-hash unique file is only covered when the canonical parent observed it
// in the current generation. An old-generation row at the same relative path
// and size must not make the current copy look covered, or retirement would
// discard the only current copy.
func TestFixReviewDuplicateUniqueCoverageRequiresCurrentGeneration(t *testing.T) {
	db := openMigratedTestDB(t)
	metadataReviewExec(t, db, `
		INSERT INTO file_source(id,code,display_name,source_type)
			VALUES(7,'fix_review_duplicate','Fix review duplicate','openlist');
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,last_seen_generation,discovered_generation)
			VALUES(100,7,'/Parent/Copy','Copy',10,10),
			      (200,7,'/Parent','Parent',11,11);
		INSERT INTO source_catalog_directory(file_source_id,path,generation,revision,partition_path,status)
			VALUES(7,'/Parent',11,1,'','complete'),
			      (7,'/Parent/Copy',11,1,'','complete');
		INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes)
			VALUES(7,'/Parent','A.mp3',1,'audio',100),
			      (7,'/Parent','B.mp3',1,'audio',200),
			      (7,'/Parent/Copy','A.mp3',1,'audio',100),
			      (7,'/Parent/Copy','B.mp3',1,'audio',200),
			      (7,'/Parent/Copy','C.mp3',1,'audio',300);
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes,observed_generation)
			VALUES(100,'A.mp3','A.mp3','audio',100,10),
			      (100,'B.mp3','B.mp3','audio',200,10),
			      (100,'C.mp3','C.mp3','audio',300,10),
			      (200,'A.mp3','A.mp3','audio',100,10),
			      (200,'B.mp3','B.mp3','audio',200,10),
			      (200,'Copy/C.mp3','Copy/C.mp3','audio',300,10);
	`)

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()

	target, err := catalogDuplicateCardRetireTargetTx(context.Background(), tx, 7, 11, 100, "/Parent/Copy")
	if err != nil {
		t.Fatal(err)
	}
	if target != 0 {
		t.Fatalf("old-generation file was accepted as coverage; retire target=%d, want 0", target)
	}
	var staleLive int
	if err = tx.QueryRow(`SELECT count(*) FROM source_catalog_entry
		WHERE id=100 AND file_source_id=7 AND relocated_to_id IS NULL`).Scan(&staleLive); err != nil {
		t.Fatal(err)
	}
	if staleLive != 1 {
		t.Fatal("stale card was retired even though the parent did not observe the unique file")
	}

	// Positive control: the same decision must retire the copy once the parent
	// has published that unique path in the current generation.
	if _, err = tx.Exec(`UPDATE source_catalog_file SET observed_generation=11
		WHERE catalog_entry_id=200 AND relative_path='Copy/C.mp3'`); err != nil {
		t.Fatal(err)
	}
	target, err = catalogDuplicateCardRetireTargetTx(context.Background(), tx, 7, 11, 100, "/Parent/Copy")
	if err != nil {
		t.Fatal(err)
	}
	if target != 200 {
		t.Fatalf("current-generation coverage retire target=%d, want 200", target)
	}
}

func fixReviewRelocationEntry(id int64, root string, lastSeen int64) catalogRelocationEntry {
	return catalogRelocationEntry{
		id:         id,
		root:       root,
		title:      "Episode 1",
		strong:     "same-content",
		layout:     "same-layout",
		discovered: lastSeen,
		lastSeen:   lastSeen,
	}
}

// Relocation passes the provisional card as the membership source and the
// established card as the destination. Rename evidence must still identify the
// older series from member generations, whichever argument order the caller
// used. Both directions must leave one series whose members all point at live
// cards.
func TestFixReviewRelocateCatalogEntryFoldsSeriesBothWays(t *testing.T) {
	cases := []struct {
		name       string
		old        catalogRelocationEntry
		target     catalogRelocationEntry
		wantSeries int64
		wantLive   int64
		wantAlias  int64
	}{
		{
			name:       "established old card adopts the new path",
			old:        fixReviewRelocationEntry(1, "/Creator/Old Show/Episode 1", 10),
			target:     fixReviewRelocationEntry(2, "/Creator/New Show/Episode 1", 11),
			wantSeries: 11,
			wantLive:   1,
			wantAlias:  2,
		},
		{
			name:       "established new card adopts the old path",
			old:        fixReviewRelocationEntry(2, "/Creator/New Show/Episode 1", 11),
			target:     fixReviewRelocationEntry(1, "/Creator/Old Show/Episode 1", 10),
			wantSeries: 12,
			wantLive:   2,
			wantAlias:  1,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			db := openMigratedTestDB(t)
			metadataReviewExec(t, db, `
				INSERT INTO file_source(id,code,display_name,source_type)
					VALUES(7,'fix_review_series','Fix review series','openlist');
				INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,content_fingerprint,layout_fingerprint,last_seen_generation,discovered_generation)
					VALUES(1,7,'/Creator/Old Show/Episode 1','Episode 1','same-content','same-layout',10,10),
					      (2,7,'/Creator/New Show/Episode 1','Episode 1','same-content','same-layout',11,11);
				INSERT INTO catalog_series(id,title,source_id,source_key)
					VALUES(11,'Old Show',7,'/Creator/Old Show
old show'),
					      (12,'New Show',7,'/Creator/New Show
new show');
				INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence)
					VALUES(11,1,1,'directory'),(12,2,1,'directory');
			`)

			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()
			if err = relocateCatalogEntryTx(context.Background(), tx, testCase.old, testCase.target, 11); err != nil {
				t.Fatalf("relocate: %v", err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}

			var series, members, deadMembers int
			if err = db.QueryRow(`SELECT
				(SELECT count(*) FROM catalog_series),
				(SELECT count(*) FROM catalog_series_member),
				(SELECT count(*) FROM catalog_series_member m
					JOIN source_catalog_entry e ON e.id=m.catalog_entry_id
					WHERE e.relocated_to_id IS NOT NULL)`).Scan(&series, &members, &deadMembers); err != nil {
				t.Fatal(err)
			}
			if series != 1 || members != 1 || deadMembers != 0 {
				t.Fatalf("series=%d members=%d dead members=%d, want one series with one live member", series, members, deadMembers)
			}
			var seriesID int64
			if err = db.QueryRow(`SELECT series_id FROM catalog_series_member`).Scan(&seriesID); err != nil {
				t.Fatal(err)
			}
			if seriesID != testCase.wantSeries {
				t.Fatalf("surviving series=%d, want %d", seriesID, testCase.wantSeries)
			}
			var liveID int64
			if err = db.QueryRow(`SELECT id FROM source_catalog_entry WHERE relocated_to_id IS NULL`).Scan(&liveID); err != nil {
				t.Fatal(err)
			}
			if liveID != testCase.wantLive {
				t.Fatalf("live entry=%d, want %d", liveID, testCase.wantLive)
			}
			var aliasTarget int64
			if err = db.QueryRow(`SELECT relocated_to_id FROM source_catalog_entry WHERE id=?`, testCase.wantAlias).Scan(&aliasTarget); err != nil {
				t.Fatal(err)
			}
			if aliasTarget != testCase.wantLive {
				t.Fatalf("alias %d points at %d, want %d", testCase.wantAlias, aliasTarget, testCase.wantLive)
			}
			var root string
			var generation int64
			if err = db.QueryRow(`SELECT root_path,last_seen_generation FROM source_catalog_entry WHERE id=?`, testCase.wantLive).Scan(&root, &generation); err != nil {
				t.Fatal(err)
			}
			if root != testCase.target.root || generation != 11 {
				t.Fatalf("survivor root=%q generation=%d, want %q/11", root, generation, testCase.target.root)
			}
		})
	}
}

// Automatic discovery must report the member safety limit instead of dropping
// the next episode after a batch has already been persisted. Seed exactly
// 10,000 members so the batch helper sees count >= maxCatalogSeriesMembers.
func TestFixReviewCatalogSeriesLimitReturnsExplicitError(t *testing.T) {
	db := openMigratedTestDB(t)
	started := time.Now()
	metadataReviewExec(t, db, `
		INSERT INTO file_source(id,code,display_name,source_type)
			VALUES(7,'fix_review_limit','Fix review limit','openlist');
		INSERT INTO catalog_series(id,title,source_id,source_key)
			VALUES(1,'Series',7,'/Series
series');
		WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i+1 FROM n WHERE i<9999)
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,last_seen_generation,discovered_generation)
			SELECT 100000+i,7,printf('/Series/Episode %d',i+1),printf('Episode %d',i+1),11,11 FROM n;
		INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence)
			SELECT 1,id,id-99999,'directory' FROM source_catalog_entry
			WHERE id BETWEEN 100000 AND 109999;
		INSERT INTO source_catalog_directory(file_source_id,path,generation,revision,partition_path,status)
			VALUES(7,'/Series',11,1,'','complete');
		INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes)
			VALUES(7,'/Series','Episode 1',1,'folder',0),
			      (7,'/Series','Episode 2',1,'folder',0);
	`)
	t.Logf("seeded 10,000 series members in %s", time.Since(started))

	const root = "/Series/Episode 10001"
	server := &Server{db: db}
	split := map[string]catalogEpisodeRootSplit{
		root: {
			Files: []openlist.File{{
				Path: "Episode 10001.mp3", Title: "Episode 10001", Kind: "audio", Size: 100,
			}},
			Title: "Episode 10001",
			Evidence: catalogSeriesEvidence{
				Key: "/Series\nseries", Title: "Series", Position: 10001, Explicit: true,
			},
		},
	}
	ids, err := server.persistCatalogEpisodeRootBatch(context.Background(), 7, []string{root}, split)
	if !errors.Is(err, errCatalogSeriesTooLarge) {
		t.Fatalf("persist error=%v, want errCatalogSeriesTooLarge", err)
	}
	if len(ids) != 0 {
		t.Fatalf("persisted ids=%v after the limit error", ids)
	}

	var entries, members, series int
	if err = db.QueryRow(`SELECT
		(SELECT count(*) FROM source_catalog_entry WHERE root_path=?),
		(SELECT count(*) FROM catalog_series_member),
		(SELECT count(*) FROM catalog_series)`, root).Scan(&entries, &members, &series); err != nil {
		t.Fatal(err)
	}
	if entries != 0 || members != 10000 || series != 1 {
		t.Fatalf("after limit error entries=%d members=%d series=%d, want 0/10000/1", entries, members, series)
	}
}
