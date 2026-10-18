package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yexca/kikoto/backend/internal/buildinfo"
	"github.com/yexca/kikoto/backend/internal/config"
	"github.com/yexca/kikoto/backend/internal/kikoeru"
	"github.com/yexca/kikoto/backend/internal/storage"
	"github.com/yexca/kikoto/backend/migrations"
)

func reviewRequest(t *testing.T, handler http.Handler, cookie *http.Cookie, method, path, body string, status int) map[string]any {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != status {
		t.Fatalf("%s %s: status %d, want %d; %s", method, path, response.Code, status, response.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestPersonalReviewRegistersOnlyMetadataAndSharesAcrossSources(t *testing.T) {
	var mu sync.Mutex
	var requests []string
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.URL.Path)
		mu.Unlock()
		if r.URL.Path != "/api/workInfo/RJ00000001" {
			http.Error(w, "unexpected non-metadata request", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(kikoeru.Work{ID: 1, SourceID: "RJ00000001", Title: "Example Work", MainCoverURL: "/cover.jpg"})
	}))
	defer remote.Close()
	db := openMigratedTestDB(t)
	if _, err := db.Exec(`INSERT INTO file_source (id, code, display_name, source_type) VALUES
		(1, 'example_remote_a', 'Example Remote A', 'kikoeru_compatible'),
		(2, 'example_remote_b', 'Example Remote B', 'kikoeru_compatible')`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int{1, 2} {
		if _, err := db.Exec(`INSERT INTO file_source_endpoint (file_source_id, base_url, api_url) VALUES (?, ?, ?)`, id, remote.URL, remote.URL); err != nil {
			t.Fatal(err)
		}
	}
	server := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", CacheRoot: t.TempDir(), DataRoot: t.TempDir()})
	createTestAdministrator(t, server, "synthetic-user", "synthetic-password")
	createAccountManagedTestUser(t, server, "synthetic-listener", "synthetic-password")
	handler := server.Routes()
	cookie := loginTestSession(t, handler, "synthetic-user", "synthetic-password")
	result := reviewRequest(t, handler, cookie, http.MethodPatch, "/api/remote-sources/1/works/RJ00000001/user-state", `{"rating":3,"note":"Remote private note"}`, http.StatusOK)
	assertReview(t, result, float64(3), "Remote private note")
	id := strconv.Itoa(int(result["workId"].(float64)))
	assertReview(t, reviewRequest(t, handler, cookie, http.MethodGet, "/api/works/"+id+"?includeMedia=false", "", http.StatusOK), float64(3), "Remote private note")
	other := reviewRequest(t, handler, cookie, http.MethodPatch, "/api/remote-sources/2/works/RJ00000001/user-state", `{"note":"Shared private note"}`, http.StatusOK)
	if other["workId"] != result["workId"] {
		t.Fatalf("source duplicated work: %v / %v", result["workId"], other["workId"])
	}
	assertReview(t, other, float64(3), "Shared private note")
	assertReview(t, reviewRequest(t, handler, cookie, http.MethodGet, "/api/remote-sources/2/works/RJ00000001", "", http.StatusOK), float64(3), "Shared private note")
	otherUser := loginTestSession(t, handler, "synthetic-listener", "synthetic-password")
	assertReview(t, reviewRequest(t, handler, otherUser, http.MethodGet, "/api/remote-sources/2/works/RJ00000001", "", http.StatusOK), nil, "")
	assertReview(t, reviewRequest(t, handler, otherUser, http.MethodPatch, "/api/remote-sources/2/works/RJ00000001/user-state", `{"rating":1,"note":"Second remote note"}`, http.StatusOK), float64(1), "Second remote note")
	assertReview(t, reviewRequest(t, handler, cookie, http.MethodGet, "/api/remote-sources/2/works/RJ00000001", "", http.StatusOK), float64(3), "Shared private note")
	library := reviewRequest(t, handler, cookie, http.MethodGet, "/api/works?q=RJ00000001", "", http.StatusOK)
	if library["total"] != float64(1) {
		t.Fatalf("registered work is not visible: %#v", library)
	}
	media := reviewRequest(t, handler, cookie, http.MethodGet, "/api/works/"+id+"/media", "", http.StatusOK)
	if len(media["mediaItems"].([]any)) != 0 {
		t.Fatalf("review created media: %#v", media)
	}
	runs := reviewRequest(t, handler, cookie, http.MethodGet, "/api/workflow-runs", "", http.StatusOK)
	if runs["total"] != float64(0) {
		t.Fatalf("review scheduled work: %#v", runs)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) == 0 {
		t.Fatal("metadata was not requested")
	}
	for _, path := range requests {
		if path != "/api/workInfo/RJ00000001" {
			t.Errorf("review requested media or directory: %s", path)
		}
	}
}

func assertReview(t *testing.T, result map[string]any, rating any, note string) {
	t.Helper()
	actual, exists := result["personalRating"]
	if !exists || actual != rating || result["personalNote"] != note {
		t.Fatalf("personal review = (%v, %v), want (%v, %q)", actual, result["personalNote"], rating, note)
	}
}

func TestPersonalReviewPartialUpdatesAndUserIsolation(t *testing.T) {
	db := openMigratedTestDB(t)
	if _, err := db.Exec(`INSERT INTO work (id, primary_code, title, rating_average) VALUES (1, 'RJ00000000', 'Example Work', 4.25)`); err != nil {
		t.Fatal(err)
	}
	server := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user"})
	createTestAdministrator(t, server, "synthetic-user", "synthetic-password")
	createAccountManagedTestUser(t, server, "synthetic-listener", "synthetic-password")
	handler := server.Routes()
	first := loginTestSession(t, handler, "synthetic-user", "synthetic-password")
	second := loginTestSession(t, handler, "synthetic-listener", "synthetic-password")
	write := func(cookie *http.Cookie, body string) {
		reviewRequest(t, handler, cookie, http.MethodPatch, "/api/works/1/user-state", body, http.StatusOK)
	}
	read := func(cookie *http.Cookie) map[string]any {
		return reviewRequest(t, handler, cookie, http.MethodGet, "/api/works/1", "", http.StatusOK)
	}
	write(first, `{"rating":4,"note":"Example private note","listeningStatus":"finished"}`)
	assertReview(t, read(first), float64(4), "Example private note")
	assertReview(t, read(second), nil, "")
	write(second, `{"rating":0,"note":"Second private note"}`)
	write(first, `{"note":"Updated note"}`)
	assertReview(t, read(first), float64(4), "Updated note")
	assertReview(t, read(second), float64(0), "Second private note")
	write(first, `{"rating":null}`)
	assertReview(t, read(first), nil, "Updated note")
	write(first, `{"rating":5,"note":""}`)
	result := read(first)
	assertReview(t, result, float64(5), "")
	if result["rating"] != float64(4.25) || result["listeningStatus"] != "finished" {
		t.Fatalf("public rating or listening status changed: %#v", result)
	}
	assertReview(t, read(second), float64(0), "Second private note")
	write(first, `{"listeningStatus":"  "}`)
	result = read(first)
	assertReview(t, result, float64(5), "")
	if result["listeningStatus"] != "none" {
		t.Fatal("empty listening mark must retain its existing clear behavior")
	}
}

func TestPersonalReviewValidationAndPermissions(t *testing.T) {
	db := openMigratedTestDB(t)
	if _, err := db.Exec(`INSERT INTO work (id, primary_code, title) VALUES (1, 'RJ00000000', 'Example Work');
		INSERT INTO file_source (id, code, display_name, source_type) VALUES (1, 'example_remote_a', 'Example Remote A', 'kikoeru_compatible');
		INSERT INTO file_source_endpoint (file_source_id, base_url, api_url) VALUES (1, 'https://source.example.invalid', 'https://source.example.invalid');`); err != nil {
		t.Fatal(err)
	}
	server := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user"})
	createTestAdministrator(t, server, "synthetic-user", "synthetic-password")
	createAccountManagedTestUser(t, server, "synthetic-listener", "synthetic-password")
	handler := server.Routes()
	cookie := loginTestSession(t, handler, "synthetic-listener", "synthetic-password")
	reviewRequest(t, handler, nil, http.MethodPatch, "/api/works/1/user-state", `{"rating":1}`, http.StatusUnauthorized)
	for _, body := range []string{`{"rating":-1}`, `{"rating":6}`, `{"rating":1.5}`, `{"rating":"4"}`, `{"note":null}`, `{"note":1}`, `{"favorite":true}`} {
		reviewRequest(t, handler, cookie, http.MethodPatch, "/api/works/1/user-state", body, http.StatusBadRequest)
	}
	// A client must not smuggle an owner id or any other field past the patch.
	// The server always writes the session user, and an unknown field is an
	// invalid request rather than a silent no-op.
	for _, body := range []string{`{"userId":999,"rating":1}`, `{"workId":2,"rating":1}`, `{"rating":1,"unknown":true}`} {
		reviewRequest(t, handler, cookie, http.MethodPatch, "/api/works/1/user-state", body, http.StatusBadRequest)
	}
	assertReview(t, reviewRequest(t, handler, cookie, http.MethodGet, "/api/works/1?includeMedia=false", "", http.StatusOK), nil, "")
	reviewRequest(t, handler, cookie, http.MethodPatch, "/api/remote-sources/1/works/RJ00000001/user-state", `{"rating":2}`, http.StatusForbidden)
	// A regular user may review an already registered object without the shared
	// library registration permission; neither path can change another user.
	assertReview(t, reviewRequest(t, handler, cookie, http.MethodPatch, "/api/remote-sources/1/works/RJ00000000/user-state", `{"rating":2}`, http.StatusOK), float64(2), "")
	demoServer := NewServer(db, config.Config{Mode: config.ModeDemo})
	if err := demoServer.BootstrapDemo(context.Background()); err != nil {
		t.Fatal(err)
	}
	demo := demoServer.Routes()
	reviewRequest(t, demo, nil, http.MethodPatch, "/api/works/1/user-state", `{"rating":4}`, http.StatusForbidden)
	reviewRequest(t, demo, nil, http.MethodPatch, "/api/remote-sources/1/works/RJ00000001/user-state", `{"rating":4}`, http.StatusForbidden)
	assertReview(t, reviewRequest(t, handler, cookie, http.MethodGet, "/api/works/1?includeMedia=false", "", http.StatusOK), float64(2), "")
}

func TestPersonalReviewSurvivesApplicationRestart(t *testing.T) {
	path, db := openRestartableMigrationDB(t)
	if _, err := db.Exec(`INSERT INTO work (id, primary_code, title) VALUES (1, 'RJ00000000', 'Example Work')`); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user"}
	server := NewServer(db, cfg)
	createTestAdministrator(t, server, "synthetic-user", "synthetic-password")
	app := httptest.NewServer(server.Routes())
	cookie := loginTestSession(t, server.Routes(), "synthetic-user", "synthetic-password")
	request, err := http.NewRequest(http.MethodPatch, app.URL+"/api/works/1/user-state", strings.NewReader(`{"rating":0,"note":"Persisted example note"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(cookie)
	response, err := app.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("save before shutdown = %d", response.StatusCode)
	}
	app.Close()
	if err := server.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := storage.MigrateFS(db, migrations.Files, buildinfo.Version); err != nil {
		t.Fatal(err)
	}
	restarted := httptest.NewServer(NewServer(db, cfg).Routes())
	defer restarted.Close()
	request, err = http.NewRequest(http.MethodGet, restarted.URL+"/api/works/RJ00000000?includeMedia=false", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(cookie)
	response, err = restarted.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("read after restart = %d", response.StatusCode)
	}
	var result map[string]any
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	assertReview(t, result, float64(0), "Persisted example note")
}

func TestPersonalReviewRejectsUnsafeOrInvalidRemoteMetadata(t *testing.T) {
	for _, scenario := range []string{"redirect", "oversized", "identity", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			started := make(chan struct{})
			var once sync.Once
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch scenario {
				case "redirect":
					http.Redirect(w, r, "http://127.0.0.2:1/private?token=synthetic-token", http.StatusFound)
				case "oversized":
					_ = json.NewEncoder(w).Encode(kikoeru.Work{ID: 1, SourceID: "RJ00000001", Title: strings.Repeat("x", 2<<20)})
				case "identity":
					_ = json.NewEncoder(w).Encode(kikoeru.Work{ID: 2, SourceID: "RJ00000002", Title: "Example Work"})
				case "cancelled":
					once.Do(func() { close(started) })
					<-r.Context().Done()
				}
			}))
			defer remote.Close()
			db := openMigratedTestDB(t)
			if _, err := db.Exec(`INSERT INTO file_source (id, code, display_name, source_type) VALUES (1, 'example_remote_a', 'Example Remote A', 'kikoeru_compatible')`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO file_source_endpoint (file_source_id, base_url, api_url) VALUES (1, ?, ?)`, remote.URL, remote.URL); err != nil {
				t.Fatal(err)
			}
			server := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user"})
			createTestAdministrator(t, server, "synthetic-user", "synthetic-password")
			handler := server.Routes()
			cookie := loginTestSession(t, handler, "synthetic-user", "synthetic-password")
			request := httptest.NewRequest(http.MethodPatch, "/api/remote-sources/1/works/RJ00000001/user-state", strings.NewReader(`{"rating":3}`))
			request.AddCookie(cookie)
			ctx, cancel := context.WithCancel(request.Context())
			defer cancel()
			response := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { handler.ServeHTTP(response, request.WithContext(ctx)); close(done) }()
			if scenario == "cancelled" {
				select {
				case <-started:
					cancel()
				case <-time.After(3 * time.Second):
					t.Fatal("upstream request did not start")
				}
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("request did not stop")
			}
			if response.Code < 400 {
				t.Fatalf("unsafe metadata accepted: %d", response.Code)
			}
			if strings.Contains(response.Body.String(), remote.URL) || strings.Contains(response.Body.String(), "synthetic-token") {
				t.Fatalf("public error disclosed upstream details: %s", response.Body.String())
			}
			library := reviewRequest(t, handler, cookie, http.MethodGet, "/api/works", "", http.StatusOK)
			if library["total"] != float64(0) {
				t.Fatalf("failed registration created a work: %#v", library)
			}
		})
	}
}
