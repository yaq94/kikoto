package sqlutil

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	// lockAttemptBudgetMillis is how long one lock attempt may wait inside the
	// driver's busy handler before the caller releases the connection and
	// retries.
	lockAttemptBudgetMillis = 100
	// lockAttemptRetryDelay is the pause between two lock attempts. No pooled
	// connection is held during the pause.
	lockAttemptRetryDelay = 10 * time.Millisecond
	// DefaultWriteBudget bounds a write whose context carries no deadline.
	// A contended writer gives up at this point instead of waiting for the
	// driver's configured busy timeout, which can outlive the caller's intent.
	DefaultWriteBudget = 5 * time.Second
)

// BoundedTx is a transaction whose leased connection returns to the pool as
// soon as the transaction ends. database/sql releases a leased connection only
// when the *sql.Conn closes, so a committed transaction would otherwise keep a
// pool slot until the caller's deferred release ran, and a handler that reads
// the database after committing would wait for that slot.
type BoundedTx struct {
	*sql.Tx
	release func()
	once    sync.Once
}

// Commit commits the transaction and returns its connection to the pool.
func (t *BoundedTx) Commit() error {
	err := t.Tx.Commit()
	t.once.Do(t.release)
	return err
}

// Rollback abandons the transaction and returns its connection to the pool.
func (t *BoundedTx) Rollback() error {
	err := t.Tx.Rollback()
	t.once.Do(t.release)
	return err
}

// BeginBoundedTx begins a write transaction without parking a pooled
// connection for the driver's full busy timeout.
//
// SQLite's busy handler is a synchronous wait inside the driver: a writer
// waiting for the single write lock holds its connection the whole time, and
// with a small pool a few parked writers leave no connection for reads or for
// the worker that will release the lock. Each attempt here shortens the busy
// timeout, and a failed attempt restores the connection's wait policy and
// returns it to the pool before the caller retries. Once the write lock is
// held, the attempt restores the configured busy timeout through the leased
// connection, so statements inside the transaction keep the normal policy.
//
// The wait stops at ctx, or at DefaultWriteBudget when ctx carries no deadline.
// The returned transaction returns its connection when it commits or rolls
// back. Callers may still defer release for the abandoned-transaction path.
func BeginBoundedTx(ctx context.Context, db *sql.DB) (*BoundedTx, func(), error) {
	waitCtx, cancelWait := writeWaitContext(ctx)
	defer cancelWait()
	for {
		if err := waitCtx.Err(); err != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			return nil, nil, err
		}
		tx, release, err := beginLockAttempt(ctx, db)
		if err == nil {
			return &BoundedTx{Tx: tx, release: release}, release, nil
		}
		if !IsLocked(err) {
			return nil, nil, err
		}
		timer := time.NewTimer(lockAttemptRetryDelay)
		select {
		case <-waitCtx.Done():
			timer.Stop()
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			return nil, nil, err
		case <-timer.C:
		}
	}
}

// ExecBounded runs one statement inside a bounded write transaction. A
// contended write releases its connection while it waits instead of holding it
// for the driver's full busy timeout.
func ExecBounded(ctx context.Context, db *sql.DB, query string, args ...any) (sql.Result, error) {
	tx, release, err := BeginBoundedTx(ctx, db)
	if err != nil {
		return nil, err
	}
	defer release()
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return result, tx.Commit()
}

// writeWaitContext bounds the lock wait to the caller's own deadline when that
// deadline is the nearer one, and otherwise to DefaultWriteBudget.
func writeWaitContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= DefaultWriteBudget {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, DefaultWriteBudget)
}

// beginLockAttempt leases one connection, shortens its busy timeout, and tries
// to take the write lock. A failed attempt restores the wait policy and returns
// the connection to the pool before the caller retries.
func beginLockAttempt(ctx context.Context, db *sql.DB) (*sql.Tx, func(), error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, nil, err
	}
	var configured int
	if err := conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&configured); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	restore := func() {
		restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		if _, err := conn.ExecContext(restoreCtx, fmt.Sprintf("PRAGMA busy_timeout=%d", configured)); err != nil {
			// Never return a connection with a modified wait policy to the pool.
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
		_ = conn.Close()
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout=%d", min(configured, lockAttemptBudgetMillis))); err != nil {
		restore()
		return nil, nil, err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		restore()
		return nil, nil, err
	}
	// The write lock is held from here on, so the transaction's own statements
	// run under the pool's configured wait policy again.
	if _, err := conn.ExecContext(context.WithoutCancel(ctx), fmt.Sprintf("PRAGMA busy_timeout=%d", configured)); err != nil {
		_ = tx.Rollback()
		restore()
		return nil, nil, err
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			_ = tx.Rollback()
			_ = conn.Close()
		})
	}
	return tx, release, nil
}

// IsLocked reports whether err is SQLite refusing to take a lock, which
// SQLITE_BUSY (5) and SQLITE_LOCKED (6) both express. The extended result code
// is masked off so every variant of those two states matches.
func IsLocked(err error) bool {
	var sqliteErr interface{ Code() int }
	if !errors.As(err, &sqliteErr) {
		return false
	}
	return sqliteErr.Code()&255 == 5 || sqliteErr.Code()&255 == 6
}

// IsBusy reports whether err is SQLite reporting a locked or busy database,
// including drivers that describe the state in the message only.
func IsBusy(err error) bool {
	if err == nil {
		return false
	}
	if IsLocked(err) {
		return true
	}
	for current := err; current != nil; current = errors.Unwrap(current) {
		message := strings.ToLower(current.Error())
		if strings.Contains(message, "database is locked") ||
			strings.Contains(message, "database table is locked") ||
			strings.Contains(message, "database is busy") ||
			strings.Contains(message, "sqlite_busy") {
			return true
		}
	}
	return false
}
