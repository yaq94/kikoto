package httpapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/yexca/kikoto/backend/internal/sqlutil"
)

const maxCatalogSeriesMembers = 100

var errCatalogGroupingConflict = errors.New("catalog grouping needs a fresh preview or conflict review")

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
		SELECT e.id,e.title,e.file_source_id,fs.display_name,e.primary_code,e.work_id,m.position,
		ROW_NUMBER() OVER(PARTITION BY COALESCE(identity.identity_work_id,-e.id) ORDER BY m.position,e.id) AS identity_rank
		FROM catalog_series_member m JOIN source_catalog_entry e ON e.id=m.catalog_entry_id
		JOIN file_source fs ON fs.id=e.file_source_id LEFT JOIN shared_work_identity identity ON identity.work_id=e.work_id
		WHERE m.series_id IN (SELECT related.id FROM catalog_series selected JOIN catalog_series related ON related.id=selected.id OR related.work_id IN (SELECT peer.work_id FROM shared_work_identity own JOIN shared_work_identity peer ON peer.identity_work_id=own.identity_work_id WHERE own.work_id=selected.work_id) WHERE selected.id=?))
		SELECT id,title,file_source_id,display_name,primary_code,work_id,position FROM grouped WHERE identity_rank=1 ORDER BY position,id`, id)
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
