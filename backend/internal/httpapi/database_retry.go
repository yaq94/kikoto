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
func beginTxWithDatabaseBusyRetry(ctx context.Context, db *sql.DB) (*catalogWriteTx, func(), error) {
	if guard, ok := ctx.Value(catalogPublicationResourceGuardKey{}).(func() error); ok && guard != nil {
		if err := guard(); err != nil {
			return nil, nil, err
		}
	}
	tx, release, err := sqlutil.BeginBoundedTx(ctx, db)
	if err != nil {
		return nil, nil, err
	}
	started := time.Now()
	id, _ := ctx.Value(catalogPublicationKey{}).(int64)
	if id > 0 {
		if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_publication_writer SET operation_id=? WHERE id=1`, id); err != nil {
			release()
			return nil, nil, err
		}
	}
	if check, ok := ctx.Value(catalogExclusionLeaseKey{}).(func(*sql.Tx) error); ok && check != nil {
		if err = check(tx.Tx); err != nil {
			release()
			return nil, nil, err
		}
	}
	observe, _ := ctx.Value(catalogWriteTimingKey{}).(func(time.Duration))
	wrapped := &catalogWriteTx{BoundedTx: tx, publication: id > 0, ctx: ctx, started: started, observe: observe}
	return wrapped, func() { release(); wrapped.recordDuration() }, nil
}
