package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/yexca/kikoto/backend/internal/buildinfo"
	"github.com/yexca/kikoto/backend/internal/config"
	"github.com/yexca/kikoto/backend/internal/storage"
	"github.com/yexca/kikoto/backend/migrations"
)

func TestWorkReferencePersonalStateUsesCodeCompatibility(t *testing.T) {
	db := openMigratedTestDB(t)
	metadataReviewExec(t, db, `INSERT INTO work(id,primary_code,title) VALUES
		(1,'RJ00000000','Example Work'),(2,'RJ00000001','Example English edition');
		INSERT INTO logical_work(id,canonical_work_id,canonical_code) VALUES(1,1,'RJ00000000');
		INSERT INTO work_edition(work_id,logical_work_id,provider_id,primary_code,base_code,metadata_language,is_canonical) VALUES
		(1,1,2,'RJ00000000','','JPN',1),(2,1,2,'RJ00000001','RJ00000000','ENG',0);
		INSERT INTO work_code_alias(logical_work_id,provider_id,primary_code) VALUES(1,2,'RJ00000004')`)
	server := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user"})
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	createTestAdministrator(t, server, "synthetic-user", "synthetic-password")
	httpServer := httptest.NewServer(server.Routes())
	t.Cleanup(httpServer.Close)
	cookie := workReferenceLogin(t, httpServer, "synthetic-user")
	for _, ref := range []string{"1", "rj00000000", "RJ00000004", "RJ00000001"} {
		var updated workUserStateResponse
		workReferenceRequest(t, httpServer, cookie, http.MethodPatch, "/api/works/"+ref+"/user-state", `{"listeningStatus":"finished"}`, http.StatusOK, &updated)
		if updated.WorkID != 1 || updated.ListeningStatus != "finished" {
			t.Fatalf("write %s changed identity or personal state: %+v", ref, updated)
		}
		var detail workDetail
		workReferenceRequest(t, httpServer, cookie, http.MethodGet, "/api/works/"+ref+"?includeMedia=false", "", http.StatusOK, &detail)
		if detail.ID != 1 || detail.ListeningStatus != "finished" {
			t.Fatalf("read %s changed identity or personal state: %+v", ref, detail)
		}
	}
	var updated workUserStateResponse
	workReferenceRequest(t, httpServer, cookie, http.MethodPatch, "/api/works/2/user-state", `{"listeningStatus":"listening"}`, http.StatusOK, &updated)
	if updated.WorkID != 2 {
		t.Fatalf("numeric edition write resolved to %d, want 2", updated.WorkID)
	}
	for _, item := range []struct {
		ref, status string
		id          int64
	}{{"2", "listening", 2}, {"RJ00000001", "finished", 1}} {
		var detail workDetail
		workReferenceRequest(t, httpServer, cookie, http.MethodGet, "/api/works/"+item.ref+"?includeMedia=false", "", http.StatusOK, &detail)
		if detail.ID != item.id || detail.ListeningStatus != item.status {
			t.Fatalf("edition read %s: id=%d status=%s", item.ref, detail.ID, detail.ListeningStatus)
		}
	}
	for _, item := range []struct {
		ref  string
		code int
	}{{"not-a-code", 400}, {"RJ00000099", 404}, {"999", 404}} {
		workReferenceRequest(t, httpServer, cookie, http.MethodPatch, "/api/works/"+item.ref+"/user-state", `{"listeningStatus":"finished"}`, item.code, nil)
	}
	workReferenceRequest(t, httpServer, nil, http.MethodPatch, "/api/works/RJ00000004/user-state", `{"listeningStatus":"finished"}`, http.StatusUnauthorized, nil)
}

// Both the v0.8.3 baseline and the preceding numbered schema use immutable
// released files. Every boot closes HTTP, application work and SQLite before
// opening the same database and applying the current packaged chain.
func TestWorkReferencesPreserveReleasedLibraryAcrossRestart(t *testing.T) {
	for _, version := range []string{"067", "068"} {
		t.Run(version, func(t *testing.T) {
			testWorkReferencesAcrossRestart(t, version)
		})
	}
}

