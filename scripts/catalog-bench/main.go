// Command catalog-bench replays the source-catalog hot paths against a
// read-only SQLite copy and prints timings plus EXPLAIN QUERY PLAN output.
//
// It never writes to the database named by --db. When --vacuum-to is set it
// creates a consistent copy with VACUUM INTO and runs the write benchmarks
// against that copy inside BEGIN IMMEDIATE ... ROLLBACK, so the copy is not
// modified either.
package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	defaultRoot     = "/asmr6/轩子"
	publishRows     = 1000
	cleanupCards    = 100
	lifecycleRows   = 100
	queryTimeout    = 10 * time.Minute
	benchmarkDriver = "modernc.org/sqlite"
)

// catalogPublishQuery is copied verbatim from
// backend/internal/httpapi/source_catalog_index_store.go. The LIMIT is bound.
const catalogPublishQuery = `SELECT d.path,d.catalog_root,o.name,o.kind,o.size_bytes,o.content_hash,o.modified_at,
	COALESCE(rd.work_split,'')
FROM source_catalog_directory d
CROSS JOIN source_catalog_observation o ON o.file_source_id=d.file_source_id AND o.directory_path=d.path AND o.revision=d.revision
LEFT JOIN source_catalog_directory rd ON rd.file_source_id=d.file_source_id AND rd.path=d.catalog_root AND rd.generation=d.generation
WHERE d.file_source_id=? AND d.generation=? AND d.status='complete' AND d.catalog_root<>''
	AND o.publish_pending=1
	AND EXISTS(SELECT 1 FROM source_catalog_audio_root r
		WHERE r.file_source_id=d.file_source_id AND r.generation=d.generation AND r.catalog_root=d.catalog_root)
ORDER BY d.path,o.name LIMIT ?`

const publishFixtureSQL = `UPDATE source_catalog_observation SET publish_pending=1
WHERE rowid IN (
	SELECT o.rowid FROM source_catalog_observation o
	JOIN source_catalog_directory d ON d.file_source_id=o.file_source_id AND d.path=o.directory_path AND d.revision=o.revision
	WHERE d.file_source_id=? AND d.generation=? AND d.status='complete' AND d.catalog_root<>''
		AND o.publish_pending=0
		AND EXISTS(SELECT 1 FROM source_catalog_audio_root r
			WHERE r.file_source_id=d.file_source_id AND r.generation=d.generation AND r.catalog_root=d.catalog_root)
	LIMIT ?)`

const progressScanSQL = `SELECT scan.workflow_run_id,run.status,scan.pause_reason,scan.options_json,scan.requests,scan.started_at,scan.updated_at
FROM source_catalog_scan scan JOIN workflow_run run ON run.id=scan.workflow_run_id
WHERE scan.file_source_id=?`

const progressStagingSQL = `SELECT
	(SELECT count(*) FROM source_catalog_directory WHERE file_source_id=?),
	(SELECT count(*) FROM source_catalog_observation WHERE file_source_id=?)`

const progressDirectorySQL = `SELECT count(*),
	coalesce(sum(status='complete' OR (status='empty' AND error_code='not_found')),0),
	coalesce(sum(status IN ('pending','listing_complete')),0),
	coalesce(sum(status='failed'),0),
	coalesce(sum(status='restricted'),0),
	coalesce(sum(status='empty' AND error_code<>'not_found'),0),
	count(DISTINCT partition_path)
FROM source_catalog_directory WHERE file_source_id=? AND generation=?`

const progressObservedSQL = `SELECT
	(SELECT count(*) FROM source_catalog_directory d JOIN source_catalog_observation o
		ON o.file_source_id=d.file_source_id AND o.directory_path=d.path AND o.revision=d.revision
		WHERE d.file_source_id=? AND d.generation=?),
	(SELECT count(*) FROM source_catalog_entry WHERE file_source_id=? AND last_seen_generation=?)`

const progressPartitionsSQL = `SELECT partition_path,count(*),sum(status='complete' OR (status='empty' AND error_code='not_found')),sum(status='failed'),sum(status='restricted'),sum(status='empty' AND error_code<>'not_found')
FROM source_catalog_directory WHERE file_source_id=? AND generation=?
GROUP BY partition_path ORDER BY partition_path LIMIT 50 OFFSET ?`

const progressDetailsSQL = `SELECT path,status,next_page,error_code FROM source_catalog_directory
WHERE file_source_id=? AND generation=? AND status<>'complete' ORDER BY path LIMIT 50 OFFSET ?`

const purgeCheckSQL = `SELECT EXISTS(
	SELECT 1 FROM source_catalog_observation o
	JOIN source_catalog_directory d ON d.file_source_id=o.file_source_id AND d.path=o.directory_path
	WHERE d.file_source_id=? AND d.generation<>? AND o.publish_pending=1)`

