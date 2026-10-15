package httpapi

import "context"

// Cleanup cannot cascade through any account-owned work/media reference. This
// deliberately includes blank state rows, saved lists/tags and listening data.
func workFamilyHasPersonalReferences(ctx context.Context, q unlinkedWorkQueryer, ids []int64) (bool, error) {
	if len(ids) == 0 {
		return false, nil
	}
	p := sqlPlaceholders(len(ids))
	args := int64Args(ids)
	for _, table := range []string{"user_work_state", "favorite_list_item", "user_work_tag", "user_listening_session", "user_work_playback_cursor", "recommendation_event"} {
		var found bool
		if err := q.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM "+table+" WHERE work_id IN ("+p+"))", args...).Scan(&found); err != nil {
			return false, err
		}
		if found {
			return true, nil
		}
	}
	for _, condition := range []string{
		`EXISTS(SELECT 1 FROM user_media_progress p WHERE p.media_item_id=m.id)`,
		`EXISTS(SELECT 1 FROM user_work_playback_cursor p WHERE p.media_item_id=m.id)`,
		`EXISTS(SELECT 1 FROM user_media_lyrics_preference p WHERE p.audio_media_item_id=m.id OR p.lyrics_media_item_id=m.id)`,
	} {
		var found bool
		if err := q.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM media_item m WHERE m.work_id IN ("+p+") AND "+condition+")", args...).Scan(&found); err != nil {
			return false, err
		}
		if found {
			return true, nil
		}
	}
	return false, nil
}
