package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/yexca/kikoto/backend/internal/config"
)

func TestCatalogSeriesManualPreviewPreservesEpisodeIdentityAndPrivateReviews(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/fs/list" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": []any{map[string]any{"name": "audio.mp3", "size": 12}}, "total": 1}})
	}))
	defer upstream.Close()
	db := openMigratedTestDB(t)
	server := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	defer func() { _ = server.Shutdown(context.Background()) }()
	createTestAdministrator(t, server, "synthetic-user", "synthetic-password")
	createAccountManagedTestUser(t, server, "synthetic-listener", "synthetic-password")
	handler := server.Routes()
	admin := loginTestSession(t, handler, "synthetic-user", "synthetic-password")
	listener := loginTestSession(t, handler, "synthetic-listener", "synthetic-password")
	create, _ := json.Marshal(map[string]any{"displayName": "Example Remote A", "sourceType": "openlist", "enabled": true, "config": map[string]any{"openListRoots": []string{"/Creator/Example A", "/Creator/Example B"}, "openListIntervalMs": 100}, "endpoint": map[string]any{"apiUrl": upstream.URL, "baseUrl": upstream.URL}})
	source := reviewRequest(t, handler, admin, "POST", "/api/file-sources", string(create), 201)
	sid := strconv.Itoa(int(source["id"].(float64)))
	reviewRequest(t, handler, admin, "POST", "/api/remote-sources/"+sid+"/catalog/scan", "{}", 200)
	list := reviewRequest(t, handler, admin, "GET", "/api/remote-sources/"+sid+"/works", "", 200)
	if list["total"] != float64(2) {
		t.Fatalf("similar roots must remain independent: %#v", list)
	}
	items := list["works"].([]any)
	a := int(items[0].(map[string]any)["catalogEntryId"].(float64))
	b := int(items[1].(map[string]any)["catalogEntryId"].(float64))
	first := reviewRequest(t, handler, admin, "PATCH", "/api/source-catalog/"+strconv.Itoa(a)+"/user-state", `{"rating":4,"note":"Admin first note"}`, 200)
	second := reviewRequest(t, handler, admin, "PATCH", "/api/source-catalog/"+strconv.Itoa(b)+"/user-state", `{"rating":1,"note":"Admin second note"}`, 200)
	reviewRequest(t, handler, listener, "PATCH", "/api/source-catalog/"+strconv.Itoa(a)+"/user-state", `{"rating":2,"note":"Listener private note"}`, 200)
	body, _ := json.Marshal(map[string]any{"title": "Example series", "catalogEntryIds": []int{a, b}})
	reviewRequest(t, handler, listener, "POST", "/api/catalog-series/previews", string(body), 403)
	preview := reviewRequest(t, handler, admin, "POST", "/api/catalog-series/previews", string(body), 201)
	for table, want := range map[string]int{"catalog_series": 0, "catalog_series_member": 0, "work": 2, "user_work_state": 3, "media_item": 0, "media_file_location": 0} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Fatalf("preview mutated %s: got %d want %d", table, count, want)
		}
	}
	pid := strconv.Itoa(int(preview["previewId"].(float64)))
	if preview["status"] != "pending" {
		t.Fatalf("preview status: %#v", preview)
	}
	before := reviewRequest(t, handler, admin, "GET", "/api/remote-sources/"+sid+"/works", "", 200)
	if before["total"] != float64(2) {
		t.Fatalf("preview changed shared membership: %#v", before)
	}
	reviewRequest(t, handler, listener, "POST", "/api/catalog-series/previews/"+pid+"/confirm", "{}", 403)
	confirmed := reviewRequest(t, handler, admin, "POST", "/api/catalog-series/previews/"+pid+"/confirm", "{}", 200)
	seriesID := strconv.Itoa(int(confirmed["seriesId"].(float64)))
	shared := reviewRequest(t, handler, listener, "GET", "/api/catalog-series/"+seriesID, "", 200)
	if shared["title"] != "Example series" || len(shared["episodes"].([]any)) != 2 {
		t.Fatalf("shared series: %#v", shared)
	}
	for _, raw := range shared["episodes"].([]any) {
		member := raw.(map[string]any)
		if _, ok := member["personalNote"]; ok {
			t.Fatal("shared series includes personal fields")
		}
	}
	for _, expect := range []struct {
		id     int
		work   any
		rating float64
		note   string
	}{{a, first["workId"], 4, "Admin first note"}, {b, second["workId"], 1, "Admin second note"}} {
		got := reviewRequest(t, handler, admin, "GET", "/api/source-catalog/"+strconv.Itoa(expect.id), "", 200)
		if got["workId"] != expect.work || got["seriesId"] != confirmed["seriesId"] {
			t.Fatalf("member identity changed: %#v", got)
		}
		assertReview(t, got, expect.rating, expect.note)
	}
	assertReview(t, reviewRequest(t, handler, listener, "GET", "/api/source-catalog/"+strconv.Itoa(a), "", 200), float64(2), "Listener private note")
	grouped := reviewRequest(t, handler, listener, "GET", "/api/remote-sources/"+sid+"/works", "", 200)
	if grouped["total"] != float64(1) || grouped["works"].([]any)[0].(map[string]any)["seriesId"] != confirmed["seriesId"] {
		t.Fatalf("one series card required: %#v", grouped)
	}
	// Grouping an already grouped object is not a merge operation.
	reviewRequest(t, handler, admin, "POST", "/api/catalog-series/previews", string(body), 409)
}

