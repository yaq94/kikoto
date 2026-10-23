package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"path"
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

type catalogSeriesMemberEvidence struct {
	root, name, content, layout string
	generation                  int64
}

func catalogSeriesLiveMembersTx(ctx context.Context, tx *sql.Tx, seriesID int64) ([]catalogSeriesMemberEvidence, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+catalogEffectiveRootSQL("e")+`,
			e.content_fingerprint,e.layout_fingerprint,e.last_seen_generation
		FROM catalog_series_member m JOIN source_catalog_entry e ON e.id=m.catalog_entry_id
		WHERE m.series_id=? AND e.relocated_to_id IS NULL ORDER BY 1`, seriesID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	members := []catalogSeriesMemberEvidence{}
	for rows.Next() {
		var member catalogSeriesMemberEvidence
		var root string
		if err = rows.Scan(&root, &member.content, &member.layout, &member.generation); err != nil {
			return nil, err
		}
		member.root = root
		member.name = strings.ToLower(path.Base(root))
		members = append(members, member)
	}
	return members, rows.Err()
}

// catalogSeriesDirectoryParent returns the directory that contains the series
// directory. source_key normally records the episode parent before the newline;
// older rows may lack that separator, so derive the same path from every live
// member instead. A series directly below the source root has no usable parent
// for distinguishing a rename from an unrelated sibling series.
func catalogSeriesDirectoryParent(key sql.NullString, members []catalogSeriesMemberEvidence) (string, bool) {
	seriesDir := ""
	if key.Valid {
		if parent, _, ok := strings.Cut(key.String, "\n"); ok {
			if candidate := path.Clean(strings.TrimSpace(parent)); candidate != "." {
				seriesDir = candidate
			}
		}
	}
	if seriesDir == "" {
		for _, member := range members {
			candidate := path.Clean(path.Dir(member.root))
			if seriesDir == "" {
				seriesDir = candidate
				continue
			}
			if candidate != seriesDir {
				return "", false
			}
		}
	}
	if seriesDir == "" || seriesDir == "/" || !path.IsAbs(seriesDir) {
		return "", false
	}
	parent := path.Dir(seriesDir)
	if parent == "/" || parent == "." {
		return "", false
	}
	return parent, true
}