func testWorkReferencesAcrossRestart(t *testing.T, version string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "library.db")
	db, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	catalog := fstest.MapFS{}
	entries, err := fs.ReadDir(migrations.Files, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".sql") || len(name) < 4 || name[:3] > version {
			continue
		}
		data, err := fs.ReadFile(migrations.Files, name)
		if err != nil {
			t.Fatal(err)
		}
		catalog[name] = &fstest.MapFile{Data: data}
	}
	if version == "068" {
		const baseline = "baseline/068_v0.8.3.sql"
		data, err := fs.ReadFile(migrations.Files, baseline)
		if err != nil {
			t.Fatal(err)
		}
		catalog[baseline] = &fstest.MapFile{Data: data}
	}
	if err := storage.MigrateFS(db, catalog, "v0.8.3"); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user"}
	old := NewServer(db, cfg)
	createTestAdministrator(t, old, "synthetic-user", "synthetic-password")
	createAccountManagedTestUser(t, old, "synthetic-listener", "synthetic-password")
	metadataReviewExec(t, db, `INSERT INTO app_setting(key,value_json) VALUES('library_mode','"standard"');
		INSERT INTO work(id,primary_code,title,rating_average) VALUES
		(1,'RJ00000000','Example Work',4.5),(2,'RJ00000001','Example English edition',4.0),
		(3,'RJ00000002','Example legacy edition',3.5),(4,'RJ00000003','Example legacy origin',3.0);
		INSERT INTO logical_work(id,canonical_work_id,canonical_code) VALUES(1,1,'RJ00000000');
		INSERT INTO work_edition(work_id,logical_work_id,provider_id,primary_code,base_code,metadata_language,is_canonical) VALUES
		(1,1,2,'RJ00000000','','JPN',1),(2,1,2,'RJ00000001','RJ00000000','ENG',0);
		INSERT INTO work_code_alias(logical_work_id,provider_id,primary_code) VALUES(1,2,'RJ00000004');
		INSERT INTO metadata_snapshot(work_id,provider_id,external_id,snapshot_json) VALUES
		(3,2,'example','{"workno":"RJ00000002","base_code":"RJ00000003"}');
		INSERT INTO user_work_state(user_id,work_id,listening_status,rating,note)
		SELECT id,1,'listening',0,'Example original note' FROM user_account WHERE username='synthetic-user';
		INSERT INTO user_work_state(user_id,work_id,listening_status,rating,note)
		SELECT id,2,'paused',5,'Example edition note' FROM user_account WHERE username='synthetic-user';
		INSERT INTO user_work_state(user_id,work_id,listening_status,rating,note)
		SELECT id,4,'finished',2,'Example legacy note' FROM user_account WHERE username='synthetic-user';
		INSERT INTO user_work_state(user_id,work_id,listening_status,rating,note)
		SELECT id,1,'want_to_listen',3,'Example other note' FROM user_account WHERE username='synthetic-listener'`)
	if err := old.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	var cookie, otherCookie *http.Cookie
	note := "Example original note"
	for boot := range 3 {
		db, err := storage.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := storage.MigrateFS(db, completeMigrationFS(t), buildinfo.Version); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		server := NewServer(db, cfg)
		httpServer := httptest.NewServer(server.Routes())
		stop := func() {
			httpServer.Close()
			if err := server.Shutdown(context.Background()); err != nil {
				t.Error(err)
			}
			if err := db.Close(); err != nil {
				t.Error(err)
			}
		}
		t.Cleanup(stop)
		if cookie == nil {
			cookie = workReferenceLogin(t, httpServer, "synthetic-user")
			otherCookie = workReferenceLogin(t, httpServer, "synthetic-listener")
		}
		for _, ref := range []string{"1", "rj00000000", "RJ00000004", "RJ00000001"} {
			var detail workDetail
			workReferenceRequest(t, httpServer, cookie, http.MethodGet, "/api/works/"+ref+"?includeMedia=false", "", http.StatusOK, &detail)
			if detail.ID != 1 || detail.PrimaryCode != "RJ00000000" || detail.PersonalRating == nil || *detail.PersonalRating != 0 || detail.PersonalNote != note || detail.ListeningStatus != "listening" || detail.Rating == nil || *detail.Rating != 4.5 {
				t.Fatalf("boot %d reference %s lost original state: %+v", boot, ref, detail)
			}
			if len(detail.Translations) != 2 || detail.Translations[0].WorkID == nil || detail.Translations[1].WorkID == nil {
				t.Fatalf("boot %d lost edition relationship: %+v", boot, detail.Translations)
			}
			for _, edition := range detail.Translations {
				canonical := *edition.WorkID == 1 && edition.PrimaryCode == "RJ00000000" && edition.Origin
				translated := *edition.WorkID == 2 && edition.PrimaryCode == "RJ00000001" && !edition.Origin
				if !canonical && !translated {
					t.Fatalf("boot %d changed edition identity: %+v", boot, edition)
				}
			}
		}
		for _, item := range []struct {
			ref, code, note, status string
			id, rating              int64
		}{{"2", "RJ00000001", "Example edition note", "paused", 2, 5}, {"RJ00000002", "RJ00000003", "Example legacy note", "finished", 4, 2}, {"4", "RJ00000003", "Example legacy note", "finished", 4, 2}} {
			var detail workDetail
			workReferenceRequest(t, httpServer, cookie, http.MethodGet, "/api/works/"+item.ref+"?includeMedia=false", "", http.StatusOK, &detail)
			if detail.ID != item.id || detail.PrimaryCode != item.code || detail.PersonalRating == nil || *detail.PersonalRating != item.rating || detail.PersonalNote != item.note || detail.ListeningStatus != item.status {
				t.Fatalf("boot %d reference %s lost edition or legacy state: %+v", boot, item.ref, detail)
			}
		}
		var other workDetail
		workReferenceRequest(t, httpServer, otherCookie, http.MethodGet, "/api/works/RJ00000004?includeMedia=false", "", http.StatusOK, &other)
		if other.PersonalRating == nil || *other.PersonalRating != 3 || other.PersonalNote != "Example other note" {
			t.Fatalf("boot %d changed another user's review: %+v", boot, other)
		}
		var listed struct {
			Total int `json:"total"`
		}
		workReferenceRequest(t, httpServer, cookie, http.MethodGet, "/api/works?sort=added", "", http.StatusOK, &listed)
		if listed.Total != 3 {
			t.Fatalf("boot %d library total=%d, want 3 (canonical plus two legacy works)", boot, listed.Total)
		}
		if boot == 0 {
			var updated workUserStateResponse
			workReferenceRequest(t, httpServer, cookie, http.MethodPatch, "/api/works/RJ00000004/user-state", `{"note":"Example updated note"}`, http.StatusOK, &updated)
			if updated.WorkID != 1 || updated.PersonalRating == nil || *updated.PersonalRating != 0 || updated.PersonalNote != "Example updated note" {
				t.Fatalf("alias update changed omitted fields: %+v", updated)
			}
			note = "Example updated note"
		}
		stop()
	}
}

func workReferenceLogin(t *testing.T, server *httptest.Server, username string) *http.Cookie {
	t.Helper()
	response, err := server.Client().Post(server.URL+"/api/auth/login", "application/json", strings.NewReader(`{"username":"`+username+`","password":"synthetic-password"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d", response.StatusCode)
	}
	for _, cookie := range response.Cookies() {
		if cookie.Name == sessionCookieName {
			return cookie
		}
	}
	t.Fatal("login did not return a session")
	return nil
}

func workReferenceRequest(t *testing.T, server *httptest.Server, cookie *http.Cookie, method, path, body string, status int, target any) {
	t.Helper()
	request, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != status {
		t.Fatalf("%s %s: status=%d want=%d body=%s", method, path, response.StatusCode, status, data)
	}
	if target != nil {
		if err := json.Unmarshal(data, target); err != nil {
			t.Fatal(err)
		}
	}
}
