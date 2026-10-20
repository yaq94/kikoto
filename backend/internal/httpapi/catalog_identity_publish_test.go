package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
)

// Zero-padded T001/T002 names are work identifiers, not track numbers. A real
// scan must publish two cards instead of folding them into one creator card.
func TestCatalogScanPublishesZeroPaddedWorksApart(t *testing.T) {
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/":               {looseDir("T001 Crossing"), looseDir("T002 Departure")},
		"/T001 Crossing":  {looseAudio("track.mp3")},
		"/T002 Departure": {looseAudio("track.mp3")},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	cards := f.cards(t)
	if cards["T001 Crossing"] == nil || cards["T002 Departure"] == nil || len(cards) != 2 {
		t.Fatalf("zero-padded works collapsed: %#v", cards)
	}
}

// 第1話 and 第1集 use different counters. They are separate episodes and must
// not be merged by a generic episode marker.
func TestCatalogScanKeepsDifferentEpisodeCountersApart(t *testing.T) {
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/":                    {looseDir("Series: Example")},
		"/Series: Example":     {looseDir("第1話"), looseDir("第1集")},
		"/Series: Example/第1話": {looseAudio("a.mp3")},
		"/Series: Example/第1集": {looseAudio("b.mp3")},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	var seriesID int64
	if err := f.server.db.QueryRow(`SELECT id FROM catalog_series`).Scan(&seriesID); err != nil {
		t.Fatal(err)
	}
	view := reviewRequest(t, f.handler, f.cookie, http.MethodGet, "/api/catalog-series/"+strconv.FormatInt(seriesID, 10), "", 200)
	episodes := view["episodes"].([]any)
	if len(episodes) != 2 {
		t.Fatalf("different counters collapsed: %#v", episodes)
	}
}

// A 410 answer is a confirmed disappearance. The card is retained as missing,
// not treated as an unreadable directory or a completed empty one.
func TestCatalogScanTreatsGoneDirectoryAsMissing(t *testing.T) {
	var gone atomic.Bool
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&input)
		if input.Path == "/Work" && gone.Load() {
			http.Error(w, "gone", http.StatusGone)
			return
		}
		items := []map[string]any{}
		switch input.Path {
		case "/":
			items = []map[string]any{{"name": "Work", "is_dir": true}}
		case "/Work":
			items = []map[string]any{{"name": "track.mp3", "size": 100}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": len(items)}})
	}))
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("first scan = %#v", p)
	}
	var id int64
	if err := f.server.db.QueryRow(`SELECT id FROM source_catalog_entry WHERE root_path='/Work' AND relocated_to_id IS NULL`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	gone.Store(true)
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"force":true}`, 202)
	p := f.drain(t)
	if p["status"] != "complete" {
		t.Fatalf("rescan = %#v", p)
	}
	// A confirmed disappearance is complete coverage, not an unconfirmed gap.
	if p["completedDirectories"] != p["directories"] || p["unconfirmedEmptyDirectories"] != float64(0) {
		t.Fatalf("gone directory was not counted as completed: %#v", p)
	}
	var status string
	if err := f.server.db.QueryRow(`SELECT availability_status FROM source_catalog_entry WHERE id=?`, id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "missing" {
		t.Fatalf("410 status=%q, want missing", status)
	}
}
