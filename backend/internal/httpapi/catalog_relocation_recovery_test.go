package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"testing"
)

// Renaming the series directory itself keeps the established series id and
// both episodes. The episode manifests prove each child is the same work even
// though the parent path, and therefore the derived series key, changed.
func TestCatalogSeriesDirectoryRenameKeepsOneSeries(t *testing.T) {
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/":                                  {looseDir("Creator")},
		"/Creator":                           {looseDir("Series: Example")},
		"/Creator/Series: Example":           {looseDir("Episode 1"), looseDir("Episode 2")},
		"/Creator/Series: Example/Episode 1": {looseAudioSized("01.mp3", 100), looseAudioSized("02.mp3", 200)},
		"/Creator/Series: Example/Episode 2": {looseAudioSized("01.mp3", 300), looseAudioSized("02.mp3", 400)},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("first scan = %#v", p)
	}
	var seriesID int64
	if err := f.server.db.QueryRow(`SELECT id FROM catalog_series`).Scan(&seriesID); err != nil {
		t.Fatal(err)
	}
	var oldOne, oldTwo int64
	if err := f.server.db.QueryRow(`SELECT id FROM source_catalog_entry WHERE root_path='/Creator/Series: Example/Episode 1' AND relocated_to_id IS NULL`).Scan(&oldOne); err != nil {
		t.Fatal(err)
	}
	if err := f.server.db.QueryRow(`SELECT id FROM source_catalog_entry WHERE root_path='/Creator/Series: Example/Episode 2' AND relocated_to_id IS NULL`).Scan(&oldTwo); err != nil {
		t.Fatal(err)
	}
	f.updates(map[string][]map[string]any{
		"/":                                  {looseDir("Creator")},
		"/Creator":                           {looseDir("Series: Renamed")},
		"/Creator/Series: Renamed":           {looseDir("Episode 1"), looseDir("Episode 2")},
		"/Creator/Series: Renamed/Episode 1": {looseAudioSized("01.mp3", 100), looseAudioSized("02.mp3", 200)},
		"/Creator/Series: Renamed/Episode 2": {looseAudioSized("01.mp3", 300), looseAudioSized("02.mp3", 400)},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"force":true}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("rescan = %#v", p)
	}
	var series int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM catalog_series`).Scan(&series); err != nil || series != 1 {
		t.Fatalf("series=%d, want 1: %v", series, err)
	}
	view := reviewRequest(t, f.handler, f.cookie, http.MethodGet, "/api/catalog-series/"+strconv.FormatInt(seriesID, 10), "", 200)
	episodes := view["episodes"].([]any)
	if len(episodes) != 2 {
		t.Fatalf("episodes=%d, want 2: %#v", len(episodes), episodes)
	}
	ids := map[int64]bool{}
	for _, raw := range episodes {
		ep := raw.(map[string]any)
		ids[int64(ep["catalogEntryId"].(float64))] = true
	}
	if !ids[oldOne] || !ids[oldTwo] {
		t.Fatalf("series lost the established ids %d/%d: %#v", oldOne, oldTwo, ids)
	}
	for _, id := range []int64{oldOne, oldTwo} {
		var live int
		if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_entry WHERE id=? AND file_source_id IS NOT NULL AND relocated_to_id IS NULL`, id).Scan(&live); err != nil || live != 1 {
			t.Fatalf("episode %d is not the live card: %v", id, err)
		}
	}
}

// A renamed episode takes its authored episode number, so the series order
// follows the source instead of the order the pages happened to arrive in.
func TestCatalogSeriesRenameUpdatesEpisodeNumberPosition(t *testing.T) {
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/":                          {looseDir("Series: Example")},
		"/Series: Example":           {looseDir("Episode 1"), looseDir("Episode 2")},
		"/Series: Example/Episode 1": {looseAudioSized("01.mp3", 100), looseAudioSized("02.mp3", 200)},
		"/Series: Example/Episode 2": {looseAudioSized("01.mp3", 300), looseAudioSized("02.mp3", 400)},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("first scan = %#v", p)
	}
	var oldID int64
	if err := f.server.db.QueryRow(`SELECT id FROM source_catalog_entry WHERE root_path='/Series: Example/Episode 2' AND relocated_to_id IS NULL`).Scan(&oldID); err != nil {
		t.Fatal(err)
	}
	f.updates(map[string][]map[string]any{
		"/":                          {looseDir("Series: Example")},
		"/Series: Example":           {looseDir("Episode 1"), looseDir("Episode 3")},
		"/Series: Example/Episode 1": {looseAudioSized("01.mp3", 100), looseAudioSized("02.mp3", 200)},
		"/Series: Example/Episode 3": {looseAudioSized("01.mp3", 300), looseAudioSized("02.mp3", 400)},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"force":true}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("rescan = %#v", p)
	}
	var position int
	if err := f.server.db.QueryRow(`SELECT position FROM catalog_series_member WHERE catalog_entry_id=?`, oldID).Scan(&position); err != nil {
		t.Fatal(err)
	}
	if position != 3 {
		t.Fatalf("renamed episode position=%d, want 3", position)
	}
}

// A root card that keeps its own audio must not list a creator folder that
// became its own split cards. The split card records the real directory in
// source_root, so the folder cleanup has to compare that path.
func TestRootCardDropsSplitChildFolder(t *testing.T) {
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/":        {looseAudio("root.mp3"), looseDir("Creator")},
		"/Creator": {looseAudio("Alpha.mp3"), looseAudio("Beta.mp3")},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	var rootID int64
	if err := f.server.db.QueryRow(`SELECT id FROM source_catalog_entry WHERE root_path='/' AND relocated_to_id IS NULL`).Scan(&rootID); err != nil {
		t.Fatal(err)
	}
	tracks := reviewRequest(t, f.handler, f.cookie, http.MethodGet, "/api/source-catalog/"+strconv.FormatInt(rootID, 10)+"/tracks", "", 200)["tracks"].([]any)
	for _, raw := range tracks {
		item := raw.(map[string]any)
		if item["type"] == "folder" && item["title"] == "Creator" {
			t.Fatalf("root card still lists a split child folder: %#v", tracks)
		}
	}
}

var _ = context.Background
