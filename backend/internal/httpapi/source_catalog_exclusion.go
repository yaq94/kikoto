package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"path"
	"strings"
)

const (
	catalogExclusionModeWhole     = "whole"
	catalogExclusionModeWhitelist = "whitelist"
	catalogExclusionAction        = "exclude"
	maxCatalogExclusionNote       = 2000
	maxCatalogExclusionPath       = 4096
)

// catalogObservationPathSQL is the source path of one observation row. A row
// in the source root already starts with '/', so it must not gain a second
// separator.
const catalogObservationPathSQL = `(CASE WHEN directory_path='/' THEN '/'||name ELSE directory_path||'/'||name END)`

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

// normalizeCatalogExclusionMode accepts the two rule kinds. "whole" excludes a
// subtree; "whitelist" is an exception inside one and never excludes anything
// by itself.
func normalizeCatalogExclusionMode(raw string) (string, error) {
	switch mode := strings.ToLower(strings.TrimSpace(raw)); mode {
	case "":
		return catalogExclusionModeWhole, nil
	case catalogExclusionModeWhole:
		return catalogExclusionModeWhole, nil
	case catalogExclusionModeWhitelist:
		return catalogExclusionModeWhitelist, nil
	default:
		return "", errors.New("mode must be whole or whitelist")
	}
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

// catalogPathExcludedSQL is the SQL form of catalogPathExcludedTx. Both the
// source and the path may be bound values ("?") or column expressions. Bound
// forms take the source id followed by the path twice for the whole-rule match,
// then twice for the whitelist exception.
func catalogPathExcludedSQL(sourceExpr, pathExpr string) string {
	return `EXISTS (
		SELECT 1 FROM source_catalog_exclusion x
		WHERE x.file_source_id=` + sourceExpr + ` AND x.enabled=1 AND x.mode='whole'
			AND (` + pathExpr + `=x.path OR substr(` + pathExpr + `,1,length(x.path)+1)=x.path || '/')
			AND NOT EXISTS (
				SELECT 1 FROM source_catalog_exclusion w
				WHERE w.file_source_id=x.file_source_id AND w.enabled=1 AND w.mode='whitelist'
					AND (` + pathExpr + `=w.path OR substr(` + pathExpr + `,1,length(w.path)+1)=w.path || '/')
					AND length(w.path) >= length(x.path)))`
}

// catalogPathLeadsToWhitelistSQL reports whether path is a strict ancestor of
// an enabled whitelist. Such a path must still be listed so the exception can
// be reached, even while its own contents stay excluded.
func catalogPathLeadsToWhitelistSQL(sourceExpr, pathExpr string) string {
	return `EXISTS (
		SELECT 1 FROM source_catalog_exclusion w
		WHERE w.file_source_id=` + sourceExpr + ` AND w.enabled=1 AND w.mode='whitelist'
			AND substr(w.path,1,length(` + pathExpr + `)+1)=` + pathExpr + ` || '/')`
}

// catalogPathExcludedTx reports whether path itself or one of its ancestors is
// excluded for this source. The prefix comparison is boundary-aware: /A does
// not exclude /AB. A whitelist is a subtree exception: it defeats an exclusion
// only when it is at least as specific as that exclusion and still covers this
// path. Evaluation is per exclusion row, so an outer rule without an exception
// keeps its subtree excluded even when a sibling has one.
func catalogPathExcludedTx(ctx context.Context, tx *sql.Tx, sourceID int64, p string) (bool, error) {
	if p == "" {
		p = "/"
	}
	var hit int
	err := tx.QueryRowContext(ctx, `SELECT `+catalogPathExcludedSQL("?", "?")+``,
		sourceID, p, p, p, p).Scan(&hit)
	return hit != 0, err
}

// catalogPathTraversableTx reports whether the index may descend into path.
// An excluded path is still traversable when it is an ancestor of a whitelist:
// the exception cannot be discovered without listing its parents. Publishing
// and cleanup keep using the strict rule, so only nodes on the way to an
// exception are re-listed.
func catalogPathTraversableTx(ctx context.Context, tx *sql.Tx, sourceID int64, p string) (bool, error) {
	if p == "" {
		p = "/"
	}
	excluded, err := catalogPathExcludedTx(ctx, tx, sourceID, p)
	if err != nil || !excluded {
		return !excluded, err
	}
	var leads int
	err = tx.QueryRowContext(ctx, `SELECT `+catalogPathLeadsToWhitelistSQL("?", "?")+``,
		sourceID, p, p).Scan(&leads)
	return leads != 0, err
}

// catalogExclusionBaseRootSQL resolves the real directory of a card. A split
// card keeps its group suffix in root_path and stores the directory in
// source_root, so matching only root_path would leave the split card and its
// files visible after an operator excluded that directory.
const catalogExclusionBaseRootSQL = `COALESCE(NULLIF(source_root,''),root_path)`

// catalogExclusionEntries returns the cards whose real directory is p or below
// it. The caller re-checks each one against the whitelist before changing it.
func catalogExclusionEntries(ctx context.Context, tx *sql.Tx, sourceID int64, p string) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM source_catalog_entry
		WHERE file_source_id=? AND (`+catalogExclusionBaseRootSQL+`=? OR substr(`+catalogExclusionBaseRootSQL+`,1,length(?)+1)=? || '/')`,
		sourceID, p, p, p)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// applyCatalogExclusionTx keeps the entry identities and every personal or
// shared reference, but removes the source rows that made the subtree
// publishable. The entries become excluded, not missing. A card covered by an
// enabled whitelist keeps its files and stays available, and the directory,
// observation and audio-root rows a whitelisted card needs are kept as well.
func applyCatalogExclusionTx(ctx context.Context, tx *sql.Tx, sourceID int64, p string) error {
	ids, err := catalogExclusionEntries(ctx, tx, sourceID, p)
	if err != nil {
		return err
	}
	for _, id := range ids {
		var base string
		if err = tx.QueryRowContext(ctx, `SELECT `+catalogExclusionBaseRootSQL+` FROM source_catalog_entry WHERE id=?`, id).Scan(&base); err != nil {
			return err
		}
		excluded, queryErr := catalogPathExcludedTx(ctx, tx, sourceID, base)
		if queryErr != nil {
			return queryErr
		}
		if !excluded {
			continue
		}
		if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_entry
			SET availability_status='excluded',updated_at=CURRENT_TIMESTAMP WHERE id=?`, id); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM source_catalog_file WHERE catalog_entry_id=?`, id); err != nil {
			return err
		}
	}
	// A parent card's root can sit above the excluded directory while one of
	// its files falls inside it. Those rows belong to the excluded subtree and
	// must go too, or the card would keep advertising audio from a path the
	// operator removed. Only entries whose base is an ancestor or descendant of
	// the excluded path are considered, so a wide source stays cheap.
	base := `COALESCE(NULLIF(b.source_root,''),b.root_path)`
	ancestor := `(?=` + base + ` OR substr(?,1,length(` + base + `)+1)=` + base + ` || '/' OR substr(` + base + `,1,length(?)+1)=? || '/')`
	fullPath := `(CASE WHEN ` + base + `='/' THEN '/' || f.relative_path ELSE ` + base + ` || '/' || f.relative_path END)`
	if _, err := tx.ExecContext(ctx, `DELETE FROM source_catalog_file WHERE (catalog_entry_id, relative_path) IN (
		SELECT f.catalog_entry_id, f.relative_path FROM source_catalog_file f JOIN source_catalog_entry b ON b.id=f.catalog_entry_id
		WHERE b.file_source_id=? AND `+ancestor+` AND `+catalogPathExcludedSQL("?", fullPath)+`)`,
		sourceID, p, p, p, p, sourceID, p, p, p, p); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM source_catalog_observation
		WHERE file_source_id=? AND (
			directory_path=? OR substr(directory_path,1,length(?)+1)=? || '/'
			OR `+catalogObservationPathSQL+`=? OR substr(`+catalogObservationPathSQL+`,1,length(?)+1)=? || '/')
			AND `+catalogPathExcludedSQL("?", catalogObservationPathSQL),
		sourceID, p, p, p, p, p, p, sourceID); err != nil {
		return err
	}
	// A directory row on the way to a whitelist is kept so the next scan can
	// reach the exception; its own contents were already filtered above.
	if _, err := tx.ExecContext(ctx, `DELETE FROM source_catalog_directory
		WHERE file_source_id=? AND (path=? OR substr(path,1,length(?)+1)=? || '/')
			AND `+catalogPathExcludedSQL("?", "path")+`
			AND NOT `+catalogPathLeadsToWhitelistSQL("?", "path"),
		sourceID, p, p, p, sourceID, p, p, p, p, sourceID, p, p); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM source_catalog_audio_root
		WHERE file_source_id=? AND (catalog_root=? OR substr(catalog_root,1,length(?)+1)=? || '/')
			AND `+catalogPathExcludedSQL("?", "catalog_root")+`
			AND NOT `+catalogPathLeadsToWhitelistSQL("?", "catalog_root"),
		sourceID, p, p, p, sourceID, p, p, p, p, sourceID, p, p); err != nil {
		return err
	}
	// The progress columns are an incremental projection. Rebuild them in the
	// same transaction so a deleted subtree cannot leave the worker believing
	// there is still pending work.
	return seedCatalogCountersTx(ctx, tx, sourceID)
}

// revokeCatalogExclusionTx restores only rows that no remaining exclusion still
// covers. Rows that are still excluded are left alone, and files return on the
// next scan because publishing now honours the same rule. A row that was
// previously checked waits for the next scan to prove availability; a row that
// was never checked can show as available again.
func revokeCatalogExclusionTx(ctx context.Context, tx *sql.Tx, sourceID int64, p string) error {
	ids, err := catalogExclusionEntries(ctx, tx, sourceID, p)
	if err != nil {
		return err
	}
	for _, id := range ids {
		var base string
		if err = tx.QueryRowContext(ctx, `SELECT `+catalogExclusionBaseRootSQL+` FROM source_catalog_entry WHERE id=?`, id).Scan(&base); err != nil {
			return err
		}
		excluded, queryErr := catalogPathExcludedTx(ctx, tx, sourceID, base)
		if queryErr != nil {
			return queryErr
		}
		if excluded {
			continue
		}
		if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_entry
			SET availability_status=CASE WHEN status_checked_at IS NULL THEN 'available' ELSE 'unavailable' END,
				updated_at=CURRENT_TIMESTAMP
			WHERE id=? AND availability_status='excluded'`, id); err != nil {
			return err
		}
	}
	return nil
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
	mode, err := normalizeCatalogExclusionMode(payload.Mode)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error(), false)
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
	// A whitelist restores whatever a whole rule had already hidden below it;
	// a whole rule reconciles the rows it now excludes.
	if mode == catalogExclusionModeWhitelist {
		err = revokeCatalogExclusionTx(r.Context(), tx.Tx, sourceID, normalized)
	} else {
		err = applyCatalogExclusionTx(r.Context(), tx.Tx, sourceID, normalized)
	}
	if err != nil {
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
	if mode == catalogExclusionModeWhitelist {
		// Rows deleted by an earlier whole rule are not recreated by a status
		// update: the files and observations are gone. Mark the bridge on the
		// path as changed so the next sync re-lists the exception and
		// republishes its audio, instead of reporting an empty whitelist.
		if _, err = tx.ExecContext(r.Context(), `UPDATE source_catalog_directory SET sync_state='changed'
			WHERE file_source_id=? AND (path=? OR substr(?,1,length(path)+1)=path || '/')`, sourceID, normalized, normalized); err != nil {
			writeError(w, err)
			return
		}
	}
	if err = tx.Commit(); err != nil {
		writeError(w, err)
		return
	}
	if mode == catalogExclusionModeWhitelist {
		if _, err = s.startCatalogIncrementalSync(r.Context(), sourceID, false); err != nil {
			// The rule is saved; the next daily tick can still re-list it.
			// Report the saved rule, but leave the failure observable.
			slog.Warn("whitelist resync failed", "source", sourceID, "error", err)
		}
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
	var excludedPath, excludedMode string
	if err = tx.QueryRowContext(r.Context(), `SELECT path,mode FROM source_catalog_exclusion
		WHERE id=? AND file_source_id=?`, exclusionID, sourceID).Scan(&excludedPath, &excludedMode); err != nil {
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
	// Removing a whitelist re-applies the whole rule above it; removing a whole
	// rule restores only what no other rule still covers.
	if excludedMode == catalogExclusionModeWhitelist {
		err = applyCatalogExclusionTx(r.Context(), tx.Tx, sourceID, excludedPath)
	} else {
		err = revokeCatalogExclusionTx(r.Context(), tx.Tx, sourceID, excludedPath)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		writeError(w, err)
		return
	}
	s.invalidateRemoteWorkCache(sourceID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": excludedPath, "mode": excludedMode})
}

// suppressExcludedCatalogObservationsTx clears the pending flag for rows whose
// full path is excluded. It runs in the page transaction so the root
// completeness check never waits for a publisher that will skip them.
func suppressExcludedCatalogObservationsTx(ctx context.Context, tx *sql.Tx, sourceID int64, directoryPath string, revision int) error {
	result, err := tx.ExecContext(ctx, `UPDATE source_catalog_observation SET publish_pending=0
		WHERE file_source_id=? AND directory_path=? AND revision=? AND publish_pending=1
			AND `+catalogPathExcludedSQL("source_catalog_observation.file_source_id", catalogObservationPathSQL),
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
