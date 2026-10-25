package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
)

const (
	legacyMigration095Filename = "095_source_field_translation_cache.sql"
	sourceFieldTranslationName = "source_field_translation"

	// The first 095 implementation had no foreign key. Commit 5defaa1c changed
	// the same file to ON DELETE CASCADE, so the historical CASCADE checksum is
	// also the frozen current checksum.
	legacy095ChecksumWithoutForeignKey = "70933dd47790fe1f15e49818fe7a5fc394e3f87258c1001a50a3b75d5a370efe"
	legacy095ChecksumCascade           = "5ff08c11d8012cced81bc0887fdd87d857d94b0a57b6d185f992cf573f7b07e4"
	currentMigration095Checksum        = legacy095ChecksumCascade

	legacy095AuditPrefix = "migration-preflight-095-"
	legacy095AuditSuffix = ".json"

	Legacy095PreflightNotApplicable  = "not_applicable"
	Legacy095PreflightNormalized     = "normalized"
	Legacy095PreflightAlreadyCurrent = "already_current"
)

type Legacy095PreflightResult struct {
	Status      string
	OldChecksum string
	NewChecksum string
	Fingerprint string
	BackupPath  string
	AuditPath   string
	ExecutedAt  time.Time
}

type legacy095PreflightAudit struct {
	SchemaVersion            int                          `json:"schemaVersion"`
	Migration                string                       `json:"migration"`
	OldChecksum              string                       `json:"oldChecksum"`
	NewChecksum              string                       `json:"newChecksum"`
	SchemaFingerprint        string                       `json:"schemaFingerprint"`
	SchemaFingerprintDetails sourceFieldTranslationSchema `json:"schemaFingerprintDetails"`
	ExecutedAt               string                       `json:"executedAt"`
	DatabaseBackupPath       string                       `json:"databaseBackupPath"`
	Result                   string                       `json:"result"`
}

type sourceFieldTranslationSchema struct {
	Table       string                         `json:"table"`
	Columns     []sourceFieldTranslationColumn `json:"columns"`
	Indexes     []sourceFieldTranslationIndex  `json:"indexes"`
	ForeignKeys []sourceFieldTranslationFK     `json:"foreignKeys"`
	Triggers    []string                       `json:"triggers"`
}

type sourceFieldTranslationColumn struct {
	Position   int     `json:"position"`
	Name       string  `json:"name"`
	Type       string  `json:"type"`
	NotNull    bool    `json:"notNull"`
	Default    *string `json:"default"`
	PrimaryKey int     `json:"primaryKey"`
	Hidden     int     `json:"hidden"`
}

type sourceFieldTranslationIndex struct {
	Name    string                              `json:"name"`
	Unique  bool                                `json:"unique"`
	Origin  string                              `json:"origin"`
	Partial bool                                `json:"partial"`
	Columns []sourceFieldTranslationIndexColumn `json:"columns"`
}

type sourceFieldTranslationIndexColumn struct {
	Sequence   int     `json:"sequence"`
	ColumnID   int     `json:"columnId"`
	Name       *string `json:"name"`
	Descending bool    `json:"descending"`
	Collation  string  `json:"collation"`
	Key        bool    `json:"key"`
}

type sourceFieldTranslationFK struct {
	ID       int    `json:"id"`
	Sequence int    `json:"sequence"`
	Table    string `json:"table"`
	From     string `json:"from"`
	To       string `json:"to"`
	OnUpdate string `json:"onUpdate"`
	OnDelete string `json:"onDelete"`
	Match    string `json:"match"`
}

type schemaQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// PreflightLegacyMigration095 normalizes the one known pre-release 095 checksum
// after proving that the live cache table is exactly the historical no-foreign-
// key shape. It runs before migration history is validated, because the ordinary
// checksum gate would otherwise stop the upgrade before 097 can rebuild it.
//
// Only the exact legacy row and schema are accepted. There is deliberately no
// option to ignore an arbitrary checksum.
func PreflightLegacyMigration095(ctx context.Context, db *sql.DB, backupDir string) (Legacy095PreflightResult, error) {
	if db == nil {
		return Legacy095PreflightResult{}, errors.New("legacy 095 preflight: nil database")
	}
	ledger, err := readLegacy095Ledger(ctx, db)
	if err != nil {
		return Legacy095PreflightResult{}, err
	}
	if ledger.checksum != legacy095ChecksumWithoutForeignKey {
		result := Legacy095PreflightResult{Status: Legacy095PreflightNotApplicable}
		if ledger.checksum == currentMigration095Checksum {
			result.Status = Legacy095PreflightAlreadyCurrent
		}
		return result, nil
	}

	actual, err := readSourceFieldTranslationSchema(ctx, db)
	if err != nil {
		return Legacy095PreflightResult{}, fmt.Errorf(
			"legacy 095 preflight rejected: cannot inspect %s: %w",
			sourceFieldTranslationName,
			err,
		)
	}
	expected := sourceFieldTranslationSchemaDefinition(false)
	if err := compareSourceFieldTranslationSchema(expected, actual); err != nil {
		return Legacy095PreflightResult{}, fmt.Errorf(
			"legacy 095 preflight rejected: %s schema fingerprint %s does not match the historical no-foreign-key fingerprint %s: %w",
			sourceFieldTranslationName,
			sourceFieldTranslationFingerprint(actual),
			sourceFieldTranslationFingerprint(expected),
			err,
		)
	}

	resolvedBackupDir, err := legacy095BackupDirectory(ctx, db, backupDir)
	if err != nil {
		return Legacy095PreflightResult{}, err
	}
	started := time.Now().UTC()
	backupName := BackupFileName(BackupKindPreMigration, started, "legacy-095-"+legacy095ChecksumWithoutForeignKey[:8])
	backupPath, err := filepath.Abs(filepath.Join(resolvedBackupDir, backupName))
	if err != nil {
		return Legacy095PreflightResult{}, fmt.Errorf("legacy 095 preflight: resolve backup path: %w", err)
	}
	if _, err := BackupInto(ctx, db, backupPath); err != nil {
		return Legacy095PreflightResult{}, fmt.Errorf(
			"legacy 095 preflight: back up the database before normalizing its checksum (free disk space or set KIKOTO_DB_BACKUP_DIR): %w",
			err,
		)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Legacy095PreflightResult{}, fmt.Errorf("legacy 095 preflight: begin checksum normalization: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	var filename string
	var version sql.NullInt64
	var checksum string
	if err := tx.QueryRowContext(ctx, `
		SELECT filename, version, checksum
		FROM schema_migration
		WHERE filename = ?
	`, legacyMigration095Filename).Scan(&filename, &version, &checksum); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Legacy095PreflightResult{}, errors.New("legacy 095 preflight: migration record disappeared during checksum normalization")
		}
		return Legacy095PreflightResult{}, fmt.Errorf("legacy 095 preflight: reread migration record: %w", err)
	}
	if checksum == currentMigration095Checksum {
		_ = tx.Rollback()
		committed = true
		return Legacy095PreflightResult{
			Status:      Legacy095PreflightAlreadyCurrent,
			OldChecksum: checksum,
			NewChecksum: currentMigration095Checksum,
			BackupPath:  backupPath,
		}, nil
	}
	if filename != legacyMigration095Filename || !version.Valid || version.Int64 != 95 || checksum != legacy095ChecksumWithoutForeignKey {
		return Legacy095PreflightResult{}, fmt.Errorf(
			"legacy 095 preflight: migration record changed during checksum normalization (filename=%q version=%v checksum=%q)",
			filename,
			version,
			checksum,
		)
	}
	actual, err = readSourceFieldTranslationSchema(ctx, tx)
	if err != nil {
		return Legacy095PreflightResult{}, fmt.Errorf("legacy 095 preflight: reinspect %s: %w", sourceFieldTranslationName, err)
	}
	if err := compareSourceFieldTranslationSchema(expected, actual); err != nil {
		return Legacy095PreflightResult{}, fmt.Errorf(
			"legacy 095 preflight rejected: %s schema fingerprint %s does not match the historical no-foreign-key fingerprint %s: %w",
			sourceFieldTranslationName,
			sourceFieldTranslationFingerprint(actual),
			sourceFieldTranslationFingerprint(expected),
			err,
		)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE schema_migration
		SET checksum = ?
		WHERE filename = ? AND version = 95 AND checksum = ?
	`, currentMigration095Checksum, legacyMigration095Filename, legacy095ChecksumWithoutForeignKey)
	if err != nil {
		return Legacy095PreflightResult{}, fmt.Errorf("legacy 095 preflight: normalize checksum: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return Legacy095PreflightResult{}, fmt.Errorf("legacy 095 preflight: read normalized row count: %w", err)
	}
	if updated != 1 {
		return Legacy095PreflightResult{}, fmt.Errorf("legacy 095 preflight: normalized %d rows, want 1", updated)
	}

	// The audit file is staged before the commit and published after it. A
	// failed commit therefore leaves no record claiming a normalization, and a
	// published record always describes a committed ledger change.
	finish := time.Now().UTC()
	audit := legacy095PreflightAudit{
		SchemaVersion:            95,
		Migration:                legacyMigration095Filename,
		OldChecksum:              legacy095ChecksumWithoutForeignKey,
		NewChecksum:              currentMigration095Checksum,
		SchemaFingerprint:        sourceFieldTranslationFingerprint(actual),
		SchemaFingerprintDetails: actual,
		ExecutedAt:               finish.Format(time.RFC3339Nano),
		DatabaseBackupPath:       backupPath,
		Result:                   Legacy095PreflightNormalized,
	}
	auditPath, auditPartial, err := stageLegacy095PreflightAudit(resolvedBackupDir, finish, audit)
	if err != nil {
		return Legacy095PreflightResult{}, fmt.Errorf("legacy 095 preflight: record audit: %w", err)
	}
	if err := tx.Commit(); err != nil {
		_ = os.Remove(auditPartial)
		return Legacy095PreflightResult{}, fmt.Errorf("legacy 095 preflight: commit checksum normalization: %w", err)
	}
	committed = true

	if err := publishLegacy095PreflightAudit(auditPartial, auditPath); err != nil {
		return Legacy095PreflightResult{}, fmt.Errorf(
			"legacy 095 preflight: checksum normalization committed, but audit file %s could not be published: %w",
			auditPartial,
			err,
		)
	}
	slog.Info("normalized legacy 095 migration checksum",
		"migration", legacyMigration095Filename,
		"old_checksum", legacy095ChecksumWithoutForeignKey,
		"new_checksum", currentMigration095Checksum,
		"backup", backupPath,
		"audit", auditPath,
	)
	return Legacy095PreflightResult{
		Status:      Legacy095PreflightNormalized,
		OldChecksum: legacy095ChecksumWithoutForeignKey,
		NewChecksum: currentMigration095Checksum,
		Fingerprint: sourceFieldTranslationFingerprint(actual),
		BackupPath:  backupPath,
		AuditPath:   auditPath,
		ExecutedAt:  finish,
	}, nil
}

type legacy095Ledger struct {
	checksum string
}

func readLegacy095Ledger(ctx context.Context, queryer schemaQueryer) (legacy095Ledger, error) {
	var columns int
	if err := queryer.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM pragma_table_info('schema_migration')
		WHERE name IN ('filename', 'version', 'checksum')
	`).Scan(&columns); err != nil {
		return legacy095Ledger{}, fmt.Errorf("legacy 095 preflight: inspect migration ledger: %w", err)
	}
	if columns != 3 {
		var hasFilename int
		if err := queryer.QueryRowContext(ctx, `
			SELECT COUNT(*)
			FROM pragma_table_info('schema_migration')
			WHERE name = 'filename'
		`).Scan(&hasFilename); err != nil {
			return legacy095Ledger{}, fmt.Errorf("legacy 095 preflight: inspect migration ledger: %w", err)
		}
		if hasFilename == 0 {
			return legacy095Ledger{}, nil
		}
		var legacyRows int
		if err := queryer.QueryRowContext(ctx, `
			SELECT COUNT(*)
			FROM schema_migration
			WHERE filename = ?
		`, legacyMigration095Filename).Scan(&legacyRows); err != nil {
			return legacy095Ledger{}, fmt.Errorf("legacy 095 preflight: inspect migration ledger: %w", err)
		}
		if legacyRows > 0 {
			return legacy095Ledger{}, fmt.Errorf(
				"legacy 095 preflight rejected: migration ledger records %s without version and checksum columns",
				legacyMigration095Filename,
			)
		}
		return legacy095Ledger{}, nil
	}
	rows, err := queryer.QueryContext(ctx, `
		SELECT filename, version, checksum
		FROM schema_migration
		WHERE filename = ?
	`, legacyMigration095Filename)
	if err != nil {
		return legacy095Ledger{}, fmt.Errorf("legacy 095 preflight: read migration ledger: %w", err)
	}
	defer rows.Close()
	var count int
	var ledger legacy095Ledger
	for rows.Next() {
		var filename string
		var version sql.NullInt64
		var checksum string
		if err := rows.Scan(&filename, &version, &checksum); err != nil {
			return legacy095Ledger{}, fmt.Errorf("legacy 095 preflight: read migration ledger: %w", err)
		}
		count++
		if count > 1 {
			return legacy095Ledger{}, fmt.Errorf("legacy 095 preflight rejected: migration ledger records %s more than once", legacyMigration095Filename)
		}
		if !version.Valid || version.Int64 != 95 {
			return legacy095Ledger{}, fmt.Errorf(
				"legacy 095 preflight rejected: %s has version %v, want 95",
				filename,
				version,
			)
		}
		ledger.checksum = checksum
	}
	if err := rows.Err(); err != nil {
		return legacy095Ledger{}, fmt.Errorf("legacy 095 preflight: read migration ledger: %w", err)
	}
	// A 095 row without a checksum would otherwise be adopted from the packaged
	// catalog without proving the live table shape. Checksums predate 095, so
	// this row is not a pre-checksum ledger and must match one frozen revision.
	if count == 1 && ledger.checksum == "" {
		return legacy095Ledger{}, fmt.Errorf(
			"legacy 095 preflight rejected: %s has no recorded checksum; restore the ledger or a backup",
			legacyMigration095Filename,
		)
	}
	return ledger, nil
}

