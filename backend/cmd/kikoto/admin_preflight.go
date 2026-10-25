package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/yexca/kikoto/backend/internal/storage"
)

func runAdminPreflight095(args []string, stdout io.Writer, stderr io.Writer) int {
	flags := flag.NewFlagSet("kikoto admin preflight-095", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = fmt.Fprint(stderr, adminUsage) }
	databaseFlag := flags.String("database", "", "database path (defaults to KIKOTO_DB_PATH)")
	backupDirFlag := flags.String("backup-dir", "", "directory for the preflight backup and audit record")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		flags.Usage()
		return 2
	}
	databasePath := strings.TrimSpace(*databaseFlag)
	if databasePath == "" {
		databasePath = strings.TrimSpace(os.Getenv("KIKOTO_DB_PATH"))
	}
	backupDir := strings.TrimSpace(*backupDirFlag)
	if backupDir == "" {
		backupDir = strings.TrimSpace(os.Getenv("KIKOTO_DB_BACKUP_DIR"))
	}
	if databasePath == "" {
		_, _ = fmt.Fprintln(stderr, "database path is empty")
		return 1
	}
	if databasePath != ":memory:" && !strings.HasPrefix(databasePath, "file:") {
		if _, err := os.Stat(databasePath); errors.Is(err, fs.ErrNotExist) {
			_, _ = fmt.Fprintf(stderr, "database %s does not exist\n", databasePath)
			return 1
		} else if err != nil {
			_, _ = fmt.Fprintln(stderr, "inspect database:", err)
			return 1
		}
	}
	if backupDir == "" && databasePath != ":memory:" && !strings.HasPrefix(databasePath, "file:") {
		backupDir = filepath.Join(filepath.Dir(databasePath), "backups")
	}
	if backupDir == "" {
		_, _ = fmt.Fprintln(stderr, "a backup directory is required; pass --backup-dir")
		return 1
	}

	db, err := storage.Open(databasePath)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "open database:", err)
		return 1
	}
	defer func() { _ = db.Close() }()

	result, err := storage.PreflightLegacyMigration095(context.Background(), db, backupDir)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "legacy 095 preflight:", err)
		return 1
	}
	switch result.Status {
	case storage.Legacy095PreflightNormalized:
		_, _ = fmt.Fprintln(stdout, "Legacy 095 checksum normalized.")
		_, _ = fmt.Fprintf(stdout, "Old checksum: %s\n", result.OldChecksum)
		_, _ = fmt.Fprintf(stdout, "New checksum: %s\n", result.NewChecksum)
		_, _ = fmt.Fprintf(stdout, "Schema fingerprint: %s\n", result.Fingerprint)
		_, _ = fmt.Fprintf(stdout, "Database backup: %s\n", result.BackupPath)
		_, _ = fmt.Fprintf(stdout, "Audit record: %s\n", result.AuditPath)
		_, _ = fmt.Fprintf(stdout, "Executed at: %s\n", result.ExecutedAt.Format("2006-01-02T15:04:05Z07:00"))
	case storage.Legacy095PreflightAlreadyCurrent:
		_, _ = fmt.Fprintln(stdout, "Legacy 095 checksum is already current; no change.")
	default:
		_, _ = fmt.Fprintln(stdout, "No legacy 095 checksum requires normalization; no change.")
	}
	return 0
}
