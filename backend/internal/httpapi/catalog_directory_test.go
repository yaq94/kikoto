package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestCatalogDirectoriesUseWorkIdentity(t *testing.T) {
	f := newRoleBoundaryFixture(t)
	if _, err := f.server.db.Exec(`
	 INSERT INTO work(id,primary_code,title) VALUES(1,'RJ00000000','Example Work'),(2,'RJ00000001','Example Edition'),(3,'','Example Numberless');
	 INSERT INTO logical_work(id,canonical_work_id,canonical_code) VALUES(1,1,'RJ00000000');
	 INSERT INTO work_edition(work_id,logical_work_id,primary_code,is_canonical) VALUES(1,1,'RJ00000000',1),(2,1,'RJ00000001',0);
	 INSERT INTO work_code_alias(logical_work_id,provider_id,primary_code) VALUES(1,2,'RJ00000004');
	 INSERT INTO file_source(id,code,display_name,source_type) VALUES(1,'example_catalog_a','Example Catalog A','openlist'),(2,'example_catalog_b','Example Catalog B','openlist');
	 INSERT INTO source_catalog_entry(id,file_source_id,root_path,primary_code,title,work_id) VALUES
	 (1,1,'/Example/RJ00000000','RJ00000000','Example Work',NULL),
	 (2,1,'/Example/RJ00000001','RJ00000001','Example Edition',2),
	 (3,1,'/Example/Numberless','','Example Numberless',3),
	 (4,2,'/Example/Numberless','','Example Numberless',3),
	 (5,2,'/Example/Unregistered','','Example Unregistered',NULL),
	 (6,2,'/Example/Alias','RJ00000004','Example Alias',NULL);
		 INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES
			(1,'track.mp3','Track','audio',100),(2,'track.mp3','Track','audio',100),
			(3,'real.wav','Display track','audio',100),(4,'real.wav','Display track','audio',100),(5,'real.wav','Display track','audio',100),
			(6,'track.mp3','Track','audio',100);
	`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		ref string
		ids []int
	}{{"1", []int{1, 6}}, {"RJ00000000", []int{1, 6}}, {"RJ00000004", []int{1, 6}}, {"2", []int{2}}, {"3", []int{3, 4}}} {
		got := reviewRequest(t, f.handler, f.cookies["user"], http.MethodGet, "/api/works/"+tc.ref+"/catalog-directories", "", http.StatusOK)["directories"].([]any)
		if len(got) != len(tc.ids) {
			t.Fatalf("%s directories = %#v", tc.ref, got)
		}
		for i, id := range tc.ids {
			if got[i].(map[string]any)["catalogEntryId"] != float64(id) || got[i].(map[string]any)["directoryType"] != "catalog" {
				t.Fatalf("%s directory = %#v", tc.ref, got[i])
			}
		}
	}
	if result := reviewRequest(t, f.handler, f.cookies["user"], http.MethodGet, "/api/source-catalog/5", "", http.StatusOK); result["workId"] != nil {
		t.Fatalf("read registered a work: %#v", result)
	}
	reviewRequest(t, f.handler, f.cookies["user"], http.MethodPost, "/api/content-translations", `{"catalogEntryId":5,"field":"directory","path":"real.wav","text":"Display track","language":"origin"}`, http.StatusOK)
	if result := reviewRequest(t, f.handler, f.cookies["user"], http.MethodGet, "/api/works", "", http.StatusOK); result["total"] != float64(2) {
		t.Fatalf("read changed library: %#v", result)
	}
	if result := reviewRequest(t, f.handler, f.cookies["user"], http.MethodGet, "/api/works/3/media", "", http.StatusOK); len(result["mediaItems"].([]any)) != 0 {
		t.Fatal("browsing created media")
	}
	first := reviewRequest(t, f.handler, f.cookies["user"], http.MethodPost, "/api/source-catalog/3/playback", `{"path":"real.wav"}`, http.StatusOK)
	again := reviewRequest(t, f.handler, f.cookies["user"], http.MethodPost, "/api/source-catalog/3/playback", `{"path":"real.wav"}`, http.StatusOK)
	if first["mediaItemId"] != again["mediaItemId"] || first["locationId"] != again["locationId"] {
		t.Fatalf("playback identity changed: %#v / %#v", first, again)
	}
	second := reviewRequest(t, f.handler, f.cookies["user"], http.MethodPost, "/api/source-catalog/4/playback", `{"path":"real.wav"}`, http.StatusOK)
	if first["mediaItemId"] == second["mediaItemId"] {
		t.Fatal("same-named files from different catalogs share a timeline")
	}
	reviewRequest(t, f.handler, f.cookies["user"], http.MethodPatch, fmt.Sprintf("/api/media-items/%.0f/progress", first["mediaItemId"]), fmt.Sprintf(`{"locationId":%.0f,"positionSeconds":42,"durationSeconds":180}`, first["locationId"]), http.StatusOK)
	cursor := reviewRequest(t, f.handler, f.cookies["user"], http.MethodGet, "/api/works/3/playback-cursor", "", http.StatusOK)["cursor"].(map[string]any)
	if cursor["mediaItemId"] != first["mediaItemId"] || cursor["positionSeconds"] != float64(42) {
		t.Fatalf("catalog cursor = %#v", cursor)
	}
	reviewRequest(t, f.handler, f.cookies["user"], http.MethodPost, "/api/source-catalog/3/playback", `{"path":"Display track"}`, http.StatusNotFound)
	reviewRequest(t, f.handler, f.cookies["user"], http.MethodPost, "/api/source-catalog/5/playback", `{"path":"real.wav"}`, http.StatusConflict)
}

