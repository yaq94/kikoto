package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/yexca/kikoto/backend/internal/sqlutil"
)

func (s *Server) updateSourceCatalogUserState(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requirePermission(w, r, "favorites:write")
	if !ok {
		return
	}
	id, err := parseInt64PathValue(r, "id")
	if err != nil {
		writeAPIError(w, 400, "invalid_request", "invalid catalog id", false)
		return
	}
	var patch workUserStatePatch
	if err = json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&patch); err != nil {
		writeAPIError(w, 400, "invalid_request", "invalid JSON body", false)
		return
	}
	if err = patch.validate(); err != nil {
		writeAPIError(w, 400, "invalid_request", err.Error(), false)
		return
	}
	if len(patch.Rating) == 0 && len(patch.Note) == 0 {
		writeAPIError(w, 400, "invalid_request", "personal review is required", false)
		return
	}
	entry, source, err := s.loadRetainedSourceCatalogEntry(r.Context(), id)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	var resolvedID int64
	if entry.PrimaryCode != "" {
		identity, resolveErr := s.resolveWorkCodeIdentity(r.Context(), entry.PrimaryCode)
		if resolveErr == nil {
			resolvedID = identity.WorkID
		} else if !errors.Is(resolveErr, sql.ErrNoRows) {
			writeError(w, resolveErr)
			return
		}
	}
	if entry.WorkID == nil && resolvedID == 0 {
		if _, ok = s.requirePermission(w, r, "remote:track"); !ok {
			return
		}
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	entry, err = resolveCatalogEntry(r.Context(), tx, id)
	if err != nil {
		writeError(w, err)
		return
	}
	id = entry.ID
	// Obtain the writer lock before reading registration state. Concurrent saves
	// cannot both observe an unregistered entry and publish separate works.
	if _, err = tx.ExecContext(r.Context(), "UPDATE source_catalog_entry SET id=id WHERE id=?", id); err != nil {
		writeError(w, err)
		return
	}
	var workID sql.NullInt64
	if err = tx.QueryRowContext(r.Context(), "SELECT work_id FROM source_catalog_entry WHERE id=?", id).Scan(&workID); err != nil {
		writeError(w, err)
		return
	}
	if !workID.Valid && resolvedID > 0 {
		workID = sql.NullInt64{Int64: resolvedID, Valid: true}
	}
	if !workID.Valid {
		if _, ok = s.requirePermission(w, r, "remote:track"); !ok {
			return
		}
		if entry.PrimaryCode != "" {
			_, err = tx.ExecContext(r.Context(), `INSERT INTO work(primary_code,title) VALUES(?,?) ON CONFLICT(primary_code) WHERE primary_code<>'' DO NOTHING`, entry.PrimaryCode, entry.Title)
			if err == nil {
				err = tx.QueryRowContext(r.Context(), "SELECT id FROM work WHERE primary_code=?", entry.PrimaryCode).Scan(&workID.Int64)
			}
		} else {
			workID.Int64, err = sqlutil.InsertID(r.Context(), tx, "INSERT INTO work(primary_code,title) VALUES('',?)", entry.Title)
		}
		if err != nil {
			writeError(w, err)
			return
		}
		workID.Valid = true
		var logicalID int64
		err = tx.QueryRowContext(r.Context(), "SELECT logical_work_id FROM work_edition WHERE work_id=?", workID.Int64).Scan(&logicalID)
		if errors.Is(err, sql.ErrNoRows) {
			logicalID, err = sqlutil.InsertID(r.Context(), tx, "INSERT INTO logical_work(canonical_work_id,canonical_code) VALUES(?,?)", workID.Int64, entry.PrimaryCode)
			if err == nil {
				_, err = tx.ExecContext(r.Context(), "INSERT INTO work_edition(work_id,logical_work_id,primary_code,is_canonical) VALUES(?,?,?,1)", workID.Int64, logicalID, entry.PrimaryCode)
			}
		}
		if err != nil {
			writeError(w, err)
			return
		}
	}
	if _, err = tx.ExecContext(r.Context(), "UPDATE source_catalog_entry SET work_id=? WHERE id=?", workID.Int64, id); err != nil {
		writeError(w, err)
		return
	}
	if source.ID != 0 {
		if err = upsertWorkSourcePresence(r.Context(), tx, workSourcePresence{WorkID: workID.Int64, FileSourceID: source.ID, PresenceType: sourcePresenceTypeRemoteSource, RemoteID: strconv.FormatInt(id, 10), RemoteCode: entry.PrimaryCode, Availability: entry.Status, RawJSON: mustJSON(map[string]any{"catalogEntryId": id})}); err != nil {
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
