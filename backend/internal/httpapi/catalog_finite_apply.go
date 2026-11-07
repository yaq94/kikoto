package httpapi

import (
	"context"
	"path"
	"strings"

	"github.com/yexca/kikoto/backend/internal/openlist"
)

type catalogFiniteSelectedEntryKey struct{}

func (s *Server) applyCatalogFiniteListing(ctx context.Context, source remoteSourceForUse, root string, listingID, operation int64) error {
	selected, selectOnly := ctx.Value(catalogFiniteSelectedEntryKey{}).(catalogEntry)
	var count int
	var state string
	if err := s.db.QueryRowContext(ctx, `SELECT received,state FROM source_catalog_listing WHERE id=?`, listingID).Scan(&count, &state); err != nil {
		return err
	}
	if state == "applied" {
		return s.cleanupCatalogFiniteTargets(ctx, listingID)
	}
	var playable bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM source_catalog_listing_item WHERE listing_id=? AND kind='audio')`, listingID).Scan(&playable); err != nil {
		return err
	}
	if !playable {
		return errCatalogNoPlayableFiles
	}
	if count <= 500 {
		rows, err := s.db.QueryContext(ctx, `SELECT name,title,kind,size_bytes FROM source_catalog_listing_item WHERE listing_id=? ORDER BY name LIMIT 500`, listingID)
		if err != nil {
			return err
		}
		files := []openlist.File{}
		for rows.Next() {
			var f openlist.File
			if err = rows.Scan(&f.Path, &f.Title, &f.Kind, &f.Size); err != nil {
				break
			}
			files = append(files, f)
		}
		if err == nil {
			err = rows.Err()
		}
		_ = rows.Close()
		if err != nil {
			return err
		}
		playable := false
		for _, f := range files {
			if f.Kind == "audio" {
				playable = true
				break
			}
		}
		if !playable {
			return errCatalogNoPlayableFiles
		}
		if selectOnly {
			files = catalogSplitCardFiles(selected.RootPath, selected.SourceRoot, files)
			playable = false
			for _, f := range files {
				if f.Kind == "audio" {
					playable = true
					break
				}
			}
			if !playable {
				return errCatalogNoPlayableFiles
			}
			_, err = s.persistOpenListSnapshot(ctx, source.ID, selected.RootPath, selected.SourceRoot, selected.Title, files)
		} else {
			_, err = s.persistOpenListRoot(ctx, source, root, files)
		}
		if err != nil {
			return err
		}
		_, err = s.execCatalogWrite(ctx, `UPDATE source_catalog_listing SET state='applied' WHERE id=?`, listingID)
		if err == nil {
			err = s.cleanupCatalogFiniteTargets(ctx, listingID)
		}
		return err
	}
	grouping, err := s.prepareCatalogGrouping(ctx, catalogGroupingIndexInput{SourceID: source.ID, Root: root, ListingID: listingID})
	if err != nil {
		return err
	}
	var mode string
	if err = s.db.QueryRowContext(ctx, `SELECT mode FROM source_catalog_grouping WHERE id=?`, grouping).Scan(&mode); err != nil {
		return err
	}
	var pinned int
	if err = s.db.QueryRowContext(ctx, catalogSplitPinnedSQL, source.ID, root).Scan(&pinned); err != nil {
		return err
	}
	children, err := s.finiteEpisodeChildren(ctx, listingID, root)
	if err != nil {
		return err
	}
	if pinned != 0 {
		children = nil
	}
	if pinned != 0 || (mode != catalogWorkSplitEpisode && catalogDirectoryCode(root) != "") {
		mode = catalogWorkSplitSingle
	}
	plan := catalogSplitPlan{root: root, groupingID: grouping}
	after := ""
	if err = s.db.QueryRowContext(ctx, `SELECT partition_cursor FROM source_catalog_listing WHERE id=?`, listingID).Scan(&after); err != nil {
		return err
	}
	for {
		tx, release, e := beginTxWithDatabaseBusyRetry(ctx, s.db)
		if e != nil {
			return e
		}
		rows, e := tx.QueryContext(ctx, `SELECT name,title,kind,size_bytes FROM source_catalog_listing_item WHERE listing_id=? AND name>? ORDER BY name LIMIT 64`, listingID, after)
		if e != nil {
			release()
			return e
		}
		batch := []openlist.File{}
		for rows.Next() {
			var f openlist.File
			if e = rows.Scan(&f.Path, &f.Title, &f.Kind, &f.Size); e != nil {
				break
			}
			batch = append(batch, f)
		}
		if e == nil {
			e = rows.Err()
		}
		_ = rows.Close()
		for _, f := range batch {
			if e != nil {
				break
			}
			target := catalogSplitTarget{Root: root, Title: path.Base(root)}
			relative := f.Path
			if len(children) > 0 {
				if f.Kind == "folder" {
					after = f.Path
					continue
				}
				if child, rel, ok := finiteChildFileTarget(children, f.Path); ok {
					target, relative = child, rel
				}
			} else if mode != catalogWorkSplitSingle && f.Kind == "audio" {
				var split bool
				var candidate catalogSplitTarget
				candidate, split, e = plan.indexedTargetFor(ctx, tx.Tx, f.Path)
				if e != nil {
					break
				}
				if split {
					target = candidate
				}
			}
			if selectOnly && target.Root != selected.RootPath {
				after = f.Path
				continue
			}
			_, e = tx.ExecContext(ctx, `INSERT INTO source_catalog_finite_target(listing_id,root_path,source_root,title,evidence_json) VALUES(?,?,?,?,?) ON CONFLICT DO NOTHING`, listingID, target.Root, target.SourceRoot, target.Title, mustJSON(target))
			if e == nil {
				_, e = tx.ExecContext(ctx, `INSERT INTO source_catalog_finite_file(listing_id,input_path,root_path,relative_path) VALUES(?,?,?,?) ON CONFLICT DO NOTHING`, listingID, f.Path, target.Root, relative)
			}
			after = f.Path
		}
		if e == nil && len(batch) > 0 {
			_, e = tx.ExecContext(ctx, `UPDATE source_catalog_listing SET partition_cursor=? WHERE id=?`, batch[len(batch)-1].Path, listingID)
		}
		if e == nil {
			e = tx.Commit()
		}
		release()
		if e != nil {
			return e
		}
		if len(batch) < 64 {
			break
		}
	}
	if err = s.applyCatalogFiniteTargets(ctx, source.ID, listingID); err != nil {
		return err
	}
	var first int64
	if err = s.db.QueryRowContext(ctx, `SELECT COALESCE(min(catalog_entry_id),0) FROM source_catalog_finite_target WHERE listing_id=?`, listingID).Scan(&first); err != nil {
		return err
	}
	if first == 0 {
		return errCatalogNoPlayableFiles
	}
	if !selectOnly {
		err = s.markCatalogFiniteStale(ctx, source.ID, root, listingID)
	}
	if err != nil {
		return err
	}
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return err
	}
	defer release()
	var parentKept int
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM source_catalog_finite_target WHERE listing_id=? AND root_path=?)`, listingID, root).Scan(&parentKept); err != nil {
		return err
	}
	if parentKept == 0 && !selectOnly {
		if err = retireCatalogSplitParentTx(ctx, tx.Tx, source.ID, root, first); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE source_catalog_listing SET state='applied' WHERE id=?`, listingID)
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	release()
	return s.cleanupCatalogFiniteTargets(ctx, listingID)
}

func (s *Server) markCatalogFiniteStale(ctx context.Context, sourceID int64, root string, listingID int64) error {
	var after int64
	for {
		tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT id FROM source_catalog_entry e WHERE file_source_id=? AND id>? AND relocated_to_id IS NULL AND root_path<>? AND (source_root=? OR (root_path>=? AND root_path<?)) AND NOT EXISTS(SELECT 1 FROM source_catalog_finite_target t WHERE t.listing_id=? AND t.catalog_entry_id=e.id) ORDER BY id LIMIT 64`, sourceID, after, root, root, strings.TrimRight(root, "/")+"/", strings.TrimRight(root, "/")+"0", listingID)
		if err != nil {
			release()
			return err
		}
		updates := []catalogEntryStatusUpdate{}
		for rows.Next() {
			var id int64
			if err = rows.Scan(&id); err != nil {
				break
			}
			updates = append(updates, catalogEntryStatusUpdate{ID: id, Status: "missing"})
			after = id
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
		if len(updates) < 64 {
			return nil
		}
	}
}
