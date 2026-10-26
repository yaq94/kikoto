package storage

import (
	"database/sql"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The integration branch's chain head is 105. These tests pin the whole
// upgrade path rather than one migration: a fresh install, the two boundaries
// that exist in the field (68 and 84), a restart, and database integrity.
func TestMigrationChain105FreshInstall(t *testing.T) {
	db := openMigrationManagerDB(t)
	if err := Migrate(db, completeMigrationDir(t)); err != nil {
		t.Fatalf("fresh install: %v", err)
	}

	assertSchemaVersion(t, db, 105)
	var history int
	if err := db.QueryRow("SELECT COUNT(*) FROM schema_migration").Scan(&history); err != nil {
		t.Fatal(err)
	}
	if want := 1 + 105 - latestReleasedBaselineVersion; history != want {
		t.Fatalf("fresh install history = %d rows, want %d", history, want)
	}
	var filename string
	if err := db.QueryRow("SELECT filename FROM schema_migration ORDER BY version DESC LIMIT 1").Scan(&filename); err != nil {
		t.Fatal(err)
	}
	if want := "105_translation_search_scope.sql"; filename != want {
		t.Fatalf("last migration = %q, want %q", filename, want)
	}
	assertMigrationIntegrity(t, db)
}

func TestMigrationChain105Upgrades068AndKeepsUserData(t *testing.T) {
	sourceDir := filepath.Join("..", "..", "migrations")
	db := openMigrationManagerDB(t)
	if err := Migrate(db, copyNumberedMigrationsThrough(t, sourceDir, 68)); err != nil {
		t.Fatalf("create 068 database: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO app_setting(key, value_json) VALUES ('chain_105_probe', '{"kept":true}')`); err != nil {
		t.Fatalf("seed user data: %v", err)
	}

	if err := Migrate(db, completeMigrationDir(t)); err != nil {
		t.Fatalf("upgrade 068 to 105: %v", err)
	}
	assertSchemaVersion(t, db, 105)
	var value string
	if err := db.QueryRow(`SELECT value_json FROM app_setting WHERE key = 'chain_105_probe'`).Scan(&value); err != nil {
		t.Fatalf("read user data after upgrade: %v", err)
	}
	if value != `{"kept":true}` {
		t.Fatalf("user data = %q, want it retained", value)
	}
	assertMigrationIntegrity(t, db)
}

// 084 is the production starting point: the released baseline is 068, and the
// field database stopped at the last pre-translation-catalog schema.
func TestMigrationChain105Upgrades084ToCurrentShape(t *testing.T) {
	sourceDir := filepath.Join("..", "..", "migrations")
	db := openMigrationManagerDB(t)
	if err := Migrate(db, copyNumberedMigrationsThrough(t, sourceDir, 84)); err != nil {
		t.Fatalf("create 084 database: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO file_source(id, code, display_name, source_type)
		VALUES (84, 'chain-084', 'Chain 084', 'openlist');
	`); err != nil {
		t.Fatalf("seed 084 rows: %v", err)
	}

	if err := Migrate(db, completeMigrationDir(t)); err != nil {
		t.Fatalf("upgrade 084 to 105: %v", err)
	}
	assertSchemaVersion(t, db, 105)
	assertCurrentSourceFieldTranslationSchema(t, db)
	assertWorkParentColumnAndIndex(t, db)
	assertTranslationPrefillFailedStatus(t, db)
	assertTranslationShortScanIndexes(t, db)
	assertCatalogEntryAdmissionTable(t, db)
	assertMigrationIntegrity(t, db)
}

func TestMigrationChain105RestartIsIdempotent(t *testing.T) {
	db := openMigrationManagerDB(t)
	dir := completeMigrationDir(t)
	if err := Migrate(db, dir); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	before := migrationChainSnapshot(t, db)
	if err := Migrate(db, dir); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	after := migrationChainSnapshot(t, db)
	if before != after {
		t.Fatalf("restart changed migration state:\nbefore: %s\nafter:  %s", before, after)
	}
	assertMigrationIntegrity(t, db)
}

func assertSchemaVersion(t *testing.T, db *sql.DB, want int) {
	t.Helper()
	var current int
	if err := db.QueryRow("SELECT current_version FROM schema_state WHERE id = 1").Scan(&current); err != nil {
		t.Fatal(err)
	}
	if current != want {
		t.Fatalf("schema version = %d, want %d", current, want)
	}
}

func assertWorkParentColumnAndIndex(t *testing.T, db *sql.DB) {
	t.Helper()
	var tableSQL string
	if err := db.QueryRow(`SELECT COALESCE(sql,'') FROM sqlite_schema WHERE type='table' AND name='work'`).Scan(&tableSQL); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tableSQL, "parent_work_id") {
		t.Fatalf("work table has no parent_work_id: %s", tableSQL)
	}
	var indexCount, foreignKeys int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='index' AND name='idx_work_parent_work'`).Scan(&indexCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_list('work')
		WHERE "from"='parent_work_id' AND "table"='work' AND on_delete='RESTRICT'`).Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if indexCount != 1 || foreignKeys != 1 {
		t.Fatalf("work parent index=%d restrict foreign key=%d, want 1/1", indexCount, foreignKeys)
	}
}

func assertTranslationPrefillFailedStatus(t *testing.T, db *sql.DB) {
	t.Helper()
	var tableSQL string
	if err := db.QueryRow(`SELECT COALESCE(sql,'') FROM sqlite_schema
		WHERE type='table' AND name='translation_prefill_state'`).Scan(&tableSQL); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tableSQL, "'failed'") {
		t.Fatalf("translation_prefill_state CHECK lacks failed: %s", tableSQL)
	}
	var failureIndex int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema
		WHERE type='index' AND name='translation_prefill_failure_source'`).Scan(&failureIndex); err != nil {
		t.Fatal(err)
	}
	if failureIndex != 1 {
		t.Fatalf("translation_prefill_failure_source index=%d, want 1", failureIndex)
	}
}

func assertTranslationShortScanIndexes(t *testing.T, db *sql.DB) {
	t.Helper()
	for name, want := range map[string]int{
		"idx_content_translation_short_scan":    1,
		"idx_content_translation_short":         0,
		"idx_content_translation_short_current": 0,
	} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema
			WHERE type='index' AND name=?`, name).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Fatalf("index %s count=%d, want %d", name, count, want)
		}
	}
	var indexSQL string
	if err := db.QueryRow(`SELECT COALESCE(sql,'') FROM sqlite_schema
		WHERE type='index' AND name='idx_content_translation_short_scan'`).Scan(&indexSQL); err != nil {
		t.Fatal(err)
	}
	normalized := strings.Join(strings.Fields(strings.ToLower(indexSQL)), " ")
	want := "create index idx_content_translation_short_scan on content_translation(language,entity_kind,field,source_id,browsable,id,origin,config_key,translated_text,entity_id)"
	if normalized != want {
		t.Fatalf("short scan index definition = %q, want %q", normalized, want)
	}
	var viewSQL string
	if err := db.QueryRow(`SELECT COALESCE(sql,'') FROM sqlite_schema
		WHERE type='view' AND name='content_translation_scope'`).Scan(&viewSQL); err != nil {
		t.Fatalf("content_translation_scope view: %v", err)
	}
	var scopeColumns int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('content_translation')
		WHERE name IN ('source_id','browsable')`).Scan(&scopeColumns); err != nil {
		t.Fatal(err)
	}
	if scopeColumns != 2 {
		t.Fatalf("content_translation scope columns=%d, want 2", scopeColumns)
	}
	var scopeTriggers int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='trigger'
		AND name LIKE 'content_translation_scope_%'`).Scan(&scopeTriggers); err != nil {
		t.Fatal(err)
	}
	if scopeTriggers != 20 {
		t.Fatalf("content_translation scope triggers=%d, want 20", scopeTriggers)
	}
}

// assertCatalogEntryAdmissionTable checks the shape a reader depends on: one
// row per card keyed by the entry, the generation it describes, and the three
// counts. The triggers are what make a stale row fall back to live counting, so
// the test fails if any of them disappears.
func assertCatalogEntryAdmissionTable(t *testing.T, db *sql.DB) {
	t.Helper()
	var tableSQL string
	if err := db.QueryRow(`SELECT COALESCE(sql,'') FROM sqlite_schema
		WHERE type='table' AND name='source_catalog_entry_admission'`).Scan(&tableSQL); err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"catalog_entry_id", "generation", "files", "audio", "non_audio"} {
		if !strings.Contains(tableSQL, column) {
			t.Fatalf("source_catalog_entry_admission lacks %s: %s", column, tableSQL)
		}
	}
	var triggers int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='trigger'
		AND name IN ('source_catalog_entry_admission_file_insert',
			'source_catalog_entry_admission_file_delete',
			'source_catalog_entry_admission_file_update')`).Scan(&triggers); err != nil {
		t.Fatal(err)
	}
	if triggers != 3 {
		t.Fatalf("source_catalog_entry_admission triggers=%d, want 3", triggers)
	}
	var foreignKeys int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_list('source_catalog_entry_admission')
		WHERE "from"='catalog_entry_id' AND "table"='source_catalog_entry' AND on_delete='CASCADE'`).Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 1 {
		t.Fatalf("source_catalog_entry_admission cascade foreign key=%d, want 1", foreignKeys)
	}
}

func migrationChainSnapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	var current, history int
	var filename string
	if err := db.QueryRow("SELECT current_version FROM schema_state WHERE id = 1").Scan(&current); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM schema_migration").Scan(&history); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT filename FROM schema_migration ORDER BY version DESC LIMIT 1").Scan(&filename); err != nil {
		t.Fatal(err)
	}
	return strings.Join([]string{strconv.Itoa(current), strconv.Itoa(history), filename}, "|")
}
