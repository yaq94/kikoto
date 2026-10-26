package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"path"
	"strings"
	"time"

	"github.com/yexca/kikoto/backend/internal/openlist"
)

// catalogRootIncompleteQuery returns the statement that reports an observation
// gap below one catalog root. Retirement and missing-state decisions only apply
// to a range the current generation fully observed: a failed, restricted,
// unconfirmed-empty or still listing directory keeps every older card and file
// row it owns. The observation arm bounds directory_path as a half-open range;
// the pending index is ordered by (file_source_id,directory_path,name), so the
// range seeks the subtree instead of testing every pending row with substr().
// A format copy is the exception to the ancestor rule: its directory is a
// sibling of the episode card and records the episode in catalog_root. A second
// arm follows that key so a failed copy cannot look like complete coverage.
// Keeping the builder separate lets a plan guard assert the exact statement.
func catalogRootIncompleteQuery(sourceID, generation int64, root string) (string, []any) {
	root = strings.TrimRight(root, "/")
	if root == "" {
		root = "/"
	}
	query := `SELECT EXISTS(
		SELECT 1 FROM source_catalog_directory d
		WHERE d.file_source_id=? AND d.generation=? AND d.status<>'complete'
			AND NOT (d.status='empty' AND d.error_code='not_found')`
	args := []any{sourceID, generation}
	if root != "/" {
		// The top-level range keeps directory_path sargable even though the
		// slash check is a second conjunct. The range may include a sibling such
		// as /root-extra; the slash predicate removes it without a scan.
		query += `
			AND d.path>=? AND d.path<?
			AND (d.path=? OR d.path>=?)`
		args = append(args, root, root+"0", root, root+"/")
		query += `
			UNION ALL
			SELECT 1 FROM source_catalog_directory d
			WHERE d.file_source_id=? AND d.generation=? AND d.catalog_root=?
				AND d.status<>'complete'
				AND NOT (d.status='empty' AND d.error_code='not_found')`
		args = append(args, sourceID, generation, root)
	}
	query += `
		UNION ALL
		SELECT 1 FROM source_catalog_observation o
		JOIN source_catalog_directory pd ON pd.file_source_id=o.file_source_id AND pd.path=o.directory_path AND o.revision=pd.revision
		WHERE o.file_source_id=? AND pd.generation=? AND o.publish_pending=1
			AND NOT (pd.status='empty' AND pd.error_code='not_found')
			-- A directory whose catalog root holds no audio publishes no card,
			-- so its folder, text and artwork rows stay pending forever. They
			-- describe a container, not missing coverage of the card below it.
			-- An audio row is always a gap because its own root will publish.
			AND (o.kind='audio' OR EXISTS(SELECT 1 FROM source_catalog_audio_root r
				WHERE r.file_source_id=pd.file_source_id AND r.generation=pd.generation AND r.catalog_root=pd.catalog_root))`
	args = append(args, sourceID, generation)
	if root != "/" {
		query += `
			AND o.directory_path>=? AND o.directory_path<?
			AND (o.directory_path=? OR o.directory_path>=?)`
		args = append(args, root, root+"0", root, root+"/")
		query += `
			UNION ALL
			SELECT 1 FROM source_catalog_directory pd
			CROSS JOIN source_catalog_observation o
			WHERE o.file_source_id=pd.file_source_id AND o.directory_path=pd.path AND o.revision=pd.revision
				AND pd.file_source_id=? AND pd.generation=? AND pd.catalog_root=? AND o.publish_pending=1
				AND NOT (pd.status='empty' AND pd.error_code='not_found')
				AND (o.kind='audio' OR EXISTS(SELECT 1 FROM source_catalog_audio_root r
					WHERE r.file_source_id=pd.file_source_id AND r.generation=pd.generation AND r.catalog_root=pd.catalog_root))`
		args = append(args, sourceID, generation, root)
	}
	query += `
	)`
	return query, args
}

// catalogSubtreeBounds returns the half-open range covering root's
// descendants. The upper sentinel root+"0" is safe because '/' sorts below
// '0', so every path under root+'/' stays below it.
func catalogSubtreeBounds(root string) (string, string) {
	root = strings.TrimRight(root, "/")
	if root == "" {
		return "", ""
	}
	return root + "/", root + "0"
}

