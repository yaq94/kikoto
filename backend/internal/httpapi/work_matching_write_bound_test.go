package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/yexca/kikoto/backend/internal/sqlutil"
	"github.com/yexca/kikoto/backend/internal/storage"
)

// TestWorkMatchingWriteBoundDoesNotHoldConnectionWhileLocked covers the worker's
// status/queue writes after matchWork fails. Another handle holds SQLite's write
// lock while the bounded write retries; the waiter keeps a single pooled
// connection, so the concurrent read can only proceed if the writer returns its
// connection between short lock attempts. A raw ExecContext would park that
// connection for the driver's 30s busy timeout.
func TestWorkMatchingWriteBoundDoesNotHoldConnectionWhileLocked(t *testing.T) {
	pool := openMigratedTestDBWithProductionPool(t)
	path := workMatchingDatabasePath(t, pool)

	holder := openWorkMatchingWriteDB(t, path, 1)
	held, err := holder.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin lock holder: %v", err)
	}
	defer func() { _ = held.Rollback() }()
	if _, err := held.Exec(`UPDATE work_matching_control SET status='held' WHERE id=1`); err != nil {
		t.Fatalf("hold write lock: %v", err)
	}

	waiter := openWorkMatchingWriteDB(t, path, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	writeDone := make(chan error, 1)
	start := time.Now()
	go func() {
		_, err := sqlutil.ExecBounded(ctx, waiter,
			`UPDATE work_matching_control SET status='retrying',attempts=?,retry_after=?,error_code='matching_failed' WHERE revision=?`,
			1, 0, 1)
		writeDone <- err
	}()

	// Let the writer lease the only pooled connection and enter its first short
	// lock attempt before the read asks the same pool for a connection.
	time.Sleep(20 * time.Millisecond)
	readCtx, cancelRead := context.WithTimeout(context.Background(), time.Second)
	defer cancelRead()
	readStarted := time.Now()
	var one int
	readErr := waiter.QueryRowContext(readCtx, `SELECT 1`).Scan(&one)
	readWait := time.Since(readStarted)
	if readErr != nil {
		t.Fatalf("read on the writer's pool while the lock was held: %v", readErr)
	}

	var writeErr error
	select {
	case writeErr = <-writeDone:
	case <-time.After(3 * time.Second):
		t.Fatal("bounded write did not return while another connection held the lock")
	}
	elapsed := time.Since(start)
	if writeErr == nil {
		t.Fatal("bounded write succeeded while another connection held the write lock")
	}
	if !sqlutil.IsBusy(writeErr) && !errors.Is(writeErr, context.DeadlineExceeded) {
		t.Fatalf("bounded write error = %v, want a lock or deadline error", writeErr)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("bounded write held the pool for %v", elapsed)
	}
	if readWait > time.Second {
		t.Fatalf("read waited %v for a pool connection", readWait)
	}
	t.Logf("bounded write returned after %v (%v); concurrent read waited %v", elapsed, writeErr, readWait)

	if err := held.Rollback(); err != nil {
		t.Fatalf("release write lock: %v", err)
	}

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelWrite()
	if _, err := sqlutil.ExecBounded(writeCtx, waiter,
		`UPDATE work_matching_control SET status='queued',attempts=0 WHERE id=1`); err != nil {
		t.Fatalf("write after the lock was released: %v", err)
	}
	var status string
	if err := waiter.QueryRow(`SELECT status FROM work_matching_control WHERE id=1`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "queued" {
		t.Fatalf("status = %q, want queued", status)
	}
}

func workMatchingDatabasePath(t *testing.T, db *sql.DB) string {
	t.Helper()
	var path string
	if err := db.QueryRow("PRAGMA database_list").Scan(new(int), new(string), &path); err != nil {
		t.Fatalf("read database path: %v", err)
	}
	if path == "" {
		t.Fatal("test database has no file path")
	}
	return path
}

func openWorkMatchingWriteDB(t *testing.T, path string, maxConns int) *sql.DB {
	t.Helper()
	db, err := storage.Open(path)
	if err != nil {
		t.Fatalf("open second handle on the test database: %v", err)
	}
	db.SetMaxOpenConns(maxConns)
	db.SetMaxIdleConns(maxConns)
	t.Cleanup(func() { _ = db.Close() })
	return db
}
