package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"path"
	"strings"
)

const (
	catalogExclusionModeWhole = "whole"
	catalogExclusionAction    = "exclude"
	maxCatalogExclusionNote   = 2000
	maxCatalogExclusionPath   = 4096
)

type catalogExclusion struct {
	ID        int64  `json:"id"`
	Path      string `json:"path"`
	Mode      string `json:"mode"`
	Action    string `json:"action"`
	Note      string `json:"note"`
	Enabled   bool   `json:"enabled"`
	CreatedAt string `json:"createdAt"`
}

type catalogExclusionPayload struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Note string `json:"note"`
}

// normalizeCatalogExclusionPath accepts only an absolute source path below the
// root. Cleaning collapses repeated and trailing slashes; ".." is rejected
// before cleaning because path.Clean would silently resolve it.
func normalizeCatalogExclusionPath(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", errors.New("path is required")
	}
	if !strings.HasPrefix(value, "/") {
		return "", errors.New("path must start with /")
	}
	if strings.ContainsRune(value, 0) {
		return "", errors.New("path must not contain a NUL byte")
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." {
			return "", errors.New("path must not contain ..")
		}
	}
	cleaned := path.Clean(value)
	if cleaned == "/" {
		return "", errors.New("path must not be the source root")
	}
	if !strings.HasPrefix(cleaned, "/") {
		return "", errors.New("path must start with /")
	}
	if len(cleaned) > maxCatalogExclusionPath {
		return "", errors.New("path is too long")
	}
	return cleaned, nil
}

// catalogPathExcludedTx reports whether path itself or one of its ancestors is
// excluded for this source. The prefix comparison is boundary-aware: /A does
// not exclude /AB.
func catalogPathExcludedTx(ctx context.Context, tx *sql.Tx, sourceID int64, p string) (bool, error) {
	if p == "" {
		p = "/"
	}
	var hit int
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM source_catalog_exclusion
		WHERE file_source_id=? AND enabled=1
			AND (path=? OR substr(?,1,length(path)+1)=path || '/'))`,
		sourceID, p, p).Scan(&hit)
	return hit != 0, err
}

// applyCatalogExclusionTx keeps the entry identities and every personal or
// shared reference, but removes the source rows that made the subtree
// publishable. The entries become excluded, not missing.
func applyCatalogExclusionTx(ctx context.Context, tx *sql.Tx, sourceID int64, p string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE source_catalog_entry
		SET availability_status='excluded',updated_at=CURRENT_TIMESTAMP
		WHERE file_source_id=? AND (root_path=? OR substr(root_path,1,length(?)+1)=? || '/')`,
		sourceID, p, p, p); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM source_catalog_file
		WHERE catalog_entry_id IN (
			SELECT id FROM source_catalog_entry
			WHERE file_source_id=? AND (root_path=? OR substr(root_path,1,length(?)+1)=? || '/'))`,
		sourceID, p, p, p); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM source_catalog_observation
		WHERE file_source_id=? AND (
			directory_path=? OR substr(directory_path,1,length(?)+1)=? || '/'
			OR (CASE WHEN directory_path='/' THEN '/'||name ELSE directory_path||'/'||name END)=?
			OR substr(CASE WHEN directory_path='/' THEN '/'||name ELSE directory_path||'/'||name END,1,length(?)+1)=? || '/')`,
		sourceID, p, p, p, p, p, p); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM source_catalog_directory
		WHERE file_source_id=? AND (path=? OR substr(path,1,length(?)+1)=? || '/')`,
		sourceID, p, p, p); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM source_catalog_audio_root
		WHERE file_source_id=? AND (catalog_root=? OR substr(catalog_root,1,length(?)+1)=? || '/')`,
		sourceID, p, p, p); err != nil {
		return err
	}
	// The progress columns are an incremental projection. Rebuild them in the
	// same transaction so a deleted subtree cannot leave the worker believing
	// there is still pending work.
	return seedCatalogCountersTx(ctx, tx, sourceID)
}

