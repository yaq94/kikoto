package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/yexca/kikoto/backend/internal/openlist"
)

const catalogEpisodeWriteRows = 100

// Episode publication has three explicit states. Staging creates or refreshes
// an entry together with its pending mapping. Reading a mapping lets activation
// assign the public source identity while the entry remains hidden. Only
// flipping the parent publication row in the final transaction publishes the
// complete split.
var (
	errCatalogEpisodeParentPinned    = errors.New("catalog episode parent retains its published identity")
	errCatalogEpisodePendingConflict = errors.New("catalog episode pending identity needs review")
)

type catalogEpisodeStage struct {
	root   string
	entry  bool
	files  []openlist.File
	writes int
}

// catalogEpisodeRootStateTx makes the split decision in the write transaction.
// A work, a series membership, or an inbound alias pins the container to its
// existing identity; a later listing cannot relocate that evaluation object.
func catalogEpisodeRootStateTx(ctx context.Context, tx *sql.Tx, sourceID int64, root string) (existing int64, pinned bool, err error) {
	err = tx.QueryRowContext(ctx, `SELECT id FROM source_catalog_entry
		WHERE file_source_id=? AND root_path=? AND relocated_to_id IS NULL`, sourceID, root).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM source_catalog_entry e WHERE e.id=? AND (
			e.work_id IS NOT NULL
			OR EXISTS(SELECT 1 FROM catalog_series_member m WHERE m.catalog_entry_id=e.id)
			OR EXISTS(SELECT 1 FROM source_catalog_entry r WHERE r.relocated_to_id=e.id)))`, existing).Scan(&pinned); err != nil {
		return 0, false, err
	}
	return existing, pinned, nil
}

func planCatalogEpisodeStages(roots []string, split map[string]catalogEpisodeRootSplit) []catalogEpisodeStage {
	stages := []catalogEpisodeStage{}
	var current *catalogEpisodeStage
	flush := func() {
		if current != nil {
			stages = append(stages, *current)
			current = nil
		}
	}
	for _, root := range roots {
		part := split[root]
		if current == nil || current.root != root {
			flush()
			current = &catalogEpisodeStage{root: root, entry: true, writes: 1}
		}
		for _, file := range part.Files {
			if current == nil || current.root != root || current.writes == catalogEpisodeWriteRows {
				flush()
				current = &catalogEpisodeStage{root: root}
			}
			current.files = append(current.files, file)
			current.writes++
		}
	}
	flush()
	return stages
}

func (s *Server) persistCatalogEpisodeRoots(ctx context.Context, source remoteSourceForUse, root string, files []openlist.File) (first int64, splitResult bool, err error) {
	split, parentFiles := splitCatalogEpisodeRoots(root, files)
	if len(split) == 0 {
		return 0, false, s.discardCatalogEpisodePending(ctx, source.ID, root, nil)
	}
	roots := make([]string, 0, len(split))
	for child := range split {
		roots = append(roots, child)
	}
	sort.Strings(roots)
	if _, pinned, stateErr := s.catalogEpisodeRootState(ctx, source.ID, root); stateErr != nil {
		return 0, false, stateErr
	} else if pinned {
		return 0, false, s.discardCatalogEpisodePending(ctx, source.ID, root, nil)
	}
	// A failed later stage or activation removes only the recoverable pending
	// rows for this snapshot. The database contains no public half-split, and
	// an unsuccessful compensation leaves those rows for the next complete scan.
	defer func() {
		if err == nil {
			return
		}
		if cleanupErr := s.discardCatalogEpisodePending(ctx, source.ID, root, roots); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("compensate catalog episode staging: %w", cleanupErr))
		}
	}()
	if err = s.beginCatalogEpisodePublication(ctx, source.ID, root); err != nil {
		return 0, true, err
	}
	if err = s.clearCatalogEpisodePendingFiles(ctx, source.ID, root); err != nil {
		return 0, true, err
	}
	stages := planCatalogEpisodeStages(roots, split)
	for start := 0; start < len(stages); start++ {
		end := start
		writes := 0
		for end < len(stages) && writes+stages[end].writes <= catalogEpisodeWriteRows {
			writes += stages[end].writes
			end++
		}
		pinned, stageErr := s.stageCatalogEpisodeBatch(ctx, source.ID, root, stages[start:end], split)
		if stageErr != nil {
			return 0, true, stageErr
		}
		if pinned {
			return 0, false, s.discardCatalogEpisodePending(ctx, source.ID, root, nil)
		}
	}
	first, err = s.activateCatalogEpisodeRoots(ctx, source.ID, root, roots, parentFiles)
	if errors.Is(err, errCatalogEpisodeParentPinned) {
		return 0, false, s.discardCatalogEpisodePending(ctx, source.ID, root, nil)
	}
	return first, true, err
}

func (s *Server) beginCatalogEpisodePublication(ctx context.Context, sourceID int64, parent string) error {
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return err
	}
	defer release()
	// A published split keeps its identity even if an older build left pending
	// mappings behind. Finish that cleanup before the state row is reset to
	// pending; otherwise the next clearCatalogEpisodePendingFiles pass would
	// treat live files as staged rows and delete them.
	var state string
	err = tx.QueryRowContext(ctx, `SELECT state FROM source_catalog_episode_publication
		WHERE source_id=? AND parent_root=?`, sourceID, parent).Scan(&state)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if state == "published" {
		if _, err = tx.ExecContext(ctx, `DELETE FROM source_catalog_episode_pending
			WHERE source_id=? AND parent_root=?`, sourceID, parent); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO source_catalog_episode_publication(
			source_id,parent_root,state,updated_at) VALUES(?,?,'pending',CURRENT_TIMESTAMP)
		ON CONFLICT(source_id,parent_root) DO UPDATE SET state='pending',updated_at=CURRENT_TIMESTAMP`,
		sourceID, parent); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Server) clearCatalogEpisodePublication(ctx context.Context, sourceID int64, parent string) error {
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return err
	}
	defer release()
	// A published split is immutable until a later staging pass explicitly
	// re-opens it. Compensation for a failed retry must not erase that state:
	// the leftover pending mappings are the only record that would otherwise
	// make the next clear pass delete already published files.
	if _, err = tx.ExecContext(ctx, `DELETE FROM source_catalog_episode_publication
		WHERE source_id=? AND parent_root=? AND state<>'published'`, sourceID, parent); err != nil {
		return err
	}
	return tx.Commit()
}