const rootIncompleteSQL = `SELECT EXISTS(
	SELECT 1 FROM source_catalog_directory d
	WHERE d.file_source_id=? AND d.generation=? AND d.status<>'complete'
		AND NOT (d.status='empty' AND d.error_code='not_found')
		AND (?='/' OR d.catalog_root=? OR d.path=? OR substr(d.path,1,length(?)+1)=? || '/')
	UNION ALL
	SELECT 1 FROM source_catalog_observation o
	JOIN source_catalog_directory pd ON pd.file_source_id=o.file_source_id AND pd.path=o.directory_path AND o.revision=pd.revision
	WHERE o.file_source_id=? AND pd.generation=? AND o.publish_pending=1
		AND NOT (pd.status='empty' AND pd.error_code='not_found')
		AND (o.kind='audio' OR EXISTS(SELECT 1 FROM source_catalog_audio_root r
			WHERE r.file_source_id=pd.file_source_id AND r.generation=pd.generation AND r.catalog_root=pd.catalog_root))
		AND (?='/' OR pd.catalog_root=? OR pd.path=? OR substr(pd.path,1,length(?)+1)=? || '/')
)`

const cleanupIDsSQL = `SELECT id FROM source_catalog_entry
WHERE file_source_id=? AND last_seen_generation=? AND id>?
ORDER BY id LIMIT ?`

const cleanupDeleteFilesSQL = `DELETE FROM source_catalog_file WHERE catalog_entry_id=? AND observed_generation<>?`

const cleanupHasDirectorySQL = `SELECT EXISTS(SELECT 1 FROM source_catalog_file WHERE catalog_entry_id=? AND kind<>'audio')`

const cleanupDeleteChildrenSQL = `DELETE FROM source_catalog_file
WHERE catalog_entry_id=? AND relative_path IN (
	SELECT substr(root_path,length(?)+2) FROM source_catalog_entry
	WHERE file_source_id=? AND id<>? AND relocated_to_id IS NULL
		AND source_root='' AND root_path>=? AND root_path<?
	UNION ALL
	SELECT substr(source_root,length(?)+2) FROM source_catalog_entry
	WHERE file_source_id=? AND id<>? AND relocated_to_id IS NULL
		AND source_root<>'' AND source_root>=? AND source_root<?
)`

const lifecycleEntriesSQL = `SELECT id,last_seen_generation FROM source_catalog_entry
WHERE file_source_id=? AND relocated_to_id IS NULL AND id>?
ORDER BY id LIMIT ?`

const lifecycleStatusSQL = `UPDATE source_catalog_entry SET availability_status=?,status_checked_at=CURRENT_TIMESTAMP WHERE id=?`

const lifecycleMediaSQL = `UPDATE media_file_location SET availability=? WHERE location_type='catalog_stream'
	AND instr(stream_url,'/api/source-catalog/' || ? || '/media?')=1
	AND (?='unavailable' OR EXISTS(SELECT 1 FROM source_catalog_file f WHERE f.catalog_entry_id=? AND f.relative_path=media_file_location.path AND f.kind='audio'))`

const lifecyclePresenceSQL = `UPDATE work_source_presence SET availability=CASE WHEN EXISTS(
	SELECT 1 FROM source_catalog_entry e WHERE e.work_id=work_source_presence.work_id AND e.file_source_id=work_source_presence.file_source_id AND e.relocated_to_id IS NULL AND e.availability_status='available') THEN 'available' ELSE ? END,
	last_checked_at=CURRENT_TIMESTAMP WHERE (work_id,file_source_id) IN(SELECT work_id,file_source_id FROM source_catalog_entry WHERE id=?)`

const lifecycleFingerprintSQL = `SELECT relative_path,size_bytes,content_hash FROM source_catalog_file
WHERE catalog_entry_id=? AND observed_generation=? AND kind='audio' ORDER BY relative_path LIMIT 10001`

const effectiveRootSQL = `SELECT CASE WHEN source_root<>'' THEN source_root ELSE root_path END FROM source_catalog_entry WHERE id=?`

const baseObservedSQL = `SELECT status,error_code FROM source_catalog_directory WHERE file_source_id=? AND path=? AND generation=?`

const rangeUncertainSQL = `SELECT EXISTS(SELECT 1 FROM source_catalog_directory d
	WHERE d.file_source_id=? AND d.generation=? AND status IN ('failed','restricted','empty','pending','listing_complete')
	AND NOT (status='empty' AND error_code='not_found')`

type measure struct {
	Name      string         `json:"name"`
	Runs      int            `json:"runs"`
	MedianMS  float64        `json:"medianMs"`
	MinMS     float64        `json:"minMs"`
	MaxMS     float64        `json:"maxMs"`
	Details   map[string]any `json:"details,omitempty"`
	Plan      []string       `json:"plan,omitempty"`
	PlanError string         `json:"planError,omitempty"`
}

type report struct {
	GeneratedAt   string         `json:"generatedAt"`
	Label         string         `json:"label,omitempty"`
	Driver        string         `json:"driver"`
	Database      string         `json:"database"`
	DatabaseBytes int64          `json:"databaseBytes"`
	Vacuum        map[string]any `json:"vacuum,omitempty"`
	SchemaVersion int            `json:"schemaVersion"`
	Counts        map[string]int `json:"counts"`
	Source        map[string]any `json:"source"`
	Root          string         `json:"root"`
	Queries       []measure      `json:"queries"`
	Logs          map[string]any `json:"logs,omitempty"`
	Warnings      []string       `json:"warnings,omitempty"`
}

