package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yexca/kikoto/backend/internal/config"
)

// A voice catalog row is a metadata observation: the audio only exists at the
// remote source, so there is no last published file set to fall back on. Only a
// row the source currently answers for may offer an import or playback action.
func TestVoiceCatalogRemoteWorkPlayableRequiresCurrentAvailability(t *testing.T) {
	db := openMigratedTestDB(t)
	server := NewServer(db, config.Config{})
	ctx := context.Background()
	cases := []struct {
		name         string
		availability string
		workID       sql.NullInt64
		wantStatus   string
		wantPlayable bool
	}{
		{name: "available", availability: "available", wantStatus: "available", wantPlayable: true},
		{name: "unavailable", availability: "unavailable", wantStatus: "unavailable", wantPlayable: false},
		{name: "error", availability: "error", wantStatus: "error", wantPlayable: false},
		{name: "unknown", availability: "unknown", wantStatus: "unknown", wantPlayable: false},
		{name: "missing", availability: "missing", wantStatus: "not_found", wantPlayable: false},
		{name: "not_found", availability: "not_found", wantStatus: "not_found", wantPlayable: false},
		{name: "disabled", availability: "disabled", wantStatus: "disabled", wantPlayable: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			row := voiceCatalogMatchRow{
				PrimaryCode: "RJ00000021", RemoteCode: "RJ00000021", Title: "Example Voice Work",
				Availability: testCase.availability, ItemWorkID: testCase.workID,
			}
			work, err := server.buildVoiceCatalogRemoteWork(ctx, row, "RJ00000021", "ok", map[string]canonicalWorkRef{}, map[string]sourceAvailabilityState{})
			if err != nil {
				t.Fatalf("build voice catalog work: %v", err)
			}
			if work.Availability != testCase.wantStatus {
				t.Fatalf("%s availability = %q, want %q", testCase.name, work.Availability, testCase.wantStatus)
			}
			if work.RemotePlayable != testCase.wantPlayable {
				t.Fatalf("remotePlayable for %q = %t, want %t", testCase.availability, work.RemotePlayable, testCase.wantPlayable)
			}
		})
	}
}

// A row the source last answered as unknown keeps its catalog entry in the
// remote match list; only playability is withheld. The creator page opens the
// source from that entry and checks it, so removing the entry would leave a
// card that offers an action and does nothing.
func TestVoiceCatalogRemoteMatchesKeepUnknownRowsWithoutClaimingPlayback(t *testing.T) {
	db := openMigratedTestDB(t)
	for _, statement := range []string{
		"INSERT INTO person (id, display_name) VALUES (1, 'Example Voice')",
		"INSERT INTO metadata_provider (id, code, display_name) VALUES (11, 'kikoeru_source_example_remote', 'Example Remote')",
		"INSERT INTO file_source (id, code, display_name, source_type, enabled) VALUES (11, 'example_remote', 'Example Remote', 'kikoeru_compatible', 1)",
		"INSERT INTO file_source_endpoint (file_source_id, base_url, api_url) VALUES (11, 'https://example.invalid', 'https://example.invalid')",
		"INSERT INTO voice_catalog_item (id, person_id, primary_code, title) VALUES (21, 1, 'RJ00000021', 'Example Voice Work')",
		"INSERT INTO voice_catalog_source (catalog_item_id, provider_id, remote_code, availability, last_seen_at) VALUES (21, 11, 'RJ00000021', 'unknown', '2026-10-01 00:00:00')",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	server := NewServer(db, config.Config{})
	request := httptest.NewRequest(http.MethodGet, "/api/voices/1/remote-matches", nil)
	request.SetPathValue("personId", "1")
	response := httptest.NewRecorder()
	server.getVoiceRemoteMatches(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	var body struct {
		RemoteMatches []struct {
			Works []struct {
				Availability   string `json:"availability"`
				RemotePlayable bool   `json:"remotePlayable"`
				HasRemote      bool   `json:"hasRemote"`
			} `json:"works"`
		} `json:"remoteMatches"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.RemoteMatches) != 1 || len(body.RemoteMatches[0].Works) != 1 {
		t.Fatalf("remote matches = %+v, want the unknown catalog row", body)
	}
	work := body.RemoteMatches[0].Works[0]
	if work.Availability != "unknown" || work.RemotePlayable {
		t.Fatalf("unknown catalog row = %+v, want unknown and not playable", work)
	}
	if work.HasRemote {
		t.Fatalf("unknown catalog row = %+v, want no confirmed remote presence", work)
	}
}

// A disabled source is reported through the refresh status rather than the row,
// and it must not resurrect playback for a row that is otherwise available.
func TestVoiceCatalogRemoteWorkDisabledSourceIsNotPlayable(t *testing.T) {
	db := openMigratedTestDB(t)
	server := NewServer(db, config.Config{})
	row := voiceCatalogMatchRow{PrimaryCode: "RJ00000022", RemoteCode: "RJ00000022", Availability: "available"}
	work, err := server.buildVoiceCatalogRemoteWork(context.Background(), row, "RJ00000022", "disabled", map[string]canonicalWorkRef{}, map[string]sourceAvailabilityState{})
	if err != nil {
		t.Fatalf("build voice catalog work: %v", err)
	}
	if work.Availability != "disabled" || work.RemotePlayable {
		t.Fatalf("disabled source work = %+v, want disabled and not playable", work)
	}
}
