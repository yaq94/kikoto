package httpapi

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
)

type catalogAudioTestFile struct {
	name string
	size int64
	hash string
}

func seedCatalogAudioDirectory(t *testing.T, db *sql.DB, sourceID int64, directory string, sizes []int64) {
	t.Helper()
	files := make([]catalogAudioTestFile, 0, len(sizes))
	for index, size := range sizes {
		files = append(files, catalogAudioTestFile{name: fmt.Sprintf("%02d.mp3", index), size: size})
	}
	seedCatalogAudioDirectoryFiles(t, db, sourceID, directory, files)
}

func seedCatalogAudioDirectoryFiles(t *testing.T, db *sql.DB, sourceID int64, directory string, files []catalogAudioTestFile) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO source_catalog_directory(file_source_id,path,generation,revision,partition_path,catalog_root,status)
		VALUES(?,?,1,1,?,?,'complete')`, sourceID, directory, directory, directory); err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if _, err := db.Exec(`INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes,content_hash)
				VALUES(?,?,?,1,'audio',?,?)`, sourceID, directory, file.name, file.size, file.hash); err != nil {
			t.Fatal(err)
		}
	}
}

func duplicateDirectoryResult(t *testing.T, parentSizes, childSizes []int64) bool {
	t.Helper()
	parent := make([]catalogAudioTestFile, 0, len(parentSizes))
	child := make([]catalogAudioTestFile, 0, len(childSizes))
	for index, size := range parentSizes {
		parent = append(parent, catalogAudioTestFile{name: fmt.Sprintf("%02d.mp3", index), size: size})
	}
	for index, size := range childSizes {
		child = append(child, catalogAudioTestFile{name: fmt.Sprintf("%02d.mp3", index), size: size})
	}
	return duplicateDirectoryFilesResult(t, parent, child)
}

func duplicateDirectoryFilesResult(t *testing.T, parent, child []catalogAudioTestFile) bool {
	t.Helper()
	db := openMigratedTestDB(t)
	const sourceID = int64(84)
	seedCatalogCleanupJob(t, db, sourceID, 804)
	seedCatalogAudioDirectoryFiles(t, db, sourceID, "/collection", parent)
	seedCatalogAudioDirectoryFiles(t, db, sourceID, "/collection/copy", child)
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	duplicate, err := catalogDuplicateDirectoryTx(context.Background(), tx, sourceID, 1, "/collection/copy")
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

func TestCatalogDuplicateDirectoryMatchesSmallPackageCopy(t *testing.T) {
	files := make([]catalogAudioTestFile, 19)
	for index := range files {
		files[index] = catalogAudioTestFile{
			name: fmt.Sprintf("Track %02d.mp3", index+1),
			size: 10_000_000 + int64(index)*4_096,
		}
	}
	if !duplicateDirectoryFilesResult(t, files, files) {
		t.Fatal("a small package copy must still be treated as a duplicate")
	}
}

func TestCatalogDuplicateDirectoryKeepsDistinctSmallWork(t *testing.T) {
	parent := make([]catalogAudioTestFile, 19)
	child := make([]catalogAudioTestFile, 19)
	for index := range parent {
		parent[index] = catalogAudioTestFile{
			name: fmt.Sprintf("Track %02d.mp3", index+1),
			size: 10_000_000 + int64(index)*4_096,
		}
		child[index] = catalogAudioTestFile{
			name: fmt.Sprintf("Track %02d.mp3", index+1),
			size: 20_000_000 + int64(index)*8_192,
		}
	}
	if duplicateDirectoryFilesResult(t, parent, child) {
		t.Fatal("two small works with different recordings must not be merged")
	}
}

func TestCatalogDuplicateDirectoryKeepsSmallEqualSizeDifferentNames(t *testing.T) {
	parent := make([]catalogAudioTestFile, 19)
	child := make([]catalogAudioTestFile, 19)
	for index := range parent {
		size := 10_000_000 + int64(index)*4_096
		parent[index] = catalogAudioTestFile{name: fmt.Sprintf("Parent %02d.mp3", index+1), size: size}
		child[index] = catalogAudioTestFile{name: fmt.Sprintf("Child %02d.mp3", index+1), size: size}
	}
	if duplicateDirectoryFilesResult(t, parent, child) {
		t.Fatal("two small works with different names must not be merged even when their sizes match")
	}
}

func TestCatalogDuplicateDirectorySmallMatchesExactNamesAndSizes(t *testing.T) {
	files := make([]catalogAudioTestFile, 3)
	for index := range files {
		files[index] = catalogAudioTestFile{
			name: fmt.Sprintf("Track %02d.mp3", index+1),
			size: 10_000_000 + int64(index)*4_096,
		}
	}
	if !duplicateDirectoryFilesResult(t, files, files) {
		t.Fatal("three files with exact names and sizes must be recognized as a copy")
	}
}

func TestCatalogDuplicateDirectorySmallRequiresExactSizeWithoutHash(t *testing.T) {
	parent := make([]catalogAudioTestFile, 3)
	child := make([]catalogAudioTestFile, 3)
	for index := range parent {
		parent[index] = catalogAudioTestFile{
			name: fmt.Sprintf("Track %02d.mp3", index+1),
			size: 10_000_000 + int64(index)*4_096,
		}
		child[index] = parent[index]
	}
	child[1].size += 800
	if duplicateDirectoryFilesResult(t, parent, child) {
		t.Fatal("a small folder with different content sizes must not be merged by tolerance")
	}
}

func TestCatalogDuplicateDirectorySmallUsesContentHashWhenAvailable(t *testing.T) {
	parent := make([]catalogAudioTestFile, 3)
	child := make([]catalogAudioTestFile, 3)
	for index := range parent {
		hash := fmt.Sprintf("sha256:%064d", index+1)
		parent[index] = catalogAudioTestFile{
			name: fmt.Sprintf("Track %02d.mp3", index+1),
			size: 10_000_000 + int64(index)*4_096,
			hash: hash,
		}
		child[index] = catalogAudioTestFile{
			name: fmt.Sprintf("Renamed %02d.mp3", index+1),
			size: 20_000_000 + int64(index)*4_096,
			hash: hash,
		}
	}
	if !duplicateDirectoryFilesResult(t, parent, child) {
		t.Fatal("matching provider content hashes must identify a format copy regardless of names and sizes")
	}
}

func TestCatalogDuplicateDirectoryDifferentHashesOverrideMatchingSizes(t *testing.T) {
	parent := make([]catalogAudioTestFile, 3)
	child := make([]catalogAudioTestFile, 3)
	for index := range parent {
		parent[index] = catalogAudioTestFile{
			name: fmt.Sprintf("Track %02d.mp3", index+1),
			size: 10_000_000 + int64(index)*4_096,
			hash: fmt.Sprintf("sha256:parent-%d", index+1),
		}
		child[index] = catalogAudioTestFile{
			name: fmt.Sprintf("Track %02d.mp3", index+1),
			size: parent[index].size,
			hash: fmt.Sprintf("sha256:child-%d", index+1),
		}
	}
	if duplicateDirectoryFilesResult(t, parent, child) {
		t.Fatal("different provider hashes must not be merged by matching names and sizes")
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

func TestCatalogDuplicateDirectoryRejectsSubsetOfLargerParent(t *testing.T) {
	// Twenty tracks selected from a two-hundred-track collection is a real
	// release, not a format copy.
	parent := make([]int64, 200)
	child := make([]int64, 20)
	for index := range parent {
		parent[index] = 10_000_000 + int64(index)*4_096
	}
	copy(child, parent[:20])
	if duplicateDirectoryResult(t, parent, child) {
		t.Fatal("a small selection of a large collection was treated as a copy")
	}
}

func TestCatalogDuplicateDirectoryIgnoresStaleParentSizes(t *testing.T) {
	db := openMigratedTestDB(t)
	const sourceID = int64(85)
	seedCatalogCleanupJob(t, db, sourceID, 805)
	sizes := make([]int64, 20)
	for index := range sizes {
		sizes[index] = 10_000_000 + int64(index)*4_096
	}
	seedCatalogAudioDirectory(t, db, sourceID, "/collection", sizes)
	seedCatalogAudioDirectory(t, db, sourceID, "/collection/copy", sizes)
	// The parent failed this generation; its old sizes are not evidence that
	// the child repeats it.
	if _, err := db.Exec(`UPDATE source_catalog_directory SET generation=1,status='failed'
		WHERE file_source_id=? AND path='/collection'`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE source_catalog_directory SET generation=2,status='complete'
		WHERE file_source_id=? AND path='/collection/copy'`, sourceID); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	duplicate, err := catalogDuplicateDirectoryTx(context.Background(), tx, sourceID, 2, "/collection/copy")
	if err != nil {
		t.Fatal(err)
	}
	if duplicate {
		t.Fatal("a failed parent generation was used as copy evidence")
	}
}
