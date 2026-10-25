package personal

import (
	"context"
	"database/sql"
	"errors"
)

const maxTransferParentDepth = 64

// transferParent names the one series an episode belongs to. workID is zero
// while the series is known only through catalog discovery. key names a
// top-level series BackupWork that creates the same parent.
type transferParent struct {
	seriesID int64
	workID   int64
	title    string
	create   bool
	key      string
	freeRef  bool
	// nested marks a series work that itself has a parent. The episode's parent
	// chain is inspected separately so a loop reports parent_cycle while any
	// other nesting reports parent_conflict.
	nested bool
}

type transferRefState struct {
	work    sql.NullInt64
	catalog sql.NullInt64
	series  sql.NullInt64
	exists  bool
}

func loadTransferRefState(ctx context.Context, tx *sql.Tx, ref string) (transferRefState, error) {
	state := transferRefState{}
	err := tx.QueryRowContext(ctx, `SELECT work_id,catalog_entry_id,series_id FROM personal_transfer_ref WHERE ref=?`, ref).
		Scan(&state.work, &state.catalog, &state.series)
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	state.exists = true
	return state, nil
}

// resolveParentPlan matches only retained series references. It never matches a
// parent from a title, source path or the episode's own membership. A reference
// owned by a catalog entry or an ordinary work is contradictory.
func resolveParentPlan(ctx context.Context, tx *sql.Tx, parent *BackupIdentity) (transferParent, string, error) {
	plan := transferParent{}
	if !validBackupParent(parent) {
		return plan, "parent_conflict", nil
	}
	refs := append([]string{parent.Ref}, parent.References...)
	for index, ref := range refs {
		state, err := loadTransferRefState(ctx, tx, ref)
		if err != nil {
			return plan, "", err
		}
		if !state.exists {
			// The parent's own reference may be new. A declared alias must
			// resolve; an unknown alias is not evidence for this series.
			if index > 0 {
				return plan, "parent_conflict", nil
			}
			plan.freeRef = true
			continue
		}
		if state.catalog.Valid {
			return plan, "parent_conflict", nil
		}
		if state.series.Valid {
			var title string
			var work sql.NullInt64
			err = tx.QueryRowContext(ctx, `SELECT title,work_id FROM catalog_series WHERE id=?`, state.series.Int64).Scan(&title, &work)
			if errors.Is(err, sql.ErrNoRows) {
				return plan, "parent_conflict", nil
			}
			if err != nil {
				return plan, "", err
			}
			if plan.seriesID != 0 && plan.seriesID != state.series.Int64 {
				return plan, "parent_conflict", nil
			}
			if work.Valid {
				var parentOfWork sql.NullInt64
				if err = tx.QueryRowContext(ctx, `SELECT parent_work_id FROM work WHERE id=?`, work.Int64).Scan(&parentOfWork); err != nil {
					if errors.Is(err, sql.ErrNoRows) {
						return plan, "parent_conflict", nil
					}
					return plan, "", err
				}
				if parentOfWork.Valid {
					plan.nested = true
				}
				if plan.workID != 0 && plan.workID != work.Int64 {
					return plan, "parent_conflict", nil
				}
				plan.workID = work.Int64
			}
			plan.seriesID, plan.title = state.series.Int64, title
			continue
		}
		if state.work.Valid {
			var seriesID int64
			var title string
			err = tx.QueryRowContext(ctx, `SELECT series.id,series.title FROM catalog_series series
				JOIN work w ON w.id=series.work_id
				WHERE series.work_id=? AND w.parent_work_id IS NULL`, state.work.Int64).Scan(&seriesID, &title)
			if errors.Is(err, sql.ErrNoRows) {
				return plan, "parent_conflict", nil
			}
			if err != nil {
				return plan, "", err
			}
			if plan.seriesID != 0 && plan.seriesID != seriesID {
				return plan, "parent_conflict", nil
			}
			if plan.workID != 0 && plan.workID != state.work.Int64 {
				return plan, "parent_conflict", nil
			}
			plan.seriesID, plan.workID, plan.title = seriesID, state.work.Int64, title
		}
	}
	if plan.seriesID != 0 {
		return plan, "", nil
	}
	if !plan.freeRef {
		// The parent reference exists but names no series.
		return plan, "parent_conflict", nil
	}
	plan.create = true
	plan.title = parent.Title
	return plan, "", nil
}

