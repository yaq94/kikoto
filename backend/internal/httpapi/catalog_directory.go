package httpapi

import (
	"context"
	"database/sql"
	"net/http"
	"time"
)

// Refresh is an explicit, single-entry scan with the same budgets as catalog
// discovery. Publish only a complete playable snapshot; failures keep the old one.
func (s *Server) refreshCatalogDirectory(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePermission(w, r, "sources:write"); !ok {
		return
	}
	id, err := parseInt64PathValue(r, "id")
	if err != nil {
		writeAPIError(w, 400, "invalid_request", "invalid catalog id", false)
		return
	}
	entry, source, err := s.loadSourceCatalogEntry(r.Context(), id)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	files, err := s.openListClient(source).ScanRoot(ctx, entry.RootPath)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	playable := false
	for _, file := range files {
		if file.Kind == "audio" {
			playable = true
			break
		}
	}
	if !playable {
		writeAPIError(w, 502, "upstream_error", "catalog directory has no playable files", true)
		return
	}
	if _, err = s.persistOpenListRoot(ctx, source, entry.RootPath, files); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"catalogEntryId": id, "status": "available"})
}

// A catalog directory identifies a source entry, not a second work. Exact work
// references retain their edition; unregistered numbered entries use existing aliases.
func (s *Server) getWorkCatalogDirectories(w http.ResponseWriter, r *http.Request) {
	id, err := s.workReadID(r)
	if err != nil {
		writeWorkReadError(w, err)
		return
	}
	if !s.requireDemoWork(w, r, id) {
		return
	}
	var code string
	if err = s.db.QueryRowContext(r.Context(), "SELECT primary_code FROM work WHERE id=?", id).Scan(&code); err != nil {
		writeWorkReadError(w, err)
		return
	}
	rows, err := s.db.QueryContext(r.Context(), `SELECT entry.id,entry.file_source_id,source.display_name,entry.title,entry.primary_code,source.enabled,entry.work_id,
	 COALESCE(strftime('%Y-%m-%dT%H:%M:%SZ',entry.updated_at),'')
	 FROM source_catalog_entry AS entry JOIN file_source AS source ON source.id=entry.file_source_id
	 WHERE source.source_type='openlist' AND (entry.work_id IN (SELECT peer.work_id FROM shared_work_identity own JOIN shared_work_identity peer ON peer.identity_work_id=own.identity_work_id WHERE own.work_id=?) OR (entry.work_id IS NULL AND entry.primary_code<>'' AND
	 (entry.primary_code=? OR entry.primary_code IN (SELECT alias.primary_code FROM work_code_alias AS alias JOIN logical_work AS logical ON logical.id=alias.logical_work_id WHERE logical.canonical_work_id=?))))
	 ORDER BY source.priority,source.id,entry.id`, id, code, id)
	if err != nil {
		writeError(w, err)
		return
	}
	defer func() { _ = rows.Close() }()
	directories := []map[string]any{}
	for rows.Next() {
		var entryID, sourceID int64
		var sourceName, title, primaryCode, checkedAt string
		var enabled bool
		var exactWorkID sql.NullInt64
		if err = rows.Scan(&entryID, &sourceID, &sourceName, &title, &primaryCode, &enabled, &exactWorkID, &checkedAt); err != nil {
			writeError(w, err)
			return
		}
		status := "available"
		if !enabled {
			status = "disabled"
		}
		directoryWorkID := id
		if exactWorkID.Valid {
			directoryWorkID = exactWorkID.Int64
		}
		directories = append(directories, map[string]any{"catalogEntryId": entryID, "sourceId": sourceID, "sourceName": sourceName, "title": title, "primaryCode": primaryCode, "workId": directoryWorkID, "directoryType": "catalog", "status": status, "checkedAt": checkedAt})
	}
	if err = rows.Err(); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"directories": directories})
}
