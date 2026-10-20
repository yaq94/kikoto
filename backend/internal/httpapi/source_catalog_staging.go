package httpapi

import (
	"context"
	"time"
)

// Only complete coverage retires staging observations from older generations
// and page revisions. Catalog identities, file snapshots and reviews are never
// touched. Each writer transaction removes at most 100 staging objects.
func (s *Server) cleanupCatalogStagingBatch(ctx context.Context, job workflowJobRecord, sourceID int64) (int64, error) {
	started := time.Now()
	written := int64(0)
	defer func() { observeCatalogWrite(ctx, "cleanup_staging", started, int(written)) }()
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return 0, err
	}
	defer release()
	if err = verifyCatalogLease(ctx, tx.Tx, job); err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM source_catalog_observation WHERE rowid IN (
	SELECT o.rowid FROM source_catalog_observation o JOIN source_catalog_directory d ON d.file_source_id=o.file_source_id AND d.path=o.directory_path
	WHERE d.file_source_id=? AND (d.generation<>? OR o.revision<>d.revision) LIMIT ?)`, sourceID, job.RunID, catalogStagingRows)
	if err != nil {
		return 0, err
	}
	removed, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if removed < catalogStagingRows {
		result, err = tx.ExecContext(ctx, `DELETE FROM source_catalog_directory WHERE rowid IN (
		SELECT d.rowid FROM source_catalog_directory d WHERE d.file_source_id=? AND d.generation<>?
		AND NOT EXISTS(SELECT 1 FROM source_catalog_observation o WHERE o.file_source_id=d.file_source_id AND o.directory_path=d.path) LIMIT ?)`, sourceID, job.RunID, catalogStagingRows-removed)
		if err != nil {
			return 0, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		removed += n
	}
	// The audio-root table holds one row per generation and root. Publishing
	// only reads the current generation, so older rows are reclaimable staging
	// data and would otherwise grow with every daily rescan.
	if removed < catalogStagingRows {
		result, err = tx.ExecContext(ctx, `DELETE FROM source_catalog_audio_root WHERE rowid IN (
		SELECT rowid FROM source_catalog_audio_root WHERE file_source_id=? AND generation<>? LIMIT ?)`, sourceID, job.RunID, catalogStagingRows-removed)
		if err != nil {
			return 0, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		removed += n
	}
	written = removed
	return removed, tx.Commit()
}
