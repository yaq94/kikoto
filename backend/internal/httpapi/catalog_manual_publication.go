package httpapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/yexca/kikoto/backend/internal/openlist"
)

// Finite scans already have a bounded input set. Persist that input into the
// common listing stage before applying it, so retries do not repeat completed
// file batches or expose a partly replaced card.
func (s *Server) persistOpenListSnapshot(ctx context.Context, sourceID int64, root, sourceRoot, title string, files []openlist.File) (int64, error) {
	op, _ := ctx.Value(catalogPublicationKey{}).(int64)
	if op == 0 {
		return 0, errors.New("manual catalog snapshot requires publication")
	}
	digest := sha256.New()
	for _, f := range files {
		_, _ = fmt.Fprintf(digest, "%s\x00%s\x00%s\x00%d\x00", f.Path, f.Title, f.Kind, f.Size)
	}
	version := hex.EncodeToString(digest.Sum(nil))
	options := mustJSON(map[string]string{"sourceRoot": sourceRoot, "title": title, "kind": "manual"})
	var stage catalogSyncListing
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return 0, err
	}
	err = tx.QueryRowContext(ctx, `SELECT id,next_page,received,state,apply_cursor,remove_cursor FROM source_catalog_listing WHERE file_source_id=? AND directory_path=? AND generation=? AND observed_version=? AND options_json=? ORDER BY id DESC LIMIT 1`, sourceID, root, -op, version, options).Scan(&stage.ID, &stage.Page, &stage.Received, &stage.State, &stage.ApplyCursor, &stage.RemoveCursor)
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, `INSERT INTO source_catalog_listing(file_source_id,directory_path,generation,base_revision,observed_version,options_json,expected_total) VALUES(?,?,?,0,?,?,?) RETURNING id,next_page,received,state,apply_cursor,remove_cursor`, sourceID, root, -op, version, options, len(files)).Scan(&stage.ID, &stage.Page, &stage.Received, &stage.State, &stage.ApplyCursor, &stage.RemoveCursor)
	}
	if err == nil {
		err = tx.Commit()
	}
	release()
	if err != nil {
		return 0, err
	}
	for stage.State == "fetching" {
		end := min(stage.Received+catalogSyncListingWriteRows, len(files))
		tx, release, err = beginTxWithDatabaseBusyRetry(ctx, s.db)
		if err != nil {
			return 0, err
		}
		for _, f := range files[stage.Received:end] {
			_, err = tx.ExecContext(ctx, `INSERT INTO source_catalog_listing_item(listing_id,directory_path,name,kind,size_bytes,title,content_hash,modified_at) VALUES(?,?,?,?,?,?,'','')`, stage.ID, root, f.Path, f.Kind, f.Size, f.Title)
			if err != nil {
				break
			}
		}
		nextState := "fetching"
		if end == len(files) {
			nextState = "complete"
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE source_catalog_listing SET received=?,next_page=next_page+1,state=? WHERE id=?`, end, nextState, stage.ID)
		}
		if err == nil {
			err = tx.Commit()
		}
		release()
		if err != nil {
			return 0, err
		}
		stage.Received, stage.State = end, nextState
	}
	var id int64
	tx, release, err = beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return 0, err
	}
	id, err = persistOpenListEntryTx(ctx, tx.Tx, sourceID, root, sourceRoot, title)
	if err == nil {
		err = tx.Commit()
	}
	release()
	if err != nil {
		return 0, err
	}
	if stage.State == "applied" {
		return id, nil
	}
	for stage.RemoveCursor != "cleared" {
		tx, release, err = beginTxWithDatabaseBusyRetry(ctx, s.db)
		if err != nil {
			return 0, err
		}
		result, deleteErr := tx.ExecContext(ctx, `DELETE FROM source_catalog_file WHERE rowid IN(SELECT rowid FROM source_catalog_file WHERE catalog_entry_id=? LIMIT 64)`, id)
		err = deleteErr
		var n int64
		if err == nil {
			n, err = result.RowsAffected()
		}
		if err == nil && n < 64 {
			_, err = tx.ExecContext(ctx, `UPDATE source_catalog_listing SET remove_cursor='cleared',state='applying' WHERE id=?`, stage.ID)
		}
		if err == nil {
			err = tx.Commit()
		}
		release()
		if err != nil {
			return 0, err
		}
		if n < 64 {
			stage.RemoveCursor = "cleared"
		}
	}
	for {
		tx, release, err = beginTxWithDatabaseBusyRetry(ctx, s.db)
		if err != nil {
			return 0, err
		}
		rows, readErr := tx.QueryContext(ctx, `SELECT name,title,kind,size_bytes FROM source_catalog_listing_item WHERE listing_id=? AND name>? ORDER BY name LIMIT 64`, stage.ID, stage.ApplyCursor)
		err = readErr
		batch := []openlist.File{}
		if err == nil {
			for rows.Next() {
				var f openlist.File
				if err = rows.Scan(&f.Path, &f.Title, &f.Kind, &f.Size); err != nil {
					break
				}
				batch = append(batch, f)
			}
			if err == nil {
				err = rows.Err()
			}
			_ = rows.Close()
		}
		for _, f := range batch {
			if err != nil {
				break
			}
			err = persistOpenListFileTx(ctx, tx.Tx, id, f)
		}
		if err == nil && len(batch) > 0 {
			_, err = tx.ExecContext(ctx, `UPDATE source_catalog_listing SET apply_cursor=? WHERE id=?`, batch[len(batch)-1].Path, stage.ID)
		}
		if err == nil {
			err = tx.Commit()
		}
		release()
		if err != nil {
			return 0, err
		}
		if len(batch) < 64 {
			break
		}
		stage.ApplyCursor = batch[len(batch)-1].Path
	}
	tx, release, err = beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return 0, err
	}
	defer release()
	if sourceRoot == "" {
		if err = attachCatalogSeriesForRootTx(ctx, tx.Tx, id, sourceID, root); err != nil {
			return 0, err
		}
	}
	if err = setCatalogEntryStatusTx(ctx, tx.Tx, id, "available"); err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM source_catalog_relocation_candidate WHERE catalog_entry_id=?`, id); err != nil {
		return 0, err
	}
	if err = refreshCatalogEntryAdmissionTx(ctx, tx.Tx, id); err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_listing SET state='applied' WHERE id=?`, stage.ID); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func (s *Server) markStaleSnapshotCards(ctx context.Context, sourceID int64, root string, keep map[int64]bool) error {
	var after int64
	for {
		tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT id,root_path FROM source_catalog_entry WHERE file_source_id=? AND relocated_to_id IS NULL AND id>? AND (source_root=? OR root_path=? OR (root_path>=? AND root_path<?)) ORDER BY id LIMIT 64`, sourceID, after, root, root, root+"/", root+"0")
		if err != nil {
			release()
			return err
		}
		updates := []catalogEntryStatusUpdate{}
		count := 0
		for rows.Next() {
			var id int64
			var cardRoot string
			if err = rows.Scan(&id, &cardRoot); err != nil {
				break
			}
			count++
			after = id
			if !keep[id] && cardRoot != root {
				updates = append(updates, catalogEntryStatusUpdate{ID: id, Status: "missing"})
			}
		}
		if err == nil {
			err = rows.Err()
		}
		_ = rows.Close()
		if err == nil {
			err = setCatalogEntryStatusesTx(ctx, tx.Tx, updates)
		}
		if err == nil {
			err = tx.Commit()
		}
		release()
		if err != nil {
			return err
		}
		if count < 64 {
			return nil
		}
		if err = pauseCatalogWriter(ctx); err != nil {
			return err
		}
	}
}