// catalogSeriesRenameEvidenceTx reports whether two series describe the same
// group after its own directory was renamed. The old live episode names must
// still be present in the new series, which allows a rename and a new episode
// to be published by the same scan. Both series must share a comparable parent
// directory, and the old members must come from an earlier generation. Every
// old episode needs one matching fingerprint; a partial match is not enough.
func catalogSeriesRenameEvidenceTx(ctx context.Context, tx *sql.Tx, oldSeries, newSeries int64) (bool, error) {
	if oldSeries == newSeries {
		return true, nil
	}
	var oldSource, newSource sql.NullInt64
	var oldKey, newKey sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT
			(SELECT source_id FROM catalog_series WHERE id=?),
			(SELECT source_key FROM catalog_series WHERE id=?),
			(SELECT source_id FROM catalog_series WHERE id=?),
			(SELECT source_key FROM catalog_series WHERE id=?)`, oldSeries, oldSeries, newSeries, newSeries).Scan(&oldSource, &oldKey, &newSource, &newKey); err != nil {
		return false, err
	}
	if !oldSource.Valid || !newSource.Valid || oldSource.Int64 != newSource.Int64 {
		return false, nil
	}
	oldMembers, err := catalogSeriesLiveMembersTx(ctx, tx, oldSeries)
	if err != nil {
		return false, err
	}
	newMembers, err := catalogSeriesLiveMembersTx(ctx, tx, newSeries)
	if err != nil {
		return false, err
	}
	if len(oldMembers) == 0 {
		return false, nil
	}
	oldParent, oldComparable := catalogSeriesDirectoryParent(oldKey, oldMembers)
	newParent, newComparable := catalogSeriesDirectoryParent(newKey, newMembers)
	if !oldComparable || !newComparable || oldParent != newParent {
		return false, nil
	}
	remaining := map[string]int{}
	for _, member := range oldMembers {
		remaining[member.name]++
	}
	for _, member := range newMembers {
		if remaining[member.name] > 0 {
			remaining[member.name]--
		}
	}
	for _, count := range remaining {
		if count != 0 {
			return false, nil
		}
	}
	if len(newMembers) == 0 {
		return false, nil
	}
	oldGeneration, newGeneration := oldMembers[0].generation, newMembers[0].generation
	for _, member := range oldMembers[1:] {
		if member.generation > oldGeneration {
			oldGeneration = member.generation
		}
	}
	for _, member := range newMembers[1:] {
		if member.generation < newGeneration {
			newGeneration = member.generation
		}
	}
	if oldGeneration >= newGeneration {
		return false, nil
	}
	for _, oldMember := range oldMembers {
		matched := false
		for _, newMember := range newMembers {
			if oldMember.name != newMember.name {
				continue
			}
			if catalogFingerprintUsable(oldMember.content) && oldMember.content == newMember.content {
				matched = true
				break
			}
			if catalogFingerprintUsable(oldMember.layout) && oldMember.layout == newMember.layout {
				matched = true
				break
			}
		}
		if !matched {
			return false, nil
		}
	}
	return true, nil
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
			if _, execErr := tx.ExecContext(ctx, `UPDATE catalog_series_member SET position=? WHERE catalog_entry_id=?`, evidence.Position, toID); execErr != nil {
				return execErr
			}
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
//
// A renamed directory also changes the series key. The survivor adopts the new
// key, otherwise the next episode discovered under that directory creates a
// second series for the same group.
func foldCatalogSeriesTx(ctx context.Context, tx *sql.Tx, fromSeries, toSeries int64) error {
	var (
		newTitle string
		newKey   sql.NullString
		fromWork sql.NullInt64
		toWork   sql.NullInt64
	)
	if err := tx.QueryRowContext(ctx, `SELECT title,source_key,work_id FROM catalog_series WHERE id=?`, fromSeries).Scan(&newTitle, &newKey, &fromWork); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT work_id FROM catalog_series WHERE id=?`, toSeries).Scan(&toWork); err != nil {
		return err
	}
	// Reviews hang off the series work row. Two registered series cannot be
	// folded without choosing whose records the survivor keeps, so both stay
	// for an explicit decision.
	if fromWork.Valid && toWork.Valid && fromWork.Int64 != toWork.Int64 {
		return errCatalogSeriesRetireConflict
	}

	var maxPosition int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position),0) FROM catalog_series_member WHERE series_id=?`, toSeries).Scan(&maxPosition); err != nil {
		return err
	}
	type foldedMember struct{ id, position int64 }
	members := []foldedMember{}
	rows, err := tx.QueryContext(ctx, `SELECT catalog_entry_id,position FROM catalog_series_member WHERE series_id=? ORDER BY position`, fromSeries)
	if err != nil {
		return err
	}
	for rows.Next() {
		var member foldedMember
		if err = rows.Scan(&member.id, &member.position); err != nil {
			break
		}
		members = append(members, member)
	}
	readErr := rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	if readErr != nil {
		return readErr
	}
	for _, member := range members {
		// Keep the authored episode number when the slot is free; append only
		// when another episode already owns it, so folding never renumbers.
		position := member.position
		var occupied int
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM catalog_series_member WHERE series_id=? AND position=?)`, toSeries, position).Scan(&occupied); err != nil {
			return err
		}
		if occupied != 0 {
			maxPosition++
			position = maxPosition
		}
		if _, err = tx.ExecContext(ctx, `UPDATE catalog_series_member SET series_id=?,position=? WHERE catalog_entry_id=?`, toSeries, position, member.id); err != nil {
			return err
		}
	}
	// Previews and personal import references name the series id, not just its
	// members. Move them so a pending decision and an exported reference stay
	// resolvable after the row disappears.
	if _, err = tx.ExecContext(ctx, `UPDATE catalog_series_preview SET series_id=? WHERE series_id=?`, toSeries, fromSeries); err != nil {
		return err
	}
	var fromRef, toRef int
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM personal_transfer_ref WHERE series_id=?),EXISTS(SELECT 1 FROM personal_transfer_ref WHERE series_id=?)`, fromSeries, toSeries).Scan(&fromRef, &toRef); err != nil {
		return err
	}
	if fromRef != 0 {
		if toRef != 0 {
			// One stable reference per series: the survivor's own reference wins.
			if _, err = tx.ExecContext(ctx, `DELETE FROM personal_transfer_ref WHERE series_id=?`, fromSeries); err != nil {
				return err
			}
		} else if _, err = tx.ExecContext(ctx, `UPDATE personal_transfer_ref SET series_id=? WHERE series_id=?`, toSeries, fromSeries); err != nil {
			return err
		}
	}
	// work_id is UNIQUE, so the retired row must release it before the survivor
	// takes over the reviews.
	if fromWork.Valid {
		if _, err = tx.ExecContext(ctx, `UPDATE catalog_series SET work_id=NULL WHERE id=?`, fromSeries); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM catalog_series WHERE id=? AND NOT EXISTS(SELECT 1 FROM catalog_series_member WHERE series_id=?)`, fromSeries, fromSeries); err != nil {
		return err
	}
	if fromWork.Valid && !toWork.Valid {
		if _, err = tx.ExecContext(ctx, `UPDATE catalog_series SET work_id=? WHERE id=?`, fromWork.Int64, toSeries); err != nil {
			return err
		}
	}
	if newKey.Valid && newKey.String != "" {
		if _, err = tx.ExecContext(ctx, `UPDATE catalog_series SET source_key=? WHERE id=? AND source_key<>?`, newKey.String, toSeries, newKey.String); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE catalog_series SET title=? WHERE id=? AND title<>?`, newTitle, toSeries, newTitle)
	return err
}
