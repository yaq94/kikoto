package httpapi

import (
	"context"
	"fmt"
	"testing"
)

// A completed incremental sync can leave many cards without audio. The
// post-sync status pass must still finish all of them while keeping each
// transaction bounded.
func TestCatalogSyncReconcileEmptyEntriesCompletesAcrossBoundedBatches(t *testing.T) {
	db := openMigratedTestDB(t)
	const sourceID, generation = int64(91), int64(9)
	if _, err := db.Exec(`INSERT INTO file_source(id,code,display_name,source_type)
		VALUES(?,?,?,?)`, sourceID, "reconcile-source", "Reconcile source", "openlist"); err != nil {
		t.Fatal(err)
	}
	const entries = 205
	for index := 0; index < entries; index++ {
		root := fmt.Sprintf("/empty/%03d", index)
		if _, err := db.Exec(`INSERT INTO source_catalog_entry(file_source_id,root_path,title,availability_status)
			VALUES(?,?,?,'available')`, sourceID, root, root); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO source_catalog_directory(
				file_source_id,path,generation,revision,partition_path,catalog_root,status)
			VALUES(?,?,?,?,?,?,'complete')`, sourceID, root, generation, 1, root, root); err != nil {
			t.Fatal(err)
		}
	}
	server := &Server{db: db}
	if err := server.reconcileCatalogSyncEmptyEntries(context.Background(), sourceID, generation); err != nil {
		t.Fatal(err)
	}
	var missing int
	if err := db.QueryRow(`SELECT count(*) FROM source_catalog_entry
		WHERE file_source_id=? AND availability_status='missing'`, sourceID).Scan(&missing); err != nil {
		t.Fatal(err)
	}
	if missing != entries {
		t.Fatalf("missing=%d, want %d", missing, entries)
	}
}