// clearCatalogEpisodePendingFiles replaces each hidden snapshot instead of
// merging a new listing into stale staged rows. Every transaction removes at
// most 100 file entries.
func (s *Server) clearCatalogEpisodePendingFiles(ctx context.Context, sourceID int64, parent string) error {
	for {
		removed, err := s.clearCatalogEpisodePendingFilesBatch(ctx, sourceID, parent)
		if err != nil {
			return err
		}
		if removed < catalogEpisodeWriteRows {
			return nil
		}
	}
}

func (s *Server) clearCatalogEpisodePendingFilesBatch(ctx context.Context, sourceID int64, parent string) (int, error) {
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return 0, err
	}
	defer release()
	rows, err := tx.QueryContext(ctx, `SELECT f.rowid FROM source_catalog_file f
		JOIN source_catalog_episode_pending p ON p.catalog_entry_id=f.catalog_entry_id
		WHERE p.source_id=? AND p.parent_root=?
			AND NOT EXISTS(SELECT 1 FROM source_catalog_episode_publication publication
				WHERE publication.source_id=p.source_id AND publication.parent_root=p.parent_root
					AND publication.state='published')
		ORDER BY f.rowid LIMIT ?`,
		sourceID, parent, catalogEpisodeWriteRows)
	if err != nil {
		return 0, err
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
		return 0, err
	}
	if readErr != nil {
		return 0, readErr
	}
	if len(ids) > 0 {
		if _, err = tx.ExecContext(ctx, `DELETE FROM source_catalog_file WHERE rowid IN (`+
			strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`)`, int64Args(ids)...); err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return len(ids), nil
}

// catalogEpisodeRootState is only an early read. Every stage and the final
// activation repeat the check inside their bounded write transaction.
func (s *Server) catalogEpisodeRootState(ctx context.Context, sourceID int64, root string) (int64, bool, error) {
	var existing int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM source_catalog_entry
		WHERE file_source_id=? AND root_path=? AND relocated_to_id IS NULL`, sourceID, root).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	var busy int
	if err = s.db.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM source_catalog_entry e WHERE e.id=? AND (
			e.work_id IS NOT NULL
			OR EXISTS(SELECT 1 FROM catalog_series_member m WHERE m.catalog_entry_id=e.id)
			OR EXISTS(SELECT 1 FROM source_catalog_entry r WHERE r.relocated_to_id=e.id)))`, existing).Scan(&busy); err != nil {
		return 0, false, err
	}
	return existing, busy != 0, nil
}

