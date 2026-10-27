package httpapi

import (
	"context"
	"database/sql"
	"testing"

	"github.com/yexca/kikoto/backend/internal/config"
)

// A voice catalog row keeps its last published file set through a failed,
// restricted or unconfirmed observation, so only a confirmed absence removes
// playback. The voice vocabulary spells that absence "not_found", and a
// disabled source keeps its rows visible as "disabled"; both must be
// unplayable even though catalogVerdictPlayable only knows "missing" and
// "excluded".
func TestVoiceCatalogRemoteWorkPlayableFollowsCatalogVerdict(t *testing.T) {
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
		{name: "unavailable", availability: "unavailable", wantStatus: "unavailable", wantPlayable: true},
		{name: "error", availability: "error", wantStatus: "error", wantPlayable: true},
		{name: "unknown", availability: "unknown", wantStatus: "unknown", wantPlayable: true},
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
