package personal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"time"
)

type ImportPreview struct {
	issueBytes        int
	Works             int           `json:"works"`
	Playlists         int           `json:"playlists"`
	Tags              int           `json:"tags"`
	MatchedWorks      int           `json:"matchedWorks"`
	MissingCodes      []string      `json:"missingCodes"`
	Conflicts         int           `json:"conflicts"`
	UnmatchedProgress int           `json:"unmatchedProgress"`
	NewWorks          int           `json:"newWorks"`
	Issues            []ImportIssue `json:"issues,omitempty"`
}

func (p *ImportPreview) addIssue(issue ImportIssue) error {
	p.issueBytes += len(issue.Ref) + len(issue.Title) + len(issue.Reason)
	if issue.Incoming != nil {
		p.issueBytes += len(issue.Incoming.Note)
	}
	if issue.Existing != nil {
		p.issueBytes += len(issue.Existing.Note)
	}
	if p.issueBytes > MaxTransferBytes {
		return ErrLimit
	}
	p.Issues = append(p.Issues, issue)
	return nil
}

type TransferReview struct {
	Rating *int   `json:"rating"`
	Note   string `json:"note"`
}
type ImportIssue struct {
	Title    string          `json:"title,omitempty"`
	Ref      string          `json:"ref"`
	Reason   string          `json:"reason"`
	Incoming *TransferReview `json:"incoming,omitempty"`
	Existing *TransferReview `json:"existing,omitempty"`
}

type ImportResult struct {
	ImportedWorks   int           `json:"importedWorks"`
	SkippedWorks    int           `json:"skippedWorks"`
	Playlists       int           `json:"playlists"`
	Tags            int           `json:"tags"`
	SkippedProgress int           `json:"skippedProgress"`
	Issues          []ImportIssue `json:"issues,omitempty"`
}

type importPlan struct {
	preview          ImportPreview
	works            map[string]int64
	matches          map[string]identityMatch
	reviewConflicts  map[int64]bool
	progressConflict map[int64]bool
	statusConflict   map[int64]bool
	media            map[string]int64
	blocked          map[string]bool
	identities       map[string]int64
	reviews          map[int64]TransferReview
}

func resolveCode(ctx context.Context, tx *sql.Tx, code string) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(l.canonical_work_id,w.id) FROM work w LEFT JOIN work_edition e ON e.work_id=w.id LEFT JOIN logical_work l ON l.id=e.logical_work_id WHERE w.primary_code=?`, code).Scan(&id)
	if !errors.Is(err, sql.ErrNoRows) {
		return id, err
	}
	// Only unambiguous provider-declared aliases of existing works are accepted.
	var count int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(DISTINCT w.id),COALESCE(MIN(w.id),0) FROM work_code_alias a JOIN logical_work l ON l.id=a.logical_work_id JOIN work w ON w.id=l.canonical_work_id WHERE UPPER(a.primary_code)=?`, code).Scan(&count, &id)
	if err != nil {
		return 0, err
	}
	if count != 1 {
		return 0, nil
	}
	return id, nil
}

func matchProgress(ctx context.Context, tx *sql.Tx, work int64, p *BackupProgress) (int64, error) {
	if p.MediaTitle == "" {
		return 0, nil
	}
	if p.MediaIdentity != nil {
		m, err := resolveTransferIdentity(ctx, tx, p.MediaIdentity, p.MediaWorkCode)
		if err != nil || m.work == 0 || m.problem != "" {
			return 0, err
		}
		var canonical int64
		err = tx.QueryRowContext(ctx, `SELECT COALESCE(l.canonical_work_id,w.id) FROM work w LEFT JOIN work_edition e ON e.work_id=w.id LEFT JOIN logical_work l ON l.id=e.logical_work_id WHERE w.id=?`, m.work).Scan(&canonical)
		if err != nil || (canonical != work && m.work != work) {
			return 0, err
		}
		var id, count int64
		err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MIN(id),0) FROM media_item WHERE work_id=? AND title=? AND kind IN ('audio','video') AND track_no IS ? AND disc_no IS ?`, m.work, p.MediaTitle, p.TrackNumber, p.DiscNumber).Scan(&count, &id)
		if count != 1 {
			return 0, err
		}
		return id, err
	}
	mediaWork, err := resolveCode(ctx, tx, p.MediaWorkCode)
	if err != nil || mediaWork != work {
		return 0, err
	}
	var id int64
	var count int
	// Never guess between duplicate file names or bind a foreign source-local id.
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MIN(m.id),0) FROM media_item m JOIN work w ON w.id=m.work_id WHERE w.primary_code=? AND m.title=? AND m.kind IN ('audio','video') AND m.track_no IS ? AND m.disc_no IS ?`, p.MediaWorkCode, p.MediaTitle, p.TrackNumber, p.DiscNumber).Scan(&count, &id)
	if err != nil {
		return 0, err
	}
	if count != 1 {
		return 0, nil
	}
	return id, nil
}

