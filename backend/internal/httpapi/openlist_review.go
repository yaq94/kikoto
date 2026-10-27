package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
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
	entry, ok := s.lockCatalogEntry(r.Context(), w, r, tx, retained.ID)
	if !ok {
		return
	}
	workID, ok := s.ensureCatalogEntryWork(r.Context(), w, r, tx, entry)
	if !ok {
		return
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

// lockCatalogEntry takes the writer lock before reading entry identity. Every
// field used afterwards is then the row a concurrent rename or source move
// committed before the lock, never a pre-transaction snapshot. The retained
// id is followed through a relocation before it is returned.
func (s *Server) lockCatalogEntry(ctx context.Context, w http.ResponseWriter, r *http.Request, tx *sql.Tx, retainedID int64) (catalogEntry, bool) {
	if _, err := tx.ExecContext(ctx, "UPDATE source_catalog_entry SET id=id WHERE id=?", retainedID); err != nil {
		writeError(w, err)
		return catalogEntry{}, false
	}
	entry, err := resolveCatalogEntry(ctx, tx, retainedID)
	if err != nil {
		writeError(w, err)
		return catalogEntry{}, false
	}
	if entry.SourceID != 0 {
		admission, admissionErr := catalogPublishedEntryAdmission(ctx, tx, entry.ID)
		if admissionErr != nil {
			writeError(w, admissionErr)
			return catalogEntry{}, false
		}
		// A retained missing record has no current published file set to judge;
		// every other source-backed card must pass the same admission rule as
		// the detail, tracks and browse routes.
		if catalogEntryAdmissionRequired(entry) && !admission.Admitted() {
			writeCatalogEntryNotAvailable(w, admission)
			return catalogEntry{}, false
		}
	}
	return entry, true
}

// ensureCatalogEntryWork registers the work a catalog entry stands for,
// reusing a work that already carries the same code and creating one when this
// entry is its first publication. The caller already holds the writer lock and
// has checked that the entry may be used.
func (s *Server) ensureCatalogEntryWork(ctx context.Context, w http.ResponseWriter, r *http.Request, tx *sql.Tx, entry catalogEntry) (sql.NullInt64, bool) {
	var resolvedID int64
	var err error
	if entry.PrimaryCode != "" {
		resolvedID, err = resolveCatalogReviewWorkID(ctx, tx, entry.PrimaryCode)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			writeError(w, err)
			return sql.NullInt64{}, false
		}
		if errors.Is(err, sql.ErrNoRows) {
			resolvedID = 0
		}
	}
	var workID sql.NullInt64
	if err = tx.QueryRowContext(ctx, "SELECT work_id FROM source_catalog_entry WHERE id=?", entry.ID).Scan(&workID); err != nil {
		writeError(w, err)
		return sql.NullInt64{}, false
	}
	if !workID.Valid && resolvedID > 0 {
		workID = sql.NullInt64{Int64: resolvedID, Valid: true}
	}
	if !workID.Valid {
		if _, ok := s.requirePermission(w, r, "remote:track"); !ok {
			return sql.NullInt64{}, false
		}
		if entry.PrimaryCode != "" {
			_, err = tx.ExecContext(ctx, `INSERT INTO work(primary_code,title) VALUES(?,?) ON CONFLICT(primary_code) WHERE primary_code<>'' DO NOTHING`, entry.PrimaryCode, entry.Title)
			if err == nil {
				err = tx.QueryRowContext(ctx, "SELECT id FROM work WHERE primary_code=?", entry.PrimaryCode).Scan(&workID.Int64)
			}
		} else {
			workID.Int64, err = sqlutil.InsertID(ctx, tx, "INSERT INTO work(primary_code,title) VALUES('',?)", entry.Title)
		}
		if err != nil {
			writeError(w, err)
			return sql.NullInt64{}, false
		}
		workID.Valid = true
		var logicalID int64
		err = tx.QueryRowContext(ctx, "SELECT logical_work_id FROM work_edition WHERE work_id=?", workID.Int64).Scan(&logicalID)
		if errors.Is(err, sql.ErrNoRows) {
			logicalID, err = sqlutil.InsertID(ctx, tx, "INSERT INTO logical_work(canonical_work_id,canonical_code) VALUES(?,?)", workID.Int64, entry.PrimaryCode)
			if err == nil {
				_, err = tx.ExecContext(ctx, "INSERT INTO work_edition(work_id,logical_work_id,primary_code,is_canonical) VALUES(?,?,?,1)", workID.Int64, logicalID, entry.PrimaryCode)
			}
		}
		if err != nil {
			writeError(w, err)
			return sql.NullInt64{}, false
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE source_catalog_entry SET work_id=? WHERE id=?", workID.Int64, entry.ID); err != nil {
		writeError(w, err)
		return sql.NullInt64{}, false
	}
	if entry.SourceID != 0 {
		if err = upsertWorkSourcePresence(ctx, tx, workSourcePresence{WorkID: workID.Int64, FileSourceID: entry.SourceID, PresenceType: sourcePresenceTypeRemoteSource, RemoteID: strconv.FormatInt(entry.ID, 10), RemoteCode: entry.PrimaryCode, Availability: entry.Status, RawJSON: mustJSON(map[string]any{"catalogEntryId": entry.ID})}); err != nil {
			writeError(w, err)
			return sql.NullInt64{}, false
		}
	}
	return workID, true
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
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	entry, ok := s.lockCatalogEntry(r.Context(), w, r, tx, retained.ID)
	if !ok {
		return
	}
	workID, ok := s.ensureCatalogEntryWork(r.Context(), w, r, tx, entry)
	if !ok {
		return
	}
	if err = tx.Commit(); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"workId": workID.Int64})
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
