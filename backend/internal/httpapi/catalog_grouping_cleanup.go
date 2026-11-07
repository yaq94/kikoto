package httpapi

import (
	"context"
	"database/sql"
	"errors"
)

func (s *Server) cleanupCatalogGroupingVersions(ctx context.Context, in catalogGroupingIndexInput) error {
	for {
		var id int64
		err := s.db.QueryRowContext(ctx, `SELECT id FROM source_catalog_grouping WHERE file_source_id=? AND root_path=? AND version<>? AND (version LIKE 'observation:%' OR NOT EXISTS(SELECT 1 FROM source_catalog_listing l WHERE ('listing:'||l.id)=source_catalog_grouping.version)) ORDER BY id LIMIT 1`, in.SourceID, in.Root, in.version()).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		queries := []string{
			`DELETE FROM source_catalog_duplicate_available WHERE (comparison_id,name) IN(SELECT m.comparison_id,m.name FROM source_catalog_duplicate_comparison c JOIN source_catalog_duplicate_available m ON m.comparison_id=c.id WHERE c.child_grouping=? OR c.parent_grouping=? LIMIT 64)`,
			`DELETE FROM source_catalog_duplicate_match WHERE (comparison_id,child_name) IN(SELECT m.comparison_id,m.child_name FROM source_catalog_duplicate_comparison c JOIN source_catalog_duplicate_match m ON m.comparison_id=c.id WHERE c.child_grouping=? OR c.parent_grouping=? LIMIT 64)`,
			`DELETE FROM source_catalog_duplicate_comparison WHERE id IN(SELECT id FROM source_catalog_duplicate_comparison WHERE child_grouping=? OR parent_grouping=? LIMIT 64)`,
			`DELETE FROM source_catalog_grouping_name WHERE grouping_id=? AND name IN(SELECT name FROM source_catalog_grouping_name WHERE grouping_id=? LIMIT 64)`,
			`DELETE FROM source_catalog_grouping_folder WHERE grouping_id=? AND name IN(SELECT name FROM source_catalog_grouping_folder WHERE grouping_id=? LIMIT 64)`,
			`DELETE FROM source_catalog_grouping_target WHERE grouping_id=? AND key IN(SELECT key FROM source_catalog_grouping_target WHERE grouping_id=? LIMIT 64)`,
			`DELETE FROM source_catalog_grouping_bucket WHERE grouping_id=? AND (kind,key) IN(SELECT kind,key FROM source_catalog_grouping_bucket WHERE grouping_id=? LIMIT 64)`,
		}
		for _, query := range queries {
			for {
				tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
				if err != nil {
					return err
				}
				result, err := tx.ExecContext(ctx, query, id, id)
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
				if n < 64 {
					break
				}
			}
		}
		if _, err = s.execCatalogWrite(ctx, `DELETE FROM source_catalog_grouping WHERE id=?`, id); err != nil {
			return err
		}
	}
}

// Derived finite targets are no longer needed once apply_cursor and applied
// state describe all source-owned writes. Drain them before the listing header
// can be reclaimed; a header's foreign-key cascade must not become a large tx.
func (s *Server) cleanupCatalogFiniteTargets(ctx context.Context, listingID int64) error {
	for _, table := range []struct{ name, key string }{{"source_catalog_finite_file", "input_path"}, {"source_catalog_finite_target", "root_path"}, {"source_catalog_finite_directory", "path"}} {
		for {
			tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
			if err != nil {
				return err
			}
			result, err := tx.ExecContext(ctx, `DELETE FROM `+table.name+` WHERE listing_id=? AND `+table.key+` IN(SELECT `+table.key+` FROM `+table.name+` WHERE listing_id=? LIMIT 64)`, listingID, listingID)
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
			if n < 64 {
				break
			}
		}
	}
	return nil
}