func (s Store) PreviewImport(ctx context.Context, user int64, b Backup) (ImportPreview, error) {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ImportPreview{}, err
	}
	defer func() { _ = tx.Rollback() }()
	p, err := buildImportPlan(ctx, tx, user, b)
	return p.preview, err
}

func (s Store) Import(ctx context.Context, user int64, b Backup, overwrite bool) (ImportResult, error) {
	return s.ImportWithRegistration(ctx, user, b, overwrite, false)
}

func (s Store) ImportWithRegistration(ctx context.Context, user int64, b Backup, overwrite, allowRegistration bool) (ImportResult, error) {
	result := ImportResult{}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback() }()
	// Serialize admission and conflict readback before planning an atomic import.
	if _, err = tx.ExecContext(ctx, `UPDATE user_account SET id=id WHERE id=?`, user); err != nil {
		return result, err
	}
	plan, err := buildImportPlan(ctx, tx, user, b)
	if err != nil {
		return result, err
	}
	if plan.preview.NewWorks > 0 && !allowRegistration {
		return result, ErrRegistrationPermission
	}
	result.Issues = plan.preview.Issues
	for _, tag := range b.Tags {
		if _, err = importTag(ctx, tx, user, tag, overwrite); err != nil {
			return result, err
		}
		result.Tags++
	}
	appliedReviews := map[int64]TransferReview{}
	for _, w := range b.Works {
		id := plan.works[w.key()]
		if plan.blocked[w.key()] {
			result.SkippedWorks++
			continue
		}
		if id == 0 && w.Identity != nil {
			id, err = registerTransferIdentityWork(ctx, tx, w, plan.matches[w.key()])
			if err != nil {
				return result, err
			}
			plan.works[w.key()] = id
			plan.identities[w.key()] = id
			plan.reviews[id] = TransferReview{Rating: w.Rating, Note: w.Note}
		}
		if id == 0 {
			result.SkippedWorks++
			continue
		}
		if w.Identity != nil {
			if err = saveTransferReferences(ctx, tx, id, w.Identity); err != nil {
				return result, err
			}
		}
		identity := plan.identities[w.key()]
		keep := importKeep{review: !overwrite && plan.reviewConflicts[identity], status: !overwrite && plan.statusConflict[id], progress: !overwrite && plan.progressConflict[id]}
		if err = importWork(ctx, tx, user, id, plan.media[w.key()], w, overwrite, keep); err != nil {
			return result, err
		}
		// A keep import only carries review fields that were present in the
		// backup. nil rating and an empty note mean "absent", not "clear".
		if review := plan.reviews[identity]; !keep.review && (overwrite || review.Rating != nil || review.Note != "") {
			appliedReviews[identity] = review
		}
		result.ImportedWorks++
		if w.Progress != nil && plan.media[w.key()] == 0 {
			result.SkippedProgress++
		}
	}
	for identity, review := range appliedReviews {
		switch {
		case overwrite || (review.Rating != nil && review.Note != ""):
			_, err = tx.ExecContext(ctx, `INSERT INTO user_work_state(user_id,work_id,rating,note) VALUES(?,?,?,?) ON CONFLICT(user_id,work_id) DO UPDATE SET rating=excluded.rating,note=excluded.note,updated_at=CURRENT_TIMESTAMP`, user, identity, review.Rating, review.Note)
		case review.Rating != nil:
			_, err = tx.ExecContext(ctx, `INSERT INTO user_work_state(user_id,work_id,rating) VALUES(?,?,?) ON CONFLICT(user_id,work_id) DO UPDATE SET rating=excluded.rating,updated_at=CURRENT_TIMESTAMP`, user, identity, review.Rating)
		case review.Note != "":
			_, err = tx.ExecContext(ctx, `INSERT INTO user_work_state(user_id,work_id,note) VALUES(?,?,?) ON CONFLICT(user_id,work_id) DO UPDATE SET note=excluded.note,updated_at=CURRENT_TIMESTAMP`, user, identity, review.Note)
		}
		if err != nil {
			return result, err
		}
	}
	for key := range plan.blocked {
		plan.works[key] = 0
	}
	for _, list := range b.Playlists {
		unresolved := false
		for _, item := range list.Items {
			if item.WorkRef != "" && plan.works[item.key()] == 0 {
				unresolved = true
				result.Issues = append(result.Issues, ImportIssue{Ref: item.WorkRef, Title: list.Name, Reason: "playlist_preserved"})
			}
		}
		if unresolved {
			continue
		}
		changed, err := importPlaylist(ctx, tx, user, list, plan.works, overwrite)
		if err != nil {
			return result, err
		}
		if changed {
			result.Playlists++
		}
	}
	// Match the existing favorite-list contract: the bit is derived membership.
	_, err = tx.ExecContext(ctx, `UPDATE user_work_state SET favorite=EXISTS(SELECT 1 FROM favorite_list_item i JOIN favorite_list l ON l.id=i.list_id WHERE l.user_id=? AND l.kind='user' AND i.work_id=user_work_state.work_id) WHERE user_id=?`, user, user)
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func importTag(ctx context.Context, tx *sql.Tx, user int64, tag BackupTag, overwrite bool) (int64, error) {
	t := tagScopes[tag.Scope]
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM `+t.tags+` WHERE user_id=? AND LOWER(name)=LOWER(?) ORDER BY id LIMIT 1`, user, tag.Name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		result, err := tx.ExecContext(ctx, `INSERT INTO `+t.tags+` (user_id,name,color) VALUES (?,?,?)`, user, tag.Name, tag.Color)
		if err != nil {
			return 0, err
		}
		return result.LastInsertId()
	}
	if err == nil && overwrite {
		_, err = tx.ExecContext(ctx, `UPDATE `+t.tags+` SET color=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND user_id=?`, tag.Color, id, user)
	}
	return id, err
}

// importKeep names the fields whose existing value must survive a keep
// import. Each field is decided independently so a stored tag or cursor does
// not suppress a review the user does not have yet.
type importKeep struct {
	review, status, progress bool
}

func importWork(ctx context.Context, tx *sql.Tx, user, id, media int64, w BackupWork, overwrite bool, keep importKeep) error {
	// "none" is the export default and "" has always meant no status here.
	// A keep import must not turn either into an explicit clear.
	if keep.status || w.ListeningStatus == "" || (!overwrite && w.ListeningStatus == "none") {
		_, err := tx.ExecContext(ctx, `INSERT INTO user_work_state (user_id,work_id) VALUES (?,?) ON CONFLICT(user_id,work_id) DO NOTHING`, user, id)
		if err != nil {
			return err
		}
	} else {
		_, err := tx.ExecContext(ctx, `INSERT INTO user_work_state (user_id,work_id,listening_status) VALUES (?,?,?) ON CONFLICT(user_id,work_id) DO UPDATE SET listening_status=excluded.listening_status,updated_at=CURRENT_TIMESTAMP`, user, id, w.ListeningStatus)
		if err != nil {
			return err
		}
	}
	if overwrite {
		if _, err := tx.ExecContext(ctx, `DELETE FROM user_work_tag WHERE user_id=? AND work_id=?`, user, id); err != nil {
			return err
		}
	}
	for _, name := range w.Tags {
		tag, err := importTag(ctx, tx, user, BackupTag{Scope: "work", Name: name}, false)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO user_work_tag (user_id,work_id,user_tag_id) VALUES (?,?,?) ON CONFLICT DO NOTHING`, user, id, tag); err != nil {
			return err
		}
	}
	if p := w.Progress; p != nil && media > 0 && !keep.progress {
		// Import is a new explicit edit, regardless of the backup's historical date.
		// The marker sorts after valid client ids so tied offline reports stay stale.
		_, err := tx.ExecContext(ctx, `INSERT INTO user_work_playback_cursor (user_id,work_id,media_item_id,position_seconds,duration_seconds,completed,last_played_at,report_order,report_id) VALUES (?,?,?,?,?,?,NULLIF(?,''),?,'~import') ON CONFLICT(user_id,work_id) DO UPDATE SET media_item_id=excluded.media_item_id,file_source_id=NULL,location_id=NULL,location_type='',position_seconds=excluded.position_seconds,duration_seconds=excluded.duration_seconds,completed=excluded.completed,last_played_at=excluded.last_played_at,updated_at=CURRENT_TIMESTAMP,report_order=MAX(report_order+1,excluded.report_order),report_id=excluded.report_id`, user, id, media, p.PositionSeconds, p.DurationSeconds, p.Completed, p.LastPlayedAt, time.Now().UnixMilli())
		if err != nil {
			return err
		}
	}
	if w.Identity != nil && w.Progress != nil && !keep.progress {
		if media > 0 {
			_, err := tx.ExecContext(ctx, `DELETE FROM personal_transfer_progress WHERE user_id=? AND work_id=?`, user, id)
			if err != nil {
				return err
			}
		} else {
			raw, marshalErr := json.Marshal(w.Progress)
			if marshalErr != nil {
				return marshalErr
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO personal_transfer_progress(user_id,work_id,progress_json) VALUES(?,?,?) ON CONFLICT(user_id,work_id) DO UPDATE SET progress_json=excluded.progress_json`, user, id, string(raw)); err != nil {
				return err
			}
		}
	}
	if stats := w.Statistics; stats != nil {
		var seconds float64
		var count int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(listened_seconds),0),COUNT(*) FROM user_listening_session WHERE user_id=? AND work_id=?`, user, id).Scan(&seconds, &count); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO user_listening_import (user_id,work_id,listened_seconds,listen_count,last_played_at) VALUES (?,?,?,?,?) ON CONFLICT(user_id,work_id) DO UPDATE SET listened_seconds=MAX(listened_seconds,excluded.listened_seconds),listen_count=MAX(listen_count,excluded.listen_count),last_played_at=MAX(last_played_at,excluded.last_played_at)`, user, id, math.Max(0, stats.ListenedSeconds-seconds), max(int64(0), stats.ListenCount-count), stats.LastPlayedAt); err != nil {
			return err
		}
	}
	return nil
}

func importPlaylist(ctx context.Context, tx *sql.Tx, user int64, list BackupPlaylist, works map[string]int64, overwrite bool) (bool, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM favorite_list WHERE user_id=? AND name=? AND kind='user'`, user, list.Name).Scan(&id)
	if err == nil && !overwrite {
		return false, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		result, insertErr := tx.ExecContext(ctx, `INSERT INTO favorite_list (user_id,name,description,icon,sort_order,kind) VALUES (?,?,?,?,?,'user')`, user, list.Name, list.Description, list.Icon, list.SortOrder)
		if insertErr != nil {
			return false, insertErr
		}
		id, err = result.LastInsertId()
	} else if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE favorite_list SET description=?,icon=?,sort_order=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND user_id=?`, list.Description, list.Icon, list.SortOrder, id, user)
		if err == nil {
			_, err = tx.ExecContext(ctx, `DELETE FROM favorite_list_item WHERE list_id=?`, id)
		}
	}
	if err != nil {
		return false, err
	}
	for _, item := range list.Items {
		work := works[item.key()]
		if work == 0 {
			continue
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO favorite_list_item (list_id,work_id,note,sort_order) VALUES (?,?,?,?) ON CONFLICT DO NOTHING`, id, work, item.Note, item.SortOrder); err != nil {
			return false, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO user_work_state (user_id,work_id,favorite) VALUES (?,?,1) ON CONFLICT(user_id,work_id) DO UPDATE SET favorite=1`, user, work); err != nil {
			return false, err
		}
	}
	return true, nil
}