func readSourceFieldTranslationSchema(ctx context.Context, queryer schemaQueryer) (sourceFieldTranslationSchema, error) {
	var objectType string
	if err := queryer.QueryRowContext(ctx, `
		SELECT type
		FROM sqlite_schema
		WHERE name = ? AND type IN ('table', 'view')
	`, sourceFieldTranslationName).Scan(&objectType); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return sourceFieldTranslationSchema{}, fmt.Errorf("table is missing")
		}
		return sourceFieldTranslationSchema{}, err
	}
	if objectType != "table" {
		return sourceFieldTranslationSchema{}, fmt.Errorf("object is a %s, want a table", objectType)
	}

	schema := sourceFieldTranslationSchema{
		Table:       sourceFieldTranslationName,
		Columns:     []sourceFieldTranslationColumn{},
		Indexes:     []sourceFieldTranslationIndex{},
		ForeignKeys: []sourceFieldTranslationFK{},
		Triggers:    []string{},
	}
	columnRows, err := queryer.QueryContext(ctx, `
		SELECT cid, name, type, "notnull", dflt_value, pk, hidden
		FROM pragma_table_xinfo(?)
		ORDER BY cid
	`, sourceFieldTranslationName)
	if err != nil {
		return sourceFieldTranslationSchema{}, err
	}
	for columnRows.Next() {
		var column sourceFieldTranslationColumn
		var notNull int
		var defaultValue sql.NullString
		if err := columnRows.Scan(
			&column.Position,
			&column.Name,
			&column.Type,
			&notNull,
			&defaultValue,
			&column.PrimaryKey,
			&column.Hidden,
		); err != nil {
			columnRows.Close()
			return sourceFieldTranslationSchema{}, err
		}
		column.NotNull = notNull != 0
		if defaultValue.Valid {
			column.Default = stringPointer(defaultValue.String)
		}
		schema.Columns = append(schema.Columns, column)
	}
	if err := columnRows.Close(); err != nil {
		return sourceFieldTranslationSchema{}, err
	}
	if err := columnRows.Err(); err != nil {
		return sourceFieldTranslationSchema{}, err
	}

	indexRows, err := queryer.QueryContext(ctx, `
		SELECT name, "unique", origin, partial
		FROM pragma_index_list(?)
		ORDER BY name
	`, sourceFieldTranslationName)
	if err != nil {
		return sourceFieldTranslationSchema{}, err
	}
	for indexRows.Next() {
		var index sourceFieldTranslationIndex
		var unique, partial int
		if err := indexRows.Scan(&index.Name, &unique, &index.Origin, &partial); err != nil {
			indexRows.Close()
			return sourceFieldTranslationSchema{}, err
		}
		index.Unique = unique != 0
		index.Partial = partial != 0
		index.Columns = []sourceFieldTranslationIndexColumn{}
		indexColumnRows, err := queryer.QueryContext(ctx, `
			SELECT seqno, cid, name, "desc", coll, "key"
			FROM pragma_index_xinfo(?)
			ORDER BY seqno
		`, index.Name)
		if err != nil {
			indexRows.Close()
			return sourceFieldTranslationSchema{}, err
		}
		for indexColumnRows.Next() {
			var column sourceFieldTranslationIndexColumn
			var name sql.NullString
			var descending, key int
			if err := indexColumnRows.Scan(
				&column.Sequence,
				&column.ColumnID,
				&name,
				&descending,
				&column.Collation,
				&key,
			); err != nil {
				indexColumnRows.Close()
				indexRows.Close()
				return sourceFieldTranslationSchema{}, err
			}
			if name.Valid {
				column.Name = stringPointer(name.String)
			}
			column.Descending = descending != 0
			column.Key = key != 0
			index.Columns = append(index.Columns, column)
		}
		if err := indexColumnRows.Close(); err != nil {
			indexRows.Close()
			return sourceFieldTranslationSchema{}, err
		}
		if err := indexColumnRows.Err(); err != nil {
			indexRows.Close()
			return sourceFieldTranslationSchema{}, err
		}
		schema.Indexes = append(schema.Indexes, index)
	}
	if err := indexRows.Close(); err != nil {
		return sourceFieldTranslationSchema{}, err
	}
	if err := indexRows.Err(); err != nil {
		return sourceFieldTranslationSchema{}, err
	}

	foreignKeyRows, err := queryer.QueryContext(ctx, `
		SELECT id, seq, "table", "from", "to", on_update, on_delete, match
		FROM pragma_foreign_key_list(?)
		ORDER BY id, seq
	`, sourceFieldTranslationName)
	if err != nil {
		return sourceFieldTranslationSchema{}, err
	}
	for foreignKeyRows.Next() {
		var foreignKey sourceFieldTranslationFK
		if err := foreignKeyRows.Scan(
			&foreignKey.ID,
			&foreignKey.Sequence,
			&foreignKey.Table,
			&foreignKey.From,
			&foreignKey.To,
			&foreignKey.OnUpdate,
			&foreignKey.OnDelete,
			&foreignKey.Match,
		); err != nil {
			foreignKeyRows.Close()
			return sourceFieldTranslationSchema{}, err
		}
		schema.ForeignKeys = append(schema.ForeignKeys, foreignKey)
	}
	if err := foreignKeyRows.Close(); err != nil {
		return sourceFieldTranslationSchema{}, err
	}
	if err := foreignKeyRows.Err(); err != nil {
		return sourceFieldTranslationSchema{}, err
	}

	triggerRows, err := queryer.QueryContext(ctx, `
		SELECT name
		FROM sqlite_schema
		WHERE type = 'trigger' AND tbl_name = ?
		ORDER BY name
	`, sourceFieldTranslationName)
	if err != nil {
		return sourceFieldTranslationSchema{}, err
	}
	for triggerRows.Next() {
		var name string
		if err := triggerRows.Scan(&name); err != nil {
			triggerRows.Close()
			return sourceFieldTranslationSchema{}, err
		}
		schema.Triggers = append(schema.Triggers, name)
	}
	if err := triggerRows.Close(); err != nil {
		return sourceFieldTranslationSchema{}, err
	}
	if err := triggerRows.Err(); err != nil {
		return sourceFieldTranslationSchema{}, err
	}
	return schema, nil
}

