package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/yexca/kikoto/backend/internal/config"
)

func TestMergeVoiceWorkViewsKeepsKnownIdentityWithItsRemoteObservations(t *testing.T) {
	known := voiceKnownWork{WorkID: 11, PrimaryCode: "RJ00000001", Title: "Example Work 1"}
	origin := voiceCatalogRemoteWorkForTest("rj00000001", "RJ00000001", "available")
	translation := voiceCatalogRemoteWorkForTest("rj00000001", "RJ00000002", "available")

	views := mergeVoiceWorkViews([]voiceKnownWork{known}, []voiceRemoteSourceSet{
		{SourceID: 7, Status: "ok", Works: []voiceRemoteWork{origin, translation}},
	})
	if len(views) != 1 || views[0].Known == nil || views[0].Known.WorkID != 11 {
		t.Fatalf("views = %+v, want the one known work", views)
	}
	merged := views[0].Known
	if merged.Remote {
		t.Fatalf("remote = true, want the known work's own availability flag untouched")
	}
	tag, ok := voiceSourceTagForID(merged.SourceTags, 7)
	if !ok || tag.Status != "available" || tag.Count != 1 {
		t.Fatalf("source tag = %+v (present %t), want an available tag for the observed source", tag, ok)
	}
	if got := voiceObservationCodes(merged.RemoteObservations); !reflect.DeepEqual(got, []string{"RJ00000001", "RJ00000002"}) {
		t.Fatalf("observations = %v, want the canonical edition first", got)
	}

	known.RemoteCode = "RJ00000002"
	views = mergeVoiceWorkViews([]voiceKnownWork{known}, []voiceRemoteSourceSet{
		{SourceID: 7, Status: "ok", Works: []voiceRemoteWork{origin, translation}},
	})
	if got := voiceObservationCodes(views[0].Known.RemoteObservations); !reflect.DeepEqual(got, []string{"RJ00000002", "RJ00000001"}) {
		t.Fatalf("observations = %v, want the known exact edition first", got)
	}
}

func TestMergeVoiceWorkViewsPrefersPersistedObservationWhenCatalogHasNoRow(t *testing.T) {
	known := voiceKnownWork{
		WorkID: 11, PrimaryCode: "RJ00000001",
		RemoteObservations: []voiceRemoteObservation{
			{SourceID: 9, SourceCode: "example_remote_b", SourceName: "Example Remote B", RemoteCode: "RJ00000003", Status: "available"},
		},
	}
	views := mergeVoiceWorkViews([]voiceKnownWork{known}, []voiceRemoteSourceSet{{SourceID: 9, Status: "error"}})
	if len(views) != 1 || views[0].Known == nil {
		t.Fatalf("views = %+v, want the one known work", views)
	}
	observations := views[0].Known.RemoteObservations
	if len(observations) != 1 || observations[0].SourceID != 9 || observations[0].RemoteCode != "RJ00000003" || observations[0].Status != "available" {
		t.Fatalf("observations = %+v, want the persisted exact source route", observations)
	}
}

func TestMergeVoiceWorkViewsKeepsOneRemoteOnlyWorkPerCode(t *testing.T) {
	missing := voiceCatalogRemoteWorkForTest("RJ00000004", "RJ00000004", "not_found")
	missing.RemotePlayable, missing.HasRemote = false, false
	other := missing
	other.SourceID, other.SourceCode, other.SourceName = 8, "example_remote_b", "Example Remote B"

	views := mergeVoiceWorkViews(nil, []voiceRemoteSourceSet{
		{SourceID: 7, Status: "error", Works: []voiceRemoteWork{missing}},
		{SourceID: 8, Status: "ok", Works: []voiceRemoteWork{other}},
	})
	if len(views) != 1 || views[0].Remote == nil || views[0].Remote.SourceID != 7 {
		t.Fatalf("views = %+v, want one remote-only work from the first source", views)
	}
	if len(views[0].RemoteObservations) != 2 {
		t.Fatalf("observations = %+v, want one per source", views[0].RemoteObservations)
	}
	if _, remote, _ := views[0].availability(); remote || !views[0].observedMissing() {
		t.Fatalf("view = %+v, want an explicitly missing work", views[0])
	}
}

