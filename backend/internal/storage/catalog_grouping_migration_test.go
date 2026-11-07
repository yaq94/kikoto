package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCatalogGroupingMigration108UpgradeAndRollback(t *testing.T) {
	db := openMigrationManagerDB(t)
	source := filepath.Join("..", "..", "migrations")
	if err := Migrate(db, copyNumberedMigrationsThrough(t, source, 107)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO file_source(id,code,display_name,source_type) VALUES(7,'synthetic','Example source','openlist'); INSERT INTO source_catalog_entry(id,file_source_id,root_path,title) VALUES(1,7,'/Example','Example Work')`); err != nil {
		t.Fatal(err)
	}
	dir := copyNumberedMigrationsThrough(t, source, 108)
	file := filepath.Join(dir, "108_catalog_grouping.sql")
	valid, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(file, append(append([]byte{}, valid...), []byte("\nSELECT * FROM synthetic_missing_table;\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	if err = Migrate(db, dir); err == nil {
		t.Fatal("faulted108 succeeded")
	}
	assertSchemaVersion(t, db, 107)
	var n int
	if err = db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name='source_catalog_grouping'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rolled back stage=%d %v", n, err)
	}
	if err = os.WriteFile(file, valid, 0600); err != nil {
		t.Fatal(err)
	}
	if err = Migrate(db, dir); err != nil {
		t.Fatal(err)
	}
	assertSchemaVersion(t, db, 108)
	if err = db.QueryRow(`SELECT count(*) FROM published_source_catalog_entry WHERE id=1`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("stable existing identity=%d %v", n, err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM source_catalog_grouping`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("migration copied catalog=%d %v", n, err)
	}
	if err = Migrate(db, dir); err != nil {
		t.Fatal(err)
	}
	assertMigrationIntegrity(t, db)
}
