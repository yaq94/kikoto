package httpapi

import (
	"context"
	"database/sql"
	"net/http"
	"path"
	"strings"
	"unicode/utf8"
)

// A listed name whose bytes are not valid UTF-8, or that carries U+FFFD, cannot
// be requested: the source answers "object not found" for the replacement path
// and the whole run stays partial. The bytes were lost before this layer could
// read them, so the scanner records the name instead of guessing a repair.
const (
	catalogEncodingUnresolved = "unresolved"
	catalogEncodingRecovered  = "recovered"
	// catalogEncodingIgnored is reserved for an operator decision made outside
	// the scanner. It is never written here and never appears on the fix list.
	catalogEncodingIgnored = "ignored"
)

// catalogEncodingIssueRecord is one row of the operator-facing fix list. The
// raw name keeps exactly what the listing carried, replacement characters and
// all; nothing is normalized.
type catalogEncodingIssueRecord struct {
	ID            int64  `json:"id"`
	SourceID      int64  `json:"sourceId"`
	ParentPath    string `json:"parentPath"`
	RawName       string `json:"rawName"`
	CandidateCode string `json:"candidateCode"`
	SuggestedName string `json:"suggestedName"`
	Status        string `json:"status"`
	ResolvedPath  string `json:"resolvedPath,omitempty"`
	CreatedAt     string `json:"createdAt"`
	UpdatedAt     string `json:"updatedAt"`
}

// catalogNameEncodingDamaged reports a name that must not be turned into a
// source path. A replacement character is treated as damage even when it
// arrived as valid UTF-8: the origin meant some other character, and using the
// replacement verbatim is the guess this module refuses to make.
func catalogNameEncodingDamaged(name string) bool {
	if name == "" {
		return false
	}
	return !utf8.ValidString(name) || strings.ContainsRune(name, utf8.RuneError)
}

// catalogEncodingCandidateCode extracts the work code that survived the
// corruption. The shared parser only accepts contiguous digits, so a damaged
// digit yields no code rather than a truncated one. The value is diagnostic
// only: the scanner never requests a path built from it.
func catalogEncodingCandidateCode(name string) string {
	if !catalogNameEncodingDamaged(name) {
		return ""
	}
	return catalogDirectoryCode(name)
}

// recordCatalogEncodingIssueTx writes the durable record. A rescan updates the
// same row: the issue is one damaged name in one parent, not one row per scan.
// raw_name keeps the listed bytes with no cleanup; an invalid byte survives the
// TEXT column unchanged, so the operator can see what actually arrived.
//
// The update is limited to rows the scanner still owns. An operator who marked
// a name ignored or recovered keeps that decision when the same name reappears;
// the scanner never turns such a row back into unresolved.
func recordCatalogEncodingIssueTx(ctx context.Context, tx *sql.Tx, sourceID int64, parent, name string) (string, error) {
	code := catalogEncodingCandidateCode(name)
	_, err := tx.ExecContext(ctx, `INSERT INTO source_catalog_encoding_issue
		(file_source_id,parent_path,raw_name,candidate_code,status)
		VALUES(?,?,?,?,?)
		ON CONFLICT(file_source_id,parent_path,raw_name) DO UPDATE SET
			candidate_code=excluded.candidate_code,
			updated_at=CURRENT_TIMESTAMP
		WHERE source_catalog_encoding_issue.status=?`,
		sourceID, parent, name, code, catalogEncodingUnresolved, catalogEncodingUnresolved)
	return code, err
}

// expandCatalogChildTx replaces the plain enqueue for one listed child.
// A clean name enters the generation as before. A damaged name is recorded and
// never requested: neither the replacement path nor a sibling chosen by code
// matching is enqueued, because both are guesses about bytes the source never
// delivered.
func (s *Server) expandCatalogChildTx(ctx context.Context, tx *sql.Tx, sourceID, generation int64, parent, partition, root, name string) (bool, error) {
	child := path.Join(parent, name)
	part := partition
	if parent == "/" {
		part = child
	}
	if !catalogNameEncodingDamaged(name) {
		return enqueueCatalogDirectoryTx(ctx, tx, sourceID, generation, child, part, root)
	}
	if _, err := recordCatalogEncodingIssueTx(ctx, tx, sourceID, parent, name); err != nil {
		return false, err
	}
	// The damaged row must not stay in the publish queue. It names an
	// unrequestable path, and a folder row that keeps publish_pending=1 also
	// counts as unobserved coverage for the range in catalogRootIncomplete.
	// Clearing the flag drops it from both queues; the issue table carries the
	// only record this row still needs.
	if _, err := tx.ExecContext(ctx, `UPDATE source_catalog_observation SET publish_pending=0
		WHERE file_source_id=? AND directory_path=? AND name=?
			AND revision=(SELECT revision FROM source_catalog_directory WHERE file_source_id=? AND path=?)`,
		sourceID, parent, name, sourceID, parent); err != nil {
		return false, err
	}
	return false, nil
}

// catalogEncodingFixList returns the damaged names an operator still has to
// resolve. Recovered and ignored rows stay in the table for history but never
// appear here.
func (s *Server) catalogEncodingFixList(ctx context.Context, sourceID int64) ([]catalogEncodingIssueRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,file_source_id,parent_path,raw_name,candidate_code,
		suggested_name,status,resolved_path,created_at,updated_at
		FROM source_catalog_encoding_issue WHERE file_source_id=? AND status=?
		ORDER BY parent_path,raw_name`, sourceID, catalogEncodingUnresolved)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	issues := []catalogEncodingIssueRecord{}
	for rows.Next() {
		var issue catalogEncodingIssueRecord
		if err = rows.Scan(&issue.ID, &issue.SourceID, &issue.ParentPath, &issue.RawName, &issue.CandidateCode,
			&issue.SuggestedName, &issue.Status, &issue.ResolvedPath, &issue.CreatedAt, &issue.UpdatedAt); err != nil {
			return nil, err
		}
		issues = append(issues, issue)
	}
	return issues, rows.Err()
}

// catalogEncodingUnresolvedCount is the independent counter for a settled run.
// It never feeds failed, restricted or unconfirmed-empty.
func (s *Server) catalogEncodingUnresolvedCount(ctx context.Context, sourceID int64) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM source_catalog_encoding_issue
		WHERE file_source_id=? AND status=?`, sourceID, catalogEncodingUnresolved).Scan(&count)
	return count, err
}

func (s *Server) catalogEncodingRecoveredCount(ctx context.Context, sourceID int64) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM source_catalog_encoding_issue
		WHERE file_source_id=? AND status=?`, sourceID, catalogEncodingRecovered).Scan(&count)
	return count, err
}

// listSourceCatalogEncodingIssues is the read-only fix list. It is registered
// as GET /api/remote-sources/{id}/catalog/encoding-issues.
func (s *Server) listSourceCatalogEncodingIssues(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePermission(w, r, "sources:write"); !ok {
		return
	}
	id, err := parseInt64PathValue(r, "id")
	if err != nil {
		writeAPIError(w, 400, "invalid_request", "invalid source id", false)
		return
	}
	issues, err := s.catalogEncodingFixList(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	recovered, err := s.catalogEncodingRecoveredCount(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{
		"sourceId":   id,
		"unresolved": len(issues),
		"recovered":  recovered,
		"issues":     issues,
	})
}