func TestFilterVoiceWorkViewsByAvailabilityAndQuery(t *testing.T) {
	sourceID := int64(7)
	observed := func(status string) []circleSourceStat {
		return []circleSourceStat{{Key: "source:7", SourceID: &sourceID, DisplayName: "Example Remote A", Status: status}}
	}
	remoteOnly := voiceCatalogRemoteWorkForTest("RJ00000005", "RJ00000005", "available")
	views := []voiceWorkView{
		{Known: &voiceKnownWork{PrimaryCode: "RJ00000001", Title: "Example Work 1", Local: true}},
		{Known: &voiceKnownWork{PrimaryCode: "RJ00000002", Title: "Example Work 2", SourceTags: observed("not_found")}},
		{Known: &voiceKnownWork{PrimaryCode: "RJ00000003", Title: "Example Work 3", SourceTags: observed("error")}},
		{Known: &voiceKnownWork{
			PrimaryCode: "RJ00000004", Title: "Example Work 4", Cache: true,
			UserTags: []workUserTag{{Name: "Example Tag"}},
		}},
		{Remote: &remoteOnly},
	}
	for _, test := range []struct {
		availability, query string
		want                []string
	}{
		{"all", "", []string{"RJ00000001", "RJ00000002", "RJ00000003", "RJ00000004", "RJ00000005"}},
		{"available", "", []string{"RJ00000001", "RJ00000004", "RJ00000005"}},
		{"local", "", []string{"RJ00000001"}},
		{"remote", "", []string{"RJ00000005"}},
		// A source that failed, or was never checked, is not a missing work.
		{"missing", "", []string{"RJ00000002"}},
		{"all", "example tag", []string{"RJ00000004"}},
		{"all", "rj00000005", []string{"RJ00000005"}},
		{"local", "work 4", []string{}},
	} {
		if got := voiceWorkViewCodes(filterVoiceWorkViews(views, test.query, test.availability)); !reflect.DeepEqual(got, test.want) {
			t.Errorf("availability %q query %q = %v, want %v", test.availability, test.query, got, test.want)
		}
	}
}

func TestSortVoiceWorkViewsKeepsWorksWithoutTheFactLast(t *testing.T) {
	known := func(code, title, releaseDate string, rating float64, sales int64) voiceWorkView {
		return voiceWorkView{Known: &voiceKnownWork{
			PrimaryCode: code, Title: title, ReleaseDate: &releaseDate, Rating: &rating, Sales: &sales,
		}}
	}
	// A remote match can arrive without a release date, rating, or sales count.
	bare := voiceCatalogRemoteWorkForTest("RJ00000003", "RJ00000003", "available")
	bare.Title = "Delta"
	works := []voiceWorkView{
		known("RJ00000000", "Beta", "2024-03-01", 4.1, 300),
		known("RJ00000001", "alpha", "2024-01-01", 4.9, 100),
		known("RJ00000002", "Gamma", "2024-02-01", 4.5, 200),
		{Remote: &bare},
	}
	for _, test := range []struct {
		sort, direction string
		want            []string
	}{
		{"code", "asc", []string{"RJ00000000", "RJ00000001", "RJ00000002", "RJ00000003"}},
		{"code", "desc", []string{"RJ00000003", "RJ00000002", "RJ00000001", "RJ00000000"}},
		{"title", "asc", []string{"RJ00000001", "RJ00000000", "RJ00000003", "RJ00000002"}},
		{"sales", "desc", []string{"RJ00000000", "RJ00000002", "RJ00000001", "RJ00000003"}},
		{"release", "desc", []string{"RJ00000000", "RJ00000002", "RJ00000001", "RJ00000003"}},
		{"release", "asc", []string{"RJ00000001", "RJ00000002", "RJ00000000", "RJ00000003"}},
		{"rating", "desc", []string{"RJ00000001", "RJ00000002", "RJ00000000", "RJ00000003"}},
		{"rating", "asc", []string{"RJ00000000", "RJ00000002", "RJ00000001", "RJ00000003"}},
	} {
		sorted := append([]voiceWorkView{}, works...)
		sortVoiceWorkViews(sorted, test.sort, test.direction)
		if got := voiceWorkViewCodes(sorted); !reflect.DeepEqual(got, test.want) {
			t.Errorf("sort %s %s = %v, want %v", test.sort, test.direction, got, test.want)
		}
	}
}