func sourceFieldTranslationFingerprint(schema sourceFieldTranslationSchema) string {
	encoded, err := json.Marshal(schema)
	if err != nil {
		panic("marshal source_field_translation schema fingerprint: " + err.Error())
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func compareSourceFieldTranslationSchema(expected, actual sourceFieldTranslationSchema) error {
	var differences []string
	if expected.Table != actual.Table {
		differences = append(differences, fmt.Sprintf("table name is %q, want %q", actual.Table, expected.Table))
	}
	differences = append(differences, compareColumns(expected.Columns, actual.Columns)...)
	differences = append(differences, compareIndexes(expected.Indexes, actual.Indexes)...)
	if !reflect.DeepEqual(expected.ForeignKeys, actual.ForeignKeys) {
		differences = append(differences, fmt.Sprintf(
			"foreign keys are %s, want %s",
			formatJSON(actual.ForeignKeys),
			formatJSON(expected.ForeignKeys),
		))
	}
	if !reflect.DeepEqual(expected.Triggers, actual.Triggers) {
		differences = append(differences, fmt.Sprintf(
			"triggers are %v, want %v",
			actual.Triggers,
			expected.Triggers,
		))
	}
	if len(differences) == 0 {
		return nil
	}
	return errors.New(strings.Join(differences, "; "))
}

func compareColumns(expected, actual []sourceFieldTranslationColumn) []string {
	expectedByName := make(map[string]sourceFieldTranslationColumn, len(expected))
	actualByName := make(map[string]sourceFieldTranslationColumn, len(actual))
	expectedOrder := make([]string, 0, len(expected))
	actualOrder := make([]string, 0, len(actual))
	for _, column := range expected {
		expectedByName[column.Name] = column
		expectedOrder = append(expectedOrder, column.Name)
	}
	for _, column := range actual {
		actualByName[column.Name] = column
		actualOrder = append(actualOrder, column.Name)
	}
	var differences []string
	if !reflect.DeepEqual(expectedOrder, actualOrder) {
		differences = append(differences, fmt.Sprintf("columns are %v, want %v", actualOrder, expectedOrder))
	}
	for name, expectedColumn := range expectedByName {
		actualColumn, exists := actualByName[name]
		if !exists {
			continue
		}
		if !reflect.DeepEqual(expectedColumn, actualColumn) {
			differences = append(differences, fmt.Sprintf("column %q definition differs", name))
		}
	}
	return differences
}

func compareIndexes(expected, actual []sourceFieldTranslationIndex) []string {
	expectedByName := make(map[string]sourceFieldTranslationIndex, len(expected))
	actualByName := make(map[string]sourceFieldTranslationIndex, len(actual))
	for _, index := range expected {
		expectedByName[index.Name] = index
	}
	for _, index := range actual {
		actualByName[index.Name] = index
	}
	var unexpected, missing, changed []string
	for name, actualIndex := range actualByName {
		expectedIndex, exists := expectedByName[name]
		if !exists {
			unexpected = append(unexpected, name)
			continue
		}
		if !reflect.DeepEqual(expectedIndex, actualIndex) {
			changed = append(changed, name)
		}
	}
	for name := range expectedByName {
		if _, exists := actualByName[name]; !exists {
			missing = append(missing, name)
		}
	}
	var differences []string
	if len(unexpected) > 0 {
		differences = append(differences, fmt.Sprintf("unexpected indexes %v", sortedStrings(unexpected)))
	}
	if len(missing) > 0 {
		differences = append(differences, fmt.Sprintf("missing indexes %v", sortedStrings(missing)))
	}
	if len(changed) > 0 {
		differences = append(differences, fmt.Sprintf("index definitions differ for %v", sortedStrings(changed)))
	}
	return differences
}

func sortedStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}

func formatJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(encoded)
}