func catalogRootIncomplete(ctx context.Context, tx *sql.Tx, sourceID, generation int64, root string) (bool, error) {
	query, args := catalogRootIncompleteQuery(sourceID, generation, root)
	var incomplete int
	err := tx.QueryRowContext(ctx, query, args...).Scan(&incomplete)
	return incomplete != 0, err
}

// catalogBaseObservedTx reports whether the current generation observed the
// directory base well enough to judge what it contains. A path that this
// generation never listed keeps the row of the generation that did, so a
// missing row is not proof of deletion by itself: the nearest observed ancestor
// decides. A complete listing (or a confirmed 404/410) of that ancestor proves
// the path gone; an unconfirmed-empty, failed, restricted or pending ancestor
// proves nothing and keeps the caller's old rows.
func catalogBaseObservedTx(ctx context.Context, tx *sql.Tx, sourceID, generation int64, base string) (bool, error) {
	if base == "" {
		base = "/"
	}
	for {
		var status, code string
		err := tx.QueryRowContext(ctx, `SELECT status,error_code FROM source_catalog_directory
			WHERE file_source_id=? AND path=? AND generation=?`, sourceID, base, generation).Scan(&status, &code)
		if errors.Is(err, sql.ErrNoRows) {
			if base == "/" {
				// Without any row for the configured root nothing below it was
				// observed, so no deletion can be proven.
				return false, nil
			}
			parent := path.Dir(base)
			if parent == "." || parent == base {
				return false, nil
			}
			base = parent
			continue
		}
		if err != nil {
			return false, err
		}
		if status == "complete" {
			return true, nil
		}
		// A definite 404/410 is a completed observation: the directory is gone.
		return status == "empty" && code == "not_found", nil
	}
}

// catalogRangeUncertain reports a directory below base that the current
// generation could not observe. An unconfirmed-empty, failed, restricted or
// still-pending directory proves nothing about deletion, so callers keep the
// rows recorded by earlier generations.
func catalogRangeUncertain(ctx context.Context, tx *sql.Tx, sourceID, generation int64, base string) (bool, error) {
	// status='empty' with error_code='not_found' is a definite 404/410 answer,
	// not an unconfirmed listing. Treating it as uncertain kept the whole
	// source partial and preserved rows a deleted directory no longer owns.
	if base == "" {
		base = "/"
	}
	observed, err := catalogBaseObservedTx(ctx, tx, sourceID, generation, base)
	if err != nil {
		return false, err
	}
	if !observed {
		return true, nil
	}
	const uncertainStatus = ` status IN ('failed','restricted','empty','pending','listing_complete')
		AND NOT (status='empty' AND error_code='not_found')`
	query := `SELECT EXISTS(SELECT 1 FROM source_catalog_directory d
		WHERE d.file_source_id=? AND d.generation=? AND ` + uncertainStatus
	args := []any{sourceID, generation}
	// The subtree predicate is a half-open path range so the directory index
	// seeks it. A parameterised "base is /" branch made SQLite evaluate the
	// range for every row of the generation instead of seeking it.
	if base != "/" {
		query += ` AND (d.path=? OR (d.path>=? AND d.path<?))`
		args = append(args, base, base+"/", base+"0")
		// A format copy stores the episode as catalog_root while its directory
		// sits beside the episode. The subtree predicate alone cannot see that
		// row, so a second arm follows the recorded root.
		query += `
			UNION ALL
			SELECT 1 FROM source_catalog_directory d
			WHERE d.file_source_id=? AND d.generation=? AND ` + uncertainStatus + `
				AND d.catalog_root=?`
		args = append(args, sourceID, generation, base)
	}
	query += `)`
	var uncertain int
	err = tx.QueryRowContext(ctx, query, args...).Scan(&uncertain)
	return uncertain != 0, err
}

func catalogRangeUncertainSetSQL(baseCount int) string {
	values := strings.TrimSuffix(strings.Repeat("(?),", baseCount), ",")
	return `WITH bases(path) AS (VALUES ` + values + `)
		SELECT DISTINCT b.path
		FROM source_catalog_directory d
		JOIN bases b ON b.path='/' OR d.path=b.path OR (d.path>=b.path||'/' AND d.path<b.path||'0')
		WHERE d.file_source_id=? AND d.generation=?
			AND d.status IN ('failed','restricted','empty','pending','listing_complete')
			AND NOT (d.status='empty' AND d.error_code='not_found')`
}

