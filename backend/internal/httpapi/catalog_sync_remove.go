package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"path"
	"strings"
)

// A complete listing reconciles the stored files once, not once per removed
// child. Limit rows before evaluating derived locations so even a sparse match
// releases the writer and persists progress after a bounded amount of work.
func (s *Server) removeCatalogSyncListingFiles(ctx context.Context, sourceID, generation int64, dir catalogSyncDirectoryState, options catalogIndexOptions) error {
	var listingID, after int64
	var complete bool
	err := s.db.QueryRowContext(ctx, `SELECT id,
	 CASE WHEN json_type(NULLIF(remove_cursor,''),'$.name') IS NULL THEN COALESCE(json_extract(NULLIF(remove_cursor,''),'$.fileAfter'),0) ELSE 0 END,
	 COALESCE(json_extract(NULLIF(remove_cursor,''),'$.filesComplete'),0)
	 FROM source_catalog_listing WHERE file_source_id=? AND directory_path=? AND generation=? AND base_revision=? AND state IN ('complete','applying') ORDER BY id DESC LIMIT 1`, sourceID, dir.Path, generation, dir.Revision).Scan(&listingID, &after, &complete)
	if err != nil || complete {
		return err
	}
	for {
		if err = s.checkCatalogSyncBatch(ctx, options); err != nil {
			return err
		}
		next, done, batchErr := s.removeCatalogSyncListingFileBatch(ctx, sourceID, listingID, after, dir)
		if batchErr != nil || done {
			return batchErr
		}
		after = next
	}
}

func (s *Server) removeCatalogSyncListingFileBatch(ctx context.Context, sourceID, listingID, after int64, dir catalogSyncDirectoryState) (int64, bool, error) {
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return after, false, err
	}
	defer release()
	rows, err := tx.QueryContext(ctx, `WITH candidate AS MATERIALIZED (
	 SELECT rowid AS id,catalog_entry_id,relative_path,source_root FROM source_catalog_file
	 WHERE rowid>? ORDER BY rowid LIMIT ?
	) SELECT f.id,e.file_source_id,f.relative_path,
	 CASE WHEN f.source_root<>'' THEN f.source_root WHEN e.source_root<>'' THEN e.source_root ELSE e.root_path END
	 FROM candidate f JOIN source_catalog_entry e ON e.id=f.catalog_entry_id ORDER BY f.id`, after, catalogSyncListingWriteRows)
	if err != nil {
		return after, false, err
	}
	type candidate struct {
		id, source     int64
		relative, root string
	}
	files := make([]candidate, 0, catalogSyncListingWriteRows)
	for rows.Next() {
		var file candidate
		var source sql.NullInt64
		if err = rows.Scan(&file.id, &source, &file.relative, &file.root); err != nil {
			break
		}
		file.source = source.Int64
		files = append(files, file)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return after, false, err
	}
	for _, file := range files {
		after = file.id
		if file.source != sourceID {
			continue
		}
		relative, inside := strings.CutPrefix(path.Join(file.root, file.relative), strings.TrimRight(dir.Path, "/")+"/")
		if !inside {
			continue
		}
		name, _, nested := strings.Cut(relative, "/")
		var kind string
		err = tx.QueryRowContext(ctx, `SELECT kind FROM source_catalog_observation WHERE file_source_id=? AND directory_path=? AND name=? AND revision<>?`, sourceID, dir.Path, name, dir.Revision+1).Scan(&kind)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return after, false, err
		}
		if nested && kind != "folder" {
			continue
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM source_catalog_file WHERE rowid=?`, file.id); err != nil {
			return after, false, err
		}
	}
	done := len(files) < catalogSyncListingWriteRows
	if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_listing SET remove_cursor=json_object('fileAfter',?,'filesComplete',?) WHERE id=?`, after, done, listingID); err != nil {
		return after, false, err
	}
	return after, done, tx.Commit()
}
