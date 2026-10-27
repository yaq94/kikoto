package httpapi

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
)

// A series evaluation has its own work identity. Its members are neither
// registered nor rewritten by this transaction.
func (s *Server) updateCatalogSeriesReview(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requirePermission(w, r, "favorites:write")
	if !ok {
		return
	}
	id, err := parseInt64PathValue(r, "id")
	if err != nil {
		writeAPIError(w, 400, "invalid_request", "invalid series id", false)
		return
	}
	var patch workUserStatePatch
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&patch); err != nil {
		writeAPIError(w, 400, "invalid_request", "invalid review body", false)
		return
	}
	if decoder.Decode(new(any)) != io.EOF {
		writeAPIError(w, 400, "invalid_request", "invalid review body", false)
		return
	}
	if err = patch.validate(); err != nil {
		writeAPIError(w, 400, "invalid_request", err.Error(), false)
		return
	}
	if patch.ListeningStatus != nil || (len(patch.Rating) == 0 && len(patch.Note) == 0) {
		writeAPIError(w, 400, "invalid_request", "personal review is required", false)
		return
	}
	tx, release, err := beginTxWithDatabaseBusyRetry(r.Context(), s.db)
	if err != nil {
		writeError(w, err)
		return
	}
	defer release()
	// Acquire the writer before reading the optional identity so concurrent first
	// saves cannot publish two works for the same series.
	if _, err = tx.ExecContext(r.Context(), `UPDATE catalog_series SET id=id WHERE id=?`, id); err != nil {
		writeError(w, err)
		return
	}
	var title string
	var workID sql.NullInt64
	if err = tx.QueryRowContext(r.Context(), `SELECT title,work_id FROM catalog_series WHERE id=?`, id).Scan(&title, &workID); err != nil {
		writeCatalogGroupingError(w, err)
		return
	}
	if !workID.Valid {
		if _, ok = s.requirePermission(w, r, "remote:track"); !ok {
			return
		}
		workID.Int64, err = insertWorkWithEdition(r.Context(), tx.Tx, "", title)
		if err != nil {
			writeError(w, err)
			return
		}
		if _, err = tx.ExecContext(r.Context(), `UPDATE catalog_series SET work_id=? WHERE id=?`, workID.Int64, id); err != nil {
			writeError(w, err)
			return
		}
	}
	state, err := patchWorkUserState(r.Context(), tx, user.ID, workID.Int64, patch)
	if err != nil {
		writeError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, state)
}
