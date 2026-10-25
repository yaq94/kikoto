package testfixture

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
)

var numberedMigration = regexp.MustCompile(`^([0-9]{3})_[a-z0-9][a-z0-9_]*\.sql$`)

// MigrationDir copies a numbered migration chain into a temporary directory and
// fills any missing version with an empty step. Feature branches can add a
// migration before the intermediate numbers exist; the production loader
// requires a contiguous chain, so tests build that chain themselves instead of
// committing placeholder migrations or depending on a development baseline.
//
// A contiguous source chain is copied unchanged, so the helper is a no-op for
// integration and release branches.
func MigrationDir(source string) (string, error) {
	destination, err := os.MkdirTemp("", "kikoto-test-migrations-")
	if err != nil {
		return "", err
	}
	if err := MigrationDirAt(destination, source); err != nil {
		return "", err
	}
	return destination, nil
}

// MigrationDirAt builds the same contiguous chain inside an existing directory
// so a test can tie the copy's lifetime to t.TempDir.
func MigrationDirAt(destination, source string) error {
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	files := map[int]string{}
	maxVersion := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		match := numberedMigration.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		version, err := strconv.Atoi(match[1])
		if err != nil {
			return err
		}
		if _, exists := files[version]; exists {
			return fmt.Errorf("migration version %03d is used twice", version)
		}
		files[version] = entry.Name()
		if version > maxVersion {
			maxVersion = version
		}
	}
	if maxVersion == 0 {
		return fmt.Errorf("no numbered migrations in %s", source)
	}
	versions := make([]int, 0, len(files))
	for version := range files {
		versions = append(versions, version)
	}
	sort.Ints(versions)
	for version := 1; version <= maxVersion; version++ {
		name, ok := files[version]
		if !ok {
			name = fmt.Sprintf("%03d_test_gap.sql", version)
			if err := os.WriteFile(filepath.Join(destination, name), []byte("-- test-only contiguous-chain placeholder\nSELECT 1;\n"), 0o600); err != nil {
				return err
			}
			continue
		}
		contents, err := os.ReadFile(filepath.Join(source, name))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(destination, name), contents, 0o600); err != nil {
			return err
		}
	}
	for _, dir := range []string{"baseline", "compat"} {
		if err := copyMigrationSubdir(filepath.Join(source, dir), filepath.Join(destination, dir)); err != nil {
			return err
		}
	}
	return nil
}

func copyMigrationSubdir(source, destination string) error {
	entries, err := os.ReadDir(source)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		contents, err := os.ReadFile(filepath.Join(source, entry.Name()))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(destination, entry.Name()), contents, 0o600); err != nil {
			return err
		}
	}
	return nil
}
