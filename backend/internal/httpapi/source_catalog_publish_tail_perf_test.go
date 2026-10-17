package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/yexca/kikoto/backend/internal/config"
	"github.com/yexca/kikoto/backend/internal/storage"
)

// TestSourceCatalogPublishTailPerformance measures the publisher against a real
// indexed database copy. The default fixture keeps the shape that produced the
// 36-second tail on the deployment host: thousands of complete directories and
// a handful of unpublished observations. It is opt-in and prints its figures;
// TestSourceCatalogPublishTailStaysBounded owns the small regression budget.
func TestSourceCatalogPublishTailPerformance(t *testing.T) {
	if os.Getenv("KIKOTO_SOURCE_INDEX_TAIL_PERF") != "1" {
		t.Skip("set KIKOTO_SOURCE_INDEX_TAIL_PERF=1 to measure the publisher tail")
	}
	sourcePath := os.Getenv("KIKOTO_SOURCE_INDEX_TAIL_DB")
	rows := envInt("KIKOTO_SOURCE_INDEX_TAIL_ROWS", 5)
	legacy := os.Getenv("KIKOTO_SOURCE_INDEX_TAIL_QUERY") == "legacy"
	fixtureDirectories, fixtureEntries := 3000, 13
	var db *sql.DB
	var sourceID, runID int64
	if sourcePath == "" {
		db = openMigratedTestDBWithProductionPool(t)
		sourceID, runID = 11, 1
		seedTailFixture(t, db, sourceID, runID, fixtureDirectories, fixtureEntries)
	} else {
		copyPath := filepath.Join(t.TempDir(), "catalog.db")
		copySQLite(t, sourcePath, copyPath)
		var err error
		db, err = storage.Open(copyPath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		if err = db.QueryRow(`SELECT file_source_id FROM source_catalog_scan ORDER BY file_source_id LIMIT 1`).Scan(&sourceID); err != nil {
			t.Fatalf("locate indexed source: %v", err)
		}
		if err = db.QueryRow(`SELECT workflow_run_id FROM source_catalog_scan WHERE file_source_id=?`, sourceID).Scan(&runID); err != nil {
			t.Fatal(err)
		}
	}
	job := runningCatalogJob(t, db, sourceID, runID)
	prepareTail(t, db, sourceID, rows)
	server := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	poolBefore := db.Stats()
	var published int
	var elapsed time.Duration
	var query string
	if legacy {
		query = `SELECT d.path,d.catalog_root,o.name,o.kind,o.size_bytes,o.content_hash
			FROM source_catalog_directory d JOIN source_catalog_observation o
			  ON o.file_source_id=d.file_source_id AND o.directory_path=d.path AND o.revision=d.revision
			WHERE d.file_source_id=? AND d.generation=? AND d.status='complete' AND d.catalog_root<>'' AND o.published_generation<>? AND EXISTS(
			  SELECT 1 FROM source_catalog_directory a JOIN source_catalog_observation b ON b.file_source_id=a.file_source_id AND b.directory_path=a.path AND b.revision=a.revision
			  WHERE a.file_source_id=d.file_source_id AND a.generation=d.generation AND a.catalog_root=d.catalog_root AND a.status='complete' AND b.kind='audio') LIMIT 100`
		started := time.Now()
		rows, err := db.Query(query, sourceID, runID, runID)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			published++
		}
		if err = rows.Close(); err != nil {
			t.Fatal(err)
		}
		elapsed = time.Since(started)
	} else {
		started := time.Now()
		var err error
		published, err = server.publishCatalogBatch(context.Background(), job, sourceID)
		if err != nil {
			t.Fatal(err)
		}
		elapsed = time.Since(started)
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	poolAfter := db.Stats()
	report := map[string]any{
		"fixture": map[string]any{"realDatabase": sourcePath != "", "directories": fixtureDirectories, "entriesPerDirectory": fixtureEntries, "tailRows": rows, "legacyQuery": legacy},
		"result":  map[string]any{"published": published, "elapsedMs": elapsed.Seconds() * 1000},
		"cpu":     map[string]any{"gomaxprocs": runtime.GOMAXPROCS(0), "logical": runtime.NumCPU(), "allocatedBytes": after.TotalAlloc - before.TotalAlloc},
		"pool":    map[string]any{"waitCount": poolAfter.WaitCount - poolBefore.WaitCount, "waitDurationMs": (poolAfter.WaitDuration - poolBefore.WaitDuration).Seconds() * 1000, "inUse": poolAfter.InUse},
		"query":   query,
	}
	if legacy {
		report["queryPlan"] = queryPlan(t, db, query, sourceID, runID, runID)
	} else {
		report["queryPlan"] = queryPlan(t, db, `WITH roots AS MATERIALIZED (
			SELECT catalog_root FROM source_catalog_audio_root WHERE file_source_id=? AND generation=?
		)
		SELECT d.path,d.catalog_root,o.name,o.kind,o.size_bytes,o.content_hash
		FROM source_catalog_observation o
		JOIN source_catalog_directory d ON d.file_source_id=o.file_source_id AND d.path=o.directory_path AND d.revision=o.revision
		JOIN roots r ON r.catalog_root=d.catalog_root
		WHERE o.file_source_id=? AND o.publish_pending=1 AND d.generation=? AND d.status='complete' AND d.catalog_root<>''
		ORDER BY o.directory_path,o.name LIMIT 100`, sourceID, runID, sourceID, runID)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("source-index-tail %s\n", encoded)
}

func seedTailFixture(t *testing.T, db *sql.DB, sourceID, runID int64, directories, entries int) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO file_source(id,code,display_name,source_type,enabled,config_json) VALUES(?,'tail-fixture','Tail fixture','openlist',1,'{}')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_run(id,workflow_code,display_name,status,trigger_type,input_json) VALUES(?,'source_catalog_index','Index source catalog','running','manual','{}')`, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_job(workflow_run_id,worker_type,status,payload_json,locked_by) VALUES(?,?,'running','{}','tail-perf')`, runID, sourceCatalogIndexWorkerType); err != nil {
		t.Fatal(err)
	}
	seedLargeCatalog(t, db, sourceID, runID, directories, entries)
}

