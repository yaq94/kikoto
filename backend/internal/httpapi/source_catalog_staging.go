package httpapi

import "context"

// Only complete coverage retires staging observations from older generations
// and page revisions. Catalog identities, file snapshots and reviews are never
// touched. Each writer transaction removes at most 100 staging objects.
func (s *Server) cleanupCatalogStagingBatch(ctx context.Context, job workflowJobRecord, sourceID int64) (int64, error) {
	tx, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = verifyCatalogLease(ctx, tx, job); err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM source_catalog_observation WHERE rowid IN (
	SELECT o.rowid FROM source_catalog_observation o JOIN source_catalog_directory d ON d.file_source_id=o.file_source_id AND d.path=o.directory_path
	WHERE d.file_source_id=? AND (d.generation<>? OR o.revision<>d.revision) LIMIT 100)`, sourceID, job.RunID)
	if err != nil {
		return 0, err
	}
	removed, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if removed < 100 {
		result, err = tx.ExecContext(ctx, `DELETE FROM source_catalog_directory WHERE rowid IN (
		SELECT d.rowid FROM source_catalog_directory d WHERE d.file_source_id=? AND d.generation<>?
		AND NOT EXISTS(SELECT 1 FROM source_catalog_observation o WHERE o.file_source_id=d.file_source_id AND o.directory_path=d.path) LIMIT ?)`, sourceID, job.RunID, 100-removed)
		if err != nil {
			return 0, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		removed += n
	}
	return removed, tx.Commit()
}
