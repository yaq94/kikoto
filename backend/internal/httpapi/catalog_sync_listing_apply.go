package httpapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"time"
)

func (s *Server) applyCatalogSyncListingPages(ctx context.Context, sourceID, generation, runID int64, dir catalogSyncDirectoryState, listing catalogSyncListing, options catalogIndexOptions) (int, int, error) {
	changed := 0
	for {
		if err := s.checkCatalogSyncBatch(ctx, options); err != nil {
			return 0, changed, err
		}
		n, err := s.applyCatalogSyncListingBatch(ctx, sourceID, generation, dir, &listing)
		if err != nil {
			return 0, changed, err
		}
		if n < 0 {
			break
		}
		changed += n
	}
	for {
		if err := s.checkCatalogSyncBatch(ctx, options); err != nil {
			return 0, changed, err
		}
		rows, err := s.db.QueryContext(ctx, `SELECT name,kind FROM source_catalog_observation WHERE file_source_id=? AND directory_path=? AND revision<>? ORDER BY name LIMIT ?`, sourceID, dir.Path, dir.Revision+1, catalogSyncListingWriteRows)
		if err != nil {
			return 0, changed, err
		}
		removed := []catalogSyncObservation{}
		for rows.Next() {
			var row catalogSyncObservation
			if err = rows.Scan(&row.Name, &row.Kind); err != nil {
				break
			}
			removed = append(removed, row)
		}
		if err == nil {
			err = rows.Err()
		}
		_ = rows.Close()
		if err != nil {
			return 0, changed, err
		}
		if len(removed) == 0 {
			break
		}
		for _, row := range removed {
			if err = s.removeCatalogSyncObservation(ctx, sourceID, generation, dir, row, options); err != nil {
				return 0, changed, err
			}
			changed++
		}
	}
	if err := s.finishCatalogSyncListing(ctx, sourceID, generation, runID, dir, listing); err != nil {
		return 0, changed, err
	}
	return 0, changed, nil
}

func (s *Server) applyCatalogSyncListingBatch(ctx context.Context, sourceID, generation int64, dir catalogSyncDirectoryState, listing *catalogSyncListing) (int, error) {
	started := time.Now()
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return 0, err
	}
	defer release()
	rows, err := tx.QueryContext(ctx, `SELECT name,kind,size_bytes,content_hash,modified_at FROM source_catalog_listing_item WHERE listing_id=? AND name>? ORDER BY name LIMIT ?`, listing.ID, listing.ApplyCursor, catalogSyncListingWriteRows)
	if err != nil {
		return 0, err
	}
	items := []catalogSyncObservation{}
	for rows.Next() {
		var item catalogSyncObservation
		if err = rows.Scan(&item.Name, &item.Kind, &item.Size, &item.Hash, &item.Modified); err != nil {
			break
		}
		items = append(items, item)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return 0, err
	}
	if len(items) == 0 {
		return -1, nil
	}
	changed := 0
	for _, item := range items {
		child := path.Join(dir.Path, item.Name)
		traversable, err := catalogPathTraversableTx(ctx, tx.Tx, sourceID, child)
		if err != nil {
			return 0, err
		}
		if !traversable {
			continue
		}
		if catalogNameEncodingDamaged(item.Name) {
			if _, err = recordCatalogEncodingIssueTx(ctx, tx.Tx, sourceID, dir.Path, item.Name); err != nil {
				return 0, err
			}
			continue
		}
		var old catalogSyncObservation
		err = tx.QueryRowContext(ctx, `SELECT kind,size_bytes,content_hash,modified_at,revision FROM source_catalog_observation WHERE file_source_id=? AND directory_path=? AND name=?`, sourceID, dir.Path, item.Name).Scan(&old.Kind, &old.Size, &old.Hash, &old.Modified, &old.Revision)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
		different := errors.Is(err, sql.ErrNoRows) || old.Revision != dir.Revision || old.Kind != item.Kind || old.Size != item.Size || old.Hash != item.Hash || old.Modified != item.Modified
		if different {
			changed++
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes,content_hash,modified_at,publish_pending) VALUES(?,?,?,?,?,?,?,?,?)
			ON CONFLICT(file_source_id,directory_path,name) DO UPDATE SET revision=excluded.revision,kind=excluded.kind,size_bytes=excluded.size_bytes,content_hash=excluded.content_hash,modified_at=excluded.modified_at,publish_pending=excluded.publish_pending`, sourceID, dir.Path, item.Name, dir.Revision+1, item.Kind, item.Size, item.Hash, item.Modified, catalogSyncBoolInt(different)); err != nil {
			return 0, err
		}
		if item.Kind == "folder" {
			partition := dir.Partition
			if dir.Path == "/" {
				partition = child
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO source_catalog_directory(file_source_id,path,generation,partition_path,catalog_root,status,sync_state,observed_version) VALUES(?,?,?,?,?,'pending','changed',?) ON CONFLICT(file_source_id,path) DO NOTHING`, sourceID, child, generation, partition, dir.Root, item.Modified); err != nil {
				return 0, err
			}
		}
	}
	next := items[len(items)-1].Name
	if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_listing SET state='applying',apply_cursor=? WHERE id=?`, next, listing.ID); err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	listing.ApplyCursor = next
	observeCatalogWrite(ctx, "sync_listing_apply", started, len(items))
	return changed, nil
}

func (s *Server) removeCatalogSyncObservation(ctx context.Context, sourceID, generation int64, dir catalogSyncDirectoryState, row catalogSyncObservation, options catalogIndexOptions) error {
	child := path.Join(dir.Path, row.Name)
	if err := s.removeCatalogSyncListingFiles(ctx, sourceID, generation, dir, options); err != nil {
		return err
	}
	if row.Kind == "folder" {
		for {
			if err := s.checkCatalogSyncBatch(ctx, options); err != nil {
				return err
			}
			tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
			if err != nil {
				return err
			}
			result, err := tx.ExecContext(ctx, `DELETE FROM source_catalog_observation WHERE rowid IN (SELECT rowid FROM source_catalog_observation WHERE file_source_id=? AND (directory_path=? OR (directory_path>=? AND directory_path<?)) LIMIT ?)`, sourceID, child, child+"/", child+"0", catalogSyncListingWriteRows)
			var n int64
			if err == nil {
				n, err = result.RowsAffected()
			}
			if err == nil {
				err = tx.Commit()
			}
			release()
			if err != nil {
				return err
			}
			if n == 0 {
				break
			}
		}
		for {
			if err := s.checkCatalogSyncBatch(ctx, options); err != nil {
				return err
			}
			tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
			if err != nil {
				return err
			}
			result, err := tx.ExecContext(ctx, `UPDATE source_catalog_directory SET sync_state='excluded',status='empty',error_code='not_found',completed_at=CURRENT_TIMESTAMP
				WHERE rowid IN (SELECT rowid FROM source_catalog_directory WHERE file_source_id=? AND generation=? AND sync_state<>'excluded' AND (path=? OR (path>=? AND path<?)) LIMIT ?)`, sourceID, generation, child, child+"/", child+"0", catalogSyncListingWriteRows)
			var n int64
			if err == nil {
				n, err = result.RowsAffected()
			}
			if err == nil {
				err = tx.Commit()
			}
			release()
			if err != nil {
				return err
			}
			if n == 0 {
				break
			}
		}
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM source_catalog_observation WHERE file_source_id=? AND directory_path=? AND name=? AND revision<>?`, sourceID, dir.Path, row.Name, dir.Revision+1)
	return err
}

