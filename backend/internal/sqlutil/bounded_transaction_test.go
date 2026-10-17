package sqlutil

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// The pool's configured wait policy must survive both a committed transaction
// and an abandoned one, on every connection the pool hands out.
func TestBoundedTransactionRestoresPoolWaitPolicy(t *testing.T) {
	db := openProbeDB(t)
	ctx := context.Background()

	for index, commit := range []bool{true, false} {
		tx, release, err := BeginBoundedTx(ctx, db)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO probe(id, value) VALUES (?, 'written')", index+1); err != nil {
			t.Fatal(err)
		}
		if commit {
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
		}
		release()
		release()
	}

	for range 4 {
		conn, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var timeout int
		if err := conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&timeout); err != nil {
			t.Fatal(err)
		}
		_ = conn.Close()
		if timeout != probeBusyTimeoutMillis {
			t.Fatalf("pooled busy_timeout = %d, want %d", timeout, probeBusyTimeoutMillis)
		}
	}
	var rows int
	if err := db.QueryRow("SELECT COUNT(*) FROM probe").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("committed rows = %d, want 1", rows)
	}
}

// A writer waiting for the write lock must not hold a pooled connection, so a
// reader still gets one and the writer resumes as soon as the lock is free.
func TestBoundedTransactionDoesNotStarveThePoolWhileWaiting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "waiting.db")
	holder := openProbeDB(t, path)
	defer func() { _ = holder.Close() }()
	if _, err := holder.Exec("INSERT INTO probe(id, value) VALUES (1, 'committed')"); err != nil {
		t.Fatal(err)
	}
	held, err := holder.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := held.Exec("UPDATE probe SET value = 'held' WHERE id = 1"); err != nil {
		t.Fatal(err)
	}

	waiter := openProbeDB(t, path)
	defer func() { _ = waiter.Close() }()

	writeDone := make(chan error, 1)
	go func() {
		_, err := ExecBounded(context.Background(), waiter, "UPDATE probe SET value = 'waited' WHERE id = 1")
		writeDone <- err
	}()
	time.Sleep(300 * time.Millisecond)

	start := time.Now()
	var value string
	if err := waiter.QueryRow("SELECT value FROM probe WHERE id = 1").Scan(&value); err != nil {
		t.Fatalf("read during the lock wait: %v", err)
	}
	if value != "committed" {
		t.Fatalf("read value = %q, want the committed value", value)
	}
	readWait := time.Since(start)
	if readWait > time.Second {
		t.Fatalf("a waiting writer starved the pool for %v", readWait)
	}

	if err := held.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatalf("bounded writer failed after release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bounded writer did not resume after the lock was released")
	}
	var waited int
	if err := waiter.QueryRow("SELECT COUNT(*) FROM probe WHERE id = 1 AND value = 'waited'").Scan(&waited); err != nil {
		t.Fatal(err)
	}
	if waited != 1 {
		t.Fatalf("waited rows = %d, want 1", waited)
	}
}

// A bound that expires while the lock is still held must return an error and
// leave the database writable, rather than parking until the driver gives up.
func TestBoundedTransactionGivesUpWithinTheBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budget.db")
	holder := openProbeDB(t, path)
	defer func() { _ = holder.Close() }()
	held, err := holder.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := held.Exec("INSERT INTO probe(id, value) VALUES (1, 'held')"); err != nil {
		t.Fatal(err)
	}

	waiter := openProbeDB(t, path)
	defer func() { _ = waiter.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err = BeginBoundedTx(ctx, waiter)
	elapsed := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context deadline exceeded", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("gave up after %v, want the caller's deadline", elapsed)
	}

	if err := held.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := waiter.Exec("INSERT INTO probe(id, value) VALUES (2, 'after')"); err != nil {
		t.Fatalf("database not writable after the budget expired: %v", err)
	}
}

// IsLocked recognises the driver's contention code, and IsBusy also matches the
// message-only form so callers keep treating a busy database as retryable.
func TestLockDetection(t *testing.T) {
	busy := sqliteBusyError(5)
	if !IsLocked(busy) || !IsBusy(busy) {
		t.Fatalf("SQLITE_BUSY not detected: %v", busy)
	}
	if !IsLocked(sqliteBusyError(6)) {
		t.Fatal("SQLITE_LOCKED not detected")
	}
	if !IsLocked(sqliteBusyError(5 | 1<<8)) {
		t.Fatal("extended SQLITE_BUSY code not detected")
	}
	if IsLocked(errors.New("database is locked")) {
		t.Fatal("message-only error reported as a driver result code")
	}
	if !IsBusy(errors.New("database is locked")) || !IsBusy(errors.New("SQLITE_BUSY")) {
		t.Fatal("message-only busy conditions not detected")
	}
	if IsBusy(errors.New("no such table")) || IsBusy(nil) {
		t.Fatal("unrelated error reported as busy")
	}
}

// A read-only transaction must not take SQLite's write lock: the listing
// endpoints only read, and a deferred read transaction stays available while a
// writer holds the write lock.
func TestReadOnlyTransactionDoesNotTakeTheWriteLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "readonly.db")
	holder := openProbeDB(t, path)
	defer func() { _ = holder.Close() }()
	writer := openProbeDB(t, path)
	defer func() { _ = writer.Close() }()
	if _, err := holder.Exec("INSERT INTO probe(id, value) VALUES (1, 'committed')"); err != nil {
		t.Fatal(err)
	}

	held, err := writer.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := held.Exec("UPDATE probe SET value = 'held' WHERE id = 1"); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	read, err := holder.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("read-only transaction waited for the write lock: %v", err)
	}
	var value string
	if err := read.QueryRowContext(ctx, "SELECT value FROM probe WHERE id = 1").Scan(&value); err != nil {
		t.Fatalf("read inside a deferred transaction: %v", err)
	}
	if value != "committed" {
		t.Fatalf("read value = %q, want the committed value", value)
	}
	if err := read.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := held.Commit(); err != nil {
		t.Fatal(err)
	}
}

const probeBusyTimeoutMillis = 30000

type sqliteBusyError int

func (e sqliteBusyError) Error() string { return "database is locked (synthetic)" }
func (e sqliteBusyError) Code() int     { return int(e) }

// openProbeDB opens the production DSN shape, optionally reusing a path so a
// second handle competes for the same write lock.
func openProbeDB(t *testing.T, path ...string) *sql.DB {
	t.Helper()
	if len(path) == 0 {
		path = []string{filepath.Join(t.TempDir(), "probe.db")}
	}
	dsn := "file:" + filepath.ToSlash(path[0]) +
		"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(" + itoa(probeBusyTimeoutMillis) + ")" +
		"&_pragma=synchronous(NORMAL)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec("CREATE TABLE IF NOT EXISTS probe(id INTEGER PRIMARY KEY, value TEXT)"); err != nil {
		t.Fatal(err)
	}
	return db
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := []byte{}
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}
