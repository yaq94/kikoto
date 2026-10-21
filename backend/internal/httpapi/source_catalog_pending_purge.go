package httpapi

import (
	"context"
	"database/sql"
	"sync"
)

const (
	// catalogStalePendingPurge is both the row bound of one update and the
	// sentinel for "this batch was full". The two meanings must move together:
	// lowering the LIMIT without the sentinel would stop the purge early while
	// still reporting success.
	catalogStalePendingPurge = 2000
	// catalogStalePendingPurgeBatch bounds one call to 16,000 rows. The rest of
	// the backlog stays pending for the next scan invocation.
	catalogStalePendingPurgeBatch = 8
)

// catalogPendingPurgePause is the seam between this purge and the catalog
// writer pacing. Keeping the indirection here lets the two live in separate
// files and lets tests swap it without waiting on real pauses.
var catalogPendingPurgePause = pauseCatalogWriter

// catalogPendingPurgeRuns records the generation whose stale queues have been
// checked to empty. The scan asks on every step; without this gate the EXISTS
// join below runs against every pending row on each step. The key includes the
// pool so tests and separate servers never share a verdict.
type catalogPendingPurgeKey struct {
	db       *sql.DB
	sourceID int64
}

var catalogPendingPurgeRuns sync.Map

// purgeStaleCatalogPending clears publish_pending on observations whose
// directory no longer belongs to this scan generation. The row itself, its
// entry and file, and all personal state remain untouched. At most
// catalogStalePendingPurge*catalogStalePendingPurgeBatch rows are cleared per
// call; a remaining backlog is left for the next invocation.
func (s *Server) purgeStaleCatalogPending(ctx context.Context, job workflowJobRecord, sourceID int64) error {
	key := catalogPendingPurgeKey{db: s.db, sourceID: sourceID}
	if generation, ok := catalogPendingPurgeRuns.Load(key); ok && generation.(int64) == job.RunID {
		// The stale set is fixed per generation. A clean queue stays clean until
		// the next scan starts, so the common step does no query at all.
		return nil
	}
	// The first call for a generation pays one indexed existence check. A
	// non-empty queue then drains in bounded batches; a full final batch leaves
	// the generation unmarked so the next invocation continues.
	var stale int
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM source_catalog_observation o
		JOIN source_catalog_directory d ON d.file_source_id=o.file_source_id AND d.path=o.directory_path
		WHERE d.file_source_id=? AND d.generation<>? AND o.publish_pending=1)`,
		sourceID, job.RunID).Scan(&stale); err != nil {
		return err
	}
	if stale == 0 {
		catalogPendingPurgeRuns.Store(key, job.RunID)
		return nil
	}
	for batch := 0; batch < catalogStalePendingPurgeBatch; batch++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		cleared, err := s.purgeStaleCatalogPendingBatch(ctx, job, sourceID)
		if err != nil {
			return err
		}
		if cleared < catalogStalePendingPurge {
			catalogPendingPurgeRuns.Store(key, job.RunID)
			return nil
		}
		if batch+1 < catalogStalePendingPurgeBatch {
			if err = catalogPendingPurgePause(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Server) purgeStaleCatalogPendingBatch(ctx context.Context, job workflowJobRecord, sourceID int64) (int, error) {
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return 0, err
	}
	defer release()
	if err = verifyCatalogLease(ctx, tx.Tx, job); err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE source_catalog_observation SET publish_pending=0
WHERE rowid IN (
  SELECT o.rowid FROM source_catalog_observation o
  JOIN source_catalog_directory d
    ON d.file_source_id=o.file_source_id AND d.path=o.directory_path
  WHERE d.file_source_id=? AND d.generation<>? AND o.publish_pending=1
  LIMIT ?)`, sourceID, job.RunID, catalogStalePendingPurge)
	if err != nil {
		return 0, err
	}
	cleared, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return int(cleared), nil
}
