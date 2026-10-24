package httpapi

import (
	"context"
	"log/slog"
	"path"
	"strings"

	"github.com/yexca/kikoto/backend/internal/openlist"
)

func logCatalogEpisodeMaintenance(operation string, err error) {
	slog.Error("catalog episode maintenance failed", "operation", operation, "error", err)
}

// persistCatalogEpisodeParentFiles writes the metadata-only parent snapshot in
// bounded transactions. It runs after the episode visibility switch; the
// parent has no audio, so its rows never become a second public card.
func (s *Server) persistCatalogEpisodeParentFiles(ctx context.Context, sourceID int64, parent string, files []openlist.File) (int64, error) {
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return 0, err
	}
	defer release()
	id, err := persistOpenListEntryTx(ctx, tx.Tx, sourceID, parent, "", path.Base(parent))
	if err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM source_catalog_relocation_candidate WHERE catalog_entry_id=?`, id); err != nil {
		return 0, err
	}
	if err = attachCatalogSeriesForRootTx(ctx, tx.Tx, id, sourceID, parent); err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	if err = s.deleteCatalogEntryFiles(ctx, id); err != nil {
		return 0, err
	}
	for start := 0; start < len(files); start += catalogEpisodeWriteRows {
		end := start + catalogEpisodeWriteRows
		if end > len(files) {
			end = len(files)
		}
		tx, release, err = beginTxWithDatabaseBusyRetry(ctx, s.db)
		if err != nil {
			return 0, err
		}
		for _, file := range files[start:end] {
			if err = persistOpenListFileTx(ctx, tx.Tx, id, file); err != nil {
				_ = tx.Rollback()
				release()
				return 0, err
			}
		}
		if err = tx.Commit(); err != nil {
			release()
			return 0, err
		}
		release()
	}
	return id, nil
}

func (s *Server) discardCatalogEpisodeOrphans(ctx context.Context, sourceID int64, parent string, roots []string) error {
	current := make(map[string]bool, len(roots))
	for _, root := range roots {
		current[root] = true
	}
	rows, err := s.db.QueryContext(ctx, `SELECT e.root_path
		FROM source_catalog_episode_pending p
		JOIN source_catalog_entry e ON e.id=p.catalog_entry_id
		WHERE p.source_id=? AND p.parent_root=? ORDER BY e.root_path`, sourceID, parent)
	if err != nil {
		return err
	}
	orphans := []string{}
	seen := map[string]bool{}
	for rows.Next() {
		var root string
		if err = rows.Scan(&root); err != nil {
			break
		}
		if !current[root] && !seen[root] {
			seen[root] = true
			orphans = append(orphans, root)
		}
	}
	readErr := rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	if readErr != nil {
		return readErr
	}
	if len(orphans) == 0 {
		return nil
	}
	return s.discardCatalogEpisodePendingRoots(ctx, sourceID, parent, orphans)
}

// deleteCatalogEpisodeMappings clears bookkeeping after the parent state row
// has already published the split. Mappings are deleted in bounded batches;
// leaving one behind is harmless because the published parent state no longer
// treats it as hidden.
func (s *Server) deleteCatalogEpisodeMappings(ctx context.Context, sourceID int64, parent string, entryIDs []int64) {
	for start := 0; start < len(entryIDs); start += catalogEpisodeWriteRows {
		end := start + catalogEpisodeWriteRows
		if end > len(entryIDs) {
			end = len(entryIDs)
		}
		tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
		if err != nil {
			logCatalogEpisodeMaintenance("pending mappings", err)
			return
		}
		placeholders := make([]string, end-start)
		args := make([]any, 0, end-start+2)
		args = append(args, sourceID, parent)
		for index, id := range entryIDs[start:end] {
			placeholders[index] = "?"
			args = append(args, id)
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM source_catalog_episode_pending
			WHERE source_id=? AND parent_root=? AND catalog_entry_id IN (`+
			strings.Join(placeholders, ",")+`)`, args...)
		if err == nil {
			err = tx.Commit()
		}
		release()
		if err != nil {
			logCatalogEpisodeMaintenance("pending mappings", err)
			return
		}
	}
}

// markCatalogEpisodeStaleCards updates at most 100 entry rows per transaction.
func (s *Server) markCatalogEpisodeStaleCards(ctx context.Context, sourceID int64, parent string, keep map[int64]bool) error {
	lastID := int64(0)
	for {
		tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT id FROM source_catalog_entry
			WHERE file_source_id=? AND relocated_to_id IS NULL AND root_path<>?
				AND (source_root=? OR root_path=? OR substr(root_path,1,length(?)+1)=? || '/')
				AND id>? ORDER BY id LIMIT ?`,
			sourceID, parent, parent, parent, parent, parent, lastID, catalogEpisodeWriteRows)
		if err != nil {
			release()
			return err
		}
		ids := []int64{}
		for rows.Next() {
			var id int64
			if err = rows.Scan(&id); err != nil {
				break
			}
			ids = append(ids, id)
		}
		readErr := rows.Err()
		_ = rows.Close()
		if err != nil {
			release()
			return err
		}
		if readErr != nil {
			release()
			return readErr
		}
		if len(ids) == 0 {
			if err = tx.Commit(); err != nil {
				release()
				return err
			}
			release()
			return nil
		}
		for _, id := range ids {
			lastID = id
			if keep[id] {
				continue
			}
			if err = setCatalogEntryStatusTx(ctx, tx.Tx, id, "missing"); err != nil {
				release()
				return err
			}
		}
		if err = tx.Commit(); err != nil {
			release()
			return err
		}
		release()
	}
}

// deleteCatalogEntryFiles removes at most 100 file rows per transaction.
func (s *Server) deleteCatalogEntryFiles(ctx context.Context, entryID int64) error {
	for {
		tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT rowid FROM source_catalog_file
			WHERE catalog_entry_id=? ORDER BY rowid LIMIT ?`, entryID, catalogEpisodeWriteRows)
		if err != nil {
			release()
			return err
		}
		ids := []int64{}
		for rows.Next() {
			var id int64
			if err = rows.Scan(&id); err != nil {
				break
			}
			ids = append(ids, id)
		}
		readErr := rows.Err()
		_ = rows.Close()
		if err != nil {
			release()
			return err
		}
		if readErr != nil {
			release()
			return readErr
		}
		if len(ids) == 0 {
			if err = tx.Commit(); err != nil {
				release()
				return err
			}
			release()
			return nil
		}
		placeholders := make([]string, len(ids))
		args := make([]any, len(ids))
		for index, id := range ids {
			placeholders[index] = "?"
			args[index] = id
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM source_catalog_file WHERE rowid IN (`+
			strings.Join(placeholders, ",")+`)`, args...); err != nil {
			release()
			return err
		}
		if err = tx.Commit(); err != nil {
			release()
			return err
		}
		release()
	}
}
