package personal

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

const maxTransferSeriesMembers = 10000

// resolveTransferParent matches only the retained series reference. A missing
// reference is a new minimal series identity; a reference that names another
// object is contradictory and must stay reviewable.
func resolveTransferParent(ctx context.Context, tx *sql.Tx, parent *BackupParent) (identityMatch, error) {
	m := identityMatch{}
	if parent == nil {
		return m, nil
	}
	var seriesID, seriesWork, workID, catalogID sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT series.id,series.work_id,retained.work_id,retained.catalog_entry_id
		FROM personal_transfer_ref retained
		LEFT JOIN catalog_series series ON series.id=retained.series_id
		WHERE retained.ref=?`, parent.Ref).Scan(&seriesID, &seriesWork, &workID, &catalogID)
	if errors.Is(err, sql.ErrNoRows) {
		return m, nil
	}
	if err != nil {
		return m, err
	}
	if !seriesID.Valid || workID.Valid || catalogID.Valid {
		m.problem = "parent_conflict"
		return m, nil
	}
	m.series = seriesID.Int64
	if seriesWork.Valid {
		m.work = seriesWork.Int64
	}
	return m, nil
}

func episodeSeriesIDs(ctx context.Context, tx *sql.Tx, m identityMatch) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT member.series_id
		FROM catalog_series_member member
		JOIN source_catalog_entry entry ON entry.id=member.catalog_entry_id
		WHERE (?<>0 AND entry.work_id=?) OR (?<>0 AND entry.id=?)
		ORDER BY member.series_id`, m.work, m.work, m.catalog, m.catalog)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var result []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}

func transferParentConflict(parent identityMatch, seriesIDs []int64) string {
	if len(seriesIDs) == 0 {
		return ""
	}
	if parent.series == 0 {
		return "parent_conflict"
	}
	for _, seriesID := range seriesIDs {
		if seriesID != parent.series {
			return "parent_conflict"
		}
	}
	return ""
}

func transferParentPositionConflict(ctx context.Context, tx *sql.Tx, m identityMatch, parent *BackupParent, match identityMatch) (bool, error) {
	if parent == nil || parent.Position <= 0 || match.series == 0 {
		return false, nil
	}
	var entryID int64
	var entryWork sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT member.catalog_entry_id,entry.work_id
		FROM catalog_series_member member
		JOIN source_catalog_entry entry ON entry.id=member.catalog_entry_id
		WHERE member.series_id=? AND member.position=?`, match.series, parent.Position).Scan(&entryID, &entryWork)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if (m.catalog != 0 && entryID == m.catalog) || (m.work != 0 && entryWork.Valid && entryWork.Int64 == m.work) {
		return false, nil
	}
	return true, nil
}

func ensureTransferParentSeries(ctx context.Context, tx *sql.Tx, parent *BackupParent) (identityMatch, error) {
	current, err := resolveTransferParent(ctx, tx, parent)
	if err != nil {
		return identityMatch{}, err
	}
	if current.problem != "" {
		return identityMatch{}, ErrConflict
	}
	if current.work != 0 {
		return current, nil
	}
	if current.series != 0 {
		workID, err := insertTransferWork(ctx, tx, "", parent.Title, "series")
		if err != nil {
			return identityMatch{}, err
		}
		result, err := tx.ExecContext(ctx, `UPDATE catalog_series SET work_id=? WHERE id=? AND work_id IS NULL`, workID, current.series)
		if err != nil {
			return identityMatch{}, err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return identityMatch{}, ErrConflict
		}
		current.work = workID
		return current, nil
	}
	workID, err := insertTransferWork(ctx, tx, "", parent.Title, "series")
	if err != nil {
		return identityMatch{}, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO catalog_series(title,work_id) VALUES(?,?)`, parent.Title, workID)
	if err != nil {
		return identityMatch{}, err
	}
	seriesID, err := result.LastInsertId()
	if err != nil {
		return identityMatch{}, err
	}
	if err = claimTransferRef(ctx, tx, parent.Ref, "series_id", seriesID); err != nil {
		return identityMatch{}, err
	}
	return identityMatch{work: workID, series: seriesID}, nil
}

func episodeEntryRef(identity *BackupIdentity, parent *BackupParent) string {
	if parent.EntryRef != "" {
		return parent.EntryRef
	}
	var found string
	for _, ref := range identity.References {
		if !strings.HasPrefix(ref, "c:") {
			continue
		}
		if found != "" {
			return ""
		}
		found = ref
	}
	return found
}

