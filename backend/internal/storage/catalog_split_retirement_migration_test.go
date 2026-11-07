package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCatalogSplitRetirementMigration110UpgradeAndRollback(t *testing.T) {
	db := openMigrationManagerDB(t)
	source := filepath.Join("..", "..", "migrations")
	if err := Migrate(db, copyNumberedMigrationsThrough(t, source, 109)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO file_source(id,code,display_name,source_type) VALUES(7,'example_remote_a','Example Remote A','openlist');
	 INSERT INTO source_catalog_entry(id,file_source_id,root_path,title) VALUES(1,7,'/Example','Example Work');
	 INSERT INTO source_catalog_publication(id,file_source_id,owner) VALUES(1,7,'generation:1');`); err != nil {
		t.Fatal(err)
	}
	dir := copyNumberedMigrationsThrough(t, source, 110)
	file := filepath.Join(dir, "110_catalog_split_retirement.sql")
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
	assertSchemaVersion(t, db, 109)
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name='source_catalog_split_retirement'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rolled-back retirement state=%d err=%v", count, err)
	}
	if err = os.WriteFile(file, valid, 0600); err != nil {
		t.Fatal(err)
	}
	if err = Migrate(db, dir); err != nil {
		t.Fatal(err)
	}
	assertSchemaVersion(t, db, 110)
	if err = db.QueryRow(`SELECT count(*) FROM published_source_catalog_entry WHERE id=1`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("existing identity=%d err=%v", count, err)
	}
	if _, err = db.Exec(`INSERT INTO source_catalog_split_retirement(operation_id,root_path,generation,after_entry_id) VALUES(1,'/Example',1,64)`); err != nil {
		t.Fatal(err)
	}
	if err = Migrate(db, dir); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT after_entry_id FROM source_catalog_split_retirement WHERE operation_id=1`).Scan(&count); err != nil || count != 64 {
		t.Fatalf("migration replay changed checkpoint=%d err=%v", count, err)
	}
	assertMigrationIntegrity(t, db)
}
