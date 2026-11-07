package httpapi

import (
	"context"
	"testing"
)

func TestCatalogGroupingLongHistoryCleanupIsBounded(t *testing.T) {
	audit := &catalogWriteAudit{}
	db := openCatalogWriteAuditedDB(t, audit)
	seedCatalogPlanSource(t, db)
	metadataReviewExec(t, db, `INSERT INTO source_catalog_directory(file_source_id,path,generation,revision,partition_path,status) VALUES(7,'/Creator',1,1,'/Creator','complete');
	WITH RECURSIVE n(i) AS(SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<129) INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes) SELECT 7,'/Creator',printf('Example Recording %04d.mp3',i),1,'audio',1 FROM n`)
	s := &Server{db: db}
	ctx := context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true)
	// Seed actual prepared evidence for several historical generations. Keeping
	// different roots while preparing avoids exercising cleanup during setup.
	var last int64
	for revision := 1; revision <= 12; revision++ {
		metadataReviewExec(t, db, `UPDATE source_catalog_observation SET revision=?;UPDATE source_catalog_directory SET revision=?`, revision, revision)
		id, err := s.prepareCatalogGrouping(ctx, catalogGroupingIndexInput{SourceID: 7, Generation: 1, Root: "/Creator", Revision: revision})
		if err != nil {
			t.Fatal(err)
		}
		metadataReviewExec(t, db, `UPDATE source_catalog_grouping SET root_path=root_path||':history:'||id WHERE id=?`, id)
		last = id
	}
	metadataReviewExec(t, db, `UPDATE source_catalog_grouping SET root_path='/Creator'`)
	audit.start()
	if err := s.cleanupCatalogGroupingVersions(ctx, catalogGroupingIndexInput{SourceID: 7, Generation: 1, Root: "/Creator", Revision: 12}); err != nil {
		t.Fatal(err)
	}
	audit.stop()
	var groups, names int
	if err := db.QueryRow(`SELECT count(*),(SELECT count(*) FROM source_catalog_grouping_name WHERE grouping_id=?) FROM source_catalog_grouping`, last).Scan(&groups, &names); err != nil || groups != 1 || names != 129 {
		t.Fatalf("cleanup lost current or retained history: groups=%d names=%d %v", groups, names, err)
	}
	writes, txs, peak, tables := audit.allDetails()
	if peak > 64 {
		t.Fatalf("cleanup transaction=%d %v", peak, tables)
	}
	t.Logf("history=12 retained=1 total_writes=%d transactions=%d peak_rows=%d tables=%v", writes, txs, peak, tables)
}
