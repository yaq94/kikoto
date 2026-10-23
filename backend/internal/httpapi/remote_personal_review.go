package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// An explicit personal review can register metadata, without synchronizing the
// file tree, caching covers, tracking a source, or scheduling media work.
func (s *Server) updateRemoteWorkUserState(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requirePermission(w, r, "favorites:write")
	if !ok {
		return
	}
	sourceID, err := parseInt64PathValue(r, "id")
	code := normalizeWorkCode(r.PathValue("code"))
	if err != nil || code == "" {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid source id or work code", false)
		return
	}
	var patch workUserStatePatch
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&patch); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", jsonBodyErrorMessage(err), false)
		return
	}
	if decoder.Decode(new(any)) != io.EOF {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body", false)
		return
	}
	if err := patch.validate(); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_personal_review", err.Error(), false)
		return
	}
	if len(patch.Rating) == 0 && len(patch.Note) == 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid_personal_review", "personal review is required", false)
		return
	}
	source, err := s.loadRemoteSourceForUse(r.Context(), sourceID)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	if !source.Enabled || !isKikoeruSourceType(source.SourceType) {
		writeUpstreamError(w, errRemoteSourceNotUsable)
		return
	}
	identity, err := s.resolveWorkCodeIdentity(r.Context(), code)
	if err == nil {
		state, err := patchWorkUserState(r.Context(), s.db, user.ID, identity.WorkID, patch)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, state)
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		writeError(w, err)
		return
	}
	if _, ok := s.requirePermission(w, r, "remote:track"); !ok {
		return
	}
	client := s.kikoeruCrawlClientForSource(r.Context(), source).WithMaxResponseBytes(2 << 20)
	remoteWork, raw, err := s.resolveKikoeruWork(r.Context(), client, code)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	if normalizeWorkCode(normalizedRemoteWorkCode(remoteWork)) != code {
		writeAPIError(w, http.StatusBadGateway, "upstream_identity_mismatch", "remote work identity does not match requested code", false)
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	workID, err := upsertRemoteWork(r.Context(), tx, source, remoteWork, raw)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := upsertAvailableRemoteSourcePresence(r.Context(), tx, source, remoteWork, workID); err != nil {
		writeError(w, err)
		return
	}
	state, err := patchWorkUserState(r.Context(), tx, user.ID, workID, patch)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, state)
}
