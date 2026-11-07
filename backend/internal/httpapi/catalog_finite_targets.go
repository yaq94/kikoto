package httpapi

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/yexca/kikoto/backend/internal/openlist"
)

func (s *Server) applyCatalogFiniteTargets(ctx context.Context, sourceID, listingID int64) error {
	for {
		tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT root_path,source_root,title FROM source_catalog_finite_target WHERE listing_id=? AND catalog_entry_id IS NULL ORDER BY root_path LIMIT 50`, listingID)
		if err != nil {
			release()
			return err
		}
		type target struct{ root, sourceRoot, title string }
		batch := []target{}
		for rows.Next() {
			var t target
			if err = rows.Scan(&t.root, &t.sourceRoot, &t.title); err != nil {
				break
			}
			batch = append(batch, t)
		}
		if err == nil {
			err = rows.Err()
		}
		_ = rows.Close()
		if err != nil {
			release()
			return err
		}
		// The entry's publication/search triggers compile once for this batch.
		// Close explicitly before commit (also on an execution error) so no
		// prepared statement survives this transaction or retains its connection.
		entryStatement, err := tx.PrepareContext(ctx, openListEntryUpsertSQL)
		if err != nil {
			release()
			return err
		}
		for _, t := range batch {
			if err != nil {
				break
			}
			var id int64
			err = entryStatement.QueryRowContext(ctx, openListEntryArgs(sourceID, t.root, t.sourceRoot, t.title)...).Scan(&id)
			if err == nil {
				_, err = tx.ExecContext(ctx, `UPDATE source_catalog_finite_target SET catalog_entry_id=? WHERE listing_id=? AND root_path=?`, id, listingID, t.root)
			}
		}
		err = errors.Join(err, entryStatement.Close())
		if err == nil {
			err = tx.Commit()
		}
		release()
		if err != nil {
			return err
		}
		if len(batch) < 50 {
			break
		}
	}
	for {
		tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
		if err != nil {
			return err
		}
		var cleared string
		err = tx.QueryRowContext(ctx, `SELECT remove_cursor FROM source_catalog_listing WHERE id=?`, listingID).Scan(&cleared)
		if err != nil {
			release()
			return err
		}
		if cleared == "cleared" {
			release()
			break
		}
		var cursor struct{ Root, Path string }
		if cleared != "" {
			err = json.Unmarshal([]byte(cleared), &cursor)
		}
		if err != nil {
			release()
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT t.root_path,f.relative_path,f.catalog_entry_id FROM source_catalog_finite_target t CROSS JOIN source_catalog_file f ON f.catalog_entry_id=t.catalog_entry_id WHERE t.listing_id=? AND t.root_path>=? AND (t.root_path,f.relative_path)>(?,?) ORDER BY t.root_path,f.relative_path LIMIT 64`, listingID, cursor.Root, cursor.Root, cursor.Path)
		if err != nil {
			release()
			return err
		}
		type deletion struct {
			root, path string
			id         int64
		}
		batch := []deletion{}
		for rows.Next() {
			var d deletion
			if err = rows.Scan(&d.root, &d.path, &d.id); err != nil {
				break
			}
			batch = append(batch, d)
		}
		if err == nil {
			err = rows.Err()
		}
		_ = rows.Close()
		if err != nil {
			release()
			return err
		}
		deleteStatement, err := tx.PrepareContext(ctx, `DELETE FROM source_catalog_file WHERE catalog_entry_id=? AND relative_path=?`)
		if err != nil {
			release()
			return err
		}
		for _, d := range batch {
			if err != nil {
				break
			}
			_, err = deleteStatement.ExecContext(ctx, d.id, d.path)
			cursor.Root, cursor.Path = d.root, d.path
		}
		err = errors.Join(err, deleteStatement.Close())
		n := len(batch)
		if err == nil {
			next := mustJSON(cursor)
			if n < 64 {
				next = "cleared"
			}
			_, err = tx.ExecContext(ctx, `UPDATE source_catalog_listing SET remove_cursor=? WHERE id=?`, next, listingID)
		}
		if err == nil {
			err = tx.Commit()
		}
		release()
		if err != nil {
			return err
		}
		if n < 64 {
			break
		}
	}
	for {
		tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT f.input_path,t.catalog_entry_id,f.relative_path,i.title,i.kind,i.size_bytes FROM source_catalog_finite_file f JOIN source_catalog_finite_target t ON t.listing_id=f.listing_id AND t.root_path=f.root_path JOIN source_catalog_listing_item i ON i.listing_id=f.listing_id AND i.name=f.input_path WHERE f.listing_id=? AND f.input_path>(SELECT apply_cursor FROM source_catalog_listing WHERE id=?) ORDER BY f.input_path LIMIT 64`, listingID, listingID)
		if err != nil {
			release()
			return err
		}
		type item struct {
			input string
			id    int64
			file  openlist.File
		}
		batch := []item{}
		for rows.Next() {
			var i item
			if err = rows.Scan(&i.input, &i.id, &i.file.Path, &i.file.Title, &i.file.Kind, &i.file.Size); err != nil {
				break
			}
			batch = append(batch, i)
		}
		if err == nil {
			err = rows.Err()
		}
		_ = rows.Close()
		if err != nil {
			release()
			return err
		}
		fileStatement, err := tx.PrepareContext(ctx, openListFileUpsertSQL)
		if err != nil {
			release()
			return err
		}
		for _, i := range batch {
			if err != nil {
				break
			}
			_, err = fileStatement.ExecContext(ctx, openListFileArgs(i.id, i.file)...)
		}
		err = errors.Join(err, fileStatement.Close())
		if err == nil && len(batch) > 0 {
			_, err = tx.ExecContext(ctx, `UPDATE source_catalog_listing SET apply_cursor=? WHERE id=?`, batch[len(batch)-1].input, listingID)
		}
		if err == nil {
			err = tx.Commit()
		}
		release()
		if err != nil {
			return err
		}
		if len(batch) < 64 {
			break
		}
	}
	for {
		tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT root_path,source_root,evidence_json,catalog_entry_id FROM source_catalog_finite_target WHERE listing_id=? AND prepared=0 ORDER BY root_path LIMIT 50`, listingID)
		if err != nil {
			release()
			return err
		}
		type target struct {
			root, sourceRoot, raw string
			id                    int64
		}
		batch := []target{}
		for rows.Next() {
			var t target
			if err = rows.Scan(&t.root, &t.sourceRoot, &t.raw, &t.id); err != nil {
				break
			}
			batch = append(batch, t)
		}
		if err == nil {
			err = rows.Err()
		}
		_ = rows.Close()
		// Reuse the bounded publisher forms: one trigger compilation for the
		// status update and one for admission, with the same 50 target limit.
		updates := make([]catalogEntryStatusUpdate, 0, len(batch))
		ids := make([]int64, 0, len(batch))
		for _, t := range batch {
			if err != nil {
				break
			}
			var evidence catalogSplitTarget
			err = json.Unmarshal([]byte(t.raw), &evidence)
			if err != nil {
				break
			}
			if evidence.HasEpisode {
				err = attachCatalogSeriesEvidenceTx(ctx, tx.Tx, t.id, sourceID, evidence.Episode)
			} else if t.sourceRoot == "" {
				err = attachCatalogSeriesForRootTx(ctx, tx.Tx, t.id, sourceID, t.root)
			}
			updates = append(updates, catalogEntryStatusUpdate{ID: t.id, Status: "available"})
			ids = append(ids, t.id)
			if err == nil {
				_, err = tx.ExecContext(ctx, `UPDATE source_catalog_finite_target SET prepared=1 WHERE listing_id=? AND root_path=?`, listingID, t.root)
			}
		}
		if err == nil {
			err = setCatalogEntryStatusesTx(ctx, tx.Tx, updates)
		}
		if err == nil {
			err = refreshCatalogEntryAdmissionsTx(ctx, tx.Tx, ids)
		}
		if err == nil {
			err = tx.Commit()
		}
		release()
		if err != nil {
			return err
		}
		if len(batch) < 50 {
			return nil
		}
	}
}
