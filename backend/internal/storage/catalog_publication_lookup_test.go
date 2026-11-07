package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCatalogLookupMigration109UpgradeAndRollback(t *testing.T) {
	db := openMigrationManagerDB(t)
	source := filepath.Join("..", "..", "migrations")
	if err := Migrate(db, copyNumberedMigrationsThrough(t, source, 108)); err != nil {
		t.Fatal(err)
	}
	dir := copyNumberedMigrationsThrough(t, source, 109)
	file := filepath.Join(dir, "109_catalog_lookup_affinity.sql")
	valid, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(file, append(append([]byte{}, valid...), []byte("\nSELECT * FROM synthetic_missing_table;\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	if err = Migrate(db, dir); err == nil {
		t.Fatal("faulted migration succeeded")
	}
	assertSchemaVersion(t, db, 108)
	var view string
	if err = db.QueryRow(`SELECT sql FROM sqlite_schema WHERE name='published_source_catalog_entry'`).Scan(&view); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(view, "CAST(COALESCE") {
		t.Fatal("failed migration replaced the reader view")
	}
	if err = os.WriteFile(file, valid, 0600); err != nil {
		t.Fatal(err)
	}
	if err = Migrate(db, dir); err != nil {
		t.Fatal(err)
	}
	assertSchemaVersion(t, db, 109)
	if err = Migrate(db, dir); err != nil {
		t.Fatal(err)
	}
	assertMigrationIntegrity(t, db)
}

// Translation insertion resolves one stable entity. A correlated lookup must
// keep primary-key probes on both publication arms, even if work_id uses a
// fallback identity expression; scanning the catalog here makes bulk writes
// quadratic. No timing threshold depends on the runner or coverage overhead.
func TestCatalogPublicationCorrelatedIdentityLookupUsesKeys(t *testing.T) {
	db := openMigrationManagerDB(t)
	if err := Migrate(db, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`EXPLAIN QUERY PLAN SELECT (
	 SELECT e.file_source_id FROM published_source_catalog_entry e WHERE e.id=ct.entity_id
	) FROM content_translation ct WHERE ct.id=?`, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err = rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(detail, "SCAN live") || strings.HasPrefix(detail, "SCAN e") {
			t.Errorf("entity lookup scans the catalog: %s", detail)
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
}
