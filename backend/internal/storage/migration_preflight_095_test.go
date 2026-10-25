package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yexca/kikoto/backend/migrations"
)

func legacy095MigrationSQL(t *testing.T) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("testdata", "legacy_095_no_foreign_key.sql"))
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func TestLegacy095ChecksumConstantsMatchFrozenHistory(t *testing.T) {
	if legacy095ChecksumCascade != currentMigration095Checksum {
		t.Fatalf("historical CASCADE checksum %s differs from current %s", legacy095ChecksumCascade, currentMigration095Checksum)
	}
	current, err := migrations.Files.ReadFile(legacyMigration095Filename)
	if err != nil {
		t.Fatal(err)
	}
	if got := migrationChecksum(current); got != currentMigration095Checksum {
		t.Fatalf("current %s checksum = %s, want frozen %s", legacyMigration095Filename, got, currentMigration095Checksum)
	}
	if got := migrationChecksum([]byte(legacy095MigrationSQL(t))); got != legacy095ChecksumWithoutForeignKey {
		t.Fatalf("historical no-foreign-key checksum = %s, want %s", got, legacy095ChecksumWithoutForeignKey)
	}
}

func TestLegacy095PreflightNormalizesExactHistoricalDatabase(t *testing.T) {
	db, root := openLegacy095Database(t)
	if err := seedLegacy095Rows(db); err != nil {
		t.Fatal(err)
	}
	backupDir := filepath.Join(root, "backups")

	if err := MigrateFSWithOptions(db, migrations.Files, "test", MigrateOptions{PreflightBackupDir: backupDir}); err != nil {
		t.Fatalf("upgrade legacy 095 database: %v", err)
	}

	var version int
	var checksum string
	if err := db.QueryRow(`
		SELECT version, checksum
		FROM schema_migration
		WHERE filename = ?
	`, legacyMigration095Filename).Scan(&version, &checksum); err != nil {
		t.Fatal(err)
	}
	if version != 95 || checksum != currentMigration095Checksum {
		t.Fatalf("095 ledger = version %d checksum %s, want 95/%s", version, checksum, currentMigration095Checksum)
	}
	var current int
	if err := db.QueryRow("SELECT current_version FROM schema_state WHERE id = 1").Scan(&current); err != nil {
		t.Fatal(err)
	}
	if current != latestNumberedMigrationVersion {
		t.Fatalf("current version = %d, want %d", current, latestNumberedMigrationVersion)
	}
	assertCurrentSourceFieldTranslationSchema(t, db)
	assertMigrationIntegrity(t, db)

	var validRows, orphanRows int
	if err := db.QueryRow("SELECT COUNT(*) FROM source_field_translation WHERE cache_key = 'legacy-valid'").Scan(&validRows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM source_field_translation WHERE cache_key = 'legacy-orphan'").Scan(&orphanRows); err != nil {
		t.Fatal(err)
	}
	if validRows != 1 || orphanRows != 0 {
		t.Fatalf("rebuilt rows: valid=%d orphan=%d, want 1/0", validRows, orphanRows)
	}

	audits := legacy095AuditFiles(t, backupDir)
	if len(audits) != 1 {
		t.Fatalf("preflight audit files = %v, want one", audits)
	}
	var audit legacy095PreflightAudit
	contents, err := os.ReadFile(audits[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(contents, &audit); err != nil {
		t.Fatal(err)
	}
	if audit.SchemaVersion != 95 ||
		audit.Migration != legacyMigration095Filename ||
		audit.OldChecksum != legacy095ChecksumWithoutForeignKey ||
		audit.NewChecksum != currentMigration095Checksum ||
		audit.SchemaFingerprint == "" ||
		audit.DatabaseBackupPath == "" ||
		audit.Result != Legacy095PreflightNormalized {
		t.Fatalf("unexpected preflight audit: %+v", audit)
	}
	if !filepath.IsAbs(audit.DatabaseBackupPath) {
		t.Fatalf("backup path %q is not absolute", audit.DatabaseBackupPath)
	}
	if _, err := os.Stat(audit.DatabaseBackupPath); err != nil {
		t.Fatalf("preflight backup %q: %v", audit.DatabaseBackupPath, err)
	}
	backup, err := sql.Open("sqlite", "file:"+filepath.ToSlash(audit.DatabaseBackupPath)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = backup.Close() }()
	var backupChecksum string
	if err := backup.QueryRow("SELECT checksum FROM schema_migration WHERE filename = ?", legacyMigration095Filename).Scan(&backupChecksum); err != nil {
		t.Fatalf("read preflight backup ledger: %v", err)
	}
	if backupChecksum != legacy095ChecksumWithoutForeignKey {
		t.Fatalf("preflight backup checksum = %s, want the pre-normalization %s", backupChecksum, legacy095ChecksumWithoutForeignKey)
	}
	if _, err := time.Parse(time.RFC3339Nano, audit.ExecutedAt); err != nil {
		t.Fatalf("audit executedAt %q: %v", audit.ExecutedAt, err)
	}
	backups, err := ListBackups(backupDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 || backups[0].Kind != BackupKindPreMigration {
		t.Fatalf("preflight backups = %+v, want one pre-migration snapshot", backups)
	}

	// A restart sees the normalized checksum and does no preflight work.
	if err := MigrateFSWithOptions(db, migrations.Files, "test-restart", MigrateOptions{PreflightBackupDir: backupDir}); err != nil {
		t.Fatalf("restart after normalization: %v", err)
	}
	if got := len(legacy095AuditFiles(t, backupDir)); got != 1 {
		t.Fatalf("restart wrote another audit record: %d", got)
	}
	assertMigrationIntegrity(t, db)
}

func TestLegacy095PreflightRejectsSchemaMismatch(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		want   string
		mutate func(*testing.T, *sql.DB)
	}{
		{
			name: "extra column",
			want: "schema fingerprint",
			mutate: func(t *testing.T, db *sql.DB) {
				if _, err := db.Exec("ALTER TABLE source_field_translation ADD COLUMN unexpected TEXT"); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "missing column and index",
			want: "schema fingerprint",
			mutate: func(t *testing.T, db *sql.DB) {
				if _, err := db.Exec(`
					DROP INDEX idx_source_field_translation_owner;
					ALTER TABLE source_field_translation DROP COLUMN path;
				`); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "extra index",
			want: "schema fingerprint",
			mutate: func(t *testing.T, db *sql.DB) {
				if _, err := db.Exec("CREATE INDEX unexpected_source_field_translation_index ON source_field_translation(language)"); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:   "wrong foreign key",
			want:   "schema fingerprint",
			mutate: replaceLegacy095ForeignKeyWithSetNull,
		},
		{
			name: "extra trigger",
			want: "schema fingerprint",
			mutate: func(t *testing.T, db *sql.DB) {
				if _, err := db.Exec(`
					CREATE TRIGGER unexpected_source_field_translation_trigger
					AFTER INSERT ON source_field_translation
					BEGIN
						SELECT 1;
					END;
				`); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "missing table",
			want: "table is missing",
			mutate: func(t *testing.T, db *sql.DB) {
				if _, err := db.Exec("DROP TABLE source_field_translation"); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db, root := openLegacy095Database(t)
			if err := seedLegacy095Rows(db); err != nil {
				t.Fatal(err)
			}
			testCase.mutate(t, db)
			backupDir := filepath.Join(root, "backups")

			err := MigrateFSWithOptions(db, migrations.Files, "test", MigrateOptions{PreflightBackupDir: backupDir})
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("migration error = %v, want %q refusal", err, testCase.want)
			}
			var current int
			if err := db.QueryRow("SELECT current_version FROM schema_state WHERE id = 1").Scan(&current); err != nil {
				t.Fatal(err)
			}
			if current != 95 {
				t.Fatalf("schema version after rejected preflight = %d, want 95", current)
			}
			var checksum string
			if err := db.QueryRow("SELECT checksum FROM schema_migration WHERE filename = ?", legacyMigration095Filename).Scan(&checksum); err != nil {
				t.Fatal(err)
			}
			if checksum != legacy095ChecksumWithoutForeignKey {
				t.Fatalf("rejected preflight changed checksum to %s", checksum)
			}
			if audits := legacy095AuditFiles(t, backupDir); len(audits) != 0 {
				t.Fatalf("rejected preflight wrote audit files: %v", audits)
			}
			backups, err := ListBackups(backupDir)
			if err != nil {
				t.Fatal(err)
			}
			if len(backups) != 0 {
				t.Fatalf("rejected preflight wrote backups: %+v", backups)
			}
		})
	}
}

func TestLegacy095PreflightDoesNotBypassUnknownChecksums(t *testing.T) {
	db, root := openLegacy095Database(t)
	if _, err := db.Exec(`
		UPDATE schema_migration
		SET checksum = 'unknown-legacy-checksum'
		WHERE filename = ?
	`, legacyMigration095Filename); err != nil {
		t.Fatal(err)
	}
	backupDir := filepath.Join(root, "backups")
	err := MigrateFSWithOptions(db, migrations.Files, "test", MigrateOptions{PreflightBackupDir: backupDir})
	if err == nil || !strings.Contains(err.Error(), "checksum") || !strings.Contains(err.Error(), legacyMigration095Filename) {
		t.Fatalf("migration error = %v, want the ordinary checksum refusal", err)
	}
	var current int
	if err := db.QueryRow("SELECT current_version FROM schema_state WHERE id = 1").Scan(&current); err != nil {
		t.Fatal(err)
	}
	if current != 95 {
		t.Fatalf("schema version after checksum refusal = %d, want 95", current)
	}
	if audits := legacy095AuditFiles(t, backupDir); len(audits) != 0 {
		t.Fatalf("checksum refusal wrote audit files: %v", audits)
	}
}

func TestLegacy095PreflightRejectsMissingChecksum(t *testing.T) {
	db, root := openLegacy095Database(t)
	if _, err := db.Exec(`
		UPDATE schema_migration
		SET checksum = ''
		WHERE filename = ?
	`, legacyMigration095Filename); err != nil {
		t.Fatal(err)
	}
	backupDir := filepath.Join(root, "backups")
	err := MigrateFSWithOptions(db, migrations.Files, "test", MigrateOptions{PreflightBackupDir: backupDir})
	if err == nil || !strings.Contains(err.Error(), "has no recorded checksum") {
		t.Fatalf("migration error = %v, want the missing-checksum refusal", err)
	}
	var checksum string
	if err := db.QueryRow("SELECT checksum FROM schema_migration WHERE filename = ?", legacyMigration095Filename).Scan(&checksum); err != nil {
		t.Fatal(err)
	}
	if checksum != "" {
		t.Fatalf("refused preflight adopted checksum %s", checksum)
	}
	if audits := legacy095AuditFiles(t, backupDir); len(audits) != 0 {
		t.Fatalf("missing-checksum refusal wrote audit files: %v", audits)
	}
}

func TestLegacy095PreflightRejectsLedgerWithoutChecksumColumn(t *testing.T) {
	db, root := openLegacy095Database(t)
	if _, err := db.Exec(`
		ALTER TABLE schema_migration RENAME TO schema_migration_with_checksum;
		CREATE TABLE schema_migration (
			filename TEXT PRIMARY KEY,
			version INTEGER,
			applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		INSERT INTO schema_migration (filename, version)
		SELECT filename, version FROM schema_migration_with_checksum;
		DROP TABLE schema_migration_with_checksum;
	`); err != nil {
		t.Fatal(err)
	}
	backupDir := filepath.Join(root, "backups")
	err := MigrateFSWithOptions(db, migrations.Files, "test", MigrateOptions{PreflightBackupDir: backupDir})
	if err == nil || !strings.Contains(err.Error(), "without version and checksum columns") {
		t.Fatalf("migration error = %v, want the incomplete-ledger refusal", err)
	}
	if audits := legacy095AuditFiles(t, backupDir); len(audits) != 0 {
		t.Fatalf("incomplete-ledger refusal wrote audit files: %v", audits)
	}
}

// The preflight must complete before the ordinary ledger validator runs. A
// broken ledger therefore still receives its normalized checksum and audit
// record; the later gap error proves the validator had not rejected the old
// checksum first.
func TestLegacy095PreflightRunsBeforeLedgerValidation(t *testing.T) {
	db, root := openLegacy095Database(t)
	if _, err := db.Exec("DELETE FROM schema_migration WHERE version = 94"); err != nil {
		t.Fatal(err)
	}
	backupDir := filepath.Join(root, "backups")

	err := MigrateFSWithOptions(db, migrations.Files, "test", MigrateOptions{PreflightBackupDir: backupDir})
	if err == nil || !strings.Contains(err.Error(), "gap") {
		t.Fatalf("migration error = %v, want the ledger gap refusal", err)
	}
	var checksum string
	if err := db.QueryRow("SELECT checksum FROM schema_migration WHERE filename = ?", legacyMigration095Filename).Scan(&checksum); err != nil {
		t.Fatal(err)
	}
	if checksum != currentMigration095Checksum {
		t.Fatalf("checksum after ledger gap = %s, want the normalized %s", checksum, currentMigration095Checksum)
	}
	if audits := legacy095AuditFiles(t, backupDir); len(audits) != 1 {
		t.Fatalf("preflight audit files = %v, want one before ledger validation", audits)
	}
}

func TestMigration097LeavesCurrentAndEarlierDatabasesCompatible(t *testing.T) {
	sourceDir := filepath.Join("..", "..", "migrations")
	t.Run("current 095 with existing rows", func(t *testing.T) {
		db := openMigrationManagerDB(t)
		if err := Migrate(db, copyNumberedMigrationsThrough(t, sourceDir, 95)); err != nil {
			t.Fatalf("create current 095 database: %v", err)
		}
		if _, err := db.Exec(`
			INSERT INTO file_source(id, code, display_name, source_type)
			VALUES (97, 'current-095', 'Current 095', 'openlist');
			INSERT INTO source_field_translation(
				cache_key, source_id, field, language, source_text, translated_text
			) VALUES ('current-valid', 97, 'title', 'zh-Hans', 'Current', '当前');
			CREATE INDEX probe_current_095_rebuild ON source_field_translation(language);
		`); err != nil {
			t.Fatal(err)
		}
		backupDir := filepath.Join(t.TempDir(), "backups")
		if err := MigrateFSWithOptions(db, migrations.Files, "test", MigrateOptions{PreflightBackupDir: backupDir}); err != nil {
			t.Fatalf("upgrade current 095 database: %v", err)
		}
		assertCurrentSourceFieldTranslationSchema(t, db)
		assertMigrationIntegrity(t, db)
		assertMigration097Recorded(t, db)
		var rows int
		if err := db.QueryRow("SELECT COUNT(*) FROM source_field_translation WHERE cache_key = 'current-valid'").Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows != 1 {
			t.Fatalf("current 095 row count = %d, want 1", rows)
		}
		// The probe index exists only on the pre-097 table. Its absence proves
		// that 097 rebuilt this database instead of skipping an already-current
		// checksum.
		var probe int
		if err := db.QueryRow(`
			SELECT COUNT(*) FROM sqlite_schema
			WHERE type = 'index' AND name = 'probe_current_095_rebuild'
		`).Scan(&probe); err != nil {
			t.Fatal(err)
		}
		if probe != 0 {
			t.Fatalf("probe index survived, so 097 did not rebuild the current 095 table")
		}
		if audits := legacy095AuditFiles(t, backupDir); len(audits) != 0 {
			t.Fatalf("current 095 database triggered legacy preflight: %v", audits)
		}
		if err := MigrateFSWithOptions(db, migrations.Files, "test-restart", MigrateOptions{PreflightBackupDir: backupDir}); err != nil {
			t.Fatalf("restart after the 097 rebuild: %v", err)
		}
		assertCurrentSourceFieldTranslationSchema(t, db)
		assertMigrationIntegrity(t, db)
	})

	t.Run("084 upgrade", func(t *testing.T) {
		db := openMigrationManagerDB(t)
		if err := Migrate(db, copyNumberedMigrationsThrough(t, sourceDir, 84)); err != nil {
			t.Fatalf("create 084 database: %v", err)
		}
		backupDir := filepath.Join(t.TempDir(), "backups")
		if err := MigrateFSWithOptions(db, migrations.Files, "test", MigrateOptions{PreflightBackupDir: backupDir}); err != nil {
			t.Fatalf("upgrade 084 database: %v", err)
		}
		var current int
		if err := db.QueryRow("SELECT current_version FROM schema_state WHERE id = 1").Scan(&current); err != nil {
			t.Fatal(err)
		}
		if current != latestNumberedMigrationVersion {
			t.Fatalf("084 upgrade version = %d, want %d", current, latestNumberedMigrationVersion)
		}
		assertCurrentSourceFieldTranslationSchema(t, db)
		assertMigrationIntegrity(t, db)
		assertMigration097Recorded(t, db)
		if audits := legacy095AuditFiles(t, backupDir); len(audits) != 0 {
			t.Fatalf("084 upgrade triggered legacy preflight: %v", audits)
		}
		if err := MigrateFSWithOptions(db, migrations.Files, "test-restart", MigrateOptions{PreflightBackupDir: backupDir}); err != nil {
			t.Fatalf("restart after the 084 upgrade: %v", err)
		}
		assertCurrentSourceFieldTranslationSchema(t, db)
		assertMigrationIntegrity(t, db)
	})
}

func TestMigration097FreshInstallIntegrity(t *testing.T) {
	db := openMigrationManagerDB(t)
	if err := MigrateFS(db, migrations.Files, "test"); err != nil {
		t.Fatalf("fresh install: %v", err)
	}
	assertCurrentSourceFieldTranslationSchema(t, db)
	assertMigrationIntegrity(t, db)
	assertMigration097Recorded(t, db)
}

func assertMigration097Recorded(t *testing.T, db *sql.DB) {
	t.Helper()
	const filename = "097_source_field_translation_foreign_key.sql"
	contents, err := migrations.Files.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	var checksum string
	if err := db.QueryRow("SELECT checksum FROM schema_migration WHERE filename = ?", filename).Scan(&checksum); err != nil {
		t.Fatalf("read %s ledger row: %v", filename, err)
	}
	if want := migrationChecksum(contents); checksum != want {
		t.Fatalf("%s checksum = %s, want %s", filename, checksum, want)
	}
}

func openLegacy095Database(t *testing.T) (*sql.DB, string) {
	t.Helper()
	root := t.TempDir()
	db, err := Open(filepath.Join(root, "kikoto.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	catalog := copyNumberedMigrationsThrough(t, filepath.Join("..", "..", "migrations"), 94)
	writeMigration(t, catalog, legacyMigration095Filename, legacy095MigrationSQL(t))
	if err := Migrate(db, catalog); err != nil {
		t.Fatalf("create historical 095 database: %v", err)
	}
	var checksum string
	if err := db.QueryRow("SELECT checksum FROM schema_migration WHERE filename = ?", legacyMigration095Filename).Scan(&checksum); err != nil {
		t.Fatal(err)
	}
	if checksum != legacy095ChecksumWithoutForeignKey {
		t.Fatalf("fixture 095 checksum = %s, want %s", checksum, legacy095ChecksumWithoutForeignKey)
	}
	return db, root
}

func seedLegacy095Rows(db *sql.DB) error {
	_, err := db.Exec(`
		INSERT INTO file_source(id, code, display_name, source_type)
		VALUES (95, 'legacy-095', 'Legacy 095', 'openlist');
		INSERT INTO source_field_translation(
			cache_key, source_id, work_code, field, path, language,
			source_text, translated_text
		) VALUES
			('legacy-valid', 95, '', 'title', '/Example', 'zh-Hans', 'Example', '示例'),
			('legacy-orphan', 404, '', 'title', '/Orphan', 'zh-Hans', 'Orphan', '孤儿');
	`)
	return err
}

func replaceLegacy095ForeignKeyWithSetNull(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`
		ALTER TABLE source_field_translation RENAME TO source_field_translation_legacy;
		CREATE TABLE source_field_translation (
		  id INTEGER PRIMARY KEY AUTOINCREMENT,
		  cache_key TEXT NOT NULL UNIQUE,
		  source_id INTEGER NOT NULL REFERENCES file_source(id) ON DELETE SET NULL,
		  work_code TEXT NOT NULL DEFAULT '',
		  field TEXT NOT NULL CHECK(field IN ('title','tag','directory')),
		  path TEXT NOT NULL DEFAULT '',
		  language TEXT NOT NULL,
		  source_text TEXT NOT NULL,
		  translated_text TEXT NOT NULL,
		  model TEXT NOT NULL DEFAULT '',
		  template_version TEXT NOT NULL DEFAULT '',
		  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		INSERT INTO source_field_translation (
		  id, cache_key, source_id, work_code, field, path, language,
		  source_text, translated_text, model, template_version, created_at, updated_at
		)
		SELECT
		  id, cache_key, source_id, work_code, field, path, language,
		  source_text, translated_text, model, template_version, created_at, updated_at
		FROM source_field_translation_legacy
		WHERE source_id IN (SELECT id FROM file_source);
		DROP TABLE source_field_translation_legacy;
		CREATE INDEX idx_source_field_translation_owner
		  ON source_field_translation(source_id, work_code, field, path, language);
		CREATE INDEX idx_source_field_translation_updated
		  ON source_field_translation(updated_at, id);
	`); err != nil {
		t.Fatal(err)
	}
}

func assertCurrentSourceFieldTranslationSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	actual, err := readSourceFieldTranslationSchema(context.Background(), db)
	if err != nil {
		t.Fatalf("read source_field_translation schema: %v", err)
	}
	if err := compareSourceFieldTranslationSchema(sourceFieldTranslationSchemaDefinition(true), actual); err != nil {
		t.Fatalf("source_field_translation is not the current schema: %v", err)
	}
	if len(actual.ForeignKeys) != 1 ||
		actual.ForeignKeys[0].Table != "file_source" ||
		actual.ForeignKeys[0].From != "source_id" ||
		actual.ForeignKeys[0].OnDelete != "CASCADE" {
		t.Fatalf("source_field_translation foreign keys = %+v, want source_id -> file_source CASCADE", actual.ForeignKeys)
	}
}

func assertMigrationIntegrity(t *testing.T, db *sql.DB) {
	t.Helper()
	var quickCheck string
	if err := db.QueryRow("PRAGMA quick_check").Scan(&quickCheck); err != nil {
		t.Fatalf("quick_check: %v", err)
	}
	if quickCheck != "ok" {
		t.Fatalf("quick_check = %q, want ok", quickCheck)
	}
	rows, err := db.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatalf("foreign_key_check: %v", err)
	}
	defer rows.Close()
	if rows.Next() {
		var table string
		var rowID sql.NullInt64
		var parent string
		var constraint int
		if err := rows.Scan(&table, &rowID, &parent, &constraint); err != nil {
			t.Fatalf("read foreign_key_check: %v", err)
		}
		t.Fatalf("foreign key violation: table=%s row=%v parent=%s constraint=%d", table, rowID, parent, constraint)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("foreign_key_check: %v", err)
	}
}

func legacy095AuditFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), legacy095AuditPrefix) && strings.HasSuffix(entry.Name(), legacy095AuditSuffix) {
			paths = append(paths, filepath.Join(dir, entry.Name()))
		}
	}
	return paths
}