func TestVoiceWorksEndpointPagesKnownAndRemoteCatalogWorks(t *testing.T) {
	db := openMigratedTestDB(t)
	for _, statement := range []string{
		"INSERT INTO person (id, display_name) VALUES (1, 'Example Voice')",
		"INSERT INTO work (id, primary_code, title, release_date) VALUES (1, 'RJ00000001', 'Example Work 1', '2024-01-01')",
		"INSERT INTO work (id, primary_code, title, release_date) VALUES (2, 'RJ00000002', 'Example Work 2', '2024-02-01')",
		"INSERT INTO work_credit (work_id, person_id, role, source) VALUES (1, 1, 'voice_actor', 'test')",
		"INSERT INTO work_credit (work_id, person_id, role, source) VALUES (2, 1, 'voice_actor', 'test')",
		"INSERT INTO file_source (id, code, display_name, source_type, enabled) VALUES (11, 'example_remote_a', 'Example Remote A', 'kikoeru_compatible', 1)",
		"INSERT INTO file_source_endpoint (file_source_id, base_url, api_url) VALUES (11, 'https://source.example.invalid', 'https://source.example.invalid/api')",
		`INSERT INTO app_setting (key, value_json) VALUES ('anonymous_access_enabled', 'true')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	server := NewServer(db, config.Config{})
	if err := server.LoadAccessPolicy(context.Background()); err != nil {
		t.Fatal(err)
	}
	source := remoteSourceForUse{ID: 11, Code: "example_remote_a", DisplayName: "Example Remote A", Enabled: true, SourceType: sourceTypeKikoeruCompatible}
	candidate := func(code, title, releaseDate string) voiceCatalogCandidate {
		return voiceCatalogCandidate{
			CanonicalCode: code, RemoteCode: code, RawJSON: "{}",
			Projection: remoteCatalogWorkProjection{
				RemoteID: code, RemoteCode: code, Title: title, ReleaseDate: releaseDate, Tags: []string{}, VoiceActors: []string{},
			},
		}
	}
	if _, err := server.persistVoiceCatalogSource(context.Background(), 1, 1, voiceCatalogSourceResult{
		Source: source, Complete: true,
		Candidates: []voiceCatalogCandidate{
			// One catalog row matches a known work and two are remote-only.
			candidate("RJ00000002", "Example Work 2", "2024-02-01"),
			candidate("RJ00000003", "Example Work 3", "2024-03-01"),
			candidate("RJ00000004", "Example Work 4", "2024-04-01"),
		},
	}, true); err != nil {
		t.Fatal(err)
	}

	type pageWork struct {
		PrimaryCode        string                   `json:"primaryCode"`
		Local              *bool                    `json:"local"`
		SourceID           *int64                   `json:"sourceId"`
		RemoteObservations []voiceRemoteObservation `json:"remoteObservations"`
	}
	type worksPage struct {
		Works    []pageWork `json:"works"`
		Page     int        `json:"page"`
		PageSize int        `json:"pageSize"`
		Total    int        `json:"total"`
	}
	get := func(target string, into any) {
		t.Helper()
		response := httptest.NewRecorder()
		server.Routes().ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, body = %s", target, response.Code, response.Body.String())
		}
		if err := json.Unmarshal(response.Body.Bytes(), into); err != nil {
			t.Fatal(err)
		}
	}
	codes := func(page worksPage) []string {
		result := []string{}
		for _, work := range page.Works {
			result = append(result, work.PrimaryCode)
		}
		return result
	}

	var first worksPage
	get("/api/voices/1/works?page=1&pageSize=3", &first)
	if first.Total != 4 || first.Page != 1 || first.PageSize != 3 {
		t.Fatalf("first page = %+v, want 4 works paged by 3", first)
	}
	if got := codes(first); !reflect.DeepEqual(got, []string{"RJ00000004", "RJ00000003", "RJ00000002"}) {
		t.Fatalf("first page codes = %v, want newest release first across known and remote works", got)
	}
	remoteOnly, merged := first.Works[0], first.Works[2]
	if remoteOnly.Local != nil || remoteOnly.SourceID == nil || *remoteOnly.SourceID != 11 || len(remoteOnly.RemoteObservations) != 1 {
		t.Fatalf("remote-only work = %+v, want the remote catalog shape with its observation", remoteOnly)
	}
	if merged.Local == nil || merged.SourceID != nil || len(merged.RemoteObservations) != 1 {
		t.Fatalf("merged work = %+v, want the known shape carrying the catalog observation", merged)
	}

	// A page past the end resolves to the last page.
	var last worksPage
	get("/api/voices/1/works?page=9&pageSize=3&sort=release", &last)
	if last.Page != 2 || !reflect.DeepEqual(codes(last), []string{"RJ00000001"}) {
		t.Fatalf("last page = %+v, want page 2 with the oldest work", last)
	}

	var filtered worksPage
	get("/api/voices/1/works?availability=remote&sort=code&direction=asc&q=example+work", &filtered)
	if filtered.Total != 3 || filtered.PageSize != 24 || !reflect.DeepEqual(codes(filtered), []string{"RJ00000002", "RJ00000003", "RJ00000004"}) {
		t.Fatalf("filtered page = %+v, want the three works the source holds in code order", filtered)
	}

	var matches struct {
		RemoteMatches []voiceRemoteSourceSet `json:"remoteMatches"`
	}
	get("/api/voices/1/remote-matches?includeWorks=false", &matches)
	if len(matches.RemoteMatches) != 1 || matches.RemoteMatches[0].Total != 3 || len(matches.RemoteMatches[0].Works) != 0 {
		t.Fatalf("remote matches = %+v, want the source state and total without its works", matches.RemoteMatches)
	}
}

func voiceCatalogRemoteWorkForTest(primaryCode, remoteCode, availability string) voiceRemoteWork {
	return voiceRemoteWork{
		SourceID: 7, SourceCode: "example_remote_a", SourceName: "Example Remote A",
		PrimaryCode: primaryCode, RemoteCode: remoteCode, Title: "Example Work",
		RemotePlayable: availability == "available", HasRemote: availability == "available", Availability: availability,
	}
}

func voiceObservationCodes(observations []voiceRemoteObservation) []string {
	codes := []string{}
	for _, observation := range observations {
		codes = append(codes, observation.RemoteCode)
	}
	return codes
}

func voiceWorkViewCodes(views []voiceWorkView) []string {
	codes := []string{}
	for _, view := range views {
		if view.Known != nil {
			codes = append(codes, view.Known.PrimaryCode)
		} else {
			codes = append(codes, view.Remote.PrimaryCode)
		}
	}
	return codes
}
