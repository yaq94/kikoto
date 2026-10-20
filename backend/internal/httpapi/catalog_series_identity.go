package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"path"
	"slices"
	"strings"
)

var errCatalogSeriesRetireConflict = errors.New("catalog series membership conflicts during retirement")

// catalogSeriesMembersOfEntryTx lists the series that claim an entry through
// its alias closure. Older relocations could leave a membership on an alias,
// so reading only the live row would let one card join two series.
func catalogSeriesMembersOfEntryTx(ctx context.Context, tx *sql.Tx, entryID int64) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `WITH RECURSIVE aliases(id) AS (
		SELECT ?
		UNION ALL
		SELECT e.id FROM source_catalog_entry e JOIN aliases a ON e.relocated_to_id=a.id
	)
	SELECT DISTINCT m.series_id FROM catalog_series_member m
	WHERE m.catalog_entry_id IN (SELECT id FROM aliases) ORDER BY m.series_id`, entryID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	series := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		series = append(series, id)
	}
	return series, rows.Err()
}

// catalogEffectiveRootSQL names the real directory behind a card. A split card
// keeps a synthetic root_path and records the directory it came from in
// source_root, so anything comparing paths against sibling cards must use this
// expression rather than root_path alone.
func catalogEffectiveRootSQL(alias string) string {
	return "CASE WHEN " + alias + ".source_root<>'' THEN " + alias + ".source_root ELSE " + alias + ".root_path END"
}

// catalogSeriesRenameEvidenceTx reports whether two series describe the same
// group after its own directory was renamed. Both sides must list the same
// live episode directory names one for one; a series that gained or lost an
// episode is a different group and stays for review.
func catalogSeriesRenameEvidenceTx(ctx context.Context, tx *sql.Tx, oldSeries, newSeries int64) (bool, error) {
	if oldSeries == newSeries {
		return true, nil
	}
	// A title alone cannot identify a group: two unrelated series can share a
	// display name. The episode directories must still line up one for one.
	names := func(seriesID int64) ([]string, error) {
		rows, err := tx.QueryContext(ctx, `SELECT `+catalogEffectiveRootSQL("e")+`
			FROM catalog_series_member m JOIN source_catalog_entry e ON e.id=m.catalog_entry_id
			WHERE m.series_id=? AND e.relocated_to_id IS NULL ORDER BY 1`, seriesID)
		if err != nil {
			return nil, err
		}
		defer func() { _ = rows.Close() }()
		result := []string{}
		for rows.Next() {
			var root string
			if err = rows.Scan(&root); err != nil {
				return nil, err
			}
			result = append(result, strings.ToLower(path.Base(root)))
		}
		return result, rows.Err()
	}
	oldNames, err := names(oldSeries)
	if err != nil {
		return false, err
	}
	newNames, err := names(newSeries)
	if err != nil {
		return false, err
	}
	return len(oldNames) > 0 && slices.Equal(oldNames, newNames), nil
}

// mergeCatalogSeriesMembershipTx moves the membership of a retired alias onto
// the card that survives it. A duplicate member of the same series is dropped.
// A renamed series directory is folded into the established series so every
// episode keeps one series and one evaluation level; an unrelated series is
// left for review instead of being merged silently.
func mergeCatalogSeriesMembershipTx(ctx context.Context, tx *sql.Tx, fromID, toID int64) error {
	if fromID == toID || toID == 0 {
		return nil
	}
	fromSeries, err := catalogSeriesMembersOfEntryTx(ctx, tx, fromID)
	if err != nil || len(fromSeries) == 0 {
		return err
	}
	if len(fromSeries) > 1 {
		return errCatalogSeriesRetireConflict
	}
	toSeries, err := catalogSeriesMembersOfEntryTx(ctx, tx, toID)
	if err != nil {
		return err
	}
	if len(toSeries) > 1 {
		return errCatalogSeriesRetireConflict
	}
	if len(toSeries) == 0 {
		_, err = tx.ExecContext(ctx, `UPDATE catalog_series_member SET catalog_entry_id=? WHERE catalog_entry_id=?`, toID, fromID)
		return err
	}
	if toSeries[0] == fromSeries[0] {
		return mergeSameCatalogSeriesMemberTx(ctx, tx, fromID, toID)
	}
	renamed, err := catalogSeriesRenameEvidenceTx(ctx, tx, toSeries[0], fromSeries[0])
	if err != nil {
		return err
	}
	if !renamed {
		return errCatalogSeriesRetireConflict
	}
	if err = foldCatalogSeriesTx(ctx, tx, fromSeries[0], toSeries[0]); err != nil {
		return err
	}
	// Folding moved the target's member too. The survivor now represents the
	// same episode, so drop the duplicate without reordering the series.
	_, err = tx.ExecContext(ctx, `DELETE FROM catalog_series_member WHERE catalog_entry_id=?`, fromID)
	return err
}

