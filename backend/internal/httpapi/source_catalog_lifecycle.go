package httpapi

import (
	"context"
	"database/sql"
	"errors"

	"github.com/yexca/kikoto/backend/internal/openlist"
)

// catalogRootIncomplete reports an observation gap below one catalog root.
// Retirement and missing-state decisions only apply to a range the current
// generation fully observed: a failed, restricted, unconfirmed-empty or still
// listing directory keeps every older card and file row it owns.
func catalogRootIncomplete(ctx context.Context, tx *sql.Tx, sourceID, generation int64, root string) (bool, error) {
	if root == "" {
		root = "/"
	}
	var incomplete int
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM source_catalog_directory d
		WHERE d.file_source_id=? AND d.generation=? AND d.status<>'complete'
			AND NOT (d.status='empty' AND d.error_code='not_found')
			AND (?='/' OR d.catalog_root=? OR d.path=? OR substr(d.path,1,length(?)+1)=? || '/')
		UNION ALL
		SELECT 1 FROM source_catalog_observation o
		JOIN source_catalog_directory pd ON pd.file_source_id=o.file_source_id AND pd.path=o.directory_path
		WHERE o.file_source_id=? AND pd.generation=? AND o.publish_pending=1
			AND (?='/' OR pd.catalog_root=? OR substr(pd.path,1,length(?)+1)=? || '/')
	)`, sourceID, generation, root, root, root, root, root, root,
		sourceID, generation, root, root, root, root, root).Scan(&incomplete)
	return incomplete != 0, err
}

// catalogRangeUncertain reports a directory below base that the current
// generation could not observe. An unconfirmed-empty, failed, restricted or
// still-pending directory proves nothing about deletion, so callers keep the
// rows recorded by earlier generations.
func catalogRangeUncertain(ctx context.Context, tx *sql.Tx, sourceID, generation int64, base string) (bool, error) {
	// status='empty' with error_code='not_found' is a definite 404/410 answer,
	// not an unconfirmed listing. Treating it as uncertain kept the whole
	// source partial and preserved rows a deleted directory no longer owns.
	if base == "" {
		base = "/"
	}
	const uncertainStatus = ` status IN ('failed','restricted','empty','pending','listing_complete')
		AND NOT (status='empty' AND error_code='not_found')`
	var uncertain int
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM source_catalog_directory d
		WHERE d.file_source_id=? AND d.generation=? AND `+uncertainStatus+`
			AND (?='/' OR d.path=? OR substr(d.path,1,length(?)+1)=? || '/'))`,
		sourceID, generation, base, base, base, base, base).Scan(&uncertain)
	return uncertain != 0, err
}

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
		WHERE file_source_id=? AND relocated_to_id IS NULL AND (?='/' OR root_path=? OR source_root=? OR substr(root_path,1,length(?)+2)=? || '::')`,
		status, sourceID, root, root, root, root, root)
	return err
}

// markCatalogDirectoryStatusTx applies a directory's own outcome to the cards
// whose real path is that directory. A failed child below a card does not make
// the card unplayable: its own files were still observed and published.

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
		if e.generation != job.RunID {
			// An entry this generation did not see is only missing when the
			// range that contains it was fully observed. A failed, restricted
			// or unconfirmed-empty directory proves nothing about its files.
			var base string
			if err = tx.QueryRowContext(ctx, `SELECT CASE WHEN COALESCE(source_root,'')='' THEN root_path ELSE source_root END
				FROM source_catalog_entry WHERE id=?`, e.id).Scan(&base); err != nil {
				return false, err
			}
			uncertain, err := catalogRangeUncertain(ctx, tx.Tx, sourceID, job.RunID, base)
			if err != nil {
				return false, err
			}
			if uncertain {
				cursor = e.id
				continue
			}
		}
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
			// Republishing the card does not prove that every directory below
			// it was observed. An unconfirmed-empty or failed child may still
			// be the only place an older file row can be confirmed, so keep the
			// old rows until that subtree completes.
			var base string
			if err = tx.QueryRowContext(ctx, `SELECT CASE WHEN COALESCE(source_root,'')='' THEN root_path ELSE source_root END
				FROM source_catalog_entry WHERE id=?`, e.id).Scan(&base); err != nil {
				return false, err
			}
			uncertain, err := catalogRangeUncertain(ctx, tx.Tx, sourceID, job.RunID, base)
			if err != nil {
				return false, err
			}
			if !uncertain {
				if _, err = tx.ExecContext(ctx, `DELETE FROM source_catalog_file WHERE catalog_entry_id=? AND observed_generation<>?`, e.id, job.RunID); err != nil {
					return false, err
				}
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
	startCursor := cursor
	uncertainSeen := false
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
		var base string
		if err = tx.QueryRowContext(ctx, `SELECT CASE WHEN COALESCE(source_root,'')='' THEN root_path ELSE source_root END
			FROM source_catalog_entry WHERE id=?`, id).Scan(&base); err != nil {
			return 0, 0, err
		}
		// A failed, restricted or unconfirmed-empty directory still proves
		// nothing about its files. Deleting the older generation below it would
		// hide tracks that the source never had a chance to republish. Keep the
		// rows until a later scan completes that subtree.
		if base == "" {
			base = "/"
		}
		// The configured root is the whole tree: a failure anywhere below it
		// keeps older rows, while a fully observed root cleans them.
		uncertain, uncertainErr := catalogRangeUncertain(ctx, tx.Tx, sourceID, job.RunID, base)
		if uncertainErr != nil {
			return 0, 0, uncertainErr
		}
		if uncertain {
			uncertainSeen = true
		}
		if !uncertain {
			result, execErr := tx.ExecContext(ctx, `DELETE FROM source_catalog_file WHERE catalog_entry_id=? AND observed_generation<>?`, id, job.RunID)
			if execErr != nil {
				return 0, 0, execErr
			}
			n, execErr := result.RowsAffected()
			if execErr != nil {
				return 0, 0, execErr
			}
			removed += n
		}
		// A folder row that is now its own catalog entry belongs to that
		// entry, not to the card above it. Keeping it makes the parent list a
		// child work as one of its tracks.
		result, execErr := tx.ExecContext(ctx, `DELETE FROM source_catalog_file
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
		n, execErr := result.RowsAffected()
		if execErr != nil {
			return 0, 0, execErr
		}
		removed += n
		cursor = id
	}
	if len(ids) > 0 {
		// A card skipped because its subtree was unreadable is not finished: the
		// older rows it still lists can only be judged after that subtree
		// completes. Reaching the tail of the cursor rewinds it once, so the next
		// pass revisits those cards instead of skipping them forever. A partial
		// page keeps its forward cursor; the rewind happens when the scan ends.
		if uncertainSeen && len(ids) < 100 && startCursor != 0 {
			cursor = 0
		}
		if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_scan SET publish_cleanup_cursor=?,updated_at=CURRENT_TIMESTAMP WHERE file_source_id=?`, cursor, sourceID); err != nil {
			return 0, 0, err
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, 0, err
	}
	return int64(len(ids)), removed, nil
}
