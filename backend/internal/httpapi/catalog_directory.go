package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/yexca/kikoto/backend/internal/openlist"
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
	ctx, cancel := context.WithTimeout(r.Context(), catalogAvailabilityTimeout)
	defer cancel()
	if _, err = s.refreshCatalogEntry(ctx, id); err != nil {
		if errors.Is(err, errCatalogNoPlayableFiles) {
			writeAPIError(w, 502, "upstream_error", "catalog directory has no playable files", true)
			return
		}
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"catalogEntryId": id, "status": "available"})
}

// refreshCatalogEntry publishes a complete playable snapshot. A failed refresh
// keeps the previous snapshot and records the failure on the card itself.
func (s *Server) refreshCatalogEntry(ctx context.Context, id int64) (string, error) {
	// A manual refresh and the background availability check can name the same
	// card. One scan at a time keeps the second request from publishing an
	// older listing after the first one succeeded.
	s.catalogRefreshMu.Lock()
	defer s.catalogRefreshMu.Unlock()
	entry, source, err := s.loadSourceCatalogEntry(ctx, id)
	if err != nil {
		return "", err
	}
	sourceRoot := entry.SourceRoot
	if sourceRoot == "" {
		sourceRoot = entry.RootPath
	}
	files, err := s.openListClient(source).ScanRoot(ctx, sourceRoot)
	if err != nil {
		if ctx.Err() == nil {
			status := catalogFailureStatus(err)
			if errors.Is(err, openlist.ErrNotFound) {
				// A 404/410 from the source is a definite answer: the directory
				// is gone. Reporting it as a transient failure would keep the
				// card in a retry loop that can never succeed.
				status = "missing"
			}
			if statusErr := s.setCatalogEntryStatus(ctx, entry.ID, status); statusErr != nil {
				return status, statusErr
			}
			return status, err
		}
		return "", err
	}
	cardFiles := catalogSplitCardFiles(entry.RootPath, entry.SourceRoot, files)
	playable := false
	for _, file := range cardFiles {
		if file.Kind == "audio" {
			playable = true
			break
		}
	}
	if !playable {
		if err = s.setCatalogEntryStatus(ctx, entry.ID, "unconfirmed_empty"); err != nil {
			return "unconfirmed_empty", err
		}
		return "unconfirmed_empty", errCatalogNoPlayableFiles
	}
	if entry.SourceRoot != "" {
		// A split card refreshes only its own files; the directory scan above
		// read the real creator folder to resolve them.
		tx, release, txErr := beginTxWithDatabaseBusyRetry(ctx, s.db)
		if txErr != nil {
			return "", txErr
		}
		defer release()
		defer func() { _ = tx.Rollback() }()
		if _, txErr = persistOpenListSnapshotTx(ctx, tx.Tx, source.ID, entry.RootPath, entry.SourceRoot, entry.Title, cardFiles); txErr != nil {
			return "", txErr
		}
		if txErr = tx.Commit(); txErr != nil {
			return "", txErr
		}
	} else if _, err = s.persistOpenListRoot(ctx, source, entry.RootPath, files); err != nil {
		return "", err
	}
	return "available", nil
}

// A catalog directory identifies a source entry, not a second work. Exact work
// references retain their edition; unregistered numbered entries use existing
// aliases. Kikoeru directories are listed by the same work identity so a work
// with two sources can switch between them, and so a shared association does
// not leave one side invisible.
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
	rows, err := s.db.QueryContext(r.Context(), `WITH family AS (
		SELECT peer.work_id FROM shared_work_identity AS own
		JOIN shared_work_identity AS peer ON peer.identity_work_id=own.identity_work_id
		WHERE own.work_id=?
		UNION SELECT ?
	)
	SELECT entry.id,COALESCE(entry.file_source_id,0),COALESCE(source.display_name,''),entry.title,entry.primary_code,COALESCE(source.enabled,0),entry.work_id,
	 CASE WHEN entry.file_source_id IS NULL THEN 'missing' ELSE entry.availability_status END,
	 -- Only a real probe or a complete republication records when the source
	 -- was checked. updated_at also moves on unrelated edits, so using it here
	 -- would show a check time that never happened.
	 COALESCE(strftime('%Y-%m-%dT%H:%M:%SZ',entry.status_checked_at),'')
	FROM source_catalog_entry AS entry LEFT JOIN file_source AS source ON source.id=entry.file_source_id
	WHERE entry.relocated_to_id IS NULL AND (source.source_type='openlist' OR entry.file_source_id IS NULL)
		AND (entry.work_id IN (SELECT work_id FROM family) OR (entry.work_id IS NULL AND entry.primary_code<>'' AND
		(entry.primary_code=? OR entry.primary_code IN (SELECT alias.primary_code FROM work_code_alias AS alias JOIN logical_work AS logical ON logical.id=alias.logical_work_id WHERE logical.canonical_work_id=?))))
	UNION ALL
	SELECT 0,presence.file_source_id,COALESCE(source.display_name,''),COALESCE(json_extract(presence.raw_json,'$.title'),''),
		COALESCE(presence.remote_code,''),COALESCE(source.enabled,0),presence.work_id,presence.availability,
		COALESCE(strftime('%Y-%m-%dT%H:%M:%SZ',presence.last_checked_at),'')
	FROM work_source_presence AS presence
	JOIN file_source AS source ON source.id=presence.file_source_id
	WHERE presence.work_id IN (SELECT work_id FROM family)
		AND presence.presence_type='source'
		AND source.source_type IN ('kikoeru_compatible','kikoeru_compatible_number178')
	ORDER BY 2,1`, id, id, code, id)
	if err != nil {
		writeError(w, err)
		return
	}
	defer func() { _ = rows.Close() }()
	// Only a viewer who may track remote sources starts an outbound check.
	// A read-only or anonymous viewer keeps the recorded result, so a GET
	// never becomes upstream work without that capability.
	viewer, viewerKnown := userFromContext(r.Context())
	canCheckTabs := viewerKnown && userHasPermission(viewer, "remote:track")
	directories := []map[string]any{}
	for rows.Next() {
		var entryID, sourceID int64
		var sourceName, title, primaryCode, checkedAt, status string
		var enabled bool
		var exactWorkID sql.NullInt64
		if err = rows.Scan(&entryID, &sourceID, &sourceName, &title, &primaryCode, &enabled, &exactWorkID, &status, &checkedAt); err != nil {
			writeError(w, err)
			return
		}
		if !enabled && sourceID != 0 {
			status = "disabled"
		}
		directoryWorkID := id
		if exactWorkID.Valid {
			directoryWorkID = exactWorkID.Int64
		}
		directoryType := "catalog"
		if entryID == 0 {
			directoryType = "remote"
		}
		if entryID > 0 && enabled && canCheckTabs && catalogDirectoryNeedsCheck(status, checkedAt, time.Now()) {
			s.enqueueCatalogDirectoryCheck(entryID)
		}
		directories = append(directories, map[string]any{"catalogEntryId": entryID, "sourceId": sourceID, "sourceName": sourceName, "title": title, "primaryCode": primaryCode, "workId": directoryWorkID, "directoryType": directoryType, "status": status, "checkedAt": checkedAt})
	}
	if err = rows.Err(); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"directories": directories})
}
