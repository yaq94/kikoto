package httpapi

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
)

func seedCatalogAudioDirectory(t *testing.T, db *sql.DB, sourceID int64, directory string, sizes []int64) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO source_catalog_directory(file_source_id,path,generation,revision,partition_path,catalog_root,status)
		VALUES(?,?,1,1,?,?,'complete')`, sourceID, directory, directory, directory); err != nil {
		t.Fatal(err)
	}
	for index, size := range sizes {
		if _, err := db.Exec(`INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes)
			VALUES(?,?,?,1,'audio',?)`, sourceID, directory, fmt.Sprintf("%02d.mp3", index), size); err != nil {
			t.Fatal(err)
		}
	}
}

func duplicateDirectoryResult(t *testing.T, parentSizes, childSizes []int64) bool {
	t.Helper()
	db := openMigratedTestDB(t)
	const sourceID = int64(84)
	seedCatalogCleanupJob(t, db, sourceID, 804)
	seedCatalogAudioDirectory(t, db, sourceID, "/collection", parentSizes)
	seedCatalogAudioDirectory(t, db, sourceID, "/collection/copy", childSizes)
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	duplicate, err := catalogDuplicateDirectoryTx(context.Background(), tx, sourceID, "/collection/copy")
	if err != nil {
		t.Fatal(err)
	}
	return duplicate
}

func TestCatalogDuplicateDirectoryMatchesFilesOneToOne(t *testing.T) {
	sizes := make([]int64, 20)
	for index := range sizes {
		sizes[index] = 10_000_000 + int64(index)*4_096
	}
	if !duplicateDirectoryResult(t, sizes, sizes) {
		t.Fatal("an identical audio set must be treated as a format copy")
	}
}

func TestCatalogDuplicateDirectoryRejectsRepeatedSizesWithoutCounterparts(t *testing.T) {
	// Two parent files happen to share the child's size. The other eighteen
	// parent files do not, so the child repeats no copied tree.
	parent := make([]int64, 20)
	child := make([]int64, 20)
	for index := range parent {
		parent[index] = 30_000_000 + int64(index)*4_096
		child[index] = 20_000_000
	}
	parent[0], parent[1] = 20_000_000, 20_000_000
	if duplicateDirectoryResult(t, parent, child) {
		t.Fatal("repeated sizes without one-to-one counterparts must not be a format copy")
	}
}
