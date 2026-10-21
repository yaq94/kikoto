package httpapi

import (
	"net/http"
	"testing"
)

// A failure in one subtree must not stop the lifecycle pass for the ranges the
// generation observed completely. Before this gate change the run stayed at
// observe with cursor zero, so a single upstream defect froze every card's
// availability forever.
func TestSourceCatalogPartialRunAdvancesLifecycleForObservedRanges(t *testing.T) {
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/":     {looseDir("Bad"), looseDir("Good")},
		"/Good": {looseAudio("Alpha.mp3")},
		"/Bad":  {looseAudio("bad.mp3")},
	})
	f.failPath("/Bad")
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	progress := f.drain(t)
	if progress["status"] != "partial" {
		t.Fatalf("scan = %#v, want partial", progress)
	}
	var sourceID int64
	if err := f.server.db.QueryRow(`SELECT id FROM file_source WHERE display_name='Example Remote A'`).Scan(&sourceID); err != nil {
		t.Fatal(err)
	}
	var stage string
	var cursor int64
	if err := f.server.db.QueryRow(`SELECT lifecycle_stage,lifecycle_cursor FROM source_catalog_scan WHERE file_source_id=?`, sourceID).Scan(&stage, &cursor); err != nil {
		t.Fatal(err)
	}
	if stage == "observe" && cursor == 0 {
		t.Fatalf("lifecycle stayed at its initial position after a partial run")
	}
	var checked int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_entry WHERE file_source_id=? AND status_checked_at IS NOT NULL`, sourceID).Scan(&checked); err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("no card received an availability decision in a partial run")
	}
}
