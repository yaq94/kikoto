package httpapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"path"
	"slices"
	"strings"

	"github.com/yexca/kikoto/backend/internal/sqlutil"
)

const maxCatalogSeriesMembers = 100

var errCatalogGroupingConflict = errors.New("catalog grouping needs a fresh preview or conflict review")

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

// catalogSeriesRenameEvidenceTx reports whether two series describe the same
// group after its own directory was renamed. Titles agree, or both sides list
// the same live episode directory names one for one. A series that gained or
// lost an episode is a different group and stays for review.
func catalogSeriesRenameEvidenceTx(ctx context.Context, tx *sql.Tx, oldSeries, newSeries int64) (bool, error) {
	if oldSeries == newSeries {
		return true, nil
	}
	var oldTitle, newTitle string
	if err := tx.QueryRowContext(ctx, `SELECT title FROM catalog_series WHERE id=?`, oldSeries).Scan(&oldTitle); err != nil {
		return false, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT title FROM catalog_series WHERE id=?`, newSeries).Scan(&newTitle); err != nil {
		return false, err
	}
	if strings.EqualFold(strings.TrimSpace(oldTitle), strings.TrimSpace(newTitle)) {
		return true, nil
	}
	names := func(seriesID int64) ([]string, error) {
		rows, err := tx.QueryContext(ctx, `SELECT CASE WHEN e.source_root<>'' THEN e.source_root ELSE e.root_path END
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
	if err := tx.QueryRowContext(ctx, `SELECT CASE WHEN source_root<>'' THEN source_root ELSE root_path END FROM source_catalog_entry WHERE id=?`, fromID).Scan(&newRoot); err != nil {
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

type catalogSeriesEpisode struct {
	CatalogEntryID int64  `json:"catalogEntryId"`
	Title          string `json:"title"`
	SourceID       int64  `json:"sourceId"`
	SourceName     string `json:"sourceName"`
	PrimaryCode    string `json:"primaryCode"`
	WorkID         *int64 `json:"workId"`
	Position       int    `json:"position"`
	SeriesID       *int64 `json:"seriesId,omitempty"`
	RootPath       string `json:"-"`
	SourceRoot     string `json:"-"`
	UpdatedAt      string `json:"-"`
}

type catalogSeriesView struct {
	PersonalRating *int64                 `json:"personalRating"`
	PersonalNote   string                 `json:"personalNote"`
	ID             int64                  `json:"seriesId"`
	Title          string                 `json:"title"`
	WorkID         *int64                 `json:"workId"`
	Episodes       []catalogSeriesEpisode `json:"episodes"`
}

type catalogGroupingInput struct {
	Title    string  `json:"title"`
	EntryIDs []int64 `json:"catalogEntryIds"`
}

type catalogGroupingPreview struct {
	ID       int64                  `json:"previewId"`
	Title    string                 `json:"title"`
	Status   string                 `json:"status"`
	SeriesID *int64                 `json:"seriesId"`
	Episodes []catalogSeriesEpisode `json:"episodes"`
}

func (s *Server) getCatalogSeries(w http.ResponseWriter, r *http.Request) {
	if s.cfg.IsDemo() {
		writeAPIError(w, 404, "not_found", "series not found", false)
		return
	}
	id, err := parseInt64PathValue(r, "id")
	if err != nil {
		writeAPIError(w, 400, "invalid_request", "invalid series id", false)
		return
	}
	view := catalogSeriesView{ID: id, Episodes: []catalogSeriesEpisode{}}
	if err = s.db.QueryRowContext(r.Context(), `SELECT s.title,s.work_id,u.rating,COALESCE(u.note,'') FROM catalog_series s
	LEFT JOIN user_work_state u ON u.work_id=s.work_id AND u.user_id=? WHERE s.id=?`, optionalUserID(r.Context()), id).Scan(&view.Title, &view.WorkID, &view.PersonalRating, &view.PersonalNote); err != nil {
		writeCatalogGroupingError(w, err)
		return
	}
	rows, err := s.db.QueryContext(r.Context(), `WITH grouped AS (
		SELECT COALESCE(successor.id,e.id) AS id,
			COALESCE(NULLIF(successor.title,''),e.title) AS title,
			COALESCE(NULLIF(successor.file_source_id,0),NULLIF(e.file_source_id,0),0) AS source_id,
			COALESCE(successor_fs.display_name,fs.display_name,'') AS display_name,
			COALESCE(successor.primary_code,e.primary_code) AS primary_code,
			COALESCE(successor.work_id,e.work_id) AS work_id,m.position,
		ROW_NUMBER() OVER(PARTITION BY COALESCE(identity.identity_work_id,-e.id) ORDER BY m.position,e.id) AS identity_rank
		FROM catalog_series_member m JOIN source_catalog_entry e ON e.id=m.catalog_entry_id
		LEFT JOIN source_catalog_entry successor ON successor.id=e.relocated_to_id
		LEFT JOIN file_source fs ON fs.id=e.file_source_id
		LEFT JOIN file_source successor_fs ON successor_fs.id=successor.file_source_id
		LEFT JOIN shared_work_identity identity ON identity.work_id=COALESCE(successor.work_id,e.work_id)
		WHERE ((e.relocated_to_id IS NULL AND e.file_source_id IS NOT NULL AND e.availability_status<>'missing')
			OR (e.relocated_to_id IS NOT NULL AND successor.file_source_id IS NOT NULL AND successor.availability_status<>'missing'))
			AND m.series_id IN (SELECT related.id FROM catalog_series selected JOIN catalog_series related ON related.id=selected.id OR related.work_id IN (SELECT peer.work_id FROM shared_work_identity own JOIN shared_work_identity peer ON peer.identity_work_id=own.identity_work_id WHERE own.work_id=selected.work_id) WHERE selected.id=?))
		SELECT id,title,source_id,display_name,primary_code,work_id,position FROM grouped WHERE identity_rank=1 ORDER BY position,id`, id)
	if err != nil {
		writeError(w, err)
		return
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var episode catalogSeriesEpisode
		if err = rows.Scan(&episode.CatalogEntryID, &episode.Title, &episode.SourceID, &episode.SourceName, &episode.PrimaryCode, &episode.WorkID, &episode.Position); err != nil {
			writeError(w, err)
			return
		}
		view.Episodes = append(view.Episodes, episode)
	}
	if err = rows.Err(); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, view)
}

// Only membership is changed. Existing work identities, versions, media and
// private state remain owned by each episode. No existing series is merged.
func loadGroupingMembers(ctx context.Context, tx *sql.Tx, input catalogGroupingInput) ([]catalogSeriesEpisode, string, error) {
	if len(input.EntryIDs) < 2 || len(input.EntryIDs) > maxCatalogSeriesMembers || strings.TrimSpace(input.Title) == "" || len(input.Title) > 512 {
		return nil, "", errCatalogGroupingConflict
	}
	members := make([]catalogSeriesEpisode, 0, len(input.EntryIDs))
	seen := map[int64]bool{}
	seenWorks := map[int64]bool{}
	hash := sha256.New()
	encoder := json.NewEncoder(hash)
	for i, id := range input.EntryIDs {
		if id <= 0 || seen[id] {
			return nil, "", errCatalogGroupingConflict
		}
		seen[id] = true
		member := catalogSeriesEpisode{Position: i + 1}
		err := tx.QueryRowContext(ctx, `SELECT e.id,e.title,e.file_source_id,fs.display_name,e.primary_code,e.work_id,e.root_path,e.source_root,e.updated_at,m.series_id
			FROM source_catalog_entry e JOIN file_source fs ON fs.id=e.file_source_id
			LEFT JOIN catalog_series_member m ON m.catalog_entry_id=e.id
			WHERE e.id=? AND fs.enabled=1 AND fs.source_type='openlist'`, id).Scan(&member.CatalogEntryID, &member.Title, &member.SourceID, &member.SourceName, &member.PrimaryCode, &member.WorkID, &member.RootPath, &member.SourceRoot, &member.UpdatedAt, &member.SeriesID)
		if errors.Is(err, sql.ErrNoRows) || member.SeriesID != nil {
			return nil, "", errCatalogGroupingConflict
		}
		if err != nil {
			return nil, "", err
		}
		// A numbered edition or existing work stays exact; grouping never resolves
		// it onto another member's work or copies a user's evaluation.
		_ = encoder.Encode(member)
		_ = encoder.Encode([]string{member.RootPath, member.SourceRoot, member.UpdatedAt})
		if member.WorkID != nil {
			if seenWorks[*member.WorkID] {
				return nil, "", errCatalogGroupingConflict
			}
			seenWorks[*member.WorkID] = true
			rows, err := tx.QueryContext(ctx, `SELECT user_id,rating,note FROM user_work_state WHERE work_id=? ORDER BY user_id`, *member.WorkID)
			if err != nil {
				return nil, "", err
			}
			for rows.Next() {
				var userID int64
				var rating sql.NullInt64
				var note string
				if err = rows.Scan(&userID, &rating, &note); err != nil {
					_ = rows.Close()
					return nil, "", err
				}
				_ = encoder.Encode([]any{userID, rating, note})
			}
			err = rows.Err()
			_ = rows.Close()
			if err != nil {
				return nil, "", err
			}
		}
		members = append(members, member)
	}
	return members, hex.EncodeToString(hash.Sum(nil)), nil
}

func (s *Server) createCatalogGroupingPreview(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.requirePermission(w, r, "library:write")
	if !ok {
		return
	}
	var input catalogGroupingInput
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeAPIError(w, 400, "invalid_request", "invalid grouping request", false)
		return
	}
	input.Title = strings.TrimSpace(input.Title)
	if input.Title == "" || len(input.Title) > 512 || len(input.EntryIDs) < 2 || len(input.EntryIDs) > maxCatalogSeriesMembers {
		writeAPIError(w, 400, "invalid_request", "select 2 to 100 entries and a series title", false)
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	members, fingerprint, err := loadGroupingMembers(r.Context(), tx, input)
	if err != nil {
		writeCatalogGroupingError(w, err)
		return
	}
	var count int
	if err = tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM catalog_series_preview WHERE actor_id=? AND status='pending' AND created_at>datetime('now','-1 day')`, actor.ID).Scan(&count); err != nil {
		writeError(w, err)
		return
	}
	if count >= 32 {
		writeAPIError(w, 429, "limit_exceeded", "too many pending series previews", false)
		return
	}
	// Expired previews are disposable; completed membership and user state are not.
	if _, err = tx.ExecContext(r.Context(), `DELETE FROM catalog_series_preview WHERE actor_id=? AND (created_at<=datetime('now','-1 day') OR id IN (SELECT id FROM catalog_series_preview WHERE actor_id=? AND status='completed' ORDER BY id DESC LIMIT -1 OFFSET 31))`, actor.ID, actor.ID); err != nil {
		writeError(w, err)
		return
	}
	payload, err := json.Marshal(input)
	if err != nil {
		writeError(w, err)
		return
	}
	var id int64
	err = tx.QueryRowContext(r.Context(), `INSERT INTO catalog_series_preview(actor_id,payload_json,fingerprint) VALUES(?,?,?) RETURNING id`, actor.ID, string(payload), fingerprint).Scan(&id)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 201, catalogGroupingPreview{ID: id, Title: input.Title, Status: "pending", Episodes: members})
}

func (s *Server) catalogGroupingPreview(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.requirePermission(w, r, "library:write")
	if !ok {
		return
	}
	id, err := parseInt64PathValue(r, "id")
	if err != nil {
		writeAPIError(w, 400, "invalid_request", "invalid preview id", false)
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	view := catalogGroupingPreview{ID: id, Episodes: []catalogSeriesEpisode{}}
	var payload, fingerprint string
	err = tx.QueryRowContext(r.Context(), `SELECT payload_json,fingerprint,status,series_id FROM catalog_series_preview WHERE id=? AND actor_id=? AND created_at>datetime('now','-1 day')`, id, actor.ID).Scan(&payload, &fingerprint, &view.Status, &view.SeriesID)
	if err != nil {
		writeCatalogGroupingError(w, err)
		return
	}
	var input catalogGroupingInput
	if err = json.Unmarshal([]byte(payload), &input); err != nil {
		writeError(w, err)
		return
	}
	view.Title = input.Title
	if view.Status == "completed" {
		writeJSON(w, 200, view)
		return
	}
	members, current, err := loadGroupingMembers(r.Context(), tx, input)
	if err != nil {
		writeCatalogGroupingError(w, err)
		return
	}
	view.Episodes = members
	if current != fingerprint {
		writeCatalogGroupingError(w, errCatalogGroupingConflict)
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, 200, view)
		return
	}
	seriesID, err := sqlutil.SelectID(r.Context(), tx, `INSERT INTO catalog_series(title) VALUES(?) RETURNING id`, input.Title)
	if err != nil {
		writeError(w, err)
		return
	}
	for _, member := range members {
		if _, err = tx.ExecContext(r.Context(), `INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence) VALUES(?,?,?,'manual')`, seriesID, member.CatalogEntryID, member.Position); err != nil {
			writeError(w, err)
			return
		}
	}
	_, err = tx.ExecContext(r.Context(), `UPDATE catalog_series_preview SET status='completed',series_id=? WHERE id=?`, seriesID, id)
	if err == nil {
		_, err = tx.ExecContext(r.Context(), `DELETE FROM catalog_series_preview WHERE actor_id=? AND status='completed' AND id IN (SELECT id FROM catalog_series_preview WHERE actor_id=? AND status='completed' ORDER BY id DESC LIMIT -1 OFFSET 32)`, actor.ID, actor.ID)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		writeError(w, err)
		return
	}
	view.Status = "completed"
	view.SeriesID = &seriesID
	writeJSON(w, 200, view)
}

func writeCatalogGroupingError(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		writeAPIError(w, 404, "not_found", "series or preview not found", false)
		return
	}
	if errors.Is(err, errCatalogGroupingConflict) {
		writeAPIError(w, 409, "grouping_conflict", "refresh the preview; existing groups require conflict review", false)
		return
	}
	writeError(w, err)
}