// mergeSameCatalogSeriesMemberTx keeps one member for an episode that moved
// inside its series. The target's position carries the new episode number, so
// the survivor takes it when the slot is free; an occupied slot keeps the old
// order rather than displacing another episode.
func mergeSameCatalogSeriesMemberTx(ctx context.Context, tx *sql.Tx, fromID, toID int64) error {
	var seriesID, toPosition int64
	if err := tx.QueryRowContext(ctx, `SELECT series_id FROM catalog_series_member WHERE catalog_entry_id=?`, fromID).Scan(&seriesID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT position FROM catalog_series_member WHERE catalog_entry_id=?`, toID).Scan(&toPosition); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	// The new directory name carries the authored episode number. Adopt it only
	// when that number is explicit; a label-only rename keeps the old order.
	// The survivor still holds the old path here, so read the retired entry.
	var newRoot string
	if err := tx.QueryRowContext(ctx, `SELECT `+catalogEffectiveRootSQL("source_catalog_entry")+` FROM source_catalog_entry WHERE id=?`, fromID).Scan(&newRoot); err != nil {
		return err
	}
	evidence, marked := catalogEpisodeEvidence(newRoot)
	// Release the retired member first so its old position cannot block the
	// survivor from taking the authored number.
	if _, err := tx.ExecContext(ctx, `DELETE FROM catalog_series_member WHERE catalog_entry_id=?`, fromID); err != nil {
		return err
	}
	if marked && evidence.Position > 0 && int64(evidence.Position) != toPosition {
		var occupant int64
		err := tx.QueryRowContext(ctx, `SELECT catalog_entry_id FROM catalog_series_member WHERE series_id=? AND position=?`, seriesID, evidence.Position).Scan(&occupant)
		if errors.Is(err, sql.ErrNoRows) {
			_, err = tx.ExecContext(ctx, `UPDATE catalog_series_member SET position=? WHERE catalog_entry_id=?`, evidence.Position, toID)
		} else if err != nil {
			return err
		}
		// A different episode already owns the number; keep the old slot.
	}
	return nil
}

// foldCatalogSeriesTx moves every member of a renamed series onto the
// established one, then drops the emptied series row. The established id,
// title owner and personal state stay in place.
func foldCatalogSeriesTx(ctx context.Context, tx *sql.Tx, fromSeries, toSeries int64) error {
	var newTitle string
	if err := tx.QueryRowContext(ctx, `SELECT title FROM catalog_series WHERE id=?`, fromSeries).Scan(&newTitle); err != nil {
		return err
	}
	var maxPosition int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position),0) FROM catalog_series_member WHERE series_id=?`, toSeries).Scan(&maxPosition); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT catalog_entry_id FROM catalog_series_member WHERE series_id=? ORDER BY position`, fromSeries)
	if err != nil {
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
		return err
	}
	if readErr != nil {
		return readErr
	}
	for _, id := range ids {
		maxPosition++
		if _, err = tx.ExecContext(ctx, `UPDATE catalog_series_member SET series_id=?,position=? WHERE catalog_entry_id=?`, toSeries, maxPosition, id); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE catalog_series SET title=? WHERE id=? AND title<>?`, newTitle, toSeries, newTitle); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM catalog_series WHERE id=? AND NOT EXISTS(SELECT 1 FROM catalog_series_member WHERE series_id=?)`, fromSeries, fromSeries)
	return err
}