// catalogRangeUncertainRootSetSQL is the catalog_root arm of the bounded
// lifecycle check. Format copies are siblings of the card root rather than
// descendants, so they need a separate lookup through the root index.
func catalogRangeUncertainRootSetSQL(baseCount int) string {
	values := strings.TrimSuffix(strings.Repeat("(?),", baseCount), ",")
	return `WITH bases(path) AS (VALUES ` + values + `)
		SELECT DISTINCT b.path
		FROM source_catalog_directory d
		JOIN bases b ON d.catalog_root=b.path
		WHERE d.file_source_id=? AND d.generation=?
			AND d.status IN ('failed','restricted','empty','pending','listing_complete')
			AND NOT (d.status='empty' AND d.error_code='not_found')`
}

// catalogRangeUncertainSetTx answers catalogRangeUncertain for a bounded card
// batch with three statements: one reads the nearest directory row for every
// ancestor, one checks every base for an uncertain descendant, and one follows
// catalog_root for sibling format copies. The caller keeps the same per-base
// verdict the single-card helper produces.
func catalogRangeUncertainSetTx(ctx context.Context, tx *sql.Tx, sourceID, generation int64, bases []string) (map[string]bool, error) {
	uniqueBases := make([]string, 0, len(bases))
	seenBase := map[string]bool{}
	for _, base := range bases {
		if base == "" {
			base = "/"
		}
		if !seenBase[base] {
			seenBase[base] = true
			uniqueBases = append(uniqueBases, base)
		}
	}
	result := map[string]bool{}
	if len(uniqueBases) == 0 {
		return result, nil
	}
	ancestorSet := map[string]bool{}
	for _, base := range uniqueBases {
		for current := base; ; {
			ancestorSet[current] = true
			if current == "/" {
				break
			}
			parent := path.Dir(current)
			if parent == "." || parent == current {
				ancestorSet["/"] = true
				break
			}
			current = parent
		}
	}
	ancestors := make([]string, 0, len(ancestorSet))
	for ancestor := range ancestorSet {
		ancestors = append(ancestors, ancestor)
	}
	type directoryRow struct {
		status, code string
	}
	rowsByPath := map[string]directoryRow{}
	rows, err := tx.QueryContext(ctx, `SELECT path,status,error_code FROM source_catalog_directory
		WHERE file_source_id=? AND generation=? AND path IN (`+sqlPlaceholders(len(ancestors))+`)`,
		append([]any{sourceID, generation}, stringArgs(ancestors)...)...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var directory string
		var row directoryRow
		if err = rows.Scan(&directory, &row.status, &row.code); err != nil {
			break
		}
		rowsByPath[directory] = row
	}
	readErr := rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	if readErr != nil {
		return nil, readErr
	}
	partition := func(base string) (bool, bool) {
		for current := base; ; {
			if row, ok := rowsByPath[current]; ok {
				return true, row.status == "complete" || (row.status == "empty" && row.code == "not_found")
			}
			if current == "/" {
				return false, false
			}
			parent := path.Dir(current)
			if parent == "." || parent == current {
				return false, false
			}
			current = parent
		}
	}
	observed := map[string]bool{}
	for _, base := range uniqueBases {
		_, observed[base] = partition(base)
	}
	uncertainArgs := make([]any, 0, len(uniqueBases)+2)
	for _, base := range uniqueBases {
		uncertainArgs = append(uncertainArgs, base)
	}
	uncertainArgs = append(uncertainArgs, sourceID, generation)
	rows, err = tx.QueryContext(ctx, catalogRangeUncertainSetSQL(len(uniqueBases)), uncertainArgs...)
	if err != nil {
		return nil, err
	}
	matched := map[string]bool{}
	for rows.Next() {
		var base string
		if err = rows.Scan(&base); err != nil {
			break
		}
		matched[base] = true
	}
	readErr = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	if readErr != nil {
		return nil, readErr
	}
	for _, base := range uniqueBases {
		result[base] = !observed[base] || matched[base]
	}
	if len(uniqueBases) > 0 {
		rootArgs := append([]any{}, stringArgs(uniqueBases)...)
		rootArgs = append(rootArgs, sourceID, generation)
		rows, err = tx.QueryContext(ctx, catalogRangeUncertainRootSetSQL(len(uniqueBases)), rootArgs...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var base string
			if err = rows.Scan(&base); err != nil {
				break
			}
			matched[base] = true
		}
		readErr = rows.Err()
		_ = rows.Close()
		if err != nil {
			return nil, err
		}
		if readErr != nil {
			return nil, readErr
		}
		for _, base := range uniqueBases {
			result[base] = result[base] || matched[base]
		}
	}
	return result, nil
}