func (s *Server) stageCatalogEpisodeBatch(ctx context.Context, sourceID int64, parent string, stages []catalogEpisodeStage, split map[string]catalogEpisodeRootSplit) (bool, error) {
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return false, err
	}
	defer release()
	_, pinned, err := catalogEpisodeRootStateTx(ctx, tx.Tx, sourceID, parent)
	if err != nil || pinned {
		return pinned, err
	}
	for _, stage := range stages {
		if _, stageErr := stageCatalogEpisodeRootTx(ctx, tx.Tx, sourceID, parent, stage.root, stage.entry, stage.files, split); stageErr != nil {
			return false, stageErr
		}
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return false, nil
}

func stageCatalogEpisodeRootTx(ctx context.Context, tx *sql.Tx, sourceID int64, parent, root string, entry bool, files []openlist.File, split map[string]catalogEpisodeRootSplit) (int64, error) {
	part := split[root]
	id, pending, err := catalogEpisodeEntryTx(ctx, tx, sourceID, parent, root)
	if err != nil {
		return 0, err
	}
	if entry {
		if pending {
			// An entry without a public source and with excluded availability is
			// absent from source search, scans, series cards, and work admission.
			// Parsing reuses the ordinary snapshot code rule; it never
			// registers a work or invents a release number.
			title := part.Title
			if title == "" {
				title = path.Base(root)
			}
			if id == 0 {
				err = tx.QueryRowContext(ctx, `INSERT INTO source_catalog_entry(
					file_source_id,root_path,source_root,primary_code,title,availability_status)
					VALUES(NULL,?,?,?,?,'excluded') RETURNING id`, root, part.SourceRoot, catalogEntryCode(root), title).Scan(&id)
			} else {
				var refreshed sql.Result
				refreshed, err = tx.ExecContext(ctx, `UPDATE source_catalog_entry
					SET file_source_id=NULL,source_root=?,primary_code=?,title=?,availability_status='excluded',
						status_checked_at=NULL,content_fingerprint='',layout_fingerprint='',updated_at=CURRENT_TIMESTAMP
						WHERE id=? AND (file_source_id IS NULL OR file_source_id=?)
						AND work_id IS NULL AND relocated_to_id IS NULL`,
					part.SourceRoot, catalogEntryCode(root), title, id, sourceID)
				if err == nil {
					var affected int64
					if affected, err = refreshed.RowsAffected(); err == nil && affected != 1 {
						err = fmt.Errorf("%w: pending entry %d changed identity during refresh", errCatalogEpisodePendingConflict, id)
					}
				}
			}
		} else {
			id, err = persistOpenListEntryTx(ctx, tx, sourceID, root, part.SourceRoot, part.Title)
		}
		if err != nil {
			return 0, err
		}
		if pending {
			if _, err = tx.ExecContext(ctx, `INSERT INTO source_catalog_episode_pending(source_id,parent_root,catalog_entry_root,catalog_entry_id)
				VALUES(?,?,?,?) ON CONFLICT(source_id,parent_root,catalog_entry_id) DO NOTHING`, sourceID, parent, root, id); err != nil {
				return 0, err
			}
		}
		if err = attachCatalogSeriesEvidenceTx(ctx, tx, id, sourceID, part.Evidence); err != nil {
			return 0, err
		}
	}
	for _, file := range files {
		if err = persistOpenListFileTx(ctx, tx, id, file); err != nil {
			return 0, err
		}
	}
	return id, nil
}

