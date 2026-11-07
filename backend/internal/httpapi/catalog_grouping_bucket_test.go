package httpapi

import (
	"context"
	"testing"
)

func TestCatalogGroupingSinglePrefixAggregatesBoundedInputs(t *testing.T) {
	db := openMigratedTestDB(t)
	seedCatalogPlanSource(t, db)
	metadataReviewExec(t, db, `INSERT INTO source_catalog_directory(file_source_id,path,generation,revision,partition_path,status) VALUES(7,'/Creator',1,1,'/Creator','complete');
	WITH RECURSIVE n(i) AS(SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<1025) INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes) SELECT 7,'/Creator',printf('RJ00000001 Example Recording %04d.mp3',i),1,'audio',1 FROM n`)
	phase, consumed, batches := "", 0, 0
	ctx := context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true)
	ctx = context.WithValue(ctx, catalogGroupingObserveKey{}, func(event string, n int) {
		switch event {
		case "phase:prefix":
			phase = "prefix"
		case "phase:mode":
			phase = "mode"
		}
		if event == "aggregate" && phase == "prefix" {
			consumed += n
			batches++
			if n > 64 {
				t.Fatalf("aggregate inputs=%d", n)
			}
		}
	})
	id, err := (&Server{db: db}).prepareCatalogGrouping(ctx, catalogGroupingIndexInput{SourceID: 7, Generation: 1, Root: "/Creator", Revision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if consumed != 1025 || batches != 17 {
		t.Fatalf("prefix input work=%d batches=%d, want 1025/17", consumed, batches)
	}
	var count int
	if err = db.QueryRow(`SELECT json_extract(payload,'$.count') FROM source_catalog_grouping_bucket WHERE grouping_id=? AND kind='prefix'`, id).Scan(&count); err != nil || count != 1025 {
		t.Fatalf("bucket count=%d %v", count, err)
	}
}