// ensureParent creates or reuses the minimal series identity a backup names and
// adopts the backup reference. A free reference replaces the trigger-generated
// one; the same series reuses it; any other owner is contradictory.
func ensureParent(ctx context.Context, tx *sql.Tx, planned transferParent, parent *BackupIdentity) (transferParent, string, error) {
	resolved, problem, err := resolveParentPlan(ctx, tx, parent)
	if err != nil || problem != "" {
		return resolved, problem, err
	}
	resolved.key = planned.key
	if resolved.seriesID == 0 && planned.seriesID != 0 {
		resolved.seriesID, resolved.title = planned.seriesID, planned.title
	}
	if resolved.nested {
		return resolved, "parent_conflict", nil
	}
	// The same backup may carry the parent series as a top-level BackupWork
	// under its own work reference. That series is already registered by the
	// time episodes run, so reuse it instead of creating a second one.
	if resolved.create && planned.workID != 0 {
		var parentOfWork sql.NullInt64
		if err = tx.QueryRowContext(ctx, `SELECT parent_work_id FROM work WHERE id=?`, planned.workID).Scan(&parentOfWork); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return resolved, "parent_conflict", nil
			}
			return resolved, "", err
		}
		if parentOfWork.Valid {
			return resolved, "parent_conflict", nil
		}
		var seriesID int64
		if err = tx.QueryRowContext(ctx, `SELECT id FROM catalog_series WHERE work_id=?`, planned.workID).Scan(&seriesID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return resolved, "parent_conflict", nil
			}
			return resolved, "", err
		}
		resolved.create, resolved.workID, resolved.seriesID = false, planned.workID, seriesID
	}
	if resolved.workID != 0 && resolved.seriesID == 0 {
		var seriesID int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM catalog_series WHERE work_id=?`, resolved.workID).Scan(&seriesID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return resolved, "parent_conflict", nil
			}
			return resolved, "", err
		}
		resolved.seriesID = seriesID
	}
	created := false
	switch {
	case resolved.workID != 0:
	case resolved.seriesID != 0:
		workID, err := insertTransferWork(ctx, tx, "", resolved.title, "series")
		if err != nil {
			return resolved, "", err
		}
		result, err := tx.ExecContext(ctx, `UPDATE catalog_series SET work_id=? WHERE id=? AND work_id IS NULL`, workID, resolved.seriesID)
		if err != nil {
			return resolved, "", err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return resolved, "parent_conflict", nil
		}
		resolved.workID = workID
	default:
		created = true
		workID, err := insertTransferWork(ctx, tx, "", resolved.title, "series")
		if err != nil {
			return resolved, "", err
		}
		var seriesID int64
		if err = tx.QueryRowContext(ctx, `INSERT INTO catalog_series(title,work_id) VALUES(?,?) RETURNING id`, resolved.title, workID).Scan(&seriesID); err != nil {
			return resolved, "", err
		}
		resolved.seriesID, resolved.workID = seriesID, workID
	}
	if err = adoptParentRefs(ctx, tx, resolved, parent, created); err != nil {
		if errors.Is(err, ErrConflict) {
			return resolved, "parent_conflict", nil
		}
		return resolved, "", err
	}
	return resolved, "", nil
}

func adoptParentRefs(ctx context.Context, tx *sql.Tx, parent transferParent, identity *BackupIdentity, created bool) error {
	if parent.seriesID == 0 || parent.workID == 0 {
		return ErrConflict
	}
	state, err := loadTransferRefState(ctx, tx, identity.Ref)
	if err != nil {
		return err
	}
	switch {
	case !state.exists:
		if created {
			result, err := tx.ExecContext(ctx, `UPDATE personal_transfer_ref SET ref=? WHERE series_id=?`, identity.Ref, parent.seriesID)
			if err != nil {
				return err
			}
			changed, err := result.RowsAffected()
			if err != nil || changed != 1 {
				return ErrConflict
			}
		} else {
			if _, err = tx.ExecContext(ctx, `INSERT INTO personal_transfer_ref(ref,work_id,object_kind) VALUES(?,?,'series')`, identity.Ref, parent.workID); err != nil {
				return err
			}
		}
	case state.series.Valid && state.series.Int64 == parent.seriesID:
	case state.work.Valid && state.work.Int64 == parent.workID:
	default:
		return ErrConflict
	}
	for _, ref := range identity.References {
		if ref == identity.Ref {
			continue
		}
		state, err := loadTransferRefState(ctx, tx, ref)
		if err != nil {
			return err
		}
		if !state.exists {
			if _, err = tx.ExecContext(ctx, `INSERT INTO personal_transfer_ref(ref,work_id,object_kind) VALUES(?,?,'series')`, ref, parent.workID); err != nil {
				return err
			}
			continue
		}
		if state.series.Valid && state.series.Int64 == parent.seriesID {
			continue
		}
		if state.work.Valid && state.work.Int64 == parent.workID {
			continue
		}
		return ErrConflict
	}
	return nil
}

func episodeSeriesIDs(ctx context.Context, tx *sql.Tx, work, catalog int64) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT m.series_id FROM source_catalog_entry e
		JOIN catalog_series_member m ON m.catalog_entry_id=e.id
		WHERE (?<>0 AND e.work_id=?) OR (?<>0 AND e.id=?)
		ORDER BY m.series_id`, work, work, catalog, catalog)
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

