package httpapi

import (
	"context"
	"database/sql"
)

// A matching evaluation loads the same publication projections for every
// candidate and peer. Prepare these complex queries once for that evaluation;
// each execution still reads the transaction's current evidence and limits.
type workMatchObjectQueries struct {
	*sql.Tx
	statements map[string]*sql.Stmt
}

func prepareWorkMatchObjectQueries(ctx context.Context, tx *sql.Tx) (*workMatchObjectQueries, error) {
	q := &workMatchObjectQueries{Tx: tx, statements: make(map[string]*sql.Stmt, 3)}
	for _, query := range []string{associationWorkLevelSQL, associationCatalogLocationsSQL, associationSourceLocationsSQL} {
		statement, err := tx.PrepareContext(ctx, query)
		if err != nil {
			q.Close()
			return nil, err
		}
		q.statements[query] = statement
	}
	return q, nil
}

func (q *workMatchObjectQueries) Close() {
	for _, statement := range q.statements {
		_ = statement.Close()
	}
}

func (q *workMatchObjectQueries) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if statement, ok := q.statements[query]; ok {
		return statement.QueryRowContext(ctx, args...)
	}
	return q.Tx.QueryRowContext(ctx, query, args...)
}

func (q *workMatchObjectQueries) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if statement, ok := q.statements[query]; ok {
		return statement.QueryContext(ctx, args...)
	}
	return q.Tx.QueryContext(ctx, query, args...)
}
