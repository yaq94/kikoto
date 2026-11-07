package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"path"
	"strings"
	"unicode"
)

// Restrict the size range before sorting by name, preserving the existing
// deterministic one-to-one choice without scanning unrelated candidates.
const catalogDuplicateSizeQuery = `SELECT p.name FROM source_catalog_duplicate_available p INDEXED BY source_catalog_duplicate_available_size WHERE p.comparison_id=? AND p.size_bytes BETWEEN ? AND ? ORDER BY p.name LIMIT 1`

// EqualFold uses Unicode simple folding, which differs from ToLower for e.g.
// Greek final sigma. Canonicalize each rune's complete SimpleFold cycle only
// for the ancestor rule; the immediate-parent rule retains its lowercase key.
func catalogSimpleFoldKey(name string) string {
	return strings.Map(func(r rune) rune {
		best := r
		for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
			best = min(best, next)
		}
		return best
	}, strings.TrimSpace(name))
}

func (s *Server) prepareCatalogDuplicateIndex(ctx context.Context, in catalogGroupingIndexInput) error {
	if in.Root == "/" {
		return nil
	}
	var childCount int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM source_catalog_observation WHERE file_source_id=? AND directory_path=? AND revision=? AND kind='audio'`, in.SourceID, in.Root, in.Revision).Scan(&childCount); err != nil || childCount == 0 {
		return err
	}
	parent := path.Dir(in.Root)
	ancestor := false
	var revision, parentCount int
	for {
		err := s.db.QueryRowContext(ctx, `SELECT d.revision,(SELECT count(*) FROM source_catalog_observation o WHERE o.file_source_id=d.file_source_id AND o.directory_path=d.path AND o.revision=d.revision AND o.kind='audio') FROM source_catalog_directory d WHERE d.file_source_id=? AND d.path=? AND d.generation=? AND d.status IN ('complete','listing_complete')`, in.SourceID, parent, in.Generation).Scan(&revision, &parentCount)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if parentCount > 0 {
			break
		}
		if !ancestor {
			var folders int
			if err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM source_catalog_observation WHERE file_source_id=? AND directory_path=? AND revision=? AND kind='folder'`, in.SourceID, parent, revision).Scan(&folders); err != nil {
				return err
			}
			if folders < 2 {
				return nil
			}
		}
		if parent == "/" {
			return nil
		}
		parent = path.Dir(parent)
		ancestor = true
	}
	child, err := s.prepareCatalogGrouping(ctx, in)
	if err != nil {
		return err
	}
	parentID, err := s.prepareCatalogGrouping(ctx, catalogGroupingIndexInput{SourceID: in.SourceID, Generation: in.Generation, Root: parent, Revision: revision})
	if err != nil {
		return err
	}
	var missingHashes int
	if err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM source_catalog_grouping_name WHERE grouping_id IN (?,?) AND content_hash=''`, child, parentID).Scan(&missingHashes); err != nil {
		return err
	}
	hashes := missingHashes == 0
	var comparison int64
	if err = s.db.QueryRowContext(ctx, `INSERT INTO source_catalog_duplicate_comparison(child_grouping,parent_grouping,ancestor) VALUES(?,?,?) ON CONFLICT(child_grouping,parent_grouping) DO UPDATE SET ancestor=excluded.ancestor RETURNING id`, child, parentID, ancestor).Scan(&comparison); err != nil {
		return err
	}
	if err = s.prepareCatalogDuplicateCandidates(ctx, comparison, parentID); err != nil {
		return err
	}
	for {
		tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
		if err != nil {
			return err
		}
		var cursor string
		var complete bool
		var matched, named, exact int
		err = tx.QueryRowContext(ctx, `SELECT cursor,complete,matched,named,exact FROM source_catalog_duplicate_comparison WHERE id=?`, comparison).Scan(&cursor, &complete, &matched, &named, &exact)
		if err != nil {
			release()
			return err
		}
		if complete {
			release()
			break
		}
		rows, err := tx.QueryContext(ctx, `SELECT name,normalized_name,size_bytes,content_hash FROM source_catalog_grouping_name WHERE grouping_id=? AND name>? ORDER BY name LIMIT 64`, child, cursor)
		if err != nil {
			release()
			return err
		}
		type item struct {
			name, normalized, hash string
			size                   int64
		}
		batch := []item{}
		for rows.Next() {
			var i item
			if err = rows.Scan(&i.name, &i.normalized, &i.size, &i.hash); err != nil {
				break
			}
			batch = append(batch, i)
		}
		if err == nil {
			err = rows.Err()
		}
		_ = rows.Close()
		for _, i := range batch {
			if err != nil {
				break
			}
			query := `SELECT p.name FROM source_catalog_duplicate_available p WHERE p.comparison_id=?`
			args := []any{comparison}
			var candidate string
			if hashes {
				query += ` AND p.content_hash=?`
				args = append(args, i.hash)
			} else if ancestor {
				query += ` AND p.folded_name=? AND p.size_bytes=?`
				args = append(args, catalogSimpleFoldKey(i.name), i.size)
			} else {
				tolerance := max(int64(1024), i.size/1000)
				query += ` AND p.normalized_name=? AND p.size_bytes BETWEEN ? AND ?`
				args = append(args, i.normalized, i.size-tolerance, i.size+tolerance)
			}
			err = tx.QueryRowContext(ctx, query+` ORDER BY p.name LIMIT 1`, args...).Scan(&candidate)
			if errors.Is(err, sql.ErrNoRows) && !hashes && !ancestor {
				tolerance := max(int64(1024), i.size/1000)
				err = tx.QueryRowContext(ctx, catalogDuplicateSizeQuery, comparison, i.size-tolerance, i.size+tolerance).Scan(&candidate)
			}
			if errors.Is(err, sql.ErrNoRows) {
				err = nil
			} else if err == nil {
				_, err = tx.ExecContext(ctx, `INSERT INTO source_catalog_duplicate_match(comparison_id,child_name,parent_name) VALUES(?,?,?)`, comparison, i.name, candidate)
				if err == nil {
					_, err = tx.ExecContext(ctx, `DELETE FROM source_catalog_duplicate_available WHERE comparison_id=? AND name=?`, comparison, candidate)
				}
				matched++
			}
			if err == nil && !ancestor && !hashes {
				var n, e bool
				tolerance := max(int64(1024), i.size/1000)
				err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM source_catalog_grouping_name WHERE grouping_id=? AND normalized_name=? AND size_bytes BETWEEN ? AND ?),EXISTS(SELECT 1 FROM source_catalog_grouping_name WHERE grouping_id=? AND normalized_name=? AND size_bytes=?)`, parentID, i.normalized, i.size-tolerance, i.size+tolerance, parentID, i.normalized, i.size).Scan(&n, &e)
				if n {
					named++
				}
				if e {
					exact++
				}
			}
			cursor = i.name
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE source_catalog_duplicate_comparison SET cursor=?,matched=?,named=?,exact=? WHERE id=?`, cursor, matched, named, exact, comparison)
		}
		if err == nil && len(batch) < 64 {
			duplicate := false
			if ancestor {
				duplicate = childCount >= 2 && childCount <= parentCount && matched == childCount && (hashes || catalogWorkPartDirectory(path.Base(in.Root)) || catalogPackageDirectory(path.Base(path.Dir(in.Root))))
			} else {
				duplicate = catalogIndexedDuplicateDecision(childCount, parentCount, matched, named, exact, hashes, catalogPackageDirectory(path.Base(in.Root)))
			}
			if err == nil {
				_, err = tx.ExecContext(ctx, `UPDATE source_catalog_duplicate_comparison SET complete=1,duplicate=?,matched=?,unique_count=? WHERE id=?`, duplicate, matched, childCount-matched, comparison)
			}
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
		if err = pauseCatalogWriter(ctx); err != nil {
			return err
		}
	}
	// Match decisions suppress only bounded observation rows before the root
	// decision is committed. Unique bonuses remain in the ordinary publish queue.
	for {
		tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `UPDATE source_catalog_observation SET publish_pending=0 WHERE rowid IN(SELECT o.rowid FROM source_catalog_observation o WHERE o.file_source_id=? AND o.directory_path=? AND o.revision=? AND o.name>(SELECT cleanup_cursor FROM source_catalog_duplicate_comparison WHERE id=?) AND o.publish_pending=1 AND EXISTS(SELECT 1 FROM source_catalog_duplicate_comparison c WHERE c.id=? AND c.duplicate=1) AND (o.kind<>'audio' OR EXISTS(SELECT 1 FROM source_catalog_duplicate_match m WHERE m.comparison_id=? AND m.child_name=o.name)) ORDER BY o.name LIMIT 64) RETURNING name`, in.SourceID, in.Root, in.Revision, comparison, comparison, comparison)
		n, cursor := 0, ""
		if err == nil {
			for rows.Next() {
				var name string
				if err = rows.Scan(&name); err != nil {
					break
				}
				n++
				cursor = max(cursor, name)
			}
			if err == nil {
				err = rows.Err()
			}
			_ = rows.Close()
		}
		if err == nil && n > 0 {
			_, err = tx.ExecContext(ctx, `UPDATE source_catalog_duplicate_comparison SET cleanup_cursor=? WHERE id=?`, cursor, comparison)
		}
		if err == nil {
			err = tx.Commit()
		}
		release()
		if err != nil {
			return err
		}
		if n < 64 {
			return nil
		}
	}
}

// Removing a consumed candidate prevents equal-hash or equal-size copies from
// repeatedly scanning the used prefix. Preparation and consumption share the
// comparison's durable checkpoints, so a retry never reintroduces a candidate.
func (s *Server) prepareCatalogDuplicateCandidates(ctx context.Context, comparison, parent int64) error {
	for {
		tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
		if err != nil {
			return err
		}
		var cursor string
		var ready bool
		err = tx.QueryRowContext(ctx, `SELECT parent_cursor,parent_ready FROM source_catalog_duplicate_comparison WHERE id=?`, comparison).Scan(&cursor, &ready)
		if err != nil || ready {
			release()
			return err
		}
		rows, err := tx.QueryContext(ctx, `INSERT INTO source_catalog_duplicate_available(comparison_id,name,normalized_name,folded_name,size_bytes,content_hash) SELECT ?,name,normalized_name,folded_name,size_bytes,content_hash FROM source_catalog_grouping_name WHERE grouping_id=? AND name>? ORDER BY name LIMIT 64 RETURNING name`, comparison, parent, cursor)
		n := 0
		if err == nil {
			for rows.Next() {
				var name string
				if err = rows.Scan(&name); err != nil {
					break
				}
				n++
				cursor = max(cursor, name)
			}
			if err == nil {
				err = rows.Err()
			}
			_ = rows.Close()
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE source_catalog_duplicate_comparison SET parent_cursor=?,parent_ready=? WHERE id=?`, cursor, n < 64, comparison)
		}
		if err == nil {
			err = tx.Commit()
		}
		release()
		if err != nil {
			return err
		}
		if n < 64 {
			return nil
		}
		if err = pauseCatalogWriter(ctx); err != nil {
			return err
		}
	}
}

func catalogIndexedDuplicateDecision(child, parent, matched, named, exact int, hashes, renamed bool) bool {
	if child*5 < parent*4 {
		return false
	}
	if child == 1 || parent == 1 {
		return child == 1 && parent == 1 && hashes && matched == 1
	}
	if matched == parent && child >= parent && (child <= parent+2 || child*10 <= parent*12) && (hashes || exact >= parent) {
		return true
	}
	small := child < catalogDuplicateSmallSet || parent < catalogDuplicateSmallSet
	if hashes {
		if small {
			return child == parent && matched == child
		}
		return matched*10 >= child*9
	}
	if small {
		if renamed && child == parent && matched == child {
			return true
		}
		return child == parent && exact == child
	}
	if matched*10 < child*9 {
		return false
	}
	return child == parent && (named*10 >= child*9 || renamed)
}
