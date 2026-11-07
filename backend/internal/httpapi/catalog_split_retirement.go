package httpapi

import (
	"context"
	"database/sql"
	"errors"
)

const catalogSplitRetirementRows = 64

func queueCatalogSplitRetirementTx(ctx context.Context, tx *sql.Tx, sourceID, generation int64, root string) error {
	operation, _ := ctx.Value(catalogPublicationKey{}).(int64)
	if operation == 0 {
		// An isolated publisher without stale cards has nothing to defer. A
		// retirement requires the operation that keeps its old snapshot readable.
		var stale bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM source_catalog_entry WHERE file_source_id=? AND (source_root=? OR root_path=?) AND relocated_to_id IS NULL AND last_seen_generation<>?)`, sourceID, root, root, generation).Scan(&stale); err != nil {
			return err
		}
		if stale {
			return errors.New("split retirement requires a catalog publication")
		}
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO source_catalog_split_retirement(operation_id,root_path,generation) VALUES(?,?,?) ON CONFLICT DO NOTHING`, operation, root, generation)
	return err
}

// Both ranges use indexed keyset scans. The parent has a unique root_path;
// limiting the split range before UNION avoids sorting a complete creator root.
const catalogSplitRetirementQuery = `SELECT id FROM (
	SELECT id FROM source_catalog_entry INDEXED BY source_catalog_entry_source_root
	WHERE file_source_id=?1 AND source_root=?2 AND source_root<>'' AND id>?3
	ORDER BY id LIMIT ?4
) UNION ALL SELECT id FROM source_catalog_entry
	WHERE file_source_id=?1 AND root_path=?2 AND source_root<>?2 AND id>?3
	ORDER BY id LIMIT ?4`

func (s *Server) prepareCatalogSplitRetirements(ctx context.Context, operation int64) error {
	for {
		done, err := s.prepareCatalogSplitRetirementBatch(ctx, operation)
		if err != nil || done {
			return err
		}
		if err = pauseCatalogWriter(ctx); err != nil {
			return err
		}
	}
}

func (s *Server) prepareCatalogSplitRetirementBatch(ctx context.Context, operation int64) (bool, error) {
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return false, err
	}
	defer release()
	var sourceID, generation, after int64
	var root string
	err = tx.QueryRowContext(ctx, `SELECT p.file_source_id,r.root_path,r.generation,r.after_entry_id
		FROM source_catalog_split_retirement r JOIN source_catalog_publication p ON p.id=r.operation_id
		WHERE r.operation_id=? ORDER BY r.root_path LIMIT 1`, operation).Scan(&sourceID, &root, &generation, &after)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	incomplete, err := catalogRootIncomplete(ctx, tx.Tx, sourceID, generation, root)
	if err != nil {
		return false, err
	}
	ids := []int64{}
	if !incomplete {
		rows, err := tx.QueryContext(ctx, catalogSplitRetirementQuery, sourceID, root, after, catalogSplitRetirementRows)
		if err != nil {
			return false, err
		}
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
			return false, err
		}
	}
	for _, id := range ids {
		if err = retireStaleSplitCardTx(ctx, tx.Tx, sourceID, generation, root, id); err != nil {
			return false, err
		}
		after = id
	}
	if len(ids) < catalogSplitRetirementRows {
		_, err = tx.ExecContext(ctx, `DELETE FROM source_catalog_split_retirement WHERE operation_id=? AND root_path=?`, operation, root)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE source_catalog_split_retirement SET after_entry_id=? WHERE operation_id=? AND root_path=?`, after, operation, root)
	}
	if err != nil {
		return false, err
	}
	return false, tx.Commit()
}

// The cursor consumes actual cards, including protected ones, so a large root
// cannot turn one orphan-page transaction into an unbounded retirement.
func (s *Server) retireOrphanSplitRootsBatch(ctx context.Context, job workflowJobRecord, sourceID int64) (int, error) {
	// Resolve queued roots while their old files still supply replacement
	// evidence. The full-index lifecycle and obsolete-file cleanup follow this
	// step; publication still waits for all of them.
	if operation, _ := ctx.Value(catalogPublicationKey{}).(int64); operation > 0 {
		if err := s.prepareCatalogSplitRetirements(ctx, operation); err != nil {
			return 0, err
		}
	}
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return 0, err
	}
	defer release()
	if err = verifyCatalogLease(ctx, tx.Tx, job); err != nil {
		return 0, err
	}
	var cursor int64
	if err = tx.QueryRowContext(ctx, `SELECT orphan_cleanup_cursor FROM source_catalog_scan WHERE file_source_id=?`, sourceID).Scan(&cursor); err != nil {
		return 0, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,source_root FROM source_catalog_entry
		WHERE file_source_id=? AND source_root<>'' AND relocated_to_id IS NULL AND id>?
		ORDER BY id LIMIT ?`, sourceID, cursor, catalogRetireRoots)
	if err != nil {
		return 0, err
	}
	type card struct {
		id   int64
		root string
	}
	cards := []card{}
	for rows.Next() {
		var item card
		if err = rows.Scan(&item.id, &item.root); err != nil {
			break
		}
		cards = append(cards, item)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return 0, err
	}
	roots := map[string]bool{}
	for _, item := range cards {
		cursor = item.id
		complete, checked := roots[item.root]
		if !checked {
			if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM source_catalog_directory WHERE file_source_id=? AND generation=? AND path=? AND status='complete')`, sourceID, job.RunID, item.root).Scan(&complete); err != nil {
				return 0, err
			}
			if complete {
				incomplete, checkErr := catalogRootIncomplete(ctx, tx.Tx, sourceID, job.RunID, item.root)
				if checkErr != nil {
					return 0, checkErr
				}
				complete = !incomplete
			}
			roots[item.root] = complete
		}
		if !complete {
			continue
		}
		if err = retireStaleSplitCardTx(ctx, tx.Tx, sourceID, job.RunID, item.root, item.id); err != nil {
			return 0, err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_scan SET orphan_cleanup_cursor=? WHERE file_source_id=?`, cursor, sourceID); err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	if len(cards) == catalogRetireRoots {
		return catalogRetireRoots, nil
	}
	return len(roots), nil
}

func retireStaleSplitCardTx(ctx context.Context, tx *sql.Tx, sourceID, generation int64, parentRoot string, id int64) error {
	var blocked bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM source_catalog_entry e WHERE e.id=? AND (
		e.last_seen_generation=? OR e.relocated_to_id IS NOT NULL OR e.work_id IS NOT NULL
		OR EXISTS(SELECT 1 FROM catalog_series_member m WHERE m.catalog_entry_id=e.id)))`, id, generation).Scan(&blocked); err != nil {
		return err
	}
	if blocked {
		return nil
	}
	var children bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM source_catalog_entry WHERE relocated_to_id=?)`, id).Scan(&children); err != nil {
		return err
	}
	target, err := replacementCatalogCardTx(ctx, tx, sourceID, generation, parentRoot, id)
	if err != nil {
		return err
	}
	if children && target == 0 {
		// No unique successor can keep these aliases resolvable.
		return nil
	}
	_, err = retireCatalogCardTx(ctx, tx, id, target)
	return err
}
