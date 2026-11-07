package storage

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestTranslationRevisionMigration105UpgradeRestartAndBackupRestore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "library.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db, copyNumberedMigrationsThrough(t, filepath.Join("..", "..", "migrations"), 105)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO work(primary_code,title) VALUES('RJ00000000','Example Work');
		UPDATE translation_prefill_state SET status='complete' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, restore := range []bool{false, true} {
		if restore {
			if err := os.WriteFile(path, backup, 0600); err != nil {
				t.Fatal(err)
			}
		}
		db, err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := Migrate(db, completeMigrationDir(t)); err != nil {
			t.Fatal(err)
		}
		assertSchemaVersion(t, db, 106)
		var checked int64
		var title string
		if err := db.QueryRow(`SELECT checked_revision FROM translation_prefill_state WHERE id=1`).Scan(&checked); err != nil {
			t.Fatal(err)
		}
		if checked != -1 {
			t.Fatalf("old complete state was trusted: %d", checked)
		}
		if err := db.QueryRow(`SELECT title FROM work WHERE primary_code='RJ00000000'`).Scan(&title); err != nil || title != "Example Work" {
			t.Fatalf("work lost: %s %v", title, err)
		}
		if _, err := db.Exec(`UPDATE work SET title='Changed Work' WHERE primary_code='RJ00000000'`); err != nil {
			t.Fatal(err)
		}
		var before, after int64
		if err := db.QueryRow(`SELECT revision FROM translation_corpus_revision WHERE id=1`).Scan(&before); err != nil {
			t.Fatal(err)
		}
		if before <= 0 {
			t.Fatal("committed text update did not invalidate coverage")
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		db, err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := Migrate(db, completeMigrationDir(t)); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT revision FROM translation_corpus_revision WHERE id=1`).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if after != before {
			t.Fatalf("restart reset revision: %d to %d", before, after)
		}
		assertMigrationIntegrity(t, db)
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTranslationRevisionMigrationFailureRollsBackAndRetries(t *testing.T) {
	db := openMigrationManagerDB(t)
	source := filepath.Join("..", "..", "migrations")
	if err := Migrate(db, copyNumberedMigrationsThrough(t, source, 105)); err != nil {
		t.Fatal(err)
	}
	dir := copyNumberedMigrationsThrough(t, source, 106)
	path := filepath.Join(dir, "106_translation_corpus_revision.sql")
	valid, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	broken := append(append([]byte{}, valid...), []byte("\nSELECT value FROM synthetic_missing_table;\n")...)
	if err := os.WriteFile(path, broken, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db, dir); err == nil {
		t.Fatal("faulted migration succeeded")
	}
	assertSchemaVersion(t, db, 105)
	var tables, columns int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name='translation_corpus_revision'`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('translation_prefill_state') WHERE name='checked_revision'`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if tables != 0 || columns != 0 {
		t.Fatal("failed migration left partial schema")
	}
	if err := os.WriteFile(path, valid, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db, dir); err != nil {
		t.Fatal(err)
	}
	assertSchemaVersion(t, db, 106)
	var dirty sql.NullInt64
	if err := db.QueryRow(`SELECT dirty_version FROM schema_state WHERE id=1`).Scan(&dirty); err != nil {
		t.Fatal(err)
	}
	if dirty.Valid {
		t.Fatal("retry left dirty schema state")
	}
	assertMigrationIntegrity(t, db)
}