func TestCatalogSeriesDiscoveryUsesExplicitEpisodesAndRetainsFormats(t *testing.T) {
	for _, fullIndex := range []bool{false, true} {
		t.Run(map[bool]string{false: "finite", true: "full_index"}[fullIndex], func(t *testing.T) {
			tree := map[string][]map[string]any{
				"/":                              {{"name": "Example series", "is_dir": true}, {"name": "Creator", "is_dir": true}, {"name": "Series: Another", "is_dir": true}},
				"/Creator":                       {{"name": "Similar A", "is_dir": true}, {"name": "Similar B", "is_dir": true}},
				"/Series: Another":               {{"name": "Opening", "is_dir": true}, {"name": "Ending", "is_dir": true}},
				"/Example series":                {{"name": "Episode 10", "is_dir": true}, {"name": "Episode 2", "is_dir": true}, {"name": "Episode 1", "is_dir": true}},
				"/Example series/Episode 1":      {{"name": "MP3", "is_dir": true}, {"name": "FLAC", "is_dir": true}},
				"/Example series/Episode 1/MP3":  {{"name": "01 first.mp3"}, {"name": "02 second.mp3"}},
				"/Example series/Episode 1/FLAC": {{"name": "01 first.flac"}, {"name": "02 second.flac"}},
				"/Example series/Episode 2":      {{"name": "track.mp3"}},
				"/Example series/Episode 10":     {{"name": "track.mp3"}},
				"/Creator/Similar A":             {{"name": "track.mp3"}},
				"/Creator/Similar B":             {{"name": "track.mp3"}},
				"/Series: Another/Opening":       {{"name": "track.mp3"}},
				"/Series: Another/Ending":        {{"name": "track.mp3"}},
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/fs/list" {
					t.Error("discovery downloaded or resolved media")
					http.NotFound(w, r)
					return
				}
				var in struct {
					Path string `json:"path"`
				}
				_ = json.NewDecoder(r.Body).Decode(&in)
				files, ok := tree[in.Path]
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
			input, _ := json.Marshal(map[string]any{"displayName": "Example Remote A", "sourceType": "openlist", "enabled": true, "config": map[string]any{"openListRoots": []string{"/Example series", "/Creator/Similar A", "/Creator/Similar B", "/Series: Another/Opening", "/Series: Another/Ending"}, "openListIntervalMs": 100}, "endpoint": map[string]any{"apiUrl": upstream.URL, "baseUrl": upstream.URL}})
			source := reviewRequest(t, handler, admin, "POST", "/api/file-sources", string(input), 201)
			sid := strconv.Itoa(int(source["id"].(float64)))
			var stableSeries any
			var stableEpisodes string
			for scan := 0; scan < 2; scan++ {
				if fullIndex {
					base := "/api/remote-sources/" + sid + "/catalog/index"
					reviewRequest(t, handler, admin, "POST", base, `{"pageSize":100,"pagesPerBatch":2,"intervalMs":100}`, 202)
					for batch := 0; batch < 40; batch++ {
						if err := server.runNextQueuedWorkflowJob(context.Background()); err != nil {
							t.Fatal(err)
						}
						progress := reviewRequest(t, handler, admin, "GET", base, "", 200)
						if progress["status"] == "complete" {
							break
						}
						if batch == 39 {
							t.Fatalf("series index did not complete: %#v", progress)
						}
					}
				} else {
					reviewRequest(t, handler, admin, "POST", "/api/remote-sources/"+sid+"/catalog/scan", "{}", 200)
				}
				list := reviewRequest(t, handler, admin, "GET", "/api/remote-sources/"+sid+"/works", "", 200)
				if list["total"] != float64(4) {
					t.Fatalf("expected two series and two independent works: %#v", list)
				}
				var seriesID any
				for _, raw := range list["works"].([]any) {
					card := raw.(map[string]any)
					if card["title"] == "Example series" {
						seriesID = card["seriesId"]
					}
					if card["workId"] != nil {
						t.Fatal("discovery registered work")
					}
				}
				if seriesID == nil {
					t.Fatalf("series card missing: %#v", list)
				}
				view := reviewRequest(t, handler, admin, "GET", "/api/catalog-series/"+strconv.Itoa(int(seriesID.(float64))), "", 200)
				episodes := view["episodes"].([]any)
				if len(episodes) != 3 {
					t.Fatalf("formats became episodes: %#v", episodes)
				}
				for i, title := range []string{"Episode 1", "Episode 2", "Episode 10"} {
					if episodes[i].(map[string]any)["title"] != title {
						t.Fatalf("episode order: %#v", episodes)
					}
				}
				if scan == 0 {
					stableSeries = seriesID
					stableEpisodes = mustJSON(episodes)
				} else if seriesID != stableSeries || stableEpisodes != mustJSON(episodes) {
					t.Fatal("rescan replaced stable series or episode identities")
				}
				id := strconv.Itoa(int(episodes[0].(map[string]any)["catalogEntryId"].(float64)))
				tracks := reviewRequest(t, handler, admin, "GET", "/api/source-catalog/"+id+"/tracks", "", 200)
				if len(tracks["tracks"].([]any)) != 2 {
					t.Fatalf("format trees lost: %#v", tracks)
				}
				first := tracks["tracks"].([]any)[0].(map[string]any)
				if len(first["children"].([]any)) != 2 {
					t.Fatalf("split tracks lost: %#v", first)
				}
				search := reviewRequest(t, handler, admin, "GET", "/api/remote-sources/"+sid+"/works?q=Episode%202", "", 200)
				if search["total"] != float64(1) {
					t.Fatalf("episode search lost series: %#v", search)
				}
			}
			if library := reviewRequest(t, handler, admin, "GET", "/api/works", "", 200); library["total"] != float64(0) {
				t.Fatal("index registered a work")
			}
		})
	}
}
