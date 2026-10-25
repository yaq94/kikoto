// Package migrationfixture supplies the packaged migration catalog to tests
// with no-op placeholders for numbered files owned by branches that have not
// merged yet. The translation branch is authored before 097 and 098 are
// available, so local verification needs a contiguous chain. Placeholders are
// generated in memory and never written to the repository or a production
// binary.
package migrationfixture

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing/fstest"

	"github.com/yexca/kikoto/backend/migrations"
)

var migrationFSOnce = sync.OnceValues(func() (fs.FS, error) {
	files := fstest.MapFS{}
	for _, dir := range []string{".", "baseline", "compat"} {
		entries, err := fs.ReadDir(migrations.Files, dir)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if dir != "." {
				name = dir + "/" + name
			}
			data, err := fs.ReadFile(migrations.Files, name)
			if err != nil {
				return nil, err
			}
			files[name] = &fstest.MapFile{Data: data}
		}
	}
	for _, placeholder := range []struct {
		name string
		sql  string
	}{
		{"097_translation_chain_placeholder.sql", "-- Integration-order placeholder; the real 097 migration is supplied by its owning branch.\nSELECT 1;\n"},
		{"098_translation_chain_placeholder.sql", "-- Integration-order placeholder; the real 098 migration is supplied by its owning branch.\nSELECT 1;\n"},
	} {
		if _, exists := files[placeholder.name]; !exists {
			files[placeholder.name] = &fstest.MapFile{Data: []byte(placeholder.sql)}
		}
	}
	return files, nil
})

// FS returns the cached test catalog.
func FS() (fs.FS, error) { return migrationFSOnce() }

// TB is the subset of testing.TB the helpers need. Declaring it here keeps the
// package importable by the bench command without pulling in the testing
// package.
type TB interface {
	Helper()
	Fatal(args ...any)
	TempDir() string
}

// MustFS returns the test catalog or fails the test.
func MustFS(tb TB) fs.FS {
	tb.Helper()
	files, err := FS()
	if err != nil {
		tb.Fatal(err)
	}
	return files
}

var migrationDirOnce = sync.OnceValues(func() (string, error) {
	files, err := FS()
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "kikoto-test-migrations-")
	if err != nil {
		return "", err
	}
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		data, err := fs.ReadFile(files, entry.Name())
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dir, entry.Name()), data, 0o600); err != nil {
			return "", err
		}
	}
	return dir, nil
})

// Dir returns a materialized directory for directory-based Migrate calls.
func Dir(tb TB) string {
	tb.Helper()
	dir, err := migrationDirOnce()
	if err != nil {
		tb.Fatal(err)
	}
	return dir
}

// DirBefore materializes the test catalog up to, but excluding, filename. It
// lets a test build a previous schema, insert legacy rows, then apply the
// remaining migrations through Dir. The 097/098 placeholders are included so
// the staged chain stays contiguous.
func DirBefore(tb TB, filename string) string {
	tb.Helper()
	files, err := FS()
	if err != nil {
		tb.Fatal(err)
	}
	dir := tb.TempDir()
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		tb.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".sql") || name >= filename {
			continue
		}
		data, err := fs.ReadFile(files, name)
		if err != nil {
			tb.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			tb.Fatal(err)
		}
	}
	return dir
}

// TestFS returns a catalog that behaves like the packaged migration FS but
// carries the 097/098 integration placeholders. Production code keeps using
// migrations.Files; tests that compare the complete chain use this instead.
func TestFS() (fs.FS, error) {
	return FS()
}

// ReadFile reads one file from the test catalog.
func ReadFile(name string) ([]byte, error) {
	files, err := FS()
	if err != nil {
		return nil, err
	}
	return fs.ReadFile(files, name)
}

// CopyFile copies one catalog file into dir. It is used by tests that stage a
// previous schema before applying the remaining migrations.
func CopyFile(dir, name string) error {
	files, err := FS()
	if err != nil {
		return err
	}
	source, err := files.Open(name)
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := os.Create(filepath.Join(dir, filepath.Base(name)))
	if err != nil {
		return err
	}
	if _, err = io.Copy(target, source); err != nil {
		_ = target.Close()
		return err
	}
	return target.Close()
}
