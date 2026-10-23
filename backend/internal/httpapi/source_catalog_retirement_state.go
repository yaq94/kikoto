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
// with an empty note or default position is fillable. Portable progress and a
// playback cursor are kept whenever the target work already has a row for that
// user, matching the import conflict rule.
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
		) OR EXISTS(
			SELECT 1
			FROM user_work_playback_cursor AS old
			INNER JOIN user_work_playback_cursor AS current
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
	if err := moveRetiredCatalogPlaybackCursorsTx(ctx, tx, staleWorkID, targetWorkID); err != nil {
		return err
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

// moveRetiredCatalogPlaybackCursorsTx migrates each user's resume point without
// choosing between two existing cursors. If the target has no playable media,
// the old media item can move with the cursor. Otherwise the old item must map
// to exactly one target track; ambiguous matches keep both cards. A location is
// reused only when the old source identifies one target location. Missing
// location identity is safer than attaching the cursor to an unrelated file.
func moveRetiredCatalogPlaybackCursorsTx(ctx context.Context, tx *sql.Tx, staleWorkID, targetWorkID int64) error {
	var targetHasPlayableMedia bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1 FROM media_item
			WHERE work_id=?1 AND kind IN ('audio','video'))`, targetWorkID).Scan(&targetHasPlayableMedia); err != nil {
		return err
	}
	type playbackCursor struct {
		userID       int64
		mediaItemID  int64
		fileSourceID sql.NullInt64
	}
	rows, err := tx.QueryContext(ctx, `SELECT user_id,media_item_id,file_source_id
		FROM user_work_playback_cursor WHERE work_id=? ORDER BY user_id`, staleWorkID)
	if err != nil {
		return err
	}
	cursors := []playbackCursor{}
	for rows.Next() {
		var cursor playbackCursor
		if err := rows.Scan(&cursor.userID, &cursor.mediaItemID, &cursor.fileSourceID); err != nil {
			_ = rows.Close()
			return err
		}
		cursors = append(cursors, cursor)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	for _, cursor := range cursors {
		if !targetHasPlayableMedia {
			if _, err := tx.ExecContext(ctx, `UPDATE media_item SET work_id=?1
				WHERE id=?2 AND work_id=?3`, targetWorkID, cursor.mediaItemID, staleWorkID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE user_work_playback_cursor SET work_id=?1
				WHERE user_id=?2 AND work_id=?3`, targetWorkID, cursor.userID, staleWorkID); err != nil {
				return err
			}
			continue
		}

		targetMediaItemID, matched, err := matchRetiredCatalogPlaybackMediaTx(ctx, tx, cursor.mediaItemID, targetWorkID)
		if err != nil {
			return err
		}
		if !matched {
			return errRetiredCatalogPersonalStateConflict
		}
		fileSourceID, locationID, locationType, err := matchRetiredCatalogPlaybackLocationTx(ctx, tx, targetMediaItemID, cursor.fileSourceID)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE user_work_playback_cursor
			SET work_id=?1,media_item_id=?2,file_source_id=?3,location_id=?4,location_type=?5
			WHERE user_id=?6 AND work_id=?7`,
			targetWorkID, targetMediaItemID, fileSourceID, locationID, locationType,
			cursor.userID, staleWorkID); err != nil {
			return err
		}
	}
	return nil
}

// matchRetiredCatalogPlaybackMediaTx requires one target track to match every
// stable identity field. NULL disc and track values require NULL on both sides.
// Duration only narrows the match when both rows know it; an empty old
// fingerprint likewise does not constrain candidates. More than one match is
// ambiguous and must not be guessed from the title alone.
func matchRetiredCatalogPlaybackMediaTx(ctx context.Context, tx *sql.Tx, staleMediaItemID, targetWorkID int64) (int64, bool, error) {
	var count int
	var mediaItemID int64
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MIN(candidate.id),0)
		FROM media_item AS old
		INNER JOIN media_item AS candidate
			ON candidate.work_id=?1
			AND candidate.kind IN ('audio','video')
			AND candidate.title=old.title
			AND candidate.disc_no IS old.disc_no
			AND candidate.track_no IS old.track_no
			AND (old.duration_seconds IS NULL OR candidate.duration_seconds IS NULL
				OR ABS(candidate.duration_seconds-old.duration_seconds)<=2)
			AND (old.fingerprint='' OR candidate.fingerprint=old.fingerprint)
		WHERE old.id=?2`, targetWorkID, staleMediaItemID).Scan(&count, &mediaItemID)
	if err != nil {
		return 0, false, err
	}
	return mediaItemID, count == 1, nil
}

// matchRetiredCatalogPlaybackLocationTx returns a target location only when the
// old file source selects exactly one. Without a source, a sole target location
// is still unambiguous. Any miss or tie clears the location identity.
func matchRetiredCatalogPlaybackLocationTx(ctx context.Context, tx *sql.Tx, mediaItemID int64, oldSourceID sql.NullInt64) (sql.NullInt64, sql.NullInt64, string, error) {
	query := `SELECT id,file_source_id,location_type FROM media_file_location
		WHERE media_item_id=?1 ORDER BY id`
	args := []any{mediaItemID}
	if oldSourceID.Valid {
		query = `SELECT id,file_source_id,location_type FROM media_file_location
			WHERE media_item_id=?1 AND file_source_id=?2 ORDER BY id`
		args = append(args, oldSourceID.Int64)
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return sql.NullInt64{}, sql.NullInt64{}, "", err
	}
	type playbackLocation struct {
		id           int64
		fileSourceID int64
		locationType string
	}
	locations := []playbackLocation{}
	for rows.Next() {
		var location playbackLocation
		if err := rows.Scan(&location.id, &location.fileSourceID, &location.locationType); err != nil {
			_ = rows.Close()
			return sql.NullInt64{}, sql.NullInt64{}, "", err
		}
		locations = append(locations, location)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return sql.NullInt64{}, sql.NullInt64{}, "", err
	}
	if err := rows.Close(); err != nil {
		return sql.NullInt64{}, sql.NullInt64{}, "", err
	}
	if len(locations) != 1 {
		return sql.NullInt64{}, sql.NullInt64{}, "", nil
	}
	return sql.NullInt64{Int64: locations[0].fileSourceID, Valid: true},
		sql.NullInt64{Int64: locations[0].id, Valid: true},
		locations[0].locationType, nil
}
