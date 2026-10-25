package main

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/yexca/kikoto/backend/internal/account"
	"github.com/yexca/kikoto/backend/internal/storage"
)

func migratedCommandTestDatabase(t *testing.T) (string, *account.Store) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kikoto.db")
	db, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := storage.Migrate(db, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KIKOTO_MODE", "production")
	t.Setenv("KIKOTO_DB_PATH", path)
	t.Setenv("KIKOTO_ROOT_USERNAME", "")
	t.Setenv("KIKOTO_ROOT_PASSWORD", "")
	t.Setenv("KIKOTO_ROOT_PASSWORD_RESET", "")
	t.Setenv("KIKOTO_ROOT_ACCOUNT_MODE", "")
	return path, account.NewStore(db)
}

func TestAdminResetPasswordPrintsGeneratedPasswordForInitialAdministrator(t *testing.T) {
	_, store := migratedCommandTestDatabase(t)
	ctx := context.Background()
	if _, err := store.CreateInitialAdministrator(ctx, "synthetic-admin", "synthetic-password"); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := runAdminCommand([]string{"reset-password"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	output := stdout.String()
	if !strings.Contains(output, "Administrator password reset.") || !strings.Contains(output, "Username: synthetic-admin") {
		t.Fatalf("output = %s", output)
	}
	match := regexp.MustCompile(`(?m)^Password: (\S+)$`).FindStringSubmatch(output)
	if match == nil {
		t.Fatalf("output has no generated password: %s", output)
	}
	if _, err := store.Authenticate(ctx, "synthetic-admin", match[1], time.Now()); err != nil {
		t.Fatalf("generated password rejected: %v", err)
	}
}

func TestAdminResetPasswordReadsPasswordFromStdin(t *testing.T) {
	_, store := migratedCommandTestDatabase(t)
	var stdout, stderr bytes.Buffer
	if code := runAdminCommand([]string{"reset-password", "--username", "root", "--password-stdin"}, strings.NewReader("replace-with-a-long-random-password\n"), &stdout, &stderr); code != 1 {
		t.Fatalf("placeholder exit code = %d, want 1", code)
	}

	stdout.Reset()
	stderr.Reset()
	if code := runAdminCommand([]string{"reset-password", "--username", "synthetic-missing", "--password-stdin"}, strings.NewReader("synthetic-stdin-password\n"), &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "does not exist") {
		t.Fatalf("missing account exit code = %d, stderr = %s", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := runAdminCommand([]string{"reset-password", "--password-stdin"}, strings.NewReader("synthetic-stdin-password\n"), &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "pass --username") {
		t.Fatalf("missing target exit code = %d, stderr = %s", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := runAdminCommand([]string{"reset-password", "--username", "root", "--password-stdin"}, strings.NewReader("synthetic-stdin-password\r\n"), &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Administrator account created.") || strings.Contains(stdout.String(), "synthetic-stdin-password") {
		t.Fatalf("output = %s", stdout.String())
	}
	if _, err := store.Authenticate(context.Background(), "root", "synthetic-stdin-password", time.Now()); err != nil {
		t.Fatalf("stdin password rejected: %v", err)
	}
}

func TestAdminResetPasswordDoesNotCreateMissingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "kikoto.db")
	t.Setenv("KIKOTO_MODE", "production")
	t.Setenv("KIKOTO_DB_PATH", path)
	t.Setenv("KIKOTO_ROOT_PASSWORD_RESET", "")
	var stdout, stderr bytes.Buffer
	if code := runAdminCommand([]string{"reset-password"}, strings.NewReader(""), &stdout, &stderr); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "start Kikoto once first") {
		t.Fatalf("stderr = %s", stderr.String())
	}
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing database directory stat = %v, want still missing", err)
	}
}

func TestAdminResetPasswordRefusesEnvironmentManagedAccount(t *testing.T) {
	_, store := migratedCommandTestDatabase(t)
	if _, err := store.SyncEnvironmentAdministrator(context.Background(), "root", "synthetic-env-password"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KIKOTO_ROOT_ACCOUNT_MODE", "environment")
	t.Setenv("KIKOTO_ROOT_PASSWORD", "synthetic-env-password")
	var stdout, stderr bytes.Buffer
	if code := runAdminCommand([]string{"reset-password"}, strings.NewReader(""), &stdout, &stderr); code != 1 {
		t.Fatalf("exit code = %d, want 1; stdout = %s", code, stdout.String())
	}
	if !strings.Contains(stderr.String(), "KIKOTO_ROOT_ACCOUNT_MODE=environment") {
		t.Fatalf("stderr = %s", stderr.String())
	}
	if _, err := store.Authenticate(context.Background(), "root", "synthetic-env-password", time.Now()); err != nil {
		t.Fatalf("refused reset changed the managed password: %v", err)
	}

	root, err := store.LoadByUsername(context.Background(), "root")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateManagedUser(context.Background(), account.CreateUserInput{
		Username: "synthetic-other", DisplayName: "synthetic-other", Role: "super_admin", Password: "synthetic-password", Enabled: true, ActorUserID: root.ID,
	}); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := runAdminCommand([]string{"reset-password", "--username", "synthetic-other"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("other account exit code = %d, stderr = %s", code, stderr.String())
	}
}

func TestAdminPreflight095ReportsCurrentChecksum(t *testing.T) {
	migratedCommandTestDatabase(t)
	backupDir := filepath.Join(t.TempDir(), "backups")
	var stdout, stderr bytes.Buffer
	if code := runAdminCommand([]string{"preflight-095", "--backup-dir", backupDir}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("preflight-095 exit code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Legacy 095 checksum is already current; no change.") {
		t.Fatalf("preflight-095 output = %s", stdout.String())
	}
	if _, err := os.Stat(backupDir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("preflight-095 created a backup directory for a current database: %v", err)
	}
}

func TestAdminPreflight095ReportsNoLegacyChecksum(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KIKOTO_MODE", "production")
	t.Setenv("KIKOTO_DB_PATH", path)
	t.Setenv("KIKOTO_DB_BACKUP_DIR", "")
	var stdout, stderr bytes.Buffer
	if code := runAdminCommand([]string{"preflight-095", "--database", path}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("preflight-095 exit code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "No legacy 095 checksum requires normalization; no change.") {
		t.Fatalf("preflight-095 output = %s", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "backups")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("preflight-095 created a backup directory for an empty database: %v", err)
	}
}

func TestAdminPreflight095NormalizesLegacyDatabase(t *testing.T) {
	path := legacyCommandTestDatabase(t)
	backupDir := filepath.Join(t.TempDir(), "backups")
	var stdout, stderr bytes.Buffer
	if code := runAdminCommand([]string{"preflight-095", "--backup-dir", backupDir}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("preflight-095 exit code = %d, stderr = %s", code, stderr.String())
	}
	for _, want := range []string{
		"Legacy 095 checksum normalized.",
		"Old checksum: 70933dd47790fe1f15e49818fe7a5fc394e3f87258c1001a50a3b75d5a370efe",
		"New checksum: 5ff08c11d8012cced81bc0887fdd87d857d94b0a57b6d185f992cf573f7b07e4",
		"Schema fingerprint: sha256:",
		"Database backup: ",
		"Audit record: ",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("preflight-095 output lacks %q:\n%s", want, stdout.String())
		}
	}
	db, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var checksum string
	if err := db.QueryRow("SELECT checksum FROM schema_migration WHERE filename = ?", "095_source_field_translation_cache.sql").Scan(&checksum); err != nil {
		t.Fatal(err)
	}
	if checksum != "5ff08c11d8012cced81bc0887fdd87d857d94b0a57b6d185f992cf573f7b07e4" {
		t.Fatalf("normalized checksum = %s", checksum)
	}
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		t.Fatal(err)
	}
	var backups, audits int
	for _, entry := range entries {
		switch {
		case strings.HasSuffix(entry.Name(), ".db"):
			backups++
		case strings.HasSuffix(entry.Name(), ".json"):
			audits++
		}
	}
	if backups != 1 || audits != 1 {
		t.Fatalf("preflight artifacts = %d backups, %d audits; want 1/1", backups, audits)
	}

	stdout.Reset()
	stderr.Reset()
	if code := runAdminCommand([]string{"preflight-095", "--backup-dir", backupDir}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("second preflight-095 exit code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Legacy 095 checksum is already current; no change.") {
		t.Fatalf("second preflight-095 output = %s", stdout.String())
	}
	entries, err = os.ReadDir(backupDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("second preflight-095 wrote extra artifacts: %v", entries)
	}
}

func legacyCommandTestDatabase(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	catalog := filepath.Join(root, "migrations")
	if err := os.MkdirAll(catalog, 0o755); err != nil {
		t.Fatal(err)
	}
	sourceDir := filepath.Join("..", "..", "migrations")
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".sql") || len(name) < 4 || name[3] != '_' || name[:3] > "094" {
			continue
		}
		contents, err := os.ReadFile(filepath.Join(sourceDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(catalog, name), contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	legacy, err := os.ReadFile(filepath.Join("..", "..", "internal", "storage", "testdata", "legacy_095_no_foreign_key.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(catalog, "095_source_field_translation_cache.sql"), legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "kikoto.db")
	db, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.Migrate(db, catalog); err != nil {
		_ = db.Close()
		t.Fatalf("create legacy 095 database: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KIKOTO_MODE", "production")
	t.Setenv("KIKOTO_DB_PATH", path)
	t.Setenv("KIKOTO_ROOT_USERNAME", "")
	t.Setenv("KIKOTO_ROOT_PASSWORD", "")
	t.Setenv("KIKOTO_ROOT_PASSWORD_RESET", "")
	t.Setenv("KIKOTO_ROOT_ACCOUNT_MODE", "")
	return path
}
