package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yexca/kikoto/backend/internal/catalogadmission"
)

// The operator diagnostic counts the cards the publisher would refuse, using
// the shared threshold and only the requested source. A card from another
// source must not inflate the number.
func TestCatalogDiagnosticsOversizeCardsUseTheProbeBoundPerSource(t *testing.T) {
	db := openMigratedTestDB(t)
	metadataReviewExec(t, db, `INSERT INTO file_source(id,code,display_name,source_type,enabled) VALUES
		(71,'diagnostics-a','Diagnostics A','openlist',1),
		(72,'diagnostics-b','Diagnostics B','openlist',1);
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,availability_status) VALUES
		(7101,71,'/a/oversize','A oversize','available'),
		(7102,71,'/a/boundary','A boundary','available'),
		(7103,71,'/a/stale','A stale','available'),
		(7201,72,'/b/oversize','B oversize','available');`)
	seedAdmissionRows(t, db, 7101, "a", ".jpg", "other", catalogadmission.ProbeFiles+1, 1)
	seedAdmissionRows(t, db, 7102, "a", ".mp3", "audio", 1, 1)
	seedAdmissionRows(t, db, 7102, "n", ".jpg", "other", catalogadmission.ProbeFiles-1, 1)
	seedAdmissionRows(t, db, 7201, "b", ".jpg", "other", catalogadmission.ProbeFiles+1, 1)
	// Rows left by an older generation are provenance, not part of the
	// current release, so they must not inflate the oversize count either.
	seedAdmissionRows(t, db, 7103, "old", ".jpg", "other", catalogadmission.ProbeFiles+1, 0)

	server := &Server{db: db}
	request := httptest.NewRequest(http.MethodGet, "/api/remote-sources/71/catalog/diagnostics", nil)
	request.SetPathValue("id", "71")
	request = request.WithContext(context.WithValue(request.Context(), currentUserKey,
		currentUser{ID: 1, Permissions: []string{"sources:write"}}))
	response := httptest.NewRecorder()
	server.catalogDiagnostics(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("diagnostics status = %d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Totals struct {
			OversizeCards int64 `json:"oversizeCards"`
			Entries       int64 `json:"entries"`
		} `json:"totals"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Totals.Entries != 3 {
		t.Fatalf("entries = %d, want only the requested source's three cards", payload.Totals.Entries)
	}
	if payload.Totals.OversizeCards != 1 {
		t.Fatalf("oversizeCards = %d, want one current-generation card over the shared bound", payload.Totals.OversizeCards)
	}
}

// A pause token may carry detail after the resource name; the persisted reason
// must still be the specific pause instead of falling back to an unknown disk
// state.
func TestCatalogDeferralReasonKeepsThePauseWhenDetailFollows(t *testing.T) {
	for _, tc := range []struct {
		pause string
		want  string
	}{
		{"low_disk_space", "low_disk"},
		{"low_disk_space; 1.2 GiB free", "low_disk"},
		{"maintenance", "maintenance_hold"},
		{"maintenance_hold", "maintenance_hold"},
		{"disk_space_unknown", "disk_unknown"},
	} {
		if got := catalogDeferralReason(tc.pause); got != tc.want {
			t.Fatalf("catalogDeferralReason(%q) = %q, want %q", tc.pause, got, tc.want)
		}
	}
}
