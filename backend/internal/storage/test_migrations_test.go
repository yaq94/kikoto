package storage

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/yexca/kikoto/backend/internal/testfixture"
)

// completeMigrationDir builds a contiguous catalog from the packaged files.
// This branch adds 100 before 098/099 exist; the helper fills those numbers
// with no-op steps so chain tests exercise the real 097 and 100 SQL without
// committing placeholder migrations. A contiguous chain is copied unchanged.
func completeMigrationDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := testfixture.MigrationDirAt(dir, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	return dir
}

func completeMigrationFS(t *testing.T) fs.FS {
	t.Helper()
	return os.DirFS(completeMigrationDir(t))
}
