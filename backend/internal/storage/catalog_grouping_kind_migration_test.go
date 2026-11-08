package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCatalogGroupingKindMigration111UpgradeAndRollback(t *testing.T) {
	db := openMigrationManagerDB(t)
	source := filepath.Join("..", "..", "migrations")
	if err := Migrate(db, copyNumberedMigrationsThrough(t, source, 110)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO file_source(id,code,display_name,source_type)
		VALUES(7,'example_remote_a','Example Remote A','openlist');
		INSERT INTO source_catalog_directory(file_source_id,path,generation,revision,partition_path,status)
		VALUES(7,'/Example',1,1,'/Example','complete');
		INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes)
		VALUES(7,'/Example','Example Track.mp3',1,'audio',1),(7,'/Example','Example Folder',1,'folder',0);`); err != nil {
		t.Fatal(err)
	}
	dir := copyNumberedMigrationsThrough(t, source, 111)
	file := filepath.Join(dir, "111_catalog_grouping_kind_order.sql")
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
	assertSchemaVersion(t, db, 110)
	var columns int
	if err = db.QueryRow(`SELECT count(*) FROM pragma_index_info('source_catalog_observation_revision')`).Scan(&columns); err != nil || columns != 4 {
		t.Fatalf("rollback lost the original observation index: columns=%d err=%v", columns, err)
	}
	if err = os.WriteFile(file, valid, 0600); err != nil {
		t.Fatal(err)
	}
	if err = Migrate(db, dir); err != nil {
		t.Fatal(err)
	}
	assertSchemaVersion(t, db, 111)
	if err = db.QueryRow(`SELECT count(*) FROM pragma_index_info('source_catalog_observation_revision')`).Scan(&columns); err != nil || columns != 5 {
		t.Fatalf("ordered observation index: columns=%d err=%v", columns, err)
	}
	if err = Migrate(db, dir); err != nil {
		t.Fatal(err)
	}
	var name string
	if err = db.QueryRow(`SELECT name FROM source_catalog_observation
		WHERE file_source_id=7 AND directory_path='/Example' AND revision=1 AND kind='folder'
		ORDER BY name LIMIT 64`).Scan(&name); err != nil || name != "Example Folder" {
		t.Fatalf("retained folder=%q err=%v", name, err)
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM source_catalog_observation`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("retained observations=%d err=%v", count, err)
	}
	assertMigrationIntegrity(t, db)
}
