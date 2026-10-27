package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
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
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&patch); err != nil {
		writeAPIError(w, 400, "invalid_request", "invalid JSON body", false)
		return
	}
	if decoder.Decode(new(any)) != io.EOF {
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
	// Reject a missing or unusable source before opening a write transaction.
	// Its identity fields are deliberately not reused below: a rescan can rename
	// the entry or move it to another source before this request takes the lock.
	retained, _, err := s.loadRetainedSourceCatalogEntry(r.Context(), id)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	// Take the writer lock before reading identity. A rescan can rename the
	// entry or move it to another source between the retained read and this
	// transaction, so nothing read before the lock is reused.
	entry, err := lockCatalogEntry(r.Context(), tx, retained.ID)
	if err != nil {
		writeCatalogEntryReviewError(w, err)
		return
	}
	mayRegister := permissionHeld(r, "remote:track")
	workID, err := ensureCatalogEntryWork(r.Context(), tx, entry, mayRegister)
	if err != nil {
		writeCatalogEntryReviewError(w, err)
		return
	}
	if err = linkCatalogEntryWork(r.Context(), tx, entry, workID, entry.Status); err != nil {
		writeError(w, err)
		return
	}
	state, err := patchWorkUserState(r.Context(), tx, user.ID, workID, patch)
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

// registerSourceCatalogWork lets a card obtain the work id it needs for list
// and listening actions without writing a rating or note. Rating and note stay
// the only fields that create a personal review.
func (s *Server) registerSourceCatalogWork(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePermission(w, r, "favorites:write"); !ok {
		return
	}
	id, err := parseInt64PathValue(r, "id")
	if err != nil {
		writeAPIError(w, 400, "invalid_request", "invalid catalog id", false)
		return
	}
	retained, _, err := s.loadRetainedSourceCatalogEntry(r.Context(), id)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	mayRegister := permissionHeld(r, "remote:track")
	tx, release, err := beginTxWithDatabaseBusyRetry(r.Context(), s.db)
	if err != nil {
		writeError(w, err)
		return
	}
	defer release()
	entry, err := lockCatalogEntry(r.Context(), tx.Tx, retained.ID)
	if err != nil {
		writeCatalogEntryReviewError(w, err)
		return
	}
	workID, err := ensureCatalogEntryWork(r.Context(), tx.Tx, entry, mayRegister)
	if err != nil {
		writeCatalogEntryReviewError(w, err)
		return
	}
	if err = linkCatalogEntryWork(r.Context(), tx.Tx, entry, workID, entry.Status); err != nil {
		writeError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"workId": workID})
}

// writeCatalogEntryReviewError reports the answers a catalog write can refuse
// on: an entry that never reached admission, and a caller who may keep a
// record but may not create a work.
func writeCatalogEntryReviewError(w http.ResponseWriter, err error) {
	var unavailable catalogEntryUnavailableError
	switch {
	case errors.As(err, &unavailable):
		writeCatalogEntryNotAvailable(w, unavailable.admission)
	case errors.Is(err, errCatalogWorkRegistrationForbidden):
		writeAPIError(w, http.StatusForbidden, "permission_denied", "Creating a work from a catalog entry requires remote:track.", false)
	default:
		writeError(w, err)
	}
}

// resolveCatalogReviewWorkID applies the same canonical-code precedence as
// resolveWorkCodeIdentity, but reads through the review transaction. Using the
// server pool here would need a second connection while the writer lock is held
// and could wait forever on a single-connection deployment or test pool.
func resolveCatalogReviewWorkID(ctx context.Context, tx *sql.Tx, code string) (int64, error) {
	code = normalizeWorkCode(code)
	if code == "" {
		return 0, sql.ErrNoRows
	}
	var workID int64
	err := tx.QueryRowContext(ctx, `
		SELECT id FROM work
		WHERE UPPER(primary_code)=UPPER(?)
		LIMIT 1`, code).Scan(&workID)
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, `
			SELECT edition.work_id
			FROM work_edition AS edition
			WHERE UPPER(edition.primary_code)=UPPER(?)
			LIMIT 1`, code).Scan(&workID)
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if err == nil {
		return canonicalCatalogReviewWorkID(ctx, tx, workID, code)
	}

	var canonicalID sql.NullInt64
	var canonicalCode string
	err = tx.QueryRowContext(ctx, `
		SELECT logical.canonical_work_id,logical.canonical_code
		FROM work_code_alias AS alias
		INNER JOIN logical_work AS logical ON logical.id=alias.logical_work_id
		WHERE UPPER(alias.primary_code)=UPPER(?)
		ORDER BY CASE WHEN alias.source_work_id IS NOT NULL THEN 0 ELSE 1 END,alias.id
		LIMIT 1`, code).Scan(&canonicalID, &canonicalCode)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, sql.ErrNoRows
	}
	if err != nil {
		return 0, err
	}
	if canonicalID.Valid {
		return canonicalID.Int64, nil
	}
	if canonicalCode == "" {
		return 0, sql.ErrNoRows
	}
	err = tx.QueryRowContext(ctx, "SELECT id FROM work WHERE UPPER(primary_code)=UPPER(?) LIMIT 1", normalizeWorkCode(canonicalCode)).Scan(&workID)
	return workID, err
}

func canonicalCatalogReviewWorkID(ctx context.Context, tx *sql.Tx, workID int64, code string) (int64, error) {
	var canonicalID sql.NullInt64
	var canonicalCode, baseCode string
	err := tx.QueryRowContext(ctx, `
		SELECT logical.canonical_work_id,logical.canonical_code,edition.base_code
		FROM work_edition AS edition
		INNER JOIN logical_work AS logical ON logical.id=edition.logical_work_id
		WHERE edition.work_id=?`, workID).Scan(&canonicalID, &canonicalCode, &baseCode)
	if errors.Is(err, sql.ErrNoRows) {
		return workID, nil
	}
	if err != nil {
		return 0, err
	}
	if canonicalID.Valid {
		return canonicalID.Int64, nil
	}
	if canonicalCode != "" && normalizeWorkCode(canonicalCode) != code {
		var canonicalWorkID int64
		err = tx.QueryRowContext(ctx, "SELECT id FROM work WHERE UPPER(primary_code)=UPPER(?) LIMIT 1", normalizeWorkCode(canonicalCode)).Scan(&canonicalWorkID)
		if err == nil {
			return canonicalWorkID, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
	}
	if baseCode == "" || normalizeWorkCode(baseCode) == code {
		return workID, nil
	}
	var baseID int64
	err = tx.QueryRowContext(ctx, "SELECT id FROM work WHERE UPPER(primary_code)=UPPER(?) LIMIT 1", normalizeWorkCode(baseCode)).Scan(&baseID)
	if errors.Is(err, sql.ErrNoRows) {
		return workID, nil
	}
	return baseID, err
}
