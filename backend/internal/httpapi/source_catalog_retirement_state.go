package httpapi

import (
	"context"
	"database/sql"
	"errors"
)

var errRetiredCatalogPersonalStateConflict = errors.New("retired catalog personal state conflict")

// retiredCatalogPersonalStateConflictTx reports account-owned values that
// cannot be merged without choosing one owner's value over another. Favorite
// notes and positions conflict only inside the same user's list; a target entry
// with an empty note or default position is fillable. Portable progress is kept
// whenever the target work already has a row for that user, matching the
// import conflict rule.
func retiredCatalogPersonalStateConflictTx(ctx context.Context, tx *sql.Tx, staleWorkID, targetWorkID int64) (bool, error) {
	var conflict int
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1
			FROM favorite_list_item AS old
			INNER JOIN favorite_list_item AS current
				ON current.list_id=old.list_id AND current.work_id=?1
			WHERE old.work_id=?2 AND (
				(old.note<>'' AND current.note<>'' AND old.note<>current.note)
				OR (old.sort_order<>0 AND current.sort_order<>0 AND old.sort_order<>current.sort_order))
		) OR EXISTS(
			SELECT 1
			FROM personal_transfer_progress AS old
			INNER JOIN personal_transfer_progress AS current
				ON current.user_id=old.user_id AND current.work_id=?1
			WHERE old.work_id=?2
		)`, targetWorkID, staleWorkID).Scan(&conflict)
	return conflict != 0, err
}

// moveRetiredCatalogPersonalStateTx moves account-owned state from a format
// card's work before the card is retired. The caller decides rating and note
// conflicts; this function rechecks every retained field before deleting the
// old rows so a stale caller cannot discard an owner's value.
func moveRetiredCatalogPersonalStateTx(ctx context.Context, tx *sql.Tx, staleWorkID, targetWorkID int64) error {
	conflict, err := retiredCatalogPersonalStateConflictTx(ctx, tx, staleWorkID, targetWorkID)
	if err != nil {
		return err
	}
	if conflict {
		return errRetiredCatalogPersonalStateConflict
	}
	statements := []struct {
		query string
		args  []any
	}{
		{
			query: `INSERT INTO favorite_list_item(list_id,work_id,note,sort_order,created_at)
				SELECT list_id,?1,note,sort_order,created_at FROM favorite_list_item WHERE work_id=?2
				ON CONFLICT(list_id,work_id) DO UPDATE SET
					note=CASE WHEN favorite_list_item.note='' THEN excluded.note ELSE favorite_list_item.note END,
					sort_order=CASE WHEN favorite_list_item.sort_order=0 THEN excluded.sort_order ELSE favorite_list_item.sort_order END`,
			args: []any{targetWorkID, staleWorkID},
		},
		{
			query: `INSERT INTO user_work_tag(user_id,work_id,user_tag_id,created_at)
				SELECT user_id,?1,user_tag_id,created_at FROM user_work_tag WHERE work_id=?2
				ON CONFLICT(user_id,work_id,user_tag_id) DO NOTHING`,
			args: []any{targetWorkID, staleWorkID},
		},
		{
			query: `INSERT INTO user_work_playback_cursor(
					user_id,work_id,media_item_id,file_source_id,location_id,location_type,
					position_seconds,duration_seconds,completed,last_played_at,created_at,updated_at,
					report_order,report_id)
				SELECT user_id,?1,media_item_id,file_source_id,location_id,location_type,
					position_seconds,duration_seconds,completed,last_played_at,created_at,updated_at,
					report_order,report_id
				FROM user_work_playback_cursor WHERE work_id=?2
				ON CONFLICT(user_id,work_id) DO UPDATE SET
					media_item_id=excluded.media_item_id,
					file_source_id=excluded.file_source_id,
					location_id=excluded.location_id,
					location_type=excluded.location_type,
					position_seconds=excluded.position_seconds,
					duration_seconds=excluded.duration_seconds,
					completed=excluded.completed,
					last_played_at=excluded.last_played_at,
					updated_at=excluded.updated_at,
					report_order=excluded.report_order,
					report_id=excluded.report_id
				WHERE COALESCE(excluded.last_played_at,'') > COALESCE(user_work_playback_cursor.last_played_at,'')
					OR (COALESCE(excluded.last_played_at,'')=COALESCE(user_work_playback_cursor.last_played_at,'')
						AND excluded.updated_at>user_work_playback_cursor.updated_at)`,
			args: []any{targetWorkID, staleWorkID},
		},
		{
			query: `INSERT INTO user_listening_import(user_id,work_id,listened_seconds,listen_count,last_played_at)
				SELECT user_id,?1,listened_seconds,listen_count,last_played_at
				FROM user_listening_import WHERE work_id=?2
				ON CONFLICT(user_id,work_id) DO UPDATE SET
					listened_seconds=user_listening_import.listened_seconds+excluded.listened_seconds,
					listen_count=user_listening_import.listen_count+excluded.listen_count,
					last_played_at=CASE
						WHEN excluded.last_played_at>user_listening_import.last_played_at THEN excluded.last_played_at
						ELSE user_listening_import.last_played_at END`,
			args: []any{targetWorkID, staleWorkID},
		},
		{
			query: `INSERT INTO user_listening_day(user_id,work_id,day,listened_seconds,listen_count)
				SELECT user_id,?1,day,listened_seconds,listen_count
				FROM user_listening_day WHERE work_id=?2
				ON CONFLICT(user_id,work_id,day) DO UPDATE SET
					listened_seconds=user_listening_day.listened_seconds+excluded.listened_seconds,
					listen_count=user_listening_day.listen_count+excluded.listen_count`,
			args: []any{targetWorkID, staleWorkID},
		},
		{
			query: `INSERT INTO personal_transfer_progress(user_id,work_id,progress_json)
				SELECT user_id,?1,progress_json FROM personal_transfer_progress
				WHERE work_id=?2
					AND NOT EXISTS(SELECT 1 FROM personal_transfer_progress current
						WHERE current.user_id=personal_transfer_progress.user_id AND current.work_id=?1)
				ON CONFLICT(user_id,work_id) DO NOTHING`,
			args: []any{targetWorkID, staleWorkID},
		},
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE user_listening_session SET work_id=?1 WHERE work_id=?2`, targetWorkID, staleWorkID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE recommendation_event SET work_id=?1 WHERE work_id=?2`, targetWorkID, staleWorkID); err != nil {
		return err
	}
	for _, table := range []string{
		"favorite_list_item", "user_work_tag", "user_work_playback_cursor",
		"user_listening_import", "user_listening_day", "personal_transfer_progress",
	} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE work_id=?`, staleWorkID); err != nil {
			return err
		}
	}
	return nil
}
