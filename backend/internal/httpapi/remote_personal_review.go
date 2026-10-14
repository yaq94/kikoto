package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
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
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid source id or work code"})
		return
	}
	var patch workUserStatePatch
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if err := patch.validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if len(patch.Rating) == 0 && len(patch.Note) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "personal review is required"})
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
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "remote work identity does not match requested code"})
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
