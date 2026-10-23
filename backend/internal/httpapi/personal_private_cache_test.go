package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/yexca/kikoto/backend/internal/config"
	"github.com/yexca/kikoto/backend/internal/kikoeru"
)

func privatePersonalGET(t *testing.T, app *httptest.Server, cookie *http.Cookie, path string) map[string]any {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, app.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response, err := app.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status=%d body=%s", path, response.StatusCode, data)
	}
	if got := response.Header.Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("GET %s: Cache-Control=%q, want %q", path, got, "private, no-store")
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestPersonalFieldGETResponsesUsePrivateNoStore(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/workInfo/RJ00000000" {
			http.Error(w, "unexpected upstream request", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(kikoeru.Work{ID: 1, SourceID: "RJ00000000", Title: "Example Remote Work"})
	}))
	defer remote.Close()

	db := openMigratedTestDB(t)
	s := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	defer func() { _ = s.Shutdown(context.Background()) }()
	createTestAdministrator(t, s, "synthetic-user", "synthetic-password")
	metadataReviewExec(t, db, `
		INSERT INTO work(id,primary_code,title) VALUES
			(10,'RJ00000000','Example Remote Work'),
			(11,'','Example Catalog A'),
			(12,'','Example Catalog B'),
			(20,'','Example Series');
		INSERT INTO file_source(id,code,display_name,source_type) VALUES
			(1,'example_remote','Example Remote','kikoeru_compatible'),
			(7,'example_openlist','Example OpenList','openlist');
		INSERT INTO file_source_endpoint(file_source_id,base_url,api_url) VALUES(1,'https://source.example.invalid','`+remote.URL+`');
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,work_id) VALUES
			(1,7,'/Catalog A','Example Catalog A',11),
			(2,7,'/Catalog B','Example Catalog B',12);
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES
			(1,'track.mp3','track.mp3','audio',100),
			(2,'track.mp3','track.mp3','audio',100);
		INSERT INTO catalog_series(id,title,work_id) VALUES(1,'Example Series',20);`)

	app := httptest.NewServer(s.Routes())
	defer app.Close()
	cookie := workReferenceLogin(t, app, "synthetic-user")

	for _, path := range []string{"/api/works/10/user-state", "/api/remote-sources/1/works/RJ00000000/user-state", "/api/source-catalog/1/user-state", "/api/source-catalog/2/user-state", "/api/catalog-series/1/user-state"} {
		workReferenceRequest(t, app, cookie, http.MethodPatch, path, `{"rating":4,"note":"Private note"}`, http.StatusOK, nil)
	}

	for _, path := range []string{"/api/works/10", "/api/remote-sources/1/works/RJ00000000", "/api/source-catalog/1", "/api/catalog-series/1"} {
		view := privatePersonalGET(t, app, cookie, path)
		if view["personalNote"] != "Private note" {
			t.Fatalf("GET %s did not return the signed-in user's personal state: %#v", path, view)
		}
	}

	var association map[string]any
	workReferenceRequest(t, app, cookie, http.MethodPost, "/api/shared-associations/previews", `{"left":{"kind":"catalog","id":1},"right":{"kind":"catalog","id":2}}`, http.StatusCreated, &association)
	associationPath := "/api/shared-associations/" + strconv.Itoa(int(association["id"].(float64)))
	workReferenceRequest(t, app, cookie, http.MethodPost, associationPath+"/start", mustJSON(map[string]any{"version": association["version"]}), http.StatusOK, &association)
	if association["myLeftReview"].(map[string]any)["note"] != "Private note" {
		t.Fatal("association fixture did not expose the caller's private review")
	}
	privatePersonalGET(t, app, cookie, associationPath)

	var split map[string]any
	workReferenceRequest(t, app, cookie, http.MethodPost, associationPath+"/split-preview", `{}`, http.StatusCreated, &split)
	splitPath := "/api/shared-splits/" + strconv.Itoa(int(split["id"].(float64)))
	if !strings.Contains(mustJSON(split), "Private note") {
		t.Fatal("split fixture did not expose the caller's private review")
	}
	privatePersonalGET(t, app, cookie, splitPath)
}
