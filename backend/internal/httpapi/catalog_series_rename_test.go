package httpapi

import (
	"net/http"
	"strconv"
	"testing"
)

// A real directory rename must keep one series member, point it at the
// successor entry, and keep the old catalog link resolvable with its series.
func TestCatalogSeriesRenameKeepsOneMemberAndSuccessorNavigation(t *testing.T) {
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/":                          {looseDir("Series: Example")},
		"/Series: Example":           {looseDir("Episode 1"), looseDir("Episode 2")},
		"/Series: Example/Episode 1": {looseAudio("one.mp3")},
		"/Series: Example/Episode 2": {looseAudioSized("01.mp3", 100), looseAudioSized("02.mp3", 200)},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("first scan = %#v", p)
	}
	var oldID, seriesID int64
	if err := f.server.db.QueryRow(`SELECT id FROM source_catalog_entry WHERE root_path='/Series: Example/Episode 2' AND relocated_to_id IS NULL`).Scan(&oldID); err != nil {
		t.Fatal(err)
	}
	if err := f.server.db.QueryRow(`SELECT id FROM catalog_series`).Scan(&seriesID); err != nil {
		t.Fatal(err)
	}
	f.updates(map[string][]map[string]any{
		"/":                                  {looseDir("Series: Example")},
		"/Series: Example":                   {looseDir("Episode 1"), looseDir("Renamed Episode 2")},
		"/Series: Example/Episode 1":         {looseAudio("one.mp3")},
		"/Series: Example/Renamed Episode 2": {looseAudioSized("01.mp3", 100), looseAudioSized("02.mp3", 200)},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"force":true}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("rescan = %#v", p)
	}

	view := reviewRequest(t, f.handler, f.cookie, http.MethodGet, "/api/catalog-series/"+strconv.FormatInt(seriesID, 10), "", 200)
	episodes := view["episodes"].([]any)
	if len(episodes) != 2 {
		t.Fatalf("series members=%d, want 2: %#v", len(episodes), episodes)
	}
	var second map[string]any
	for _, raw := range episodes {
		ep := raw.(map[string]any)
		if ep["title"] == "Renamed Episode 2" {
			second = ep
		}
	}
	if second == nil {
		t.Fatalf("renamed member missing: %#v", episodes)
	}
	successorID := int64(second["catalogEntryId"].(float64))
	if successorID != oldID {
		t.Fatalf("series episode moved from the established id %d to %d", oldID, successorID)
	}
	var live int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_entry WHERE id=? AND file_source_id IS NOT NULL AND relocated_to_id IS NULL`, successorID).Scan(&live); err != nil || live != 1 {
		t.Fatalf("series episode %d is not the live card: %v", successorID, err)
	}
	var aliases int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_entry WHERE relocated_to_id=?`, successorID).Scan(&aliases); err != nil || aliases != 1 {
		t.Fatalf("renamed location has %d aliases: %v", aliases, err)
	}
	var liveAtRoot int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_entry WHERE root_path='/Series: Example/Renamed Episode 2' AND file_source_id IS NOT NULL AND relocated_to_id IS NULL`).Scan(&liveAtRoot); err != nil || liveAtRoot != 1 {
		t.Fatalf("renamed root has %d live cards: %v", liveAtRoot, err)
	}
	var members int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM catalog_series_member WHERE series_id=?`, seriesID).Scan(&members); err != nil || members != 2 {
		t.Fatalf("members=%d, want 2: %v", members, err)
	}

	// The old link still resolves, and the resolved card keeps its series
	// navigation instead of returning seriesId=null.
	oldDetail := reviewRequest(t, f.handler, f.cookie, http.MethodGet, "/api/source-catalog/"+strconv.FormatInt(oldID, 10), "", 200)
	if oldDetail["seriesId"] == nil {
		t.Fatalf("old link lost its series: %#v", oldDetail)
	}
	if int64(oldDetail["catalogEntryId"].(float64)) != successorID {
		t.Fatalf("old link resolved to %v, want %d", oldDetail["catalogEntryId"], successorID)
	}
}

// Deleting a source removes its members from the series view, but the cards
// and personal records remain addressable as missing.
func TestCatalogSeriesOmitsEntriesWithoutASource(t *testing.T) {
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/":                          {looseDir("Series: Example")},
		"/Series: Example":           {looseDir("Episode 1"), looseDir("Episode 2")},
		"/Series: Example/Episode 1": {looseAudio("one.mp3")},
		"/Series: Example/Episode 2": {looseAudioSized("01.mp3", 100), looseAudioSized("02.mp3", 200)},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	var entryID, seriesID int64
	if err := f.server.db.QueryRow(`SELECT id FROM source_catalog_entry WHERE root_path='/Series: Example/Episode 2' AND relocated_to_id IS NULL`).Scan(&entryID); err != nil {
		t.Fatal(err)
	}
	if err := f.server.db.QueryRow(`SELECT id FROM catalog_series`).Scan(&seriesID); err != nil {
		t.Fatal(err)
	}
	reviewRequest(t, f.handler, f.cookie, http.MethodPatch, "/api/source-catalog/"+strconv.FormatInt(entryID, 10)+"/user-state", `{"rating":5,"note":"kept"}`, 200)

	if _, err := f.server.db.Exec(`DELETE FROM file_source WHERE id=(SELECT file_source_id FROM source_catalog_entry WHERE id=?)`, entryID); err != nil {
		t.Fatal(err)
	}
	view := reviewRequest(t, f.handler, f.cookie, http.MethodGet, "/api/catalog-series/"+strconv.FormatInt(seriesID, 10), "", 200)
	if episodes := view["episodes"].([]any); len(episodes) != 0 {
		t.Fatalf("series kept source-less members: %#v", episodes)
	}
	detail := reviewRequest(t, f.handler, f.cookie, http.MethodGet, "/api/source-catalog/"+strconv.FormatInt(entryID, 10), "", 200)
	if detail["status"] != "missing" {
		t.Fatalf("retained card status=%v, want missing", detail["status"])
	}
	assertReview(t, detail, float64(5), "kept")
}