func main() {
	var (
		dbPath      = flag.String("db", "", "SQLite database path (opened read-only)")
		vacuumTo    = flag.String("vacuum-to", "", "optional target copy created with VACUUM INTO")
		writeDB     = flag.String("write-db", "", "disposable copy for write benchmarks; must differ from --db")
		sourceID    = flag.Int64("source-id", 0, "source id; 0 auto-detects from --root")
		generation  = flag.Int64("generation", 0, "scan generation; 0 reads source_catalog_scan")
		root        = flag.String("root", defaultRoot, "root path for catalogRootIncomplete")
		runs        = flag.Int("runs", 3, "runs for read-only benchmarks")
		writeRuns   = flag.Int("write-runs", 1, "runs for write-path benchmarks (each rolls back)")
		logsPath    = flag.String("logs", "", "optional docker logs text file")
		outPath     = flag.String("json-out", "", "optional JSON output path")
		label       = flag.String("label", "before", "label written to the report")
		only        = flag.String("only", "", "comma-separated query names to run")
		force       = flag.Bool("force", false, "overwrite the --vacuum-to target if it exists")
		cleanupFrom = flag.Int64("cleanup-cursor", 0, "starting entry id for the cleanup benchmark")
		lifecycleAt = flag.Int64("lifecycle-cursor", 0, "starting entry id for the lifecycle benchmark")
	)
	flag.Parse()
	if strings.TrimSpace(*dbPath) == "" {
		fatalf("--db is required")
	}
	if *vacuumTo != "" && samePath(*dbPath, *vacuumTo) {
		fatalf("--vacuum-to must differ from --db")
	}
	if *writeDB != "" && samePath(*dbPath, *writeDB) {
		fatalf("--write-db must differ from --db")
	}
	if *writeDB != "" {
		if _, err := os.Stat(*writeDB); err != nil {
			fatalf("--write-db: %v", err)
		}
	}
	if *vacuumTo != "" {
		if _, err := os.Stat(*vacuumTo); err == nil && !*force {
			fatalf("%s exists; pass --force to replace it", *vacuumTo)
		}
	}

	rep := report{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Label:       *label,
		Driver:      benchmarkDriver,
		Database:    absPath(*dbPath),
		Root:        *root,
		Counts:      map[string]int{},
		Queries:     []measure{},
	}
	if st, err := os.Stat(*dbPath); err == nil {
		rep.DatabaseBytes = st.Size()
	}

	workDB := *dbPath
	if *vacuumTo != "" {
		started := time.Now()
		if err := vacuumInto(*dbPath, *vacuumTo, *force); err != nil {
			fatalf("VACUUM INTO: %v", err)
		}
		elapsed := time.Since(started)
		var size int64
		if st, err := os.Stat(*vacuumTo); err == nil {
			size = st.Size()
		}
		rep.Vacuum = map[string]any{
			"target":    absPath(*vacuumTo),
			"elapsedMs": msFloat(elapsed),
			"bytes":     size,
		}
		workDB = *vacuumTo
	}
	writeWorkDB := *writeDB
	if writeWorkDB == "" {
		writeWorkDB = *vacuumTo
	}

	ctx := context.Background()
	ro, err := openReadOnly(workDB)
	if err != nil {
		fatalf("open copy: %v", err)
	}
	defer ro.Close()
	rep.SchemaVersion = readSchemaVersion(ctx, ro)
	rep.Counts = readCounts(ctx, ro)

	sid, gen, dirs, err := resolveSource(ctx, ro, *sourceID, *generation, *root)
	if err != nil {
		fatalf("resolve source: %v", err)
	}
	rep.Source = map[string]any{"id": sid, "generation": gen, "root": *root, "directories": dirs}
	globalPlanDB = workDB

	enabled := func(name string) bool {
		if strings.TrimSpace(*only) == "" {
			return true
		}
		for _, item := range strings.Split(*only, ",") {
			if strings.TrimSpace(item) == name {
				return true
			}
		}
		return false
	}

	// Write benchmarks must never touch the source database. Requiring a
	// VACUUM INTO copy keeps the read-only contract even though each write
	// benchmark rolls back.
	if writeWorkDB == "" && (enabled("cleanup_100_cards") || enabled("lifecycle_100_entries") || enabled("publish_1000_rows_fixture")) {
		fatalf("cleanup_100_cards, lifecycle_100_entries and publish_1000_rows_fixture require --vacuum-to or --write-db")
	}

	if enabled("progress_exact") {
		rep.Queries = append(rep.Queries, benchRead(ctx, "progress_exact", *runs, func(ctx context.Context) (map[string]any, error) {
			return runProgress(ctx, ro, sid, gen)
		}, []planSpec{
			{SQL: progressScanSQL, Args: []any{sid}},
			{SQL: progressStagingSQL, Args: []any{sid, sid}},
			{SQL: progressDirectorySQL, Args: []any{sid, gen}},
			{SQL: progressObservedSQL, Args: []any{sid, gen, sid, gen}},
			{SQL: progressPartitionsSQL, Args: []any{sid, gen, 0}},
			{SQL: progressDetailsSQL, Args: []any{sid, gen, 0}},
		}))
	}
	if enabled("purge_empty_pending") {
		rep.Queries = append(rep.Queries, benchRead(ctx, "purge_empty_pending", *runs, func(ctx context.Context) (map[string]any, error) {
			var stale int
			err := ro.QueryRowContext(ctx, purgeCheckSQL, sid, gen).Scan(&stale)
			return map[string]any{"stale": stale}, err
		}, []planSpec{{SQL: purgeCheckSQL, Args: []any{sid, gen}}}))
	}
	if enabled("root_incomplete") {
		args := []any{sid, gen, *root, *root, *root, *root, *root, sid, gen, *root, *root, *root, *root, *root}
		rep.Queries = append(rep.Queries, benchRead(ctx, "root_incomplete", *runs, func(ctx context.Context) (map[string]any, error) {
			var incomplete int
			err := ro.QueryRowContext(ctx, rootIncompleteSQL, args...).Scan(&incomplete)
			return map[string]any{"root": *root, "incomplete": incomplete}, err
		}, []planSpec{{SQL: rootIncompleteSQL, Args: args}}))
	}
	if enabled("publish_1000_rows") {
		rep.Queries = append(rep.Queries, benchRead(ctx, "publish_1000_rows", *runs, func(ctx context.Context) (map[string]any, error) {
			rows, err := ro.QueryContext(ctx, catalogPublishQuery, sid, gen, publishRows)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			n := 0
			for rows.Next() {
				var dir, catalogRoot, name, kind, hash, modified, split string
				var size int64
				if err := rows.Scan(&dir, &catalogRoot, &name, &kind, &size, &hash, &modified, &split); err != nil {
					return nil, err
				}
				n++
			}
			return map[string]any{"rows": n}, rows.Err()
		}, []planSpec{{SQL: catalogPublishQuery, Args: []any{sid, gen, publishRows}}}))
	}
	if enabled("publish_1000_rows_fixture") {
		rep.Queries = append(rep.Queries, benchPublishFixture(ctx, writeWorkDB, *runs, sid, gen, publishRows))
	}
	if enabled("cleanup_100_cards") {
		rep.Queries = append(rep.Queries, benchWrite(ctx, "cleanup_100_cards", writeWorkDB, *writeRuns, func(ctx context.Context, conn *sql.Conn) (map[string]any, error) {
			return runCleanup(ctx, conn, sid, gen, *cleanupFrom, cleanupCards)
		}, []planSpec{
			{SQL: cleanupIDsSQL, Args: []any{sid, gen, *cleanupFrom, cleanupCards}},
			{SQL: cleanupDeleteFilesSQL, Args: []any{0, gen}},
			{SQL: cleanupHasDirectorySQL, Args: []any{0}},
			{SQL: cleanupDeleteChildrenSQL, Args: []any{0, "", sid, 0, "", "\U0010FFFF", "", sid, 0, "", "\U0010FFFF"}},
		}))
	}
	if enabled("lifecycle_100_entries") {
		rep.Queries = append(rep.Queries, benchWrite(ctx, "lifecycle_100_entries", writeWorkDB, *writeRuns, func(ctx context.Context, conn *sql.Conn) (map[string]any, error) {
			return runLifecycle(ctx, conn, sid, gen, *lifecycleAt, lifecycleRows)
		}, []planSpec{
			{SQL: lifecycleEntriesSQL, Args: []any{sid, *lifecycleAt, lifecycleRows}},
			{SQL: lifecycleStatusSQL, Args: []any{"available", 0}},
			{SQL: lifecycleMediaSQL, Args: []any{"available", 0, "available", 0}},
			{SQL: lifecyclePresenceSQL, Args: []any{"available", 0}},
		}))
	}

	if *logsPath != "" {
		stats, err := parseLogs(*logsPath)
		if err != nil {
			rep.Warnings = append(rep.Warnings, "logs: "+err.Error())
		} else {
			rep.Logs = stats
		}
	}

	out, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		fatalf("marshal: %v", err)
	}
	out = append(out, '\n')
	if *outPath != "" {
		if err := os.WriteFile(*outPath, out, 0o644); err != nil {
			fatalf("write %s: %v", *outPath, err)
		}
	}
	os.Stdout.Write(out)
}