func catalogEpisodeEntryTx(ctx context.Context, tx *sql.Tx, sourceID int64, parent, root string) (id int64, pending bool, err error) {
	var publicSource, pendingSource sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT e.id,e.file_source_id,p.source_id
		FROM source_catalog_episode_pending p JOIN source_catalog_entry e ON e.id=p.catalog_entry_id
		WHERE p.source_id=? AND p.parent_root=? AND p.catalog_entry_root=? AND e.root_path=? AND e.relocated_to_id IS NULL`,
		sourceID, parent, root, root).Scan(&id, &publicSource, &pendingSource)
	if err == nil {
		if !pendingSource.Valid || (publicSource.Valid && publicSource.Int64 != sourceID) {
			return 0, false, fmt.Errorf("%w: pending entry %d", errCatalogEpisodePendingConflict, id)
		}
		return id, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, false, err
	}
	err = tx.QueryRowContext(ctx, `SELECT id FROM source_catalog_entry
		WHERE file_source_id=? AND root_path=? AND relocated_to_id IS NULL`, sourceID, root).Scan(&id)
	if err == nil {
		return id, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, false, err
	}
	return 0, true, nil
}

func (s *Server) activateCatalogEpisodeRoots(ctx context.Context, sourceID int64, parent string, roots []string, parentFiles []openlist.File) (int64, error) {
	var first int64
	for start := 0; start < len(roots); start += catalogEpisodeWriteRows {
		end := start + catalogEpisodeWriteRows
		if end > len(roots) {
			end = len(roots)
		}
		batchFirst, err := s.activateCatalogEpisodeBatch(ctx, sourceID, parent, roots[start:end])
		if err != nil {
			return 0, err
		}
		if first == 0 {
			first = batchFirst
		}
	}
	if first == 0 {
		return 0, errors.New("catalog episode activation produced no catalog identity")
	}
	return s.finalizeCatalogEpisodeRoots(ctx, sourceID, parent, roots, parentFiles)
}

func (s *Server) activateCatalogEpisodeBatch(ctx context.Context, sourceID int64, parent string, roots []string) (int64, error) {
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return 0, err
	}
	defer release()
	_, pinned, err := catalogEpisodeRootStateTx(ctx, tx.Tx, sourceID, parent)
	if err != nil {
		return 0, err
	}
	if pinned {
		return 0, errCatalogEpisodeParentPinned
	}
	var first int64
	for _, root := range roots {
		id, pending, lookupErr := catalogEpisodeEntryTx(ctx, tx.Tx, sourceID, parent, root)
		if lookupErr != nil {
			return 0, lookupErr
		}
		if id == 0 {
			return 0, fmt.Errorf("catalog episode %q has neither an active nor a pending entry", root)
		}
		if pending {
			// A previous attempt may have committed this hidden activation.
			// Keep the pending mapping: the final visibility transaction
			// removes every mapping together, so no partial split is public.
			var active int64
			lookupErr = tx.Tx.QueryRowContext(ctx, `SELECT id FROM source_catalog_entry
				WHERE file_source_id=? AND root_path=? AND relocated_to_id IS NULL`, sourceID, root).Scan(&active)
			if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
				return 0, lookupErr
			}
			if active != 0 && active != id {
				if err = deleteCatalogPendingEntryTx(ctx, tx.Tx, sourceID, parent, root, id, false); err != nil {
					return 0, err
				}
				id = active
			} else {
				result, updateErr := tx.Tx.ExecContext(ctx, `UPDATE source_catalog_entry
					SET file_source_id=?,availability_status='available',status_checked_at=CURRENT_TIMESTAMP,
						content_fingerprint='',layout_fingerprint='',updated_at=CURRENT_TIMESTAMP
					WHERE id=? AND (file_source_id IS NULL OR file_source_id=?) AND work_id IS NULL
						AND relocated_to_id IS NULL`, sourceID, id, sourceID)
				if updateErr != nil {
					return 0, updateErr
				}
				affected, updateErr := result.RowsAffected()
				if updateErr != nil || affected != 1 {
					return 0, fmt.Errorf("activate pending entry %d: affected=%d err=%v", id, affected, updateErr)
				}
			}
		}
		if first == 0 {
			first = id
		}
	}
	// The parent can become pinned between the batch's opening check and these
	// writes. Roll back the whole batch before reporting the conflict.
	_, pinned, err = catalogEpisodeRootStateTx(ctx, tx.Tx, sourceID, parent)
	if err != nil {
		return 0, err
	}
	if pinned {
		_ = tx.Rollback()
		return 0, errCatalogEpisodeParentPinned
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	if first == 0 {
		return 0, errors.New("catalog episode activation produced no catalog identity")
	}
	return first, nil
}

func (s *Server) finalizeCatalogEpisodeRoots(ctx context.Context, sourceID int64, parent string, roots []string, parentFiles []openlist.File) (int64, error) {
	if err := s.discardCatalogEpisodeOrphans(ctx, sourceID, parent, roots); err != nil {
		return 0, err
	}
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return 0, err
	}
	defer release()
	existing, pinned, err := catalogEpisodeRootStateTx(ctx, tx.Tx, sourceID, parent)
	if err != nil {
		return 0, err
	}
	if pinned {
		return 0, errCatalogEpisodeParentPinned
	}
	keep := map[int64]bool{}
	var first int64
	for _, root := range roots {
		id, pending, lookupErr := catalogEpisodeEntryTx(ctx, tx.Tx, sourceID, parent, root)
		if lookupErr != nil {
			return 0, lookupErr
		}
		if id == 0 {
			return 0, fmt.Errorf("catalog episode %q has neither an active nor a pending entry", root)
		}
		if pending {
			var activated sql.NullInt64
			if lookupErr = tx.QueryRowContext(ctx, `SELECT file_source_id FROM source_catalog_entry WHERE id=?`, id).Scan(&activated); lookupErr != nil {
				return 0, lookupErr
			}
			if !activated.Valid || activated.Int64 != sourceID {
				return 0, fmt.Errorf("%w: pending entry %d is not ready for publication", errCatalogEpisodePendingConflict, id)
			}
		}
		keep[id] = true
		if first == 0 {
			first = id
		}
	}
	// Every ready entry still has its pending mapping. One parent state row
	// flips the complete split public without touching each entry again.
	result, err := tx.ExecContext(ctx, `UPDATE source_catalog_episode_publication
		SET state='published',updated_at=CURRENT_TIMESTAMP
		WHERE source_id=? AND parent_root=? AND state='pending'`, sourceID, parent)
	if err != nil {
		return 0, err
	}
	if affected, affectedErr := result.RowsAffected(); affectedErr != nil || affected != 1 {
		return 0, fmt.Errorf("publish catalog episode state: affected=%d err=%v", affected, affectedErr)
	}
	// The mappings are bookkeeping for the visibility gate, so they leave in the
	// same transaction as the published state. A separate cleanup step could
	// fail after the split became public and leave rows that a later staging
	// pass would mistake for hidden files.
	if _, err = tx.ExecContext(ctx, `DELETE FROM source_catalog_episode_pending
		WHERE source_id=? AND parent_root=?`, sourceID, parent); err != nil {
		return 0, err
	}
	if existing != 0 && len(parentFiles) == 0 {
		if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_entry SET file_source_id=NULL,relocated_to_id=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, first, existing); err != nil {
			return 0, err
		}
	}
	if first == 0 {
		return 0, errors.New("catalog episode activation produced no catalog identity")
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	if len(parentFiles) > 0 {
		parentID, persistErr := s.persistCatalogEpisodeParentFiles(ctx, sourceID, parent, parentFiles)
		if persistErr != nil {
			logCatalogEpisodeMaintenance("parent metadata", persistErr)
		} else {
			keep[parentID] = true
		}
	}
	if err = s.markCatalogEpisodeStaleCards(ctx, sourceID, parent, keep); err != nil {
		logCatalogEpisodeMaintenance("stale cards", err)
	}
	if existing != 0 && len(parentFiles) == 0 {
		if err = s.deleteCatalogEntryFiles(ctx, existing); err != nil {
			logCatalogEpisodeMaintenance("relocated parent files", err)
		}
	}
	return first, nil
}

// discardCatalogEpisodePending bounds compensation by the complete source,
// parent, and known child roots. catalog_series_member deliberately restricts
// entry deletion, so its pending-only rows are removed first; file rows then
// cascade with the entry. Public cards and personal records are never targets.
func (s *Server) discardCatalogEpisodePending(ctx context.Context, sourceID int64, parent string, roots []string) error {
	return s.discardCatalogEpisodePendingState(ctx, sourceID, parent, roots, true)
}

func (s *Server) discardCatalogEpisodePendingRoots(ctx context.Context, sourceID int64, parent string, roots []string) error {
	return s.discardCatalogEpisodePendingState(ctx, sourceID, parent, roots, false)
}

func (s *Server) discardCatalogEpisodePendingState(ctx context.Context, sourceID int64, parent string, roots []string, clearPublication bool) (err error) {
	if clearPublication {
		defer func() {
			if publicationErr := s.clearCatalogEpisodePublication(ctx, sourceID, parent); publicationErr != nil {
				err = errors.Join(err, fmt.Errorf("clear catalog episode publication: %w", publicationErr))
			}
		}()
	}
	allowed := map[string]bool{}
	for _, root := range roots {
		allowed[root] = true
	}
	lastID := int64(0)
	for {
		scanned, nextID, batchErr := s.discardCatalogEpisodePendingBatch(ctx, sourceID, parent, allowed, lastID)
		if batchErr != nil {
			return batchErr
		}
		if scanned == 0 {
			return
		}
		lastID = nextID
	}
}

func (s *Server) discardCatalogEpisodePendingBatch(ctx context.Context, sourceID int64, parent string, allowed map[string]bool, afterID int64) (int, int64, error) {
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return 0, afterID, err
	}
	defer release()
	rows, err := tx.QueryContext(ctx, `SELECT p.catalog_entry_id,e.root_path,
			(SELECT count(*) FROM source_catalog_file f WHERE f.catalog_entry_id=e.id)
		FROM source_catalog_episode_pending p JOIN source_catalog_entry e ON e.id=p.catalog_entry_id
			WHERE p.source_id=? AND p.parent_root=? AND p.catalog_entry_id>?
				AND NOT EXISTS(SELECT 1 FROM source_catalog_episode_publication publication
					WHERE publication.source_id=p.source_id AND publication.parent_root=p.parent_root
						AND publication.state='published')
			ORDER BY p.catalog_entry_id LIMIT ?`, sourceID, parent, afterID, catalogEpisodeWriteRows)
	if err != nil {
		return 0, afterID, err
	}
	type pendingEntry struct {
		id    int64
		root  string
		files int
	}
	pending := []pendingEntry{}
	for rows.Next() {
		var entry pendingEntry
		if err = rows.Scan(&entry.id, &entry.root, &entry.files); err != nil {
			break
		}
		pending = append(pending, entry)
	}
	readErr := rows.Err()
	_ = rows.Close()
	if err != nil {
		return 0, afterID, err
	}
	if readErr != nil {
		return 0, afterID, readErr
	}
	nextID := afterID
	budget := catalogEpisodeWriteRows
	for _, entry := range pending {
		if len(allowed) > 0 && !allowed[entry.root] {
			nextID = entry.id
			continue
		}
		if entry.files >= catalogEpisodeWriteRows {
			if _, err = tx.ExecContext(ctx, `DELETE FROM source_catalog_file WHERE rowid IN (
				SELECT rowid FROM source_catalog_file WHERE catalog_entry_id=? LIMIT ?)`,
				entry.id, catalogEpisodeWriteRows); err != nil {
				return 0, afterID, err
			}
			break
		}
		cost := 1 + entry.files
		if cost > budget {
			break
		}
		if err = deleteCatalogPendingEntryTx(ctx, tx.Tx, sourceID, parent, entry.root, entry.id, true); err != nil {
			return 0, afterID, err
		}
		budget -= cost
		nextID = entry.id
	}
	if err = tx.Commit(); err != nil {
		return 0, afterID, err
	}
	return len(pending), nextID, nil
}

func deleteCatalogPendingEntryTx(ctx context.Context, tx *sql.Tx, sourceID int64, parent, root string, entryID int64, preservePersonalState bool) error {
	personalState := `0`
	if preservePersonalState {
		personalState = `EXISTS(SELECT 1 FROM catalog_series_member m
			JOIN catalog_series s ON s.id=m.series_id
			WHERE m.catalog_entry_id=e.id AND (s.work_id IS NOT NULL
				OR EXISTS(SELECT 1 FROM user_work_state u WHERE u.work_id=s.work_id)))`
	}
	var removable int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM source_catalog_episode_pending p JOIN source_catalog_entry e ON e.id=p.catalog_entry_id
			WHERE p.source_id=? AND p.parent_root=? AND p.catalog_entry_root=? AND p.catalog_entry_id=? AND e.root_path=?
				AND (e.file_source_id IS NULL OR e.file_source_id=?) AND e.work_id IS NULL AND e.relocated_to_id IS NULL
			AND NOT EXISTS(SELECT 1 FROM source_catalog_entry r WHERE r.relocated_to_id=e.id)
				AND NOT EXISTS(SELECT 1 FROM source_catalog_episode_publication publication
					WHERE publication.source_id=p.source_id AND publication.parent_root=p.parent_root
						AND publication.state='published')
			AND NOT (`+personalState+`))`,
		sourceID, parent, root, entryID, root, sourceID).Scan(&removable); err != nil {
		return err
	}
	if removable == 0 {
		return fmt.Errorf("%w: pending entry %d at %q", errCatalogEpisodePendingConflict, entryID, root)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM catalog_series_member WHERE catalog_entry_id=?`, entryID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM source_catalog_episode_pending
		WHERE source_id=? AND parent_root=? AND catalog_entry_root=? AND catalog_entry_id=?`, sourceID, parent, root, entryID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM source_catalog_entry WHERE id=?
			AND (file_source_id IS NULL OR file_source_id=?) AND work_id IS NULL AND relocated_to_id IS NULL
			AND NOT EXISTS(SELECT 1 FROM source_catalog_entry r WHERE r.relocated_to_id=source_catalog_entry.id)`, entryID, sourceID)
	return err
}
