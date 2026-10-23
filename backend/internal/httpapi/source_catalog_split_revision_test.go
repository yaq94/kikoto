package httpapi

import (
	"context"
	"testing"
)

// A file that disappeared in the current revision must not keep the split
// parent alive just because its old observation row is still present.
func TestCatalogSplitPlanIgnoresPreviousRevisionFiles(t *testing.T) {
	db := openMigratedTestDB(t)
	metadataReviewExec(t, db, `
		INSERT INTO file_source(id,code,display_name,source_type)
			VALUES(7,'split_revision','Split revision','openlist');
		INSERT INTO source_catalog_directory(file_source_id,path,generation,revision,partition_path,catalog_root,status,work_split)
			VALUES(7,'/Creator',7,2,'/Creator','/Creator','complete','file');
		INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes,publish_pending) VALUES
			(7,'/Creator','one.mp3',2,'audio',100,1),
			(7,'/Creator','two.mp3',2,'audio',200,1),
			(7,'/Creator','cover.jpg',1,'other',10,1);
	`)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	server := &Server{db: db}
	plan, parentNeeded, err := server.catalogSplitPlanForRoot(context.Background(), tx, 7, 7, "/Creator")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.direct) != 2 {
		t.Fatalf("split targets=%d, want the two current audio files", len(plan.direct))
	}
	if parentNeeded {
		t.Fatal("a previous revision's cover kept the split parent card alive")
	}
}
