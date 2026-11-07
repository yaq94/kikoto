package httpapi

import (
	"context"
	"database/sql"
	"errors"
)

// One entry can have thousands of already registered media locations. Their
// availability is a source projection, prepared in keyset batches before the
// publication marker, independently of the bounded entry lifecycle batch.
func (s *Server) prepareCatalogPublicationMedia(ctx context.Context, operation int64) error {
	for {
		tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
		if err != nil {
			return err
		}
		var entry, after int64
		err = tx.QueryRowContext(ctx, `SELECT entity_id,after_location_id FROM source_catalog_publication_entity WHERE operation_id=? AND entity_kind='entry' AND media_prepared=0 ORDER BY entity_id LIMIT 1`, operation).Scan(&entry, &after)
		if errors.Is(err, sql.ErrNoRows) {
			release()
			return nil
		}
		if err != nil {
			release()
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT l.id,CASE WHEN e.id IS NULL OR e.file_source_id IS NULL OR e.availability_status IN ('missing','excluded') THEN 'unavailable'
		 WHEN e.availability_status='available' THEN CASE WHEN EXISTS(SELECT 1 FROM source_catalog_file f WHERE f.catalog_entry_id=e.id AND f.kind='audio' AND m.fingerprint='catalog:'||e.id||':'||f.relative_path) THEN 'available' ELSE 'unavailable' END ELSE l.availability END
		 FROM media_item m JOIN media_file_location l ON l.media_item_id=m.id LEFT JOIN source_catalog_entry e ON e.id=?
		 WHERE m.fingerprint>=('catalog:'||?||':') AND m.fingerprint<('catalog:'||?||';') AND l.location_type='catalog_stream'
		 AND l.file_source_id=(SELECT file_source_id FROM source_catalog_publication WHERE id=?) AND l.id>? ORDER BY l.id LIMIT 64`, entry, entry, entry, operation, after)
		if err != nil {
			release()
			return err
		}
		type change struct {
			id     int64
			status string
		}
		batch := []change{}
		for rows.Next() {
			var c change
			if err = rows.Scan(&c.id, &c.status); err != nil {
				break
			}
			batch = append(batch, c)
		}
		if err == nil {
			err = rows.Err()
		}
		_ = rows.Close()
		for _, c := range batch {
			if err != nil {
				break
			}
			_, err = tx.ExecContext(ctx, `UPDATE media_file_location SET availability=? WHERE id=? AND availability<>?`, c.status, c.id, c.status)
		}
		if len(batch) > 0 {
			after = batch[len(batch)-1].id
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE source_catalog_publication_entity SET after_location_id=?,media_prepared=? WHERE operation_id=? AND entity_kind='entry' AND entity_id=?`, after, len(batch) < 64, operation, entry)
		}
		if err == nil {
			err = tx.Commit()
		}
		release()
		if err != nil {
			return err
		}
		if err = pauseCatalogWriter(ctx); err != nil {
			return err
		}
	}
}

func (s *Server) prepareCatalogPublicationRetirements(ctx context.Context, operation int64) error {
	for {
		tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
		if err != nil {
			return err
		}
		var entry int64
		var kind string
		err = tx.QueryRowContext(ctx, `SELECT entity_id,entity_kind FROM source_catalog_publication_entity
			WHERE operation_id=? AND entity_kind IN ('retired_aliases','retired_files') ORDER BY entity_kind,entity_id LIMIT 1`, operation).Scan(&entry, &kind)
		if errors.Is(err, sql.ErrNoRows) {
			release()
			return nil
		}
		if err != nil {
			release()
			return err
		}
		var result sql.Result
		if kind == "retired_aliases" {
			// The retired row holds the stable successor. Updating a batch removes
			// it from this indexed pending set, so retries never scan a consumed
			// prefix or advance a cursor without its corresponding writes.
			result, err = tx.ExecContext(ctx, `UPDATE source_catalog_entry
				SET relocated_to_id=(SELECT relocated_to_id FROM source_catalog_entry WHERE id=?)
				WHERE id IN (SELECT id FROM source_catalog_entry WHERE relocated_to_id=?
				AND id<>(SELECT relocated_to_id FROM source_catalog_entry WHERE id=?) ORDER BY id LIMIT 64)`, entry, entry, entry)
		} else {
			result, err = tx.ExecContext(ctx, `DELETE FROM source_catalog_file WHERE rowid IN(SELECT rowid FROM source_catalog_file WHERE catalog_entry_id=? ORDER BY relative_path LIMIT 64)`, entry)
		}
		var count int64
		if err == nil {
			count, err = result.RowsAffected()
		}
		if err == nil && count < 64 {
			_, err = tx.ExecContext(ctx, `DELETE FROM source_catalog_publication_entity WHERE operation_id=? AND entity_kind=? AND entity_id=?`, operation, kind, entry)
		}
		if err == nil {
			err = tx.Commit()
		}
		release()
		if err != nil {
			return err
		}
		if err = pauseCatalogWriter(ctx); err != nil {
			return err
		}
	}
}