// revokeCatalogExclusionTx restores only rows that no remaining exclusion still
// covers. A row that was previously checked waits for the next scan to prove
// availability; a row that was never checked can show as available again.
func revokeCatalogExclusionTx(ctx context.Context, tx *sql.Tx, sourceID int64, p string) error {
	_, err := tx.ExecContext(ctx, `UPDATE source_catalog_entry
		SET availability_status=CASE WHEN status_checked_at IS NULL THEN 'available' ELSE 'unavailable' END,
			updated_at=CURRENT_TIMESTAMP
		WHERE file_source_id=? AND availability_status='excluded'
			AND (root_path=? OR substr(root_path,1,length(?)+1)=? || '/')
			AND NOT EXISTS (
				SELECT 1 FROM source_catalog_exclusion x
				WHERE x.file_source_id=source_catalog_entry.file_source_id AND x.enabled=1
					AND (x.path=source_catalog_entry.root_path
						OR substr(source_catalog_entry.root_path,1,length(x.path)+1)=x.path || '/'))`,
		sourceID, p, p, p)
	return err
}

func (s *Server) catalogExclusionSourceID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := parseInt64PathValue(r, "id")
	if err != nil || id <= 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid source id", false)
		return 0, false
	}
	var sourceType string
	if err = s.db.QueryRowContext(r.Context(), `SELECT source_type FROM file_source WHERE id=?`, id).Scan(&sourceType); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAPIError(w, http.StatusNotFound, "not_found", "source not found", false)
			return 0, false
		}
		writeError(w, err)
		return 0, false
	}
	if sourceType != sourceTypeOpenList {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "catalog exclusions are only available for OpenList sources", false)
		return 0, false
	}
	return id, true
}

func (s *Server) listCatalogExclusions(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePermission(w, r, "sources:write"); !ok {
		return
	}
	sourceID, ok := s.catalogExclusionSourceID(w, r)
	if !ok {
		return
	}
	rows, err := s.db.QueryContext(r.Context(), `SELECT id,path,mode,action,note,enabled,created_at
		FROM source_catalog_exclusion WHERE file_source_id=? ORDER BY path,id`, sourceID)
	if err != nil {
		writeError(w, err)
		return
	}
	defer rows.Close()
	exclusions := []catalogExclusion{}
	for rows.Next() {
		var item catalogExclusion
		if err = rows.Scan(&item.ID, &item.Path, &item.Mode, &item.Action, &item.Note, &item.Enabled, &item.CreatedAt); err != nil {
			writeError(w, err)
			return
		}
		exclusions = append(exclusions, item)
	}
	if err = rows.Err(); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, exclusions)
}

// createCatalogExclusion applies one rule and immediately reconciles rows that
// were already published below it.
func (s *Server) createCatalogExclusion(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePermission(w, r, "sources:write"); !ok {
		return
	}
	sourceID, ok := s.catalogExclusionSourceID(w, r)
	if !ok {
		return
	}
	var payload catalogExclusionPayload
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", jsonBodyErrorMessage(err), false)
		return
	}
	normalized, err := normalizeCatalogExclusionPath(payload.Path)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error(), false)
		return
	}
	mode := strings.TrimSpace(payload.Mode)
	if mode == "" {
		mode = catalogExclusionModeWhole
	}
	if mode != catalogExclusionModeWhole {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "unsupported exclusion mode", false)
		return
	}
	note := strings.TrimSpace(payload.Note)
	if len(note) > maxCatalogExclusionNote {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "note is too long", false)
		return
	}
	tx, release, err := beginTxWithDatabaseBusyRetry(r.Context(), s.db)
	if err != nil {
		writeError(w, err)
		return
	}
	defer release()
	result, err := tx.ExecContext(r.Context(), `INSERT INTO source_catalog_exclusion
		(file_source_id,path,mode,action,note,enabled) VALUES(?,?,?,?,?,1)
		ON CONFLICT(file_source_id,path) DO NOTHING`, sourceID, normalized, mode, catalogExclusionAction, note)
	if err != nil {
		writeError(w, err)
		return
	}
	affected, err := result.RowsAffected()
	if err != nil {
		writeError(w, err)
		return
	}
	if affected == 0 {
		writeAPIError(w, http.StatusConflict, "conflict", "path is already excluded", false)
		return
	}
	if err = applyCatalogExclusionTx(r.Context(), tx.Tx, sourceID, normalized); err != nil {
		writeError(w, err)
		return
	}
	var created catalogExclusion
	if err = tx.QueryRowContext(r.Context(), `SELECT id,path,mode,action,note,enabled,created_at
		FROM source_catalog_exclusion WHERE file_source_id=? AND path=?`, sourceID, normalized).
		Scan(&created.ID, &created.Path, &created.Mode, &created.Action, &created.Note, &created.Enabled, &created.CreatedAt); err != nil {
		writeError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		writeError(w, err)
		return
	}
	s.invalidateRemoteWorkCache(sourceID)
	writeJSON(w, http.StatusCreated, created)
}

