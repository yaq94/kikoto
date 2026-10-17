package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/yexca/kikoto/backend/internal/config"
)

// A finite scan that published a series root before the episode markers were
// understood must split it on the next scan. Keeping the root card would hide
// every episode behind one work and leave the series ungrouped.
func TestCatalogSeriesFiniteRescanSplitsPublishedRoot(t *testing.T) {
	var episodes atomic.Bool
	tree := func() map[string][]map[string]any {
		root := []map[string]any{{"name": "track.mp3", "size": 100}}
		if episodes.Load() {
			root = []map[string]any{
				{"name": "Episode 1", "is_dir": true},
				{"name": "Episode 2", "is_dir": true},
			}
		}
		result := map[string][]map[string]any{"/Example series": root}
		if episodes.Load() {
			result["/Example series/Episode 1"] = []map[string]any{{"name": "MP3", "is_dir": true}}
			result["/Example series/Episode 1/MP3"] = []map[string]any{{"name": "01 first.mp3", "size": 100}}
			result["/Example series/Episode 2"] = []map[string]any{{"name": "track.mp3", "size": 100}}
		}
		return result
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/fs/list" {
			t.Error("scan downloaded or resolved media")
			http.NotFound(w, r)
			return
		}
		var in struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		files, ok := tree()[in.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": files, "total": len(files)}})
	}))
	defer upstream.Close()

	db := openMigratedTestDB(t)
	server := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	defer func() { _ = server.Shutdown(context.Background()) }()
	createTestAdministrator(t, server, "synthetic-user", "synthetic-password")
	handler := server.Routes()
	admin := loginTestSession(t, handler, "synthetic-user", "synthetic-password")
	input, _ := json.Marshal(map[string]any{
		"displayName": "Example Remote A", "sourceType": "openlist", "enabled": true,
		"config":   map[string]any{"openListRoots": []string{"/Example series"}, "openListIntervalMs": 100},
		"endpoint": map[string]any{"apiUrl": upstream.URL, "baseUrl": upstream.URL},
	})
	source := reviewRequest(t, handler, admin, http.MethodPost, "/api/file-sources", string(input), 201)
	sid := strconv.Itoa(int(source["id"].(float64)))

	reviewRequest(t, handler, admin, http.MethodPost, "/api/remote-sources/"+sid+"/catalog/scan", "{}", 200)
	first := reviewRequest(t, handler, admin, http.MethodGet, "/api/remote-sources/"+sid+"/works", "", 200)
	if first["total"] != float64(1) {
		t.Fatalf("first scan cards=%v, want the unsplit root", first["total"])
	}

	episodes.Store(true)
	reviewRequest(t, handler, admin, http.MethodPost, "/api/remote-sources/"+sid+"/catalog/scan", "{}", 200)
	second := reviewRequest(t, handler, admin, http.MethodGet, "/api/remote-sources/"+sid+"/works", "", 200)
	var seriesID any
	for _, raw := range second["works"].([]any) {
		if card := raw.(map[string]any); card["seriesId"] != nil {
			seriesID = card["seriesId"]
		}
	}
	if seriesID == nil {
		t.Fatalf("rescan did not group the episodes: %#v", second)
	}
	view := reviewRequest(t, handler, admin, http.MethodGet, "/api/catalog-series/"+strconv.Itoa(int(seriesID.(float64))), "", 200)
	if len(view["episodes"].([]any)) != 2 {
		t.Fatalf("episodes=%#v, want two", view["episodes"])
	}
}

// Once a root has a registered work, splitting it would move that identity and
// any personal record. The rescan leaves the card alone until a reviewer
// decides, even though the episode evidence is now present.
func TestCatalogSeriesFiniteRescanKeepsPinnedRoot(t *testing.T) {
	var episodes atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/fs/list" {
			http.NotFound(w, r)
			return
		}
		var in struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		items := []map[string]any{}
		switch in.Path {
		case "/Example series":
			if episodes.Load() {
				items = []map[string]any{{"name": "Episode 1", "is_dir": true}, {"name": "Episode 2", "is_dir": true}}
			} else {
				items = []map[string]any{{"name": "track.mp3", "size": 100}}
			}
		case "/Example series/Episode 1", "/Example series/Episode 2":
			items = []map[string]any{{"name": "track.mp3", "size": 100}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": len(items)}})
	}))
	defer upstream.Close()
	db := openMigratedTestDB(t)
	server := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	defer func() { _ = server.Shutdown(context.Background()) }()
	createTestAdministrator(t, server, "synthetic-user", "synthetic-password")
	handler := server.Routes()
	admin := loginTestSession(t, handler, "synthetic-user", "synthetic-password")
	input, _ := json.Marshal(map[string]any{
		"displayName": "Example Remote A", "sourceType": "openlist", "enabled": true,
		"config":   map[string]any{"openListRoots": []string{"/Example series"}, "openListIntervalMs": 100},
		"endpoint": map[string]any{"apiUrl": upstream.URL, "baseUrl": upstream.URL},
	})
	source := reviewRequest(t, handler, admin, http.MethodPost, "/api/file-sources", string(input), 201)
	sid := strconv.Itoa(int(source["id"].(float64)))
	// Publish the root as one card first, then attach a review to it.
	reviewRequest(t, handler, admin, http.MethodPost, "/api/remote-sources/"+sid+"/catalog/scan", "{}", 200)
	first := reviewRequest(t, handler, admin, http.MethodGet, "/api/remote-sources/"+sid+"/works", "", 200)
	rootID := int(first["works"].([]any)[0].(map[string]any)["catalogEntryId"].(float64))
	reviewRequest(t, handler, admin, http.MethodPatch, "/api/source-catalog/"+strconv.Itoa(rootID)+"/user-state",
		`{"rating":4,"note":"Example retained note"}`, 200)

	episodes.Store(true)
	reviewRequest(t, handler, admin, http.MethodPost, "/api/remote-sources/"+sid+"/catalog/scan", "{}", 200)
	second := reviewRequest(t, handler, admin, http.MethodGet, "/api/remote-sources/"+sid+"/works", "", 200)
	if second["total"] != float64(1) {
		t.Fatalf("pinned root was split: %#v", second)
	}
	if card := second["works"].([]any)[0].(map[string]any); int(card["catalogEntryId"].(float64)) != rootID {
		t.Fatalf("pinned root identity moved: %#v", card)
	}
	assertReview(t, reviewRequest(t, handler, admin, http.MethodGet, "/api/source-catalog/"+strconv.Itoa(rootID), "", 200), float64(4), "Example retained note")
}
