package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path"
	"strings"
)

func catalogDirectoryNeedsGroupingIndexTx(ctx context.Context, tx *sql.Tx, sourceID, generation int64, directory string, revision int) (bool, error) {
	var own int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM(SELECT 1 FROM source_catalog_observation WHERE file_source_id=? AND directory_path=? AND revision=? AND kind IN ('audio','folder') LIMIT 65)`, sourceID, directory, revision).Scan(&own); err != nil {
		return false, err
	}
	if own > 64 {
		return true, nil
	}
	for parent := path.Dir(directory); parent != "." && parent != directory; parent = path.Dir(parent) {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM(SELECT 1 FROM source_catalog_directory d JOIN source_catalog_observation o ON o.file_source_id=d.file_source_id AND o.directory_path=d.path AND o.revision=d.revision WHERE d.file_source_id=? AND d.path=? AND d.generation=? AND o.kind='audio' LIMIT 65)`, sourceID, parent, generation).Scan(&count); err != nil {
			return false, err
		}
		if count > 64 {
			return true, nil
		}
		if count > 0 || parent == "/" {
			break
		}
	}
	return false, nil
}

func (s *Server) prepareCatalogLargeGrouping(ctx context.Context, in catalogGroupingIndexInput) (int64, error) {
	if in.ListingID == 0 {
		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return 0, err
		}
		needed, err := catalogDirectoryNeedsGroupingIndexTx(ctx, tx, in.SourceID, in.Generation, in.Root, in.Revision)
		_ = tx.Rollback()
		if err != nil || !needed {
			return 0, err
		}
		id, err := s.prepareCatalogGrouping(ctx, in)
		if err == nil {
			err = s.prepareCatalogDuplicateIndex(ctx, in)
		}
		return id, err
	}
	var count int
	query := `SELECT count(*) FROM (SELECT 1 FROM source_catalog_observation WHERE file_source_id=? AND directory_path=? AND revision=? AND kind IN ('audio','folder') LIMIT 65)`
	args := []any{in.SourceID, in.Root, in.Revision}
	if in.ListingID != 0 {
		query = `SELECT count(*) FROM (SELECT 1 FROM source_catalog_listing_item WHERE listing_id=? AND directory_path=? AND kind='audio' AND instr(name,'/')=0 LIMIT 65)`
		args = []any{in.ListingID, in.Root}
	}
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return 0, err
	}
	if count <= catalogGroupingRows {
		return 0, nil
	}
	id, err := s.prepareCatalogGrouping(ctx, in)
	if err == nil && in.ListingID == 0 {
		err = s.prepareCatalogDuplicateIndex(ctx, in)
	}
	return id, err
}

// The listing transaction commits before expensive classification starts. The
// pending flag prevents child expansion from inheriting an unfinished root.
func (s *Server) finalizeCatalogGroupingDirectory(ctx context.Context, job workflowJobRecord, sourceID int64) error {
	var d catalogScanDirectory
	err := s.db.QueryRowContext(ctx, `SELECT path,partition_path,catalog_root,revision FROM source_catalog_directory WHERE file_source_id=? AND generation=? AND status='listing_complete' AND classification_pending=1 ORDER BY path LIMIT 1`, sourceID, job.RunID).Scan(&d.Path, &d.Partition, &d.Root, &d.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err = s.prepareCatalogGrouping(ctx, catalogGroupingIndexInput{SourceID: sourceID, Generation: job.RunID, Root: d.Path, Revision: d.Revision}); err != nil {
		return err
	}
	if err = s.prepareCatalogDuplicateIndex(ctx, catalogGroupingIndexInput{SourceID: sourceID, Generation: job.RunID, Root: d.Path, Revision: d.Revision}); err != nil {
		return err
	}
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return err
	}
	defer release()
	if err = verifyCatalogLease(ctx, tx.Tx, job); err != nil {
		return err
	}
	if err = completeCatalogDirectoryTx(ctx, tx.Tx, sourceID, job.RunID, &d); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_directory SET catalog_root=?,classification_pending=0 WHERE file_source_id=? AND generation=? AND path=? AND revision=?`, d.Root, sourceID, job.RunID, d.Path, d.Revision); err != nil {
		return err
	}
	if _, err = refreshCatalogAudioRootStateTx(ctx, tx.Tx, sourceID, job.RunID, d.Path); err != nil {
		return err
	}
	return tx.Commit()
}

func (p catalogSplitPlan) indexedTargetFor(ctx context.Context, tx *sql.Tx, rel string) (catalogSplitTarget, bool, error) {
	if p.groupingID == 0 {
		target, ok := p.targetFor(rel)
		return target, ok, nil
	}
	observeCatalogGrouping(ctx, "lookup", 1)
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT target_json FROM source_catalog_grouping_name WHERE grouping_id=? AND name=?`, p.groupingID, rel).Scan(&raw)
	if err == nil {
		var target catalogSplitTarget
		err = json.Unmarshal([]byte(raw), &target)
		return target, err == nil, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return catalogSplitTarget{}, false, err
	}
	if !strings.Contains(rel, "/") || !catalogWorkPartDirectory(path.Base(path.Dir(rel))) {
		return catalogSplitTarget{}, false, nil
	}
	var mode string
	if err = tx.QueryRowContext(ctx, `SELECT mode FROM source_catalog_grouping WHERE id=?`, p.groupingID).Scan(&mode); err != nil {
		return catalogSplitTarget{}, false, err
	}
	// Nested names do not inherit direct-name part/episode/structured evidence;
	// they join only the ordinary key the finite partition would calculate.
	prefix := catalogWorkPrefix(rel)
	b, err := catalogReadGroupingBucket(ctx, tx, p.groupingID, "prefix", prefix)
	if err != nil {
		return catalogSplitTarget{}, false, err
	}
	g := catalogLooseGrouping{mode: mode, splitPrefixes: map[string]bool{prefix: string(b.First) != string(b.Last) && (b.Min < 0 || b.Min > 1 || b.Distinct != b.Count || b.Max-b.Min+1 != b.Count)}}
	key := g.keyFor(rel)
	err = tx.QueryRowContext(ctx, `SELECT target_json FROM source_catalog_grouping_target WHERE grouping_id=? AND key=?`, p.groupingID, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return catalogSplitTarget{}, false, nil
	}
	if err != nil {
		return catalogSplitTarget{}, false, err
	}
	var target catalogSplitTarget
	err = json.Unmarshal([]byte(raw), &target)
	return target, err == nil, err
}

