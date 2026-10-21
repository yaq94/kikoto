// Command catalog-prune audits and removes catalog cards that can never pass
// the publish admission rules. It defaults to a dry-run: the report is the
// reviewed artifact, and -apply refuses to run without it.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"

	"github.com/yexca/kikoto/backend/internal/catalogprune"
	"github.com/yexca/kikoto/backend/internal/storage"
)

func main() {
	databasePath := flag.String("db", "", "path to the Kikoto SQLite database (required)")
	dryRun := flag.Bool("dry-run", false, "write the audit report without deleting anything (the default when -apply is absent)")
	apply := flag.Bool("apply", false, "delete the cards described by -report (default: dry-run only)")
	batch := flag.Int("batch", 100, "cards per transaction with -apply")
	reportPath := flag.String("report", "", "dry-run JSON report to verify before -apply")
	jsonOut := flag.String("json", "catalog-prune-dry-run.json", "dry-run JSON output path")
	mdOut := flag.String("md", "catalog-prune-dry-run.md", "dry-run Markdown output path")
	whitelist := flag.String("whitelist", strings.Join(catalogprune.WhitelistFromEnv(), ","), "comma-separated paths that must survive")
	limit := flag.Int("limit", 0, "limit the plan to the first N cards (tests only)")
	note := flag.String("note", "", "note recorded in the dry-run report")
	flag.Parse()

	if strings.TrimSpace(*databasePath) == "" {
		fail(errors.New("-db is required"))
	}
	options := catalogprune.Options{
		Whitelist:  splitList(*whitelist),
		BatchSize:  *batch,
		MaxEntries: *limit,
	}
	if *apply && *dryRun {
		fail(errors.New("-apply and -dry-run are mutually exclusive"))
	}
	ctx := context.Background()
	if *apply {
		if strings.TrimSpace(*reportPath) == "" {
			fail(errors.New("-apply requires -report with the reviewed dry-run JSON"))
		}
		report, err := readReport(*reportPath)
		if err != nil {
			fail(err)
		}
		db, err := storage.Open(*databasePath)
		if err != nil {
			fail(err)
		}
		defer func() { _ = db.Close() }()
		if _, err = db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
			fail(err)
		}
		result, err := catalogprune.Apply(ctx, db, report, options)
		if err != nil {
			fail(err)
		}
		encoded, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(encoded))
		return
	}

	db, err := openReadOnly(*databasePath)
	if err != nil {
		fail(err)
	}
	defer func() { _ = db.Close() }()
	report, err := catalogprune.BuildReport(ctx, db, options)
	if err != nil {
		fail(err)
	}
	report.Note = strings.TrimSpace(*note)
	if err = writeReport(report, *jsonOut, *mdOut); err != nil {
		fail(err)
	}
	fmt.Printf("dry-run candidates: %d cards, %d files, %d transfer refs\n",
		report.Candidates.Entries, report.Candidates.Files, report.Candidates.TransferRefs)
	fmt.Printf("dry-run plan: %d cards, %d files, %d observations, %d directories, %d audio roots, %d transfer refs, %d kept rows\n",
		report.Totals.Entries, report.Totals.Files, report.Totals.Observations, report.Totals.Directories,
		report.Totals.AudioRoots, report.Totals.TransferRefs, report.Totals.KeptRows)
	fmt.Printf("report: %s\n", *jsonOut)
	fmt.Printf("markdown: %s\n", *mdOut)
}

func splitList(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func openReadOnly(path string) (*sql.DB, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	separator := "?"
	if strings.Contains(absolute, "?") {
		separator = "&"
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(absolute)+separator+"mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	if err = db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func readReport(path string) (catalogprune.Report, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return catalogprune.Report{}, err
	}
	var report catalogprune.Report
	if err = json.Unmarshal(data, &report); err != nil {
		return catalogprune.Report{}, err
	}
	return report, nil
}

func writeReport(report catalogprune.Report, jsonPath, mdPath string) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(jsonPath), 0o755); err != nil && filepath.Dir(jsonPath) != "." {
		return err
	}
	if err = os.WriteFile(jsonPath, append(data, '\n'), 0o644); err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(mdPath), 0o755); err != nil && filepath.Dir(mdPath) != "." {
		return err
	}
	return os.WriteFile(mdPath, []byte(catalogprune.RenderMarkdown(report)), 0o644)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "catalog-prune:", err)
	os.Exit(1)
}
