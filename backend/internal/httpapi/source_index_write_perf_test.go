package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yexca/kikoto/backend/internal/sqlutil"
)

// This opt-in experiment reproduces the write-path contention a running catalog
// index produces on a real database, and compares the two lock-wait shapes:
//
//   - raw: every writer calls database/sql directly, so a writer waiting for
//     SQLite's single write lock keeps its pooled connection for the driver's
//     full busy timeout (30s in production).
//   - bounded: writers take the lock through sqlutil.BeginBoundedTx, which
//     releases the connection between short attempts.
//
// A long transaction stands in for the index's publish or settle batch. With a
// four-connection pool, the raw shape lets four parked writers consume the
// whole pool, so a user-facing read cannot get a connection at all. The
// experiment reports how long that read waited, how many writers completed, and
// how long the writers took to drain once the lock was free.
//
// It is a measurement, not a latency assertion: it prints its numbers for the
// ticket evidence and fails only when a mode cannot make progress at all.
func TestSourceIndexWriteContention(t *testing.T) {
	if os.Getenv("KIKOTO_SOURCE_INDEX_PERF") != "1" {
		t.Skip("set KIKOTO_SOURCE_INDEX_PERF=1 to measure index write contention")
	}
	mode := os.Getenv("KIKOTO_SOURCE_INDEX_PERF_MODE")
	if mode == "" {
		mode = "bounded"
	}
	holdFor := time.Duration(envInt("KIKOTO_SOURCE_INDEX_PERF_HOLD_MS", 3000)) * time.Millisecond
	writerCount := envInt("KIKOTO_SOURCE_INDEX_PERF_WRITERS", 4)

	db := openMigratedTestDBWithProductionPool(t)
	seedIndexWriteFixture(t, db)
	holder, err := sql.Open("sqlite", dsnFor(db))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Close() }()
	holder.SetMaxOpenConns(1)

	// One long transaction holds the write lock, standing in for an index batch.
	held, err := holder.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := held.Exec("UPDATE perf_work SET title = 'held' WHERE id = 1"); err != nil {
		t.Fatal(err)
	}

	var completed, failed, starved atomic.Int64
	var writers sync.WaitGroup
	writeStarted := time.Now()
	for worker := 0; worker < writerCount; worker++ {
		writers.Go(func() {
			// Each writer waits for the lock, then works until it has drained its
			// queue. A writer that gives up inside the bounded budget retries, so
			// both modes eventually finish and the comparison is throughput, not
			// an artificial failure count.
			remaining := 20
			for remaining > 0 {
				var writeErr error
				if mode == "bounded" {
					_, writeErr = sqlutil.ExecBounded(context.Background(), db,
						"UPDATE perf_work SET title = 'written' WHERE id = 1")
				} else {
					_, writeErr = db.Exec("UPDATE perf_work SET title = 'written' WHERE id = 1")
				}
				if writeErr == nil {
					remaining--
					completed.Add(1)
					continue
				}
				if !sqlutil.IsBusy(writeErr) {
					t.Errorf("unexpected write error: %v", writeErr)
					return
				}
				failed.Add(1)
			}
		})
	}

	// A user-facing read must get a connection while the writers wait.
	time.Sleep(500 * time.Millisecond)
	readStarted := time.Now()
	var title string
	readErr := db.QueryRow("SELECT title FROM perf_work WHERE id = 1").Scan(&title)
	readWait := time.Since(readStarted)
	if readWait > holdFor {
		starved.Add(1)
	}

	if err := held.Commit(); err != nil {
		t.Fatal(err)
	}
	drained := make(chan struct{})
	go func() { writers.Wait(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(2 * time.Minute):
		t.Fatalf("writers did not drain: completed=%d failed=%d", completed.Load(), failed.Load())
	}
	total := time.Since(writeStarted)

	report := map[string]any{
		"mode":                  mode,
		"lock_hold_ms":          holdFor.Milliseconds(),
		"writers":               writerCount,
		"reader_wait_ms":        readWait.Milliseconds(),
		"reader_starved":        starved.Load() > 0,
		"reader_error":          errorString(readErr),
		"writes_completed":      completed.Load(),
		"write_attempts_busy":   failed.Load(),
		"drain_after_release_s": total.Seconds(),
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("KIKOTO_WRITE_PERF %s\n", encoded)
	if readErr != nil {
		t.Fatalf("read failed: %v", readErr)
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// seedIndexWriteFixture creates the small table the writers contend on and the
// catalog rows a real scan would have produced.
func seedIndexWriteFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	exec := func(query string, args ...any) {
		if _, err := tx.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`CREATE TABLE perf_work(id INTEGER PRIMARY KEY, title TEXT NOT NULL)`)
	for index := 0; index < 20; index++ {
		exec(`INSERT INTO perf_work(id, title) VALUES(?, ?)`, index+1, fmt.Sprintf("Perf Work %02d", index))
	}
	exec(`INSERT OR IGNORE INTO file_source(id,code,display_name,source_type) VALUES(1,'perf_source','Perf Source','openlist')`)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// dsnFor reopens the same database file the pooled test database uses, so a
// separate handle can hold the write lock for a controlled period.
func dsnFor(db *sql.DB) string {
	var path string
	_ = db.QueryRow("PRAGMA database_list").Scan(new(int), new(string), &path)
	return "file:" + path + "?_pragma=busy_timeout(30000)&_txlock=immediate"
}

func envInt(name string, fallback int) int {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}