func insertTransferCatalogEntry(ctx context.Context, tx *sql.Tx, identity *BackupIdentity, primaryCode string, workID int64, entryRef string) (int64, error) {
	suffix := strings.TrimPrefix(entryRef, "c:")
	if suffix == "" {
		suffix = strings.TrimPrefix(identity.Ref, "w:")
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO source_catalog_entry(root_path,primary_code,title,work_id)
		VALUES(?,?,?,?)`, "personal-transfer://"+suffix, primaryCode, identity.Title, workID)
	if err != nil {
		return 0, err
	}
	entryID, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	if entryRef != "" {
		if err = claimTransferRef(ctx, tx, entryRef, "catalog_entry_id", entryID); err != nil {
			return 0, err
		}
	}
	return entryID, nil
}

func registerEpisodeTransferWork(ctx context.Context, tx *sql.Tx, work BackupWork, parent *BackupParent, match identityMatch) (int64, error) {
	if match.series == 0 {
		return 0, ErrConflict
	}
	workID, err := insertTransferWork(ctx, tx, work.PrimaryCode, work.Identity.Title, "episode")
	if err != nil {
		return 0, err
	}
	entryID, err := insertTransferCatalogEntry(ctx, tx, work.Identity, work.PrimaryCode, workID, episodeEntryRef(work.Identity, parent))
	if err != nil {
		return 0, err
	}
	if err = ensureEpisodeMembership(ctx, tx, work, workID, identityMatch{catalog: entryID}, parent, match); err != nil {
		return 0, err
	}
	return workID, nil
}

func ensureEpisodeMembership(ctx context.Context, tx *sql.Tx, work BackupWork, workID int64, match identityMatch, parent *BackupParent, parentMatch identityMatch) error {
	if parent == nil || parentMatch.series == 0 {
		return ErrConflict
	}
	entryID := match.catalog
	if parent.EntryRef != "" {
		var retainedEntry int64
		err := tx.QueryRowContext(ctx, `SELECT catalog_entry_id FROM personal_transfer_ref
			WHERE ref=? AND catalog_entry_id IS NOT NULL`, parent.EntryRef).Scan(&retainedEntry)
		switch {
		case err == nil:
			if entryID != 0 && entryID != retainedEntry {
				return ErrConflict
			}
			entryID = retainedEntry
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
	}
	if entryID == 0 && workID != 0 {
		var count int
		var only sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),MIN(id) FROM source_catalog_entry WHERE work_id=?`, workID).Scan(&count, &only); err != nil {
			return err
		}
		if count == 1 {
			entryID = only.Int64
		}
	}
	if entryID == 0 {
		var err error
		entryID, err = insertTransferCatalogEntry(ctx, tx, work.Identity, work.PrimaryCode, workID, episodeEntryRef(work.Identity, parent))
		if err != nil {
			return err
		}
	}
	var entryWork sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT work_id FROM source_catalog_entry WHERE id=?`, entryID).Scan(&entryWork); err != nil {
		return err
	}
	if !entryWork.Valid {
		if _, err := tx.ExecContext(ctx, `UPDATE source_catalog_entry SET work_id=? WHERE id=? AND work_id IS NULL`, workID, entryID); err != nil {
			return err
		}
	} else if entryWork.Int64 != workID {
		return ErrConflict
	}
	if parent.EntryRef != "" {
		if err := claimTransferRef(ctx, tx, parent.EntryRef, "catalog_entry_id", entryID); err != nil {
			return err
		}
	}
	var existingSeries int64
	err := tx.QueryRowContext(ctx, `SELECT series_id FROM catalog_series_member WHERE catalog_entry_id=?`, entryID).Scan(&existingSeries)
	if err == nil {
		if existingSeries != parentMatch.series {
			return ErrConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	position := parent.Position
	if position <= 0 {
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position),0)+1 FROM catalog_series_member WHERE series_id=?`, parentMatch.series).Scan(&position); err != nil {
			return err
		}
	}
	if position > maxTransferSeriesMembers {
		return ErrConflict
	}
	var occupied int64
	err = tx.QueryRowContext(ctx, `SELECT catalog_entry_id FROM catalog_series_member WHERE series_id=? AND position=?`, parentMatch.series, position).Scan(&occupied)
	switch {
	case err == nil && occupied != entryID:
		return ErrConflict
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return err
	}
	evidence := parent.Evidence
	if evidence == "" {
		evidence = "manual"
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence)
		VALUES(?,?,?,?)`, parentMatch.series, entryID, position, evidence)
	return err
}

func claimTransferRef(ctx context.Context, tx *sql.Tx, ref, column string, id int64) error {
	var work, catalog, series sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT work_id,catalog_entry_id,series_id FROM personal_transfer_ref WHERE ref=?`, ref).
		Scan(&work, &catalog, &series)
	if err == nil {
		switch column {
		case "catalog_entry_id":
			if catalog.Valid && catalog.Int64 == id {
				return nil
			}
		case "series_id":
			if series.Valid && series.Int64 == id {
				return nil
			}
		}
		return ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var targetExists bool
	switch column {
	case "catalog_entry_id":
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM source_catalog_entry WHERE id=?)`, id).Scan(&targetExists)
	case "series_id":
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM catalog_series WHERE id=?)`, id).Scan(&targetExists)
	default:
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if !targetExists {
		return ErrConflict
	}
	var result sql.Result
	switch column {
	case "catalog_entry_id":
		result, err = tx.ExecContext(ctx, `UPDATE personal_transfer_ref SET ref=? WHERE catalog_entry_id=?`, ref, id)
	case "series_id":
		result, err = tx.ExecContext(ctx, `UPDATE personal_transfer_ref SET ref=? WHERE series_id=?`, ref, id)
	default:
		return ErrConflict
	}
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		// A generated alias may be absent on a partially initialized database.
		// Create the exact reference only after the target row exists and the
		// requested ref was proven unclaimed above.
		switch column {
		case "catalog_entry_id":
			_, err = tx.ExecContext(ctx, `INSERT INTO personal_transfer_ref(ref,catalog_entry_id) VALUES(?,?)`, ref, id)
		case "series_id":
			_, err = tx.ExecContext(ctx, `INSERT INTO personal_transfer_ref(ref,series_id,object_kind) VALUES(?,?,'series')`, ref, id)
		}
		return err
	}
	if changed != 1 {
		return ErrConflict
	}
	return nil
}