func catalogFailureStatus(err error) string {
	if errors.Is(err, openlist.ErrMaintenance) {
		return "maintenance"
	}
	// A challenge is a transport-level refusal that hides the real answer, so it
	// must not overwrite availability with a permission verdict.
	if errors.Is(err, openlist.ErrChallenged) {
		return "restricted"
	}
	if errors.Is(err, openlist.ErrRestricted) {
		return "restricted"
	}
	return "unavailable"
}

func markCatalogRangeStatusTx(ctx context.Context, tx *sql.Tx, sourceID int64, root, status string) error {
	_, err := tx.ExecContext(ctx, `UPDATE source_catalog_entry SET availability_status=?,status_checked_at=CURRENT_TIMESTAMP
		WHERE file_source_id=? AND relocated_to_id IS NULL AND availability_status<>'excluded'
			AND (?='/' OR root_path=? OR source_root=? OR substr(root_path,1,length(?)+2)=? || '::')`,
		status, sourceID, root, root, root, root, root)
	return err
}

type catalogEntryStatusUpdate struct {
	ID     int64
	Status string
}

func (s *Server) setCatalogEntryStatus(ctx context.Context, id int64, status string) error {
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return err
	}
	defer release()
	if err = setCatalogEntryStatusTx(ctx, tx.Tx, id, status); err != nil {
		return err
	}
	return tx.Commit()
}

