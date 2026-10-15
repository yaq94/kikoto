package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yexca/kikoto/backend/internal/config"
)

func openListBoundaryApp(t *testing.T, upstream string) (*Server, http.Handler, *http.Cookie) {
	t.Helper()
	db := openMigratedTestDBWithProductionPool(t)
	_, err := db.Exec(`INSERT INTO file_source(id,code,display_name,source_type,config_json) VALUES(7,'example_remote_a','Example Remote A','openlist','{"openListRoots":["/Example Work"],"openListIntervalMs":100}');
		INSERT INTO file_source_endpoint(file_source_id,api_url,base_url) VALUES(7,?,?);
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title) VALUES(1,7,'/Existing Work','Existing Work');
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES(1,'track.mp3','Example Track','audio',16);`, upstream, upstream)
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	createTestAdministrator(t, server, "synthetic-user", "synthetic-password")
	handler := server.Routes()
	return server, handler, loginTestSession(t, handler, "synthetic-user", "synthetic-password")
}

func TestOpenListMetadataBoundariesPreserveExistingCatalog(t *testing.T) {
	for _, scenario := range []struct{ name, body, want string }{
		{"restricted", `{"code":403,"message":"private endpoint detail"}`, "restricted"},
		{"oversized", `{"code":200,"data":{"content":[],"total":0},"message":"` + strings.Repeat("x", 2<<20) + `"}`, "limit_exceeded"},
		{"traversal", `{"code":200,"data":{"content":[{"name":"../outside.mp3","size":10}],"total":1}}`, "failed"},
		{"changing pagination", `{"code":200,"data":{"content":[],"total":1}}`, "failed"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(scenario.body)) }))
			defer remote.Close()
			_, handler, cookie := openListBoundaryApp(t, remote.URL)
			result := reviewRequest(t, handler, cookie, http.MethodPost, "/api/remote-sources/7/catalog/scan", `{}`, 200)
			if result["status"] != "partial" || result["roots"].([]any)[0].(map[string]any)["status"] != scenario.want {
				t.Fatalf("invalid completion: %#v", result)
			}
			if strings.Contains(mustJSON(result), "private endpoint") {
				t.Fatal("upstream details leaked")
			}
			list := reviewRequest(t, handler, cookie, http.MethodGet, "/api/remote-sources/7/works", "", 200)
			if list["total"] != float64(1) {
				t.Fatal("failed scan removed existing entry")
			}
		})
	}
}

func TestOpenListMediaRejectsUntrustedDestinations(t *testing.T) {
	credentialURL := url.URL{Scheme: "http", Host: "source.example.invalid", Path: "/audio", User: url.UserPassword("synthetic-user", "synthetic-password")}
	for _, target := range []string{"file:///etc/passwd", credentialURL.String(), "http://127.0.0.2/audio", "http://192.0.2.1/audio", "http://[::1]/audio"} {
		t.Run(target, func(t *testing.T) {
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"name": "track.mp3", "raw_url": target}})
			}))
			defer remote.Close()
			_, handler, cookie := openListBoundaryApp(t, remote.URL)
			request := httptest.NewRequest(http.MethodGet, "/api/source-catalog/1/media?path=track.mp3", nil)
			request.AddCookie(cookie)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != 502 {
				t.Fatalf("destination accepted: %d", response.Code)
			}
			for _, secret := range []string{"synthetic-password", "127.0.0.2", "192.0.2.1", "source.example.invalid"} {
				if strings.Contains(response.Body.String(), secret) {
					t.Fatal("destination detail leaked")
				}
			}
		})
	}
	var remote *httptest.Server
	remote = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/fs/get" {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"name": "track.mp3", "raw_url": remote.URL + "/redirect"}})
			return
		}
		http.Redirect(w, r, "http://127.0.0.2/audio", http.StatusFound)
	}))
	defer remote.Close()
	_, handler, cookie := openListBoundaryApp(t, remote.URL)
	reviewRequest(t, handler, cookie, http.MethodGet, "/api/source-catalog/1/media?path=track.mp3", "", 502)
}

