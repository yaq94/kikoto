package httpapi

import (
	"context"
	"database/sql"
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
		lastSeen     string
		workID       sql.NullInt64
		wantStatus   string
		wantPlayable bool
	}{
		{name: "available", availability: "available", wantStatus: "available", wantPlayable: true},
		{name: "unavailable", availability: "unavailable", wantStatus: "unavailable", wantPlayable: false},
		{name: "error", availability: "error", wantStatus: "error", wantPlayable: false},
		{name: "unknown", availability: "unknown", wantStatus: "unknown", wantPlayable: false},
		{name: "unknown with last seen", availability: "unknown", lastSeen: "2026-10-01 00:00:00", wantStatus: "unknown", wantPlayable: false},
		{name: "missing", availability: "missing", wantStatus: "not_found", wantPlayable: false},
		{name: "not_found", availability: "not_found", wantStatus: "not_found", wantPlayable: false},
		{name: "disabled", availability: "disabled", wantStatus: "disabled", wantPlayable: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			row := voiceCatalogMatchRow{
				PrimaryCode: "RJ00000021", RemoteCode: "RJ00000021", Title: "Example Voice Work",
				Availability: testCase.availability, ItemWorkID: testCase.workID, LastSeen: testCase.lastSeen,
			}
			work, err := server.buildVoiceCatalogRemoteWork(ctx, row, "RJ00000021", "ok", map[string]canonicalWorkRef{}, map[string]sourceAvailabilityState{})
			if err != nil {
				t.Fatalf("build voice catalog work: %v", err)
			}
			if work.Availability != testCase.wantStatus {
				t.Fatalf("availability = %q, want %q", work.Availability, testCase.wantStatus)
			}
			if work.RemotePlayable != testCase.wantPlayable {
				t.Fatalf("remotePlayable for %q = %t, want %t", testCase.availability, work.RemotePlayable, testCase.wantPlayable)
			}
		})
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