func (s *Server) finishCatalogSyncListing(ctx context.Context, sourceID, generation, runID int64, dir catalogSyncDirectoryState, listing catalogSyncListing) error {
	if _, err := s.prepareCatalogLargeGrouping(ctx, catalogGroupingIndexInput{SourceID: sourceID, Generation: generation, Root: dir.Path, Revision: dir.Revision + 1}); err != nil {
		return err
	}
	digest := sha256.New()
	rows, err := s.db.QueryContext(ctx, `SELECT name,kind,size_bytes,content_hash,modified_at FROM source_catalog_listing_item WHERE listing_id=? ORDER BY name`, listing.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var item catalogSyncObservation
		if err = rows.Scan(&item.Name, &item.Kind, &item.Size, &item.Hash, &item.Modified); err != nil {
			break
		}
		_, _ = fmt.Fprintf(digest, "%s%c%s%c%d%c%s%c%s%c", item.Name, 0, item.Kind, 0, item.Size, 0, item.Hash, 0, item.Modified, 0)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return err
	}
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return err
	}
	defer release()
	status := "complete"
	if listing.Received == 0 {
		status = "empty"
	}
	root := dir.Root
	if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_directory SET revision=?,status=?,error_code=?,listing_digest=?,sync_state='listed',last_verified_run=?,last_verified_at=CURRENT_TIMESTAMP,completed_at=CURRENT_TIMESTAMP WHERE file_source_id=? AND path=?`, dir.Revision+1, status, catalogSyncEmptyCode(status), hex.EncodeToString(digest.Sum(nil)), runID, sourceID, dir.Path); err != nil {
		return err
	}
	if dir.Status != "complete" || root == "" {
		d := catalogScanDirectory{Path: dir.Path, Partition: dir.Partition, Root: root, Revision: dir.Revision + 1, Page: 1, Expected: -1}
		if err = completeCatalogDirectoryTx(ctx, tx.Tx, sourceID, generation, &d); err != nil {
			return err
		}
		root = d.Root
		if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_directory SET catalog_root=? WHERE file_source_id=? AND path=?`, root, sourceID, dir.Path); err != nil {
			return err
		}
	}
	// The direct listing owns its grouping decision. A changed existing root
	// must select the newly persisted plan too; a nested change invalidates only
	// the parent's cached completeness result.
	if root == dir.Path && status == "complete" {
		if err = decideCatalogWorkSplitTx(ctx, tx.Tx, sourceID, generation, dir.Path, dir.Revision+1); err != nil {
			return err
		}
	}
	if root != "" {
		if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_grouping SET parent_ready=0,parent_cursor_path='',parent_cursor_name='' WHERE id=(SELECT g.id FROM source_catalog_grouping g JOIN source_catalog_directory d ON d.file_source_id=g.file_source_id AND d.path=g.root_path WHERE g.file_source_id=? AND g.root_path=? AND g.version=('observation:'||d.generation||':'||d.revision))`, sourceID, root); err != nil {
			return err
		}
	}
	if status == "empty" {
		target := root
		if target == "" {
			target = dir.Path
		}
		if err = markCatalogRangeStatusTx(ctx, tx.Tx, sourceID, target, "unconfirmed_empty"); err != nil {
			return err
		}
	}
	change, err := refreshCatalogAudioRootStateTx(ctx, tx.Tx, sourceID, generation, dir.Path)
	if err != nil {
		return err
	}
	if status == "complete" {
		err = addCompletedDirectoryPublishPendingTx(ctx, tx.Tx, sourceID, generation, dir.Path, change)
	} else if change.Deleted {
		err = addCatalogPublishPendingTx(ctx, tx.Tx, sourceID, -change.Pending)
	}
	if err != nil {
		return err
	}
	if err = resetCatalogPublishCursorTx(ctx, tx.Tx, sourceID, dir.Path, true); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_listing SET state='applied' WHERE id=?`, listing.ID); err != nil {
		return err
	}
	return tx.Commit()
}
