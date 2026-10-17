package httpapi

import (
	"context"
	"database/sql"
	"time"

	"github.com/yexca/kikoto/backend/internal/sqlutil"
)

const databaseBusyRetryAttempts = 4

func withDatabaseBusyRetry(ctx context.Context, operation func() error) error {
	var err error
	for attempt := 0; attempt < databaseBusyRetryAttempts; attempt++ {
		err = operation()
		if err == nil || !sqlutil.IsBusy(err) || attempt == databaseBusyRetryAttempts-1 {
			return err
		}
		delay := time.Duration(25*(1<<attempt)) * time.Millisecond
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return err
}

// beginTxWithDatabaseBusyRetry begins a write transaction whose lock wait holds
// no pooled connection, so a writer waiting for SQLite's single write lock
// cannot starve readers or the worker that will release the lock. release must
// be called after committing or abandoning the transaction.
func beginTxWithDatabaseBusyRetry(ctx context.Context, db *sql.DB) (*sqlutil.BoundedTx, func(), error) {
	return sqlutil.BeginBoundedTx(ctx, db)
}