type planSpec struct {
	SQL  string
	Args []any
}

func benchRead(ctx context.Context, name string, runs int, fn func(context.Context) (map[string]any, error), plans []planSpec) measure {
	if runs < 1 {
		runs = 1
	}
	m := measure{Name: name, Runs: runs}
	times := make([]float64, 0, runs)
	var details map[string]any
	for i := 0; i < runs; i++ {
		runCtx, cancel := context.WithTimeout(ctx, queryTimeout)
		started := time.Now()
		d, err := fn(runCtx)
		elapsed := time.Since(started)
		cancel()
		if err != nil {
			m.PlanError = err.Error()
			break
		}
		times = append(times, msFloat(elapsed))
		details = d
	}
	m.Details = details
	m.MinMS, m.MaxMS, m.MedianMS = stats(times)
	m.Plan = collectPlans(ctx, name, plans)
	return m
}

// benchPublishFixture marks up to limit currently-unpublished observations in
// audio-bearing directories as pending, then times only the publish query. The
// fixture update and the query share one transaction that is rolled back, so
// the copy is unchanged. This measures the 1,000-row path even when the real
// backlog has zero publishable rows.
func benchPublishFixture(ctx context.Context, dbPath string, runs int, sourceID, generation int64, limit int) measure {
	if runs < 1 {
		runs = 1
	}
	m := measure{Name: "publish_1000_rows_fixture", Runs: runs}
	times := make([]float64, 0, runs)
	var details map[string]any
	for i := 0; i < runs; i++ {
		db, err := openWritable(dbPath)
		if err != nil {
			m.PlanError = err.Error()
			break
		}
		conn, err := db.Conn(ctx)
		if err != nil {
			db.Close()
			m.PlanError = err.Error()
			break
		}
		runCtx, cancel := context.WithTimeout(ctx, queryTimeout)
		if _, err = conn.ExecContext(runCtx, "BEGIN IMMEDIATE"); err == nil {
			var res sql.Result
			res, err = conn.ExecContext(runCtx, publishFixtureSQL, sourceID, generation, limit)
			if err == nil {
				var marked int64
				marked, _ = res.RowsAffected()
				started := time.Now()
				var rows *sql.Rows
				rows, err = conn.QueryContext(runCtx, catalogPublishQuery, sourceID, generation, limit)
				if err == nil {
					n := 0
					for rows.Next() {
						var dir, catalogRoot, name, kind, hash, modified, split string
						var size int64
						if err = rows.Scan(&dir, &catalogRoot, &name, &kind, &size, &hash, &modified, &split); err != nil {
							break
						}
						n++
					}
					if err == nil {
						err = rows.Err()
					}
					rows.Close()
					elapsed := time.Since(started)
					if err == nil {
						times = append(times, msFloat(elapsed))
						details = map[string]any{"rows": n, "markedPending": marked}
					}
				}
			}
		}
		_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		cancel()
		conn.Close()
		db.Close()
		if err != nil {
			m.PlanError = err.Error()
			break
		}
	}
	m.Details = details
	m.MinMS, m.MaxMS, m.MedianMS = stats(times)
	m.Plan = collectPlans(ctx, m.Name, []planSpec{
		{SQL: publishFixtureSQL, Args: []any{sourceID, generation, limit}},
		{SQL: catalogPublishQuery, Args: []any{sourceID, generation, limit}},
	})
	return m
}