func stringPointer(value string) *string {
	return &value
}

func sourceFieldTranslationSchemaDefinition(foreignKey bool) sourceFieldTranslationSchema {
	columns := []sourceFieldTranslationColumn{
		{Position: 0, Name: "id", Type: "INTEGER", PrimaryKey: 1},
		{Position: 1, Name: "cache_key", Type: "TEXT", NotNull: true},
		{Position: 2, Name: "source_id", Type: "INTEGER", NotNull: true},
		{Position: 3, Name: "work_code", Type: "TEXT", NotNull: true, Default: stringPointer("''")},
		{Position: 4, Name: "field", Type: "TEXT", NotNull: true},
		{Position: 5, Name: "path", Type: "TEXT", NotNull: true, Default: stringPointer("''")},
		{Position: 6, Name: "language", Type: "TEXT", NotNull: true},
		{Position: 7, Name: "source_text", Type: "TEXT", NotNull: true},
		{Position: 8, Name: "translated_text", Type: "TEXT", NotNull: true},
		{Position: 9, Name: "model", Type: "TEXT", NotNull: true, Default: stringPointer("''")},
		{Position: 10, Name: "template_version", Type: "TEXT", NotNull: true, Default: stringPointer("''")},
		{Position: 11, Name: "created_at", Type: "TEXT", NotNull: true, Default: stringPointer("CURRENT_TIMESTAMP")},
		{Position: 12, Name: "updated_at", Type: "TEXT", NotNull: true, Default: stringPointer("CURRENT_TIMESTAMP")},
	}
	indexColumns := func(names ...string) []sourceFieldTranslationIndexColumn {
		columns := make([]sourceFieldTranslationIndexColumn, 0, len(names)+1)
		columnIDs := map[string]int{
			"id":         0,
			"cache_key":  1,
			"source_id":  2,
			"work_code":  3,
			"field":      4,
			"path":       5,
			"language":   6,
			"updated_at": 12,
		}
		for sequence, name := range names {
			columnID := columnIDs[name]
			columns = append(columns, sourceFieldTranslationIndexColumn{
				Sequence:  sequence,
				ColumnID:  columnID,
				Name:      stringPointer(name),
				Collation: "BINARY",
				Key:       true,
			})
		}
		columns = append(columns, sourceFieldTranslationIndexColumn{
			Sequence:  len(names),
			ColumnID:  -1,
			Collation: "BINARY",
		})
		return columns
	}
	indexes := []sourceFieldTranslationIndex{
		{
			Name:    "idx_source_field_translation_owner",
			Origin:  "c",
			Columns: indexColumns("source_id", "work_code", "field", "path", "language"),
		},
		{
			Name:    "idx_source_field_translation_updated",
			Origin:  "c",
			Columns: indexColumns("updated_at", "id"),
		},
		{
			Name:    "sqlite_autoindex_source_field_translation_1",
			Unique:  true,
			Origin:  "u",
			Columns: indexColumns("cache_key"),
		},
	}
	foreignKeys := []sourceFieldTranslationFK{}
	if foreignKey {
		foreignKeys = append(foreignKeys, sourceFieldTranslationFK{
			Table:    "file_source",
			From:     "source_id",
			To:       "id",
			OnUpdate: "NO ACTION",
			OnDelete: "CASCADE",
			Match:    "NONE",
		})
	}
	return sourceFieldTranslationSchema{
		Table:       sourceFieldTranslationName,
		Columns:     columns,
		Indexes:     indexes,
		ForeignKeys: foreignKeys,
		Triggers:    []string{},
	}
}

