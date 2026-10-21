package httpapi

import (
	"net/http"
)

// catalogDiagnostics answers the operator questions that the progress payload
// deliberately does not: how many cards became works, how many are excluded,
// and which phases consumed the wall clock. It is a bounded read on demand, so
// it never runs on the polling path.
func (s *Server) catalogDiagnostics(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePermission(w, r, "sources:write"); !ok {
		return
	}
	sourceID, err := parseInt64PathValue(r, "id")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid source id", false)
		return
	}
	type totals struct {
		entries            int64
		workLinked         int64
		excluded           int64
		withAudio          int64
		withoutAudio       int64
		oversizeCards      int64
		unresolvedEncoding int64
	}
	var t totals
	if err = s.db.QueryRowContext(r.Context(), `SELECT
		count(*),
		coalesce(sum(work_id IS NOT NULL),0),
		coalesce(sum(availability_status='excluded'),0),
		coalesce(sum(EXISTS(SELECT 1 FROM source_catalog_file f WHERE f.catalog_entry_id=e.id AND f.kind='audio')),0),
		coalesce(sum(NOT EXISTS(SELECT 1 FROM source_catalog_file f WHERE f.catalog_entry_id=e.id AND f.kind='audio')),0)
		FROM source_catalog_entry e WHERE e.file_source_id=?`, sourceID).
		Scan(&t.entries, &t.workLinked, &t.excluded, &t.withAudio, &t.withoutAudio); err != nil {
		writeError(w, err)
		return
	}
	// A card over the admission ceiling is one the publisher refuses. Counting
	// it here is what makes "published 41,824" explainable when 643 cards hold
	// half of all file rows.
	if err = s.db.QueryRowContext(r.Context(), `SELECT count(*) FROM (
		SELECT catalog_entry_id FROM source_catalog_file GROUP BY catalog_entry_id HAVING count(*)>5000
	)`).Scan(&t.oversizeCards); err != nil {
		writeError(w, err)
		return
	}
	var unresolved int
	_ = s.db.QueryRowContext(r.Context(), `SELECT count(*) FROM source_catalog_encoding_issue
		WHERE file_source_id=? AND status='unresolved'`, sourceID).Scan(&unresolved)
	if unresolved != 0 {
		t.unresolvedEncoding = int64(unresolved)
	}
	conversion := 0.0
	if t.entries > 0 {
		conversion = float64(t.workLinked) / float64(t.entries)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sourceId": sourceID,
		"totals": map[string]any{
			"entries":            t.entries,
			"workLinked":         t.workLinked,
			"workLinkedRatio":    conversion,
			"excluded":           t.excluded,
			"withAudio":          t.withAudio,
			"withoutAudio":       t.withoutAudio,
			"oversizeCards":      t.oversizeCards,
			"unresolvedEncoding": t.unresolvedEncoding,
		},
	})
}