func TestOpenListMetadataRequestsAreSerializedPacedAndCancellable(t *testing.T) {
	var active, maxActive atomic.Int32
	var mu sync.Mutex
	var starts []time.Time
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := maxActive.Load(); n > old; old = maxActive.Load() {
			if maxActive.CompareAndSwap(old, n) {
				break
			}
		}
		mu.Lock()
		starts = append(starts, time.Now())
		mu.Unlock()
		select {
		case <-time.After(20 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte(`{"code":200,"data":{"content":[],"total":0}}`))
	}))
	defer remote.Close()
	_, handler, cookie := openListBoundaryApp(t, remote.URL)
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reviewRequest(t, handler, cookie, http.MethodPost, "/api/remote-sources/7/catalog/scan", `{}`, 200)
		}()
	}
	wg.Wait()
	if maxActive.Load() != 1 {
		t.Fatalf("metadata concurrency=%d", maxActive.Load())
	}
	mu.Lock()
	for i := 1; i < len(starts); i++ {
		if starts[i].Sub(starts[i-1]) < 90*time.Millisecond {
			t.Error("configured minimum interval was not applied")
		}
	}
	mu.Unlock()
	started := make(chan struct{})
	stopped := make(chan struct{})
	finish := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		close(started)
		select {
		case <-r.Context().Done():
		case <-finish:
		}
		close(stopped)
	}))
	defer slow.Close()
	defer close(finish)
	_, handler, cookie = openListBoundaryApp(t, slow.URL)
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodPost, "/api/remote-sources/7/catalog/scan", strings.NewReader(`{}`)).WithContext(ctx)
	request.AddCookie(cookie)
	done := make(chan struct{})
	go func() { defer close(done); handler.ServeHTTP(httptest.NewRecorder(), request) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled scan did not return")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("upstream request was not cancelled")
	}
}

func TestOpenListCatalogPermissionsAndConcurrentRegistration(t *testing.T) {
	server, handler, cookie := openListBoundaryApp(t, "https://source.example.invalid")
	createAccountManagedTestUser(t, server, "synthetic-listener", "synthetic-password")
	listener := loginTestSession(t, handler, "synthetic-listener", "synthetic-password")
	reviewRequest(t, handler, listener, http.MethodPost, "/api/remote-sources/7/catalog/scan", `{}`, 403)
	reviewRequest(t, handler, nil, http.MethodGet, "/api/source-catalog/1", "", 401)
	for _, body := range []string{`{"rating":6}`, `{"rating":-1}`, `{"rating":1.5}`, `{"note":null}`, `{"favorite":true}`, `{}`} {
		reviewRequest(t, handler, cookie, http.MethodPatch, "/api/source-catalog/1/user-state", body, 400)
	}
	results := make(chan float64, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result := reviewRequest(t, handler, cookie, http.MethodPatch, "/api/source-catalog/1/user-state", `{"rating":4}`, 200)
			results <- result["workId"].(float64)
		}()
	}
	wg.Wait()
	close(results)
	var id float64
	for actual := range results {
		if id != 0 && id != actual {
			t.Fatal("concurrent registration duplicated identity")
		}
		id = actual
	}
	list := reviewRequest(t, handler, cookie, http.MethodGet, "/api/works", "", 200)
	if list["total"] != float64(1) {
		t.Fatalf("concurrent registration duplicated works: %#v", list)
	}
	demo := NewServer(server.db, config.Config{Mode: config.ModeDemo})
	defer func() { _ = demo.Shutdown(context.Background()) }()
	if err := demo.BootstrapDemo(context.Background()); err != nil {
		t.Fatal(err)
	}
	reviewRequest(t, demo.Routes(), nil, http.MethodPatch, "/api/source-catalog/1/user-state", `{"rating":1}`, 403)
	reviewRequest(t, demo.Routes(), nil, http.MethodPost, "/api/remote-sources/7/catalog/scan", `{}`, 403)
	reviewRequest(t, demo.Routes(), nil, http.MethodGet, "/api/source-catalog/1", "", 404)
}
