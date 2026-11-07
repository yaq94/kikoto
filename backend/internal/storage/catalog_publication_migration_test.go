package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCatalogPublicationMigration107UpgradeRestartAndRestore(t *testing.T) {
	for _, base := range []int{105, 106} {
		t.Run(string(rune('a'+base-105)), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "catalog.db")
			db, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if err = Migrate(db, copyNumberedMigrationsThrough(t, filepath.Join("..", "..", "migrations"), base)); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(`INSERT INTO work(primary_code,title) VALUES('RJ00000000','Example Work');
			INSERT INTO source_catalog_entry(id,root_path,title,work_id) VALUES(1,'/Example Work','Example Work',1);
			INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES(1,'old.mp3','Example Track','audio',1)`); err != nil {
				t.Fatal(err)
			}
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}
			backup, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, restore := range []bool{false, true} {
				if restore {
					if err = os.WriteFile(path, backup, 0600); err != nil {
						t.Fatal(err)
					}
				}
				db, err = Open(path)
				if err != nil {
					t.Fatal(err)
				}
				if err = Migrate(db, completeMigrationDir(t)); err != nil {
					t.Fatal(err)
				}
				assertSchemaVersion(t, db, 107)
				var title, relative string
				var versions int
				if err = db.QueryRow(`SELECT e.title,f.relative_path FROM published_source_catalog_entry e JOIN published_source_catalog_file f ON f.catalog_entry_id=e.id WHERE e.id=1`).Scan(&title, &relative); err != nil || title != "Example Work" || relative != "old.mp3" {
					t.Fatalf("baseline lost: %s %s %v", title, relative, err)
				}
				if err = db.QueryRow(`SELECT count(*) FROM source_catalog_file_publication_before`).Scan(&versions); err != nil || versions != 0 {
					t.Fatalf("migration copied baseline: %d %v", versions, err)
				}
				if err = Migrate(db, completeMigrationDir(t)); err != nil {
					t.Fatal(err)
				}
				assertMigrationIntegrity(t, db)
				if err = db.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestCatalogPublicationMigration107FailureRollsBack(t *testing.T) {
	db := openMigrationManagerDB(t)
	source := filepath.Join("..", "..", "migrations")
	if err := Migrate(db, copyNumberedMigrationsThrough(t, source, 106)); err != nil {
		t.Fatal(err)
	}
	dir := copyNumberedMigrationsThrough(t, source, 107)
	path := filepath.Join(dir, "107_catalog_publication.sql")
	valid, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, append(append([]byte{}, valid...), []byte("\nSELECT value FROM synthetic_missing_table;\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	if err = Migrate(db, dir); err == nil {
		t.Fatal("faulted107 succeeded")
	}
	assertSchemaVersion(t, db, 106)
	var n int
	if err = db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name='source_catalog_publication'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("partial107 remains: %d %v", n, err)
	}
	if err = os.WriteFile(path, valid, 0600); err != nil {
		t.Fatal(err)
	}
	if err = Migrate(db, dir); err != nil {
		t.Fatal(err)
	}
	assertSchemaVersion(t, db, 107)
	assertMigrationIntegrity(t, db)
}