// setCatalogEntryStatusesTx applies a whole lifecycle batch with one statement
// per affected table. work_source_presence is aggregated by (work_id,
// file_source_id), and media locations are matched through media_item_id, whose
// catalog fingerprint carries the entry id and relative path.
func setCatalogEntryStatusesTx(ctx context.Context, tx *sql.Tx, updates []catalogEntryStatusUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	var values strings.Builder
	args := make([]any, 0, len(updates)*2)
	ids := make([]int64, 0, len(updates))
	missing := []int64{}
	available := []int64{}
	for index, update := range updates {
		if index > 0 {
			values.WriteByte(',')
		}
		values.WriteString("(?,?)")
		args = append(args, update.ID, update.Status)
		ids = append(ids, update.ID)
		switch update.Status {
		case "missing":
			missing = append(missing, update.ID)
		case "available":
			available = append(available, update.ID)
		}
	}
	if _, err := tx.ExecContext(ctx, `WITH updates(id,status) AS (VALUES `+values.String()+`)
		UPDATE source_catalog_entry
		SET availability_status=(SELECT status FROM updates u WHERE u.id=source_catalog_entry.id),
			status_checked_at=CURRENT_TIMESTAMP
		WHERE id IN (SELECT id FROM updates)`, args...); err != nil {
		return err
	}
	if len(missing) > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE media_file_location SET availability='unavailable'
			WHERE location_type='catalog_stream' AND media_item_id IN (
				SELECT m.id FROM source_catalog_entry e CROSS JOIN media_item m
				WHERE e.id IN (`+sqlPlaceholders(len(missing))+`)
					AND m.fingerprint>=('catalog:'||e.id||':')
					AND m.fingerprint<('catalog:'||e.id||';'))`, int64Args(missing)...); err != nil {
			return err
		}
	}
	if len(available) > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE media_file_location SET availability='available'
			WHERE location_type='catalog_stream' AND media_item_id IN (
				SELECT m.id FROM source_catalog_entry e
				JOIN source_catalog_file f ON f.catalog_entry_id=e.id AND f.kind='audio'
				JOIN media_item m ON m.fingerprint='catalog:'||e.id||':'||f.relative_path
				WHERE e.id IN (`+sqlPlaceholders(len(available))+`))`, int64Args(available)...); err != nil {
			return err
		}
	}
	type presencePair struct {
		workID, sourceID int64
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,work_id,file_source_id FROM source_catalog_entry
		WHERE id IN (`+sqlPlaceholders(len(ids))+`)`, int64Args(ids)...)
	if err != nil {
		return err
	}
	entryPair := map[int64]presencePair{}
	for rows.Next() {
		var id int64
		var workID, sourceID sql.NullInt64
		if err = rows.Scan(&id, &workID, &sourceID); err != nil {
			break
		}
		if workID.Valid && sourceID.Valid {
			entryPair[id] = presencePair{workID: workID.Int64, sourceID: sourceID.Int64}
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
	// Sequential per-card updates leave the status of the last card in the
	// group when no live card is available. Preserve that order while one
	// statement updates every group.
	last := map[presencePair]string{}
	pairs := []presencePair{}
	for _, update := range updates {
		pair, ok := entryPair[update.ID]
		if !ok {
			continue
		}
		if _, seen := last[pair]; !seen {
			pairs = append(pairs, pair)
		}
		last[pair] = update.Status
	}
	if len(pairs) == 0 {
		return nil
	}
	values.Reset()
	args = args[:0]
	for index, pair := range pairs {
		if index > 0 {
			values.WriteByte(',')
		}
		values.WriteString("(?,?,?)")
		args = append(args, pair.workID, pair.sourceID, last[pair])
	}
	_, err = tx.ExecContext(ctx, `WITH targets(work_id,file_source_id,fallback) AS (VALUES `+values.String()+`)
		UPDATE work_source_presence
		SET availability=CASE WHEN EXISTS(
			SELECT 1 FROM source_catalog_entry e
			WHERE e.work_id=work_source_presence.work_id
				AND e.file_source_id=work_source_presence.file_source_id
				AND e.relocated_to_id IS NULL AND e.availability_status='available')
			THEN 'available'
			ELSE (SELECT fallback FROM targets t
				WHERE t.work_id=work_source_presence.work_id AND t.file_source_id=work_source_presence.file_source_id) END,
			last_checked_at=CURRENT_TIMESTAMP
		WHERE (work_id,file_source_id) IN (SELECT work_id,file_source_id FROM targets)`, args...)
	return err
}

func setCatalogEntryStatusTx(ctx context.Context, tx *sql.Tx, id int64, status string) error {
	return setCatalogEntryStatusesTx(ctx, tx, []catalogEntryStatusUpdate{{ID: id, Status: status}})
}

// Only full successful coverage reaches this bounded final pass. Failure,
// cancellation and empty directories cannot retire a previous observation.
func (s *Server) settleCatalogLifecycleBatch(ctx context.Context, job workflowJobRecord, sourceID int64) (bool, error) {
	started := time.Now()
	observedCount := 0
	defer func() { observeCatalogWrite(ctx, "settle_lifecycle", started, observedCount) }()
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return false, err
	}
	defer release()
	if err = verifyCatalogLease(ctx, tx.Tx, job); err != nil {
		return false, err
	}
	var stage string
	var cursor int64
	if err = tx.QueryRowContext(ctx, `SELECT lifecycle_stage,lifecycle_cursor FROM source_catalog_scan WHERE file_source_id=?`, sourceID).Scan(&stage, &cursor); err != nil {
		return false, err
	}
	if stage == "done" {
		return false, nil
	}
	if stage == "reconcile" {
		// Reconciliation compares a stored manifest with the successor's
		// manifest. A card published after the observe pass has no fingerprint
		// yet, so finish that bounded work first; otherwise a real rename looks
		// like insufficient evidence and the old card is marked missing.
		rows, err := tx.QueryContext(ctx, `SELECT e.id FROM source_catalog_entry e
			WHERE e.file_source_id=? AND e.relocated_to_id IS NULL AND e.last_seen_generation=?
				AND e.availability_status<>'excluded'
				AND e.content_fingerprint='' AND e.layout_fingerprint=''
				AND (SELECT count(*) FROM source_catalog_file f WHERE f.catalog_entry_id=e.id AND f.kind='audio') BETWEEN 1 AND 10000
			ORDER BY e.id LIMIT ?`, sourceID, job.RunID, catalogLifecycleRows)
		if err != nil {
			return false, err
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
			return false, err
		}
		if readErr != nil {
			return false, readErr
		}
		for _, id := range ids {
			strong, layout, err := catalogFingerprintsTx(ctx, tx.Tx, id, job.RunID)
			if err != nil {
				return false, err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_entry SET content_fingerprint=?,layout_fingerprint=? WHERE id=?`, strong, layout, id); err != nil {
				return false, err
			}
		}
		if len(ids) > 0 {
			return true, tx.Commit()
		}
	}
	// An excluded card is not missing: the operator removed it on purpose, and
	// the next lifecycle pass must not rewrite that verdict as an availability
	// answer derived from a directory it no longer indexes.
	rows, err := tx.QueryContext(ctx, `SELECT id,last_seen_generation FROM source_catalog_entry
		WHERE file_source_id=? AND relocated_to_id IS NULL AND availability_status<>'excluded' AND id>?
		ORDER BY id LIMIT ?`, sourceID, cursor, catalogLifecycleRows)
	if err != nil {
		return false, err
	}
	type observed struct{ id, generation int64 }
	var entries []observed
	for rows.Next() {
		var e observed
		if err = rows.Scan(&e.id, &e.generation); err != nil {
			break
		}
		entries = append(entries, e)
	}
	readErr := rows.Err()
	_ = rows.Close()
	if err != nil {
		return false, err
	}
	if readErr != nil {
		return false, readErr
	}
	updates := make([]catalogEntryStatusUpdate, 0, len(entries))
	for _, e := range entries {
		if stage == "reconcile" {
			if e.generation != job.RunID {
				// Relocation compares an old card with a successor. A range the
				// current generation never observed cannot prove a rename; moving
				// the card there would be a guess based on stale evidence.
				var base string
				if err = tx.QueryRowContext(ctx, `SELECT `+catalogEffectiveRootSQL("source_catalog_entry")+`
					FROM source_catalog_entry WHERE id=?`, e.id).Scan(&base); err != nil {
					return false, err
				}
				uncertain, checkErr := catalogRangeUncertain(ctx, tx.Tx, sourceID, job.RunID, base)
				if checkErr != nil {
					return false, checkErr
				}
				if uncertain {
					cursor = e.id
					continue
				}
				if err = reconcileCatalogEntryTx(ctx, tx.Tx, sourceID, job.RunID, e.id); err != nil {
					return false, err
				}
			}
			cursor = e.id
			continue
		}
		status := "missing"
		if e.generation != job.RunID {
			// An entry this generation did not see is only missing when the
			// range that contains it was fully observed. A failed, restricted
			// or unconfirmed-empty directory proves nothing about its files.
			var base string
			if err = tx.QueryRowContext(ctx, `SELECT `+catalogEffectiveRootSQL("source_catalog_entry")+`
				FROM source_catalog_entry WHERE id=?`, e.id).Scan(&base); err != nil {
				return false, err
			}
			uncertain, err := catalogRangeUncertain(ctx, tx.Tx, sourceID, job.RunID, base)
			if err != nil {
				return false, err
			}
			if uncertain {
				cursor = e.id
				continue
			}
		}
		if e.generation == job.RunID {
			status = "available"
			if _, err = tx.ExecContext(ctx, `DELETE FROM source_catalog_relocation_candidate WHERE catalog_entry_id=?`, e.id); err != nil {
				return false, err
			}
			strong, layout, readErr := catalogFingerprintsTx(ctx, tx.Tx, e.id, job.RunID)
			if readErr != nil {
				return false, readErr
			}
			if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_entry SET content_fingerprint=?,layout_fingerprint=? WHERE id=?`, strong, layout, e.id); err != nil {
				return false, err
			}
			// Republishing the card does not prove that every directory below
			// it was observed. An unconfirmed-empty or failed child may still
			// be the only place an older file row can be confirmed, so keep the
			// old rows until that subtree completes.
			var base string
			if err = tx.QueryRowContext(ctx, `SELECT `+catalogEffectiveRootSQL("source_catalog_entry")+`
				FROM source_catalog_entry WHERE id=?`, e.id).Scan(&base); err != nil {
				return false, err
			}
			uncertain, err := catalogRangeUncertain(ctx, tx.Tx, sourceID, job.RunID, base)
			if err != nil {
				return false, err
			}
			if !uncertain {
				if _, err = tx.ExecContext(ctx, `DELETE FROM source_catalog_file WHERE catalog_entry_id=? AND observed_generation<>?`, e.id, job.RunID); err != nil {
					return false, err
				}
			}
		}
		updates = append(updates, catalogEntryStatusUpdate{ID: e.id, Status: status})
		cursor = e.id
	}
	observedCount = len(entries)
	if err = setCatalogEntryStatusesTx(ctx, tx.Tx, updates); err != nil {
		return false, err
	}
	// A republished card drops its stale-generation files above, and that
	// delete clears the stored search scope. The card is still playable, so
	// restore the scope in the same transaction; a card marked missing stays
	// cleared, which is the correct answer for a card with no current audio.
	availableIDs := []int64{}
	for _, update := range updates {
		if update.Status == "available" {
			availableIDs = append(availableIDs, update.ID)
		}
	}
	if err = refreshContentTranslationBrowsableTx(ctx, tx.Tx, availableIDs); err != nil {
		return false, err
	}
	if len(entries) < catalogLifecycleRows {
		if stage == "observe" {
			stage = "reconcile"
			cursor = 0
		} else {
			stage = "done"
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_scan SET lifecycle_stage=?,lifecycle_cursor=? WHERE file_source_id=?`, stage, cursor, sourceID); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// Republishing an entry replaces the files this generation observed, but rows
// recorded by an older generation stay until the whole run is known to be
// complete. A category or uploader card that stops being its own work then
// keeps listing children that now have their own cards, and a partial run
// never reaches the lifecycle pass that would delete them. This bounded pass
// retires those rows as soon as the entry is republished, for every outcome of
// the run, without touching an entry this generation did not publish.
func (s *Server) cleanupCatalogPublishBatch(ctx context.Context, job workflowJobRecord, sourceID int64) (scanned, removed int64, err error) {
	started := time.Now()
	defer func() { observeCatalogWrite(ctx, "cleanup_publish", started, int(scanned)) }()
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return 0, 0, err
	}
	defer release()
	if err = verifyCatalogLease(ctx, tx.Tx, job); err != nil {
		return 0, 0, err
	}
	var cursor int64
	if err = tx.QueryRowContext(ctx, `SELECT publish_cleanup_cursor FROM source_catalog_scan WHERE file_source_id=?`, sourceID).Scan(&cursor); err != nil {
		return 0, 0, err
	}
	// Older builds stored a negative "sweep again" marker here. The sweep no
	// longer rewinds, so a database written by one of those builds starts from
	// the beginning once and then continues forward.
	if cursor < 0 {
		cursor = 0
	}
	type cleanupCard struct {
		id              int64
		base            string
		hasDirectoryRow bool
	}
	rows, err := tx.QueryContext(ctx, `SELECT e.id,`+catalogEffectiveRootSQL("e")+`,
			EXISTS(SELECT 1 FROM source_catalog_file f WHERE f.catalog_entry_id=e.id AND f.kind<>'audio')
		FROM source_catalog_entry e
		WHERE e.file_source_id=? AND e.last_seen_generation=? AND e.id>?
		ORDER BY e.id LIMIT ?`, sourceID, job.RunID, cursor, catalogCleanupRows)
	if err != nil {
		return 0, 0, err
	}
	cards := []cleanupCard{}
	for rows.Next() {
		var card cleanupCard
		if err = rows.Scan(&card.id, &card.base, &card.hasDirectoryRow); err != nil {
			break
		}
		cards = append(cards, card)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return 0, 0, err
	}
	if len(cards) == 0 {
		return 0, 0, tx.Commit()
	}
	bases := make([]string, 0, len(cards))
	for _, card := range cards {
		bases = append(bases, card.base)
	}
	uncertain, err := catalogRangeUncertainSetTx(ctx, tx.Tx, sourceID, job.RunID, bases)
	if err != nil {
		return 0, 0, err
	}
	// A failed, restricted or unconfirmed-empty directory still proves nothing
	// about its files. Deleting the older generation below it would hide tracks
	// that the source never had a chance to republish. Keep those rows; the
	// cursor still advances because the verdict cannot change within this run.
	cleanIDs := []int64{}
	for _, card := range cards {
		base := card.base
		if base == "" {
			base = "/"
		}
		if !uncertain[base] {
			cleanIDs = append(cleanIDs, card.id)
		}
	}
	if len(cleanIDs) > 0 {
		args := append(int64Args(cleanIDs), job.RunID)
		result, deleteErr := tx.ExecContext(ctx, `DELETE FROM source_catalog_file
			WHERE catalog_entry_id IN (`+sqlPlaceholders(len(cleanIDs))+`) AND observed_generation<>?`, args...)
		if deleteErr != nil {
			return 0, 0, deleteErr
		}
		n, deleteErr := result.RowsAffected()
		if deleteErr != nil {
			return 0, 0, deleteErr
		}
		removed += n
	}
	// A folder row that is now its own catalog entry belongs to that entry, not
	// to the card above it. The batch mapping keeps each card's child paths
	// correlated with its own base, so one card cannot delete another card's
	// relative path. Both real-root forms use half-open ranges and the root
	// indexes from migration 083.
	childCards := []cleanupCard{}
	for _, card := range cards {
		if card.hasDirectoryRow {
			childCards = append(childCards, card)
		}
	}
	if len(childCards) > 0 {
		var values strings.Builder
		valuesArgs := make([]any, 0, len(childCards)*4)
		for index, card := range childCards {
			base := card.base
			if base == "" {
				base = "/"
			}
			parentRoot := strings.TrimRight(base, "/")
			lower, upper := catalogSubtreeBounds(base)
			if parentRoot == "" {
				// The root card owns every top-level directory. The sentinel is
				// the highest valid rune, so the range still covers all paths.
				lower, upper = "", "􏿿"
			}
			if index > 0 {
				values.WriteByte(',')
			}
			values.WriteString("(?,?,?,?)")
			valuesArgs = append(valuesArgs, card.id, parentRoot, lower, upper)
		}
		args := append(valuesArgs, sourceID, sourceID)
		result, deleteErr := tx.ExecContext(ctx, `WITH batch(id,parent_root,lower_bound,upper_bound) AS (VALUES `+values.String()+`),
			children(card_id,relative) AS (
				SELECT b.id,substr(e.root_path,length(b.parent_root)+2)
				FROM batch b JOIN source_catalog_entry e
					ON e.file_source_id=? AND e.relocated_to_id IS NULL
					AND e.id<>b.id AND e.source_root='' AND e.root_path>=b.lower_bound AND e.root_path<b.upper_bound
				UNION ALL
				SELECT b.id,substr(e.source_root,length(b.parent_root)+2)
				FROM batch b JOIN source_catalog_entry e
					ON e.file_source_id=? AND e.relocated_to_id IS NULL
					AND e.id<>b.id AND e.source_root<>'' AND e.source_root>=b.lower_bound AND e.source_root<b.upper_bound
			)
			DELETE FROM source_catalog_file
			WHERE rowid IN (SELECT f.rowid FROM children c
				JOIN source_catalog_file f ON f.catalog_entry_id=c.card_id AND f.relative_path=c.relative)`, args...)
		if deleteErr != nil {
			return 0, 0, deleteErr
		}
		n, deleteErr := result.RowsAffected()
		if deleteErr != nil {
			return 0, 0, deleteErr
		}
		removed += n
	}
	// The deletes above cleared the stored search scope of every card in this
	// batch. Their surviving file set is what the scope must describe, so one
	// statement restores the whole batch before the cursor advances.
	if err = refreshContentTranslationBrowsableTx(ctx, tx.Tx, cleanIDs); err != nil {
		return 0, 0, err
	}
	cursor = cards[len(cards)-1].id
	if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_scan SET publish_cleanup_cursor=?,updated_at=CURRENT_TIMESTAMP WHERE file_source_id=?`, cursor, sourceID); err != nil {
		return 0, 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, 0, err
	}
	return int64(len(cards)), removed, nil
}
