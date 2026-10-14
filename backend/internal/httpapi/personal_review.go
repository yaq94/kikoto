package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

// RawMessage preserves the difference between an omitted field and JSON null.
type workUserStatePatch struct {
	ListeningStatus *string         `json:"listeningStatus"`
	Favorite        *bool           `json:"favorite"`
	Rating          json.RawMessage `json:"rating"`
	Note            json.RawMessage `json:"note"`
}

func (p workUserStatePatch) validate() error {
	if p.Favorite != nil {
		return errors.New("use favorite lists to change list membership")
	}
	if p.ListeningStatus != nil && strings.TrimSpace(*p.ListeningStatus) != "" && !validListeningStatus(strings.TrimSpace(*p.ListeningStatus)) {
		return errors.New("invalid listening status")
	}
	if len(p.Rating) > 0 && string(p.Rating) != "null" {
		var rating int
		if json.Unmarshal(p.Rating, &rating) != nil || rating < 0 || rating > 5 {
			return errors.New("rating must be an integer from 0 to 5 or null")
		}
	}
	if len(p.Note) > 0 {
		var note string
		if string(p.Note) == "null" || json.Unmarshal(p.Note, &note) != nil {
			return errors.New("note must be a string")
		}
	}
	return nil
}

type userStateWriter interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func patchWorkUserState(ctx context.Context, db userStateWriter, userID, workID int64, patch workUserStatePatch) (workUserStateResponse, error) {
	status := "none"
	if patch.ListeningStatus != nil {
		status = strings.TrimSpace(*patch.ListeningStatus)
		if status == "" {
			status = "none"
		}
	}
	var rating *int64
	var note string
	if len(patch.Rating) > 0 {
		_ = json.Unmarshal(patch.Rating, &rating)
	}
	if len(patch.Note) > 0 {
		_ = json.Unmarshal(patch.Note, &note)
	}
	state := workUserStateResponse{WorkID: workID}
	var storedRating sql.NullInt64
	err := db.QueryRowContext(ctx, `
		INSERT INTO user_work_state (user_id, work_id, listening_status, rating, note)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(user_id, work_id) DO UPDATE SET
			listening_status = CASE WHEN ? THEN excluded.listening_status ELSE user_work_state.listening_status END,
			rating = CASE WHEN ? THEN excluded.rating ELSE user_work_state.rating END,
			note = CASE WHEN ? THEN excluded.note ELSE user_work_state.note END,
			updated_at = CURRENT_TIMESTAMP
		RETURNING listening_status, favorite, rating, note
	`, userID, workID, status, rating, note, patch.ListeningStatus != nil, len(patch.Rating) > 0, len(patch.Note) > 0).
		Scan(&state.ListeningStatus, &state.Favorite, &storedRating, &state.PersonalNote)
	if storedRating.Valid {
		state.PersonalRating = &storedRating.Int64
	}
	return state, err
}