func prepareTail(t *testing.T, db *sql.DB, sourceID int64, rows int) {
	t.Helper()
	keys := [][2]string{}
	result, err := db.Query(`SELECT directory_path,name FROM source_catalog_observation WHERE file_source_id=? ORDER BY directory_path DESC,name DESC LIMIT ?`, sourceID, rows)
	if err != nil {
		t.Fatal(err)
	}
	for result.Next() {
		var directory, name string
		if err = result.Scan(&directory, &name); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, [2]string{directory, name})
	}
	if err = result.Close(); err != nil {
		t.Fatal(err)
	}
	if len(keys) < rows {
		t.Fatalf("fixture has %d rows, want a %d-row tail", len(keys), rows)
	}
	if _, err = db.Exec(`UPDATE source_catalog_observation SET published_generation=1,publish_pending=0 WHERE file_source_id=?`, sourceID); err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		if _, err = db.Exec(`UPDATE source_catalog_observation SET published_generation=0,publish_pending=1 WHERE file_source_id=? AND directory_path=? AND name=?`, sourceID, key[0], key[1]); err != nil {
			t.Fatal(err)
		}
	}
}

func copySQLite(t *testing.T, sourcePath, destination string) {
	t.Helper()
	source, err := sql.Open("sqlite", "file:"+sourcePath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	// VACUUM INTO reads a consistent snapshot and writes a compact standalone
	// database without modifying the source.
	if _, err = source.Exec(`VACUUM INTO ?`, destination); err != nil {
		t.Fatal(err)
	}
}

func queryPlan(t *testing.T, db *sql.DB, query string, args ...any) []string {
	t.Helper()
	rows, err := db.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	plan := []string{}
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err = rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, strconv.Itoa(id)+":"+detail)
	}
	return plan
}
