package httpapi

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/yexca/kikoto/backend/internal/testfixture"
)

// completeMigrationFS builds a contiguous catalog from the packaged files.
// This branch adds 100 before 098/099 exist; the helper fills those numbers
// with no-op steps so HTTP tests exercise the real 097 and 100 SQL without
// committing placeholder migrations.
func completeMigrationFS(t *testing.T) fs.FS {
	t.Helper()
	return os.DirFS(completeMigrationDir(t))
}

func completeMigrationDir(t *testing.T) string {
	t.Helper()
	dir, err := testfixture.MigrationDir(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