func legacy095BackupDirectory(ctx context.Context, db *sql.DB, configured string) (string, error) {
	if strings.TrimSpace(configured) != "" {
		absolute, err := filepath.Abs(configured)
		if err != nil {
			return "", fmt.Errorf("legacy 095 preflight: resolve backup directory: %w", err)
		}
		return absolute, nil
	}
	rows, err := db.QueryContext(ctx, "PRAGMA database_list")
	if err != nil {
		return "", fmt.Errorf("legacy 095 preflight: locate database for backup: %w", err)
	}
	defer rows.Close()
	var databasePath string
	for rows.Next() {
		var sequence int
		var name, path string
		if err := rows.Scan(&sequence, &name, &path); err != nil {
			return "", fmt.Errorf("legacy 095 preflight: locate database for backup: %w", err)
		}
		if name == "main" {
			databasePath = path
		}
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("legacy 095 preflight: locate database for backup: %w", err)
	}
	if strings.TrimSpace(databasePath) == "" {
		return "", errors.New("legacy 095 preflight: a database backup directory is required for an in-memory database")
	}
	return filepath.Join(filepath.Dir(databasePath), "backups"), nil
}

func stageLegacy095PreflightAudit(dir string, executedAt time.Time, audit legacy095PreflightAudit) (path string, partial string, err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", err
	}
	name := legacy095AuditPrefix + executedAt.Format("20060102T150405.000000000Z") + legacy095AuditSuffix
	path, err = filepath.Abs(filepath.Join(dir, name))
	if err != nil {
		return "", "", err
	}
	partial = path + ".partial"
	file, err := os.OpenFile(partial, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", "", err
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(audit); err != nil {
		_ = file.Close()
		_ = os.Remove(partial)
		return "", "", err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(partial)
		return "", "", err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(partial)
		return "", "", err
	}
	return path, partial, nil
}

func publishLegacy095PreflightAudit(partial, path string) error {
	if err := os.Rename(partial, path); err != nil {
		return err
	}
	return nil
}
