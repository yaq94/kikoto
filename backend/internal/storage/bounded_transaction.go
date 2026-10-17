package storage

import (
	"context"
	"database/sql"

	"github.com/yexca/kikoto/backend/internal/sqlutil"
)

// BeginBoundedTx begins a write transaction whose lock wait never parks a
// pooled connection, so one waiting writer cannot starve readers or the writer
// that currently holds SQLite's write lock. The caller must call release after
// committing or abandoning the transaction.
func BeginBoundedTx(ctx context.Context, db *sql.DB) (*sqlutil.BoundedTx, func(), error) {
	return sqlutil.BeginBoundedTx(ctx, db)
}
