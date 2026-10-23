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
	// Take the writer lock before reading identity. Every field used below is
	// then the row a concurrent rename or source move committed before the lock,
	// never a pre-transaction snapshot.
	if _, err = tx.ExecContext(r.Context(), "UPDATE source_catalog_entry SET id=id WHERE id=?", retained.ID); err != nil {
		writeError(w, err)
		return
	}
	entry, err := resolveCatalogEntry(r.Context(), tx, retained.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	id = entry.ID
	if entry.SourceID != 0 {
		admission, admissionErr := catalogPublishedEntryAdmission(r.Context(), tx, entry.ID)
		if admissionErr != nil {
			writeError(w, admissionErr)
			return
		}
		if admission.Files > 0 && admission.Audio == 0 {
			writeCatalogEntryNotAvailable(w, admission)
			return
		}
	}
	var resolvedID int64
	if entry.PrimaryCode != "" {
		resolvedID, err = resolveCatalogReviewWorkID(r.Context(), tx, entry.PrimaryCode)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			writeError(w, err)
			return
		}
		if errors.Is(err, sql.ErrNoRows) {
			resolvedID = 0
		}
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
	if entry.SourceID != 0 {
		if err = upsertWorkSourcePresence(r.Context(), tx, workSourcePresence{WorkID: workID.Int64, FileSourceID: entry.SourceID, PresenceType: sourcePresenceTypeRemoteSource, RemoteID: strconv.FormatInt(id, 10), RemoteCode: entry.PrimaryCode, Availability: entry.Status, RawJSON: mustJSON(map[string]any{"catalogEntryId": id})}); err != nil {
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