type episodeParentState struct {
	parentWork int64
	series     []int64
	problem    string
}

func loadEpisodeParentState(ctx context.Context, tx *sql.Tx, work, catalog int64) (episodeParentState, error) {
	state := episodeParentState{}
	if work != 0 {
		var parent sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT parent_work_id FROM work WHERE id=?`, work).Scan(&parent); err != nil {
			return state, err
		}
		if parent.Valid {
			var seriesID int64
			err := tx.QueryRowContext(ctx, `SELECT id FROM catalog_series WHERE work_id=?`, parent.Int64).Scan(&seriesID)
			if errors.Is(err, sql.ErrNoRows) {
				state.problem = "parent_conflict"
			} else if err != nil {
				return state, err
			} else {
				state.parentWork = parent.Int64
				state.series = append(state.series, seriesID)
			}
		}
	}
	series, err := episodeSeriesIDs(ctx, tx, work, catalog)
	if err != nil {
		return state, err
	}
	for _, seriesID := range series {
		duplicate := false
		for _, existing := range state.series {
			if existing == seriesID {
				duplicate = true
				break
			}
		}
		if !duplicate {
			state.series = append(state.series, seriesID)
		}
	}
	if len(state.series) > 1 {
		state.problem = "parent_conflict"
	}
	return state, nil
}

// transferParentCycleProblem follows the existing parent chain, bounded so a
// malformed self-referencing row cannot recurse forever.
func transferParentCycleProblem(ctx context.Context, tx *sql.Tx, episode, parent int64) (string, error) {
	if episode == 0 || parent == 0 {
		return "", nil
	}
	if episode == parent {
		return "parent_cycle", nil
	}
	current := parent
	seen := map[int64]bool{}
	for depth := 0; depth <= maxTransferParentDepth; depth++ {
		if current == episode {
			return "parent_cycle", nil
		}
		if seen[current] {
			return "parent_cycle", nil
		}
		seen[current] = true
		var next sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT parent_work_id FROM work WHERE id=?`, current).Scan(&next)
		if errors.Is(err, sql.ErrNoRows) {
			return "parent_conflict", nil
		}
		if err != nil {
			return "", err
		}
		if !next.Valid {
			return "", nil
		}
		current = next.Int64
	}
	return "parent_cycle", nil
}

func ensureEpisodeParent(ctx context.Context, tx *sql.Tx, episode, parent int64) (string, error) {
	if episode == 0 || parent == 0 || episode == parent {
		return "parent_conflict", nil
	}
	var parentOfWork sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT parent_work_id FROM work WHERE id=?`, parent).Scan(&parentOfWork); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "parent_conflict", nil
		}
		return "", err
	}
	if parentOfWork.Valid {
		return "parent_conflict", nil
	}
	var seriesID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM catalog_series WHERE work_id=?`, parent).Scan(&seriesID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "parent_conflict", nil
		}
		return "", err
	}
	problem, err := transferParentCycleProblem(ctx, tx, episode, parent)
	if err != nil || problem != "" {
		return problem, err
	}
	var existing sql.NullInt64
	if err = tx.QueryRowContext(ctx, `SELECT parent_work_id FROM work WHERE id=?`, episode).Scan(&existing); err != nil {
		return "", err
	}
	if existing.Valid && existing.Int64 != parent {
		return "parent_conflict", nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE work SET parent_work_id=? WHERE id=? AND (parent_work_id IS NULL OR parent_work_id=?)`, parent, episode, parent)
	if err != nil {
		return "", err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return "", err
	}
	if changed != 1 {
		return "parent_conflict", nil
	}
	return "", nil
}
