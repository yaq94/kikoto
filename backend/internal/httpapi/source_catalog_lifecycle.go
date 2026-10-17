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
	// A challenge is a transport-level refusal that hides the real answer, so it
	// must not overwrite availability with a permission verdict.
	if errors.Is(err, openlist.ErrChallenged) {
		return "restricted"
	}
	if errors.Is(err, openlist.ErrRestricted) {
		return "restricted"
	}
	return "unavailable"
}

func (s *Server) setCatalogEntryStatus(ctx context.Context, id int64, status string) error {
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return err
	}
	defer release()
	if err = setCatalogEntryStatusTx(ctx, tx.Tx, id, status); err != nil {
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
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return false, err
	}
	defer release()
	if err = verifyCatalogLease(ctx, tx.Tx, job); err != nil {
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
				if err = reconcileCatalogEntryTx(ctx, tx.Tx, sourceID, job.RunID, e.id); err != nil {
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
			strong, layout, readErr := catalogFingerprintsTx(ctx, tx.Tx, e.id, job.RunID)
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
		if err = setCatalogEntryStatusTx(ctx, tx.Tx, e.id, status); err != nil {
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

// Republishing an entry replaces the files this generation observed, but rows
// recorded by an older generation stay until the whole run is known to be
// complete. A category or uploader card that stops being its own work then
// keeps listing children that now have their own cards, and a partial run
// never reaches the lifecycle pass that would delete them. This bounded pass
// retires those rows as soon as the entry is republished, for every outcome of
// the run, without touching an entry this generation did not publish.
func (s *Server) cleanupCatalogPublishBatch(ctx context.Context, job workflowJobRecord, sourceID int64) (scanned, removed int64, err error) {
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return 0, 0, err
	}
	defer release()
	if err = verifyCatalogLease(ctx, tx.Tx, job); err != nil {
		return 0, 0, err
	}
	var cursor int64
	if err = tx.QueryRowContext(ctx, `SELECT publish_cleanup_cursor FROM source_catalog_scan WHERE file_source_id=?`, sourceID).Scan(&cursor); err != nil {
		return 0, 0, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM source_catalog_entry
		WHERE file_source_id=? AND last_seen_generation=? AND id>?
		ORDER BY id LIMIT 100`, sourceID, job.RunID, cursor)
	if err != nil {
		return 0, 0, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return 0, 0, err
	}
	for _, id := range ids {
		result, execErr := tx.ExecContext(ctx, `DELETE FROM source_catalog_file WHERE catalog_entry_id=? AND observed_generation<>?`, id, job.RunID)
		if execErr != nil {
			return 0, 0, execErr
		}
		n, execErr := result.RowsAffected()
		if execErr != nil {
			return 0, 0, execErr
		}
		removed += n
		// A folder row that is now its own catalog entry belongs to that
		// entry, not to the card above it. Keeping it makes the parent list a
		// child work as one of its tracks.
		result, execErr = tx.ExecContext(ctx, `DELETE FROM source_catalog_file
			WHERE catalog_entry_id=? AND relative_path IN (
				SELECT substr(e.root_path,length(parent.root_path)+2)
				FROM source_catalog_entry e
				JOIN source_catalog_entry parent ON parent.id=?
				WHERE e.file_source_id=parent.file_source_id
					AND e.relocated_to_id IS NULL AND e.id<>parent.id
					AND parent.root_path<>'/' AND substr(e.root_path,1,length(parent.root_path)+1)=parent.root_path || '/')`, id, id)
		if execErr != nil {
			return 0, 0, execErr
		}
		n, execErr = result.RowsAffected()
		if execErr != nil {
			return 0, 0, execErr
		}
		removed += n
		cursor = id
	}
	if len(ids) > 0 {
		if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_scan SET publish_cleanup_cursor=?,updated_at=CURRENT_TIMESTAMP WHERE file_source_id=?`, cursor, sourceID); err != nil {
			return 0, 0, err
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, 0, err
	}
	return int64(len(ids)), removed, nil
}

// resetCatalogPublishCleanup restarts the cleanup cursor. Publishing order and
// entry id order differ, so a cursor that advanced during publication can pass
// an entry that is published later. The final pass starts from the beginning
// once no directory or publish row remains.
func (s *Server) resetCatalogPublishCleanup(ctx context.Context, job workflowJobRecord, sourceID int64) error {
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return err
	}
	defer release()
	if err = verifyCatalogLease(ctx, tx.Tx, job); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_scan SET publish_cleanup_cursor=0,updated_at=CURRENT_TIMESTAMP WHERE file_source_id=?`, sourceID); err != nil {
		return err
	}
	return tx.Commit()
}