// deleteCatalogExclusion revokes one rule by id. The caller passes the id from
// the list response, so a renamed or duplicated path can never delete the wrong
// rule.
func (s *Server) deleteCatalogExclusion(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePermission(w, r, "sources:write"); !ok {
		return
	}
	sourceID, ok := s.catalogExclusionSourceID(w, r)
	if !ok {
		return
	}
	exclusionID, err := parseInt64PathValue(r, "exclusionId")
	if err != nil || exclusionID <= 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid exclusion id", false)
		return
	}
	tx, release, err := beginTxWithDatabaseBusyRetry(r.Context(), s.db)
	if err != nil {
		writeError(w, err)
		return
	}
	defer release()
	var excludedPath string
	if err = tx.QueryRowContext(r.Context(), `SELECT path FROM source_catalog_exclusion
		WHERE id=? AND file_source_id=?`, exclusionID, sourceID).Scan(&excludedPath); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAPIError(w, http.StatusNotFound, "not_found", "exclusion not found", false)
			return
		}
		writeError(w, err)
		return
	}
	if _, err = tx.ExecContext(r.Context(), `DELETE FROM source_catalog_exclusion WHERE id=?`, exclusionID); err != nil {
		writeError(w, err)
		return
	}
	if err = revokeCatalogExclusionTx(r.Context(), tx.Tx, sourceID, excludedPath); err != nil {
		writeError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		writeError(w, err)
		return
	}
	s.invalidateRemoteWorkCache(sourceID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": excludedPath})
}

// suppressExcludedCatalogObservationsTx clears the pending flag for rows whose
// full path is excluded. It runs in the page transaction so the root
// completeness check never waits for a publisher that will skip them.
func suppressExcludedCatalogObservationsTx(ctx context.Context, tx *sql.Tx, sourceID int64, directoryPath string, revision int) error {
	result, err := tx.ExecContext(ctx, `UPDATE source_catalog_observation SET publish_pending=0
		WHERE file_source_id=? AND directory_path=? AND revision=? AND publish_pending=1
			AND EXISTS(SELECT 1 FROM source_catalog_exclusion x
				WHERE x.file_source_id=source_catalog_observation.file_source_id AND x.enabled=1
					AND ((CASE WHEN source_catalog_observation.directory_path='/' THEN '/'||source_catalog_observation.name ELSE source_catalog_observation.directory_path||'/'||source_catalog_observation.name END)=x.path
						OR substr((CASE WHEN source_catalog_observation.directory_path='/' THEN '/'||source_catalog_observation.name ELSE source_catalog_observation.directory_path||'/'||source_catalog_observation.name END),1,length(x.path)+1)=x.path || '/'))`,
		sourceID, directoryPath, revision)
	if err != nil {
		return err
	}
	cleared, err := result.RowsAffected()
	if err != nil || cleared == 0 {
		return err
	}
	// Recompute the projected count instead of decrementing: a row counts only
	// once its catalog root has an audio root, and that membership can change
	// in the same page transaction.
	_, err = tx.ExecContext(ctx, `UPDATE source_catalog_scan SET
		publish_pending_remaining=(SELECT count(*) FROM source_catalog_directory d
			JOIN source_catalog_observation o ON o.file_source_id=d.file_source_id AND o.directory_path=d.path AND o.revision=d.revision
			WHERE d.file_source_id=source_catalog_scan.file_source_id AND d.generation=source_catalog_scan.workflow_run_id
				AND d.status='complete' AND d.catalog_root<>'' AND o.publish_pending=1
				AND EXISTS(SELECT 1 FROM source_catalog_audio_root r
					WHERE r.file_source_id=d.file_source_id AND r.generation=d.generation AND r.catalog_root=d.catalog_root)),
		updated_at=CURRENT_TIMESTAMP
		WHERE file_source_id=?`, sourceID)
	return err
}