func (p catalogSplitPlan) indexedParentNeeded(ctx context.Context, tx *sql.Tx, sourceID, generation int64) (bool, error) {
	var ready, needed bool
	var cursorPath, cursorName string
	if err := tx.QueryRowContext(ctx, `SELECT parent_ready,parent_needed,parent_cursor_path,parent_cursor_name FROM source_catalog_grouping WHERE id=?`, p.groupingID).Scan(&ready, &needed, &cursorPath, &cursorName); err != nil {
		return false, err
	}
	if ready {
		return needed, nil
	}
	complete, err := catalogRootFullyObservedTx(ctx, tx, sourceID, generation, p.root)
	if err != nil {
		return false, err
	}
	if !complete {
		return true, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT o.directory_path,o.name,o.kind FROM source_catalog_directory d JOIN source_catalog_observation o ON o.file_source_id=d.file_source_id AND o.directory_path=d.path AND o.revision=d.revision WHERE d.file_source_id=? AND d.generation=? AND d.catalog_root=? AND d.path>=? AND o.name>CASE WHEN d.path=? THEN ? ELSE '' END AND o.kind<>'folder' ORDER BY d.path,o.name LIMIT 64`, sourceID, generation, p.root, cursorPath, cursorPath, cursorName)
	if err != nil {
		return false, err
	}
	count := 0
	for rows.Next() {
		var directory, name, kind string
		if err = rows.Scan(&directory, &name, &kind); err != nil {
			break
		}
		count++
		cursorPath, cursorName = directory, name
		if kind != "audio" {
			needed = true
			break
		}
		rel := strings.TrimPrefix(path.Join(directory, name), strings.TrimRight(p.root, "/")+"/")
		var split bool
		_, split, err = p.indexedTargetFor(ctx, tx, rel)
		if err != nil {
			break
		}
		if !split {
			needed = true
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return false, err
	}
	ready = needed || count < 64
	_, err = tx.ExecContext(ctx, `UPDATE source_catalog_grouping SET parent_ready=?,parent_needed=?,parent_cursor_path=?,parent_cursor_name=? WHERE id=?`, ready, needed, cursorPath, cursorName, p.groupingID)
	return !ready || needed, err
}

// Resolve only roots in the next publication batch. Each durable parent cursor
// update releases the writer after 64 inputs, even for one very large root.
func (s *Server) prepareCatalogPublicationGrouping(ctx context.Context, sourceID, generation int64) error {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT catalog_root FROM (`+catalogPublishQuery+`)`, sourceID, generation, catalogPublishRows)
	if err != nil {
		return err
	}
	roots := []string{}
	for rows.Next() {
		var root string
		if err = rows.Scan(&root); err != nil {
			break
		}
		roots = append(roots, root)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, root := range roots {
		for {
			tx, release, e := beginTxWithDatabaseBusyRetry(ctx, s.db)
			if e != nil {
				return e
			}
			var id int64
			var ready bool
			e = tx.QueryRowContext(ctx, `SELECT g.id,g.parent_ready FROM source_catalog_grouping g JOIN source_catalog_directory d ON d.file_source_id=g.file_source_id AND d.path=g.root_path WHERE g.file_source_id=? AND g.root_path=? AND g.version=('observation:'||d.generation||':'||d.revision) AND g.phase='ready'`, sourceID, root).Scan(&id, &ready)
			if errors.Is(e, sql.ErrNoRows) || ready {
				release()
				break
			}
			if e != nil {
				release()
				return e
			}
			complete, e := catalogRootFullyObservedTx(ctx, tx.Tx, sourceID, generation, root)
			if e != nil {
				release()
				return e
			}
			if !complete {
				release()
				break
			}
			_, e = (catalogSplitPlan{root: root, groupingID: id}).indexedParentNeeded(ctx, tx.Tx, sourceID, generation)
			if e == nil {
				e = tx.Commit()
			}
			release()
			if e != nil {
				return e
			}
			if e = pauseCatalogWriter(ctx); e != nil {
				return e
			}
		}
	}
	return nil
}
