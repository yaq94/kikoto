package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/yexca/kikoto/backend/internal/openlist"
)

func catalogFailureStatus(err error) string {
	if errors.Is(err, openlist.ErrMaintenance) {
		return "maintenance"
	}
	if errors.Is(err, openlist.ErrRestricted) {
		return "restricted"
	}
	return "unavailable"
}

func (s *Server) setCatalogEntryStatus(ctx context.Context, id int64, status string) error {
	tx, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = setCatalogEntryStatusTx(ctx, tx, id, status); err != nil {
		return err
	}
	return tx.Commit()
}

func markCatalogRangeStatusTx(ctx context.Context, tx *sql.Tx, sourceID int64, root, status string) error {
	_, err := tx.ExecContext(ctx, `UPDATE source_catalog_entry SET availability_status=?,status_checked_at=CURRENT_TIMESTAMP
		WHERE file_source_id=? AND relocated_to_id IS NULL AND (?='/' OR root_path=? OR instr(root_path,?)=1)`, status, sourceID, root, root, strings.TrimRight(root, "/")+"/")
	return err
}

func setCatalogEntryStatusTx(ctx context.Context, tx *sql.Tx, id int64, status string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE source_catalog_entry SET availability_status=?,status_checked_at=CURRENT_TIMESTAMP WHERE id=?`, status, id); err != nil {
		return err
	}
	if status == "missing" || status == "available" {
		availability := "unavailable"
		if status == "available" {
			availability = "available"
		}
		if _, err := tx.ExecContext(ctx, `UPDATE media_file_location SET availability=? WHERE location_type='catalog_stream'
			AND instr(stream_url,'/api/source-catalog/' || ? || '/media?')=1
			AND (?='unavailable' OR EXISTS(SELECT 1 FROM source_catalog_file f WHERE f.catalog_entry_id=? AND f.relative_path=media_file_location.path AND f.kind='audio'))`, availability, id, availability, id); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `UPDATE work_source_presence SET availability=CASE WHEN EXISTS(
		SELECT 1 FROM source_catalog_entry e WHERE e.work_id=work_source_presence.work_id AND e.file_source_id=work_source_presence.file_source_id AND e.relocated_to_id IS NULL AND e.availability_status='available') THEN 'available' ELSE ? END,
		last_checked_at=CURRENT_TIMESTAMP WHERE (work_id,file_source_id) IN(SELECT work_id,file_source_id FROM source_catalog_entry WHERE id=?)`, status, id)
	return err
}

// Only full successful coverage reaches this bounded final pass. Failure,
// cancellation and empty directories cannot retire a previous observation.
func (s *Server) settleCatalogLifecycleBatch(ctx context.Context, job workflowJobRecord, sourceID int64) (bool, error) {
	tx, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = verifyCatalogLease(ctx, tx, job); err != nil {
		return false, err
	}
	var stage string
	var cursor int64
	if err = tx.QueryRowContext(ctx, `SELECT lifecycle_stage,lifecycle_cursor FROM source_catalog_scan WHERE file_source_id=?`, sourceID).Scan(&stage, &cursor); err != nil {
		return false, err
	}
	if stage == "done" {
		return false, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,last_seen_generation FROM source_catalog_entry WHERE file_source_id=? AND relocated_to_id IS NULL AND id>? ORDER BY id LIMIT 100`, sourceID, cursor)
	if err != nil {
		return false, err
	}
	type observed struct{ id, generation int64 }
	var entries []observed
	for rows.Next() {
		var e observed
		if err = rows.Scan(&e.id, &e.generation); err != nil {
			break
		}
		entries = append(entries, e)
	}
	readErr := rows.Err()
	_ = rows.Close()
	if err != nil {
		return false, err
	}
	if readErr != nil {
		return false, readErr
	}
	for _, e := range entries {
		if stage == "reconcile" {
			if e.generation != job.RunID {
				if err = reconcileCatalogEntryTx(ctx, tx, sourceID, job.RunID, e.id); err != nil {
					return false, err
				}
			}
			cursor = e.id
			continue
		}
		status := "missing"
		if e.generation == job.RunID {
			status = "available"
			if _, err = tx.ExecContext(ctx, `DELETE FROM source_catalog_relocation_candidate WHERE catalog_entry_id=?`, e.id); err != nil {
				return false, err
			}
			strong, layout, readErr := catalogFingerprintsTx(ctx, tx, e.id, job.RunID)
			if readErr != nil {
				return false, readErr
			}
			if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_entry SET content_fingerprint=?,layout_fingerprint=? WHERE id=?`, strong, layout, e.id); err != nil {
				return false, err
			}
			if _, err = tx.ExecContext(ctx, `DELETE FROM source_catalog_file WHERE catalog_entry_id=? AND observed_generation<>?`, e.id, job.RunID); err != nil {
				return false, err
			}
		}
		if err = setCatalogEntryStatusTx(ctx, tx, e.id, status); err != nil {
			return false, err
		}
		cursor = e.id
	}
	if len(entries) < 100 {
		if stage == "observe" {
			stage = "reconcile"
			cursor = 0
		} else {
			stage = "done"
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_scan SET lifecycle_stage=?,lifecycle_cursor=? WHERE file_source_id=?`, stage, cursor, sourceID); err != nil {
		return false, err
	}
	return true, tx.Commit()
}