func TestCatalogDirectoryRefreshIsBoundedAndKeepsSnapshotOnFailure(t *testing.T) {
	var failing atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&input)
		if r.URL.Path != "/api/fs/list" || input.Path != "/Example/One" {
			t.Errorf("unexpected scope: %s %s", r.URL.Path, input.Path)
			http.Error(w, "wrong scope", 500)
			return
		}
		if failing.Load() {
			http.Error(w, "synthetic failure", 500)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"total": 1, "content": []map[string]any{{"name": "updated.wav", "size": 100, "type": 3}}}})
	}))
	defer upstream.Close()
	f := newRoleBoundaryFixture(t)
	if _, err := f.server.db.Exec(`INSERT INTO file_source(id,code,display_name,source_type) VALUES(1,'example_catalog','Example Catalog','openlist');
	 INSERT INTO source_catalog_entry(id,file_source_id,root_path,title) VALUES(1,1,'/Example/One','Example One'),(2,1,'/Example/Other','Example Other');
	 INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES(1,'old.wav','Old track','audio',100);`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.db.Exec("INSERT INTO file_source_endpoint(file_source_id,api_url,base_url) VALUES(1,?,?)", upstream.URL, upstream.URL); err != nil {
		t.Fatal(err)
	}
	reviewRequest(t, f.handler, f.cookies["user"], http.MethodPost, "/api/source-catalog/1/refresh", `{}`, http.StatusForbidden)
	reviewRequest(t, f.handler, f.cookies["admin"], http.MethodPost, "/api/source-catalog/1/refresh", `{}`, http.StatusOK)
	read := func() map[string]any {
		return reviewRequest(t, f.handler, f.cookies["user"], http.MethodGet, "/api/source-catalog/1/tracks", "", http.StatusOK)["tracks"].([]any)[0].(map[string]any)
	}
	if read()["path"] != "updated.wav" {
		t.Fatal("directory refresh did not update snapshot")
	}
	failing.Store(true)
	reviewRequest(t, f.handler, f.cookies["admin"], http.MethodPost, "/api/source-catalog/1/refresh", `{}`, http.StatusBadGateway)
	if read()["path"] != "updated.wav" {
		t.Fatal("failed refresh discarded snapshot")
	}
}