func benchWrite(ctx context.Context, name, dbPath string, runs int, fn func(context.Context, *sql.Conn) (map[string]any, error), plans []planSpec) measure {
	if runs < 1 {
		runs = 1
	}
	m := measure{Name: name, Runs: runs}
	times := make([]float64, 0, runs)
	var details map[string]any
	for i := 0; i < runs; i++ {
		db, err := openWritable(dbPath)
		if err != nil {
			m.PlanError = err.Error()
			break
		}
		conn, err := db.Conn(ctx)
		if err != nil {
			db.Close()
			m.PlanError = err.Error()
			break
		}
		runCtx, cancel := context.WithTimeout(ctx, queryTimeout)
		started := time.Now()
		if _, err = conn.ExecContext(runCtx, "BEGIN IMMEDIATE"); err == nil {
			details, err = fn(runCtx, conn)
		}
		elapsed := time.Since(started)
		_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		cancel()
		conn.Close()
		db.Close()
		if err != nil {
			m.PlanError = err.Error()
			break
		}
		times = append(times, msFloat(elapsed))
	}
	m.Details = details
	m.MinMS, m.MaxMS, m.MedianMS = stats(times)
	m.Plan = collectPlans(ctx, name, plans)
	return m
}

func runProgress(ctx context.Context, db *sql.DB, sourceID, generation int64) (map[string]any, error) {
	details := map[string]any{}
	breakdown := map[string]float64{}
	details["breakdownMs"] = breakdown
	var runID int64
	var status, pauseReason, options, startedAt, updatedAt string
	var requests int
	stepStarted := time.Now()
	if err := db.QueryRowContext(ctx, progressScanSQL, sourceID).Scan(&runID, &status, &pauseReason, &options, &requests, &startedAt, &updatedAt); err != nil {
		return nil, err
	}
	breakdown["scanRow"] = msFloat(time.Since(stepStarted))
	details["runId"] = runID
	details["status"] = status
	details["pauseReason"] = pauseReason
	details["requests"] = requests
	var stagingDirs, stagingObs int
	stepStarted = time.Now()
	if err := db.QueryRowContext(ctx, progressStagingSQL, sourceID, sourceID).Scan(&stagingDirs, &stagingObs); err != nil {
		return nil, err
	}
	breakdown["stagingCounts"] = msFloat(time.Since(stepStarted))
	details["stagingDirectories"] = stagingDirs
	details["stagingObservations"] = stagingObs
	var dirs, completed, pending, failed, restricted, empty, partitions int
	stepStarted = time.Now()
	if err := db.QueryRowContext(ctx, progressDirectorySQL, sourceID, runID).Scan(&dirs, &completed, &pending, &failed, &restricted, &empty, &partitions); err != nil {
		return nil, err
	}
	breakdown["directoryAggregates"] = msFloat(time.Since(stepStarted))
	details["directories"] = dirs
	details["completed"] = completed
	details["pending"] = pending
	details["failed"] = failed
	details["restricted"] = restricted
	details["unconfirmedEmpty"] = empty
	details["partitions"] = partitions
	var observed, indexed int
	stepStarted = time.Now()
	if err := db.QueryRowContext(ctx, progressObservedSQL, sourceID, runID, sourceID, runID).Scan(&observed, &indexed); err != nil {
		return nil, err
	}
	breakdown["observedJoin"] = msFloat(time.Since(stepStarted))
	details["observed"] = observed
	details["indexed"] = indexed
	stepStarted = time.Now()
	rows, err := db.QueryContext(ctx, progressPartitionsSQL, sourceID, runID, 0)
	if err != nil {
		return nil, err
	}
	partRows := 0
	for rows.Next() {
		var p string
		var a, b, c, d, e int
		if err := rows.Scan(&p, &a, &b, &c, &d, &e); err != nil {
			rows.Close()
			return nil, err
		}
		partRows++
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	details["partitionRows"] = partRows
	breakdown["partitionPage"] = msFloat(time.Since(stepStarted))
	stepStarted = time.Now()
	rows, err = db.QueryContext(ctx, progressDetailsSQL, sourceID, runID, 0)
	if err != nil {
		return nil, err
	}
	detailRows := 0
	for rows.Next() {
		var p, s, code string
		var next int
		if err := rows.Scan(&p, &s, &next, &code); err != nil {
			rows.Close()
			return nil, err
		}
		detailRows++
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	details["detailRows"] = detailRows
	breakdown["detailPage"] = msFloat(time.Since(stepStarted))
	return details, nil
}

func runCleanup(ctx context.Context, conn *sql.Conn, sourceID, generation, cursor int64, limit int) (map[string]any, error) {
	rows, err := conn.QueryContext(ctx, cleanupIDsSQL, sourceID, generation, cursor, limit)
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	removed := int64(0)
	uncertainCards := 0
	directoryCards := 0
	for _, id := range ids {
		var base string
		if err = conn.QueryRowContext(ctx, effectiveRootSQL, id).Scan(&base); err != nil {
			return nil, err
		}
		uncertain, err := rangeUncertain(ctx, conn, sourceID, generation, base)
		if err != nil {
			return nil, err
		}
		if uncertain {
			uncertainCards++
		} else {
			res, err := conn.ExecContext(ctx, cleanupDeleteFilesSQL, id, generation)
			if err != nil {
				return nil, err
			}
			n, _ := res.RowsAffected()
			removed += n
		}
		var hasDirectory int
		if err = conn.QueryRowContext(ctx, cleanupHasDirectorySQL, id).Scan(&hasDirectory); err != nil {
			return nil, err
		}
		if hasDirectory == 0 {
			continue
		}
		directoryCards++
		parentRoot := strings.TrimRight(base, "/")
		lower, upper := parentRoot+"/", parentRoot+"0"
		if parentRoot == "" {
			lower, upper = "", "\U0010FFFF"
		}
		res, err := conn.ExecContext(ctx, cleanupDeleteChildrenSQL, id, parentRoot, sourceID, id, lower, upper, parentRoot, sourceID, id, lower, upper)
		if err != nil {
			return nil, err
		}
		n, _ := res.RowsAffected()
		removed += n
	}
	if len(ids) > 0 {
		if _, err = conn.ExecContext(ctx, `UPDATE source_catalog_scan SET publish_cleanup_cursor=?,updated_at=CURRENT_TIMESTAMP WHERE file_source_id=?`, ids[len(ids)-1], sourceID); err != nil {
			return nil, err
		}
	}
	return map[string]any{"scanned": len(ids), "removedFiles": removed, "uncertainCards": uncertainCards, "directoryCards": directoryCards, "cursorFrom": cursor}, nil
}

func runLifecycle(ctx context.Context, conn *sql.Conn, sourceID, generation, cursor int64, limit int) (map[string]any, error) {
	var stage string
	var storedCursor int64
	if err := conn.QueryRowContext(ctx, `SELECT lifecycle_stage,lifecycle_cursor FROM source_catalog_scan WHERE file_source_id=?`, sourceID).Scan(&stage, &storedCursor); err != nil {
		return nil, err
	}
	details := map[string]any{"stage": stage, "storedCursor": storedCursor}
	if stage == "done" {
		details["skipped"] = "stage=done"
		return details, nil
	}
	if stage == "reconcile" {
		details["skipped"] = "stage=reconcile (this harness times the observe path)"
		return details, nil
	}
	rows, err := conn.QueryContext(ctx, lifecycleEntriesSQL, sourceID, cursor, limit)
	if err != nil {
		return nil, err
	}
	type entry struct{ id, gen int64 }
	entries := []entry{}
	for rows.Next() {
		var e entry
		if err = rows.Scan(&e.id, &e.gen); err != nil {
			break
		}
		entries = append(entries, e)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	updated := 0
	uncertainSkipped := 0
	for _, e := range entries {
		status := "missing"
		if e.gen == generation {
			status = "available"
			if _, err = conn.ExecContext(ctx, `DELETE FROM source_catalog_relocation_candidate WHERE catalog_entry_id=?`, e.id); err != nil {
				return nil, err
			}
			if _, _, err = fingerprints(ctx, conn, e.id, generation); err != nil {
				return nil, err
			}
			var base string
			if err = conn.QueryRowContext(ctx, effectiveRootSQL, e.id).Scan(&base); err != nil {
				return nil, err
			}
			uncertain, err := rangeUncertain(ctx, conn, sourceID, generation, base)
			if err != nil {
				return nil, err
			}
			if !uncertain {
				if _, err = conn.ExecContext(ctx, cleanupDeleteFilesSQL, e.id, generation); err != nil {
					return nil, err
				}
			}
		} else {
			var base string
			if err = conn.QueryRowContext(ctx, effectiveRootSQL, e.id).Scan(&base); err != nil {
				return nil, err
			}
			uncertain, err := rangeUncertain(ctx, conn, sourceID, generation, base)
			if err != nil {
				return nil, err
			}
			if uncertain {
				uncertainSkipped++
				continue
			}
		}
		if err = setStatus(ctx, conn, e.id, status); err != nil {
			return nil, err
		}
		updated++
	}
	if len(entries) > 0 {
		if _, err = conn.ExecContext(ctx, `UPDATE source_catalog_scan SET lifecycle_stage=?,lifecycle_cursor=? WHERE file_source_id=?`, stage, entries[len(entries)-1].id, sourceID); err != nil {
			return nil, err
		}
	}
	details["scanned"] = len(entries)
	details["statusUpdates"] = updated
	details["uncertainSkipped"] = uncertainSkipped
	details["cursorFrom"] = cursor
	return details, nil
}

func setStatus(ctx context.Context, conn *sql.Conn, id int64, status string) error {
	if _, err := conn.ExecContext(ctx, lifecycleStatusSQL, status, id); err != nil {
		return err
	}
	availability := "unavailable"
	if status == "available" {
		availability = "available"
	}
	if _, err := conn.ExecContext(ctx, lifecycleMediaSQL, availability, id, availability, id); err != nil {
		return err
	}
	_, err := conn.ExecContext(ctx, lifecyclePresenceSQL, status, id)
	return err
}

func fingerprints(ctx context.Context, conn *sql.Conn, id, generation int64) (string, string, error) {
	rows, err := conn.QueryContext(ctx, lifecycleFingerprintSQL, id, generation)
	if err != nil {
		return "", "", err
	}
	defer rows.Close()
	content, layout := sha256.New(), sha256.New()
	count := 0
	complete := true
	for rows.Next() {
		var p, hash string
		var size int64
		if err := rows.Scan(&p, &size, &hash); err != nil {
			return "", "", err
		}
		count++
		fmt.Fprintf(layout, "%s\x00%d\n", p, size)
		fmt.Fprintf(content, "%s\x00%d\x00%s\n", p, size, hash)
		if hash == "" {
			complete = false
		}
	}
	if err := rows.Err(); err != nil {
		return "", "", err
	}
	if count == 0 || count > 10000 {
		return "", "", nil
	}
	strong := ""
	if complete {
		strong = hex.EncodeToString(content.Sum(nil))
	}
	return strong, hex.EncodeToString(layout.Sum(nil)), nil
}

func rangeUncertain(ctx context.Context, conn *sql.Conn, sourceID, generation int64, base string) (bool, error) {
	if base == "" {
		base = "/"
	}
	observed, err := baseObserved(ctx, conn, sourceID, generation, base)
	if err != nil {
		return false, err
	}
	if !observed {
		return true, nil
	}
	query := rangeUncertainSQL
	args := []any{sourceID, generation}
	if base != "/" {
		query += ` AND (d.path=? OR (d.path>=? AND d.path<?))`
		args = append(args, base, base+"/", base+"0")
	}
	query += `)`
	var uncertain int
	if err = conn.QueryRowContext(ctx, query, args...).Scan(&uncertain); err != nil {
		return false, err
	}
	return uncertain != 0, nil
}

func baseObserved(ctx context.Context, conn *sql.Conn, sourceID, generation int64, base string) (bool, error) {
	for {
		var status, code string
		err := conn.QueryRowContext(ctx, baseObservedSQL, sourceID, base, generation).Scan(&status, &code)
		if errors.Is(err, sql.ErrNoRows) {
			if base == "/" {
				return false, nil
			}
			parent := path.Dir(base)
			if parent == "." || parent == base {
				return false, nil
			}
			base = parent
			continue
		}
		if err != nil {
			return false, err
		}
		if status == "complete" {
			return true, nil
		}
		return status == "empty" && code == "not_found", nil
	}
}

func collectPlans(ctx context.Context, name string, specs []planSpec) []string {
	dbPath := globalPlanDB
	if dbPath == "" {
		return nil
	}
	db, err := openReadOnly(dbPath)
	if err != nil {
		return []string{"open for plan: " + err.Error()}
	}
	defer db.Close()
	out := []string{}
	for i, spec := range specs {
		rows, err := db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+spec.SQL, spec.Args...)
		if err != nil {
			out = append(out, fmt.Sprintf("#%d error: %v", i+1, err))
			continue
		}
		for rows.Next() {
			var id, parent, notUsed int
			var detail string
			if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
				out = append(out, fmt.Sprintf("#%d scan error: %v", i+1, err))
				break
			}
			out = append(out, fmt.Sprintf("#%d %s", i+1, detail))
		}
		rows.Close()
	}
	return out
}

var globalPlanDB string

func vacuumInto(source, target string, force bool) error {
	if force {
		_ = os.Remove(target)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	// VACUUM INTO writes a new file, but query_only(1) rejects VACUUM itself.
	// Open the source with mode=ro only; the source is still not writable.
	dsn := "file:" + filepath.ToSlash(source) + "?mode=ro&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec("VACUUM INTO ?", target)
	return err
}

func openReadOnly(dbPath string) (*sql.DB, error) {
	// Match the product connection: 16 MiB page cache per connection and a
	// 256 MiB mmap window. Without these the harness measures SQLite defaults,
	// not the production driver configuration.
	dsn := "file:" + filepath.ToSlash(dbPath) +
		"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(30000)&_pragma=cache_size(-16000)&_pragma=mmap_size(268435456)"
	return sql.Open("sqlite", dsn)
}

func openWritable(dbPath string) (*sql.DB, error) {
	dsn := "file:" + filepath.ToSlash(dbPath) +
		"?_pragma=busy_timeout(30000)&_pragma=cache_size(-16000)&_pragma=mmap_size(268435456)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func readSchemaVersion(ctx context.Context, db *sql.DB) int {
	var version int
	_ = db.QueryRowContext(ctx, `SELECT current_version FROM schema_state LIMIT 1`).Scan(&version)
	if version == 0 {
		_ = db.QueryRowContext(ctx, `SELECT COALESCE(max(version),0) FROM schema_migration`).Scan(&version)
	}
	return version
}

func readCounts(ctx context.Context, db *sql.DB) map[string]int {
	counts := map[string]int{}
	queries := map[string]string{
		"sourceCatalogEntry":       `SELECT count(*) FROM source_catalog_entry`,
		"sourceCatalogFile":        `SELECT count(*) FROM source_catalog_file`,
		"sourceCatalogObservation": `SELECT count(*) FROM source_catalog_observation`,
		"sourceCatalogDirectory":   `SELECT count(*) FROM source_catalog_directory`,
		"publishPending":           `SELECT count(*) FROM source_catalog_observation WHERE publish_pending=1`,
	}
	for name, q := range queries {
		var n int
		if err := db.QueryRowContext(ctx, q).Scan(&n); err == nil {
			counts[name] = n
		}
	}
	return counts
}

func resolveSource(ctx context.Context, db *sql.DB, sourceID, generation int64, root string) (int64, int64, int, error) {
	if sourceID == 0 || generation == 0 {
		var sid, gen int64
		var n int
		err := db.QueryRowContext(ctx, `SELECT file_source_id,generation,count(*) FROM source_catalog_directory
			WHERE path=? OR path LIKE ?||'/%' GROUP BY file_source_id,generation ORDER BY count(*) DESC LIMIT 1`, root, root).Scan(&sid, &gen, &n)
		if err != nil {
			return 0, 0, 0, err
		}
		if sourceID == 0 {
			sourceID = sid
		}
		if generation == 0 {
			generation = gen
		}
	}
	var dirs int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM source_catalog_directory WHERE file_source_id=? AND generation=?`, sourceID, generation).Scan(&dirs); err != nil {
		return 0, 0, 0, err
	}
	return sourceID, generation, dirs, nil
}

var slowKindPattern = regexp.MustCompile(`kind=([a-zA-Z0-9_]+)`)
var slowElapsedPattern = regexp.MustCompile(`elapsed_ms=([0-9]+)`)

func parseLogs(logPath string) (map[string]any, error) {
	data, err := os.ReadFile(logPath)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(data), "\n")
	nonEmpty := 0
	slow, busy := 0, 0
	kinds := map[string]int{}
	elapsed := map[string][]int{}
	examples := []string{}
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			nonEmpty++
		}
		if strings.Contains(line, "SQLITE_BUSY") {
			busy++
			if len(examples) < 5 {
				examples = append(examples, strings.TrimSpace(line))
			}
		}
		if strings.Contains(strings.ToLower(line), "slow") {
			slow++
			kind := "unknown"
			if m := slowKindPattern.FindStringSubmatch(line); len(m) == 2 {
				kind = m[1]
			}
			kinds[kind]++
			if m := slowElapsedPattern.FindStringSubmatch(line); len(m) == 2 {
				if v, err := strconv.Atoi(m[1]); err == nil {
					elapsed[kind] = append(elapsed[kind], v)
				}
			}
		}
	}
	elapsedStats := map[string]any{}
	for kind, values := range elapsed {
		sort.Ints(values)
		elapsedStats[kind] = map[string]any{
			"count": len(values),
			"min":   values[0],
			"p50":   values[len(values)/2],
			"max":   values[len(values)-1],
		}
	}
	return map[string]any{
		"file":         absPath(logPath),
		"lines":        nonEmpty,
		"slow":         slow,
		"sqliteBusy":   busy,
		"slowKinds":    kinds,
		"slowElapsed":  elapsedStats,
		"busyExamples": examples,
	}, nil
}

func stats(values []float64) (min, max, median float64) {
	if len(values) == 0 {
		return 0, 0, 0
	}
	min, max = values[0], values[0]
	for _, v := range values {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	n := len(sorted)
	if n%2 == 0 {
		return min, max, (sorted[n/2-1] + sorted[n/2]) / 2
	}
	return min, max, sorted[n/2]
}

func msFloat(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000.0
}

func samePath(a, b string) bool {
	aa, errA := filepath.Abs(a)
	bb, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return a == b
	}
	return filepath.Clean(aa) == filepath.Clean(bb)
}

func absPath(p string) string {
	if p == "" {
		return ""
	}
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
