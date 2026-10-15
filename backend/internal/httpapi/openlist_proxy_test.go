package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yexca/kikoto/backend/internal/buildinfo"
	"github.com/yexca/kikoto/backend/internal/config"
	"github.com/yexca/kikoto/backend/internal/storage"
	"github.com/yexca/kikoto/backend/migrations"
)

func TestOpenListSourceProxySettingsSurviveRestart(t *testing.T) {
	var endpoint string
	origin := httptest.NewServer(openListSampleHandler(func() string { return endpoint }, 'd'))
	defer origin.Close()
	endpoint = origin.URL
	var authorization atomic.Value
	sourceProxy := newOpenListSampleProxy(t, openListSampleHandler(func() string { return endpoint }, 's'), &authorization)
	globalProxy := newOpenListSampleProxy(t, openListSampleHandler(func() string { return endpoint }, 'g'), nil)
	filename := filepath.Join(t.TempDir(), "library.db")
	cfg := config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", DataRoot: t.TempDir(), CacheRoot: t.TempDir()}
	for boot := 0; boot < 2; boot++ {
		db, err := storage.Open(filename)
		if err != nil {
			t.Fatal(err)
		}
		if err = storage.MigrateFS(db, migrations.Files, buildinfo.Version); err != nil {
			t.Fatal(err)
		}
		server := NewServer(db, cfg)
		app := httptest.NewServer(server.Routes())
		t.Cleanup(func() { app.Close(); _ = server.Shutdown(context.Background()); _ = db.Close() })
		if boot == 0 {
			createTestAdministrator(t, server, "synthetic-user", "synthetic-password")
			createAccountManagedTestUser(t, server, "synthetic-listener", "synthetic-password")
			metadataReviewExec(t, db, `INSERT INTO file_source(id,code,display_name,source_type) VALUES
				(7,'example_remote_a','Example Remote A','openlist'),
				(8,'example_remote_b','Example Remote B','kikoeru_compatible'),
				(9,'example_remote_c','Example Remote C','kikoeru_compatible_number178'),
				(10,'example_local','Example Local','local_folder');
				INSERT INTO source_catalog_entry(id,file_source_id,root_path,title) VALUES(1,7,'/Example Work','Example Work');
				INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES(1,'track.wav','Example Track','audio',4096);`)
			if _, err = db.Exec(`INSERT INTO file_source_endpoint(file_source_id,api_url,base_url) VALUES(7,?,?)`, endpoint, endpoint); err != nil {
				t.Fatal(err)
			}
		}
		admin := workReferenceLogin(t, app, "synthetic-user")
		if boot == 0 {
			proxy := customProxy("example", sourceProxy.URL)
			proxy["username"], proxy["password"] = "synthetic-user", "synthetic-password"
			routes := map[string]any{"remote": routeTo("global"), "sources": map[string]any{
				"7": map[string]any{"mode": "proxy", "proxyIds": []string{"example"}},
				"8": map[string]any{"mode": "direct"}, "9": map[string]any{"mode": "direct"},
				"10": map[string]any{"mode": "direct"}, "999": map[string]any{"mode": "direct"},
			}}
			payload := proxySettingsBody(t, []map[string]any{proxy, customProxy("global", globalProxy.URL)}, routes)
			other := workReferenceLogin(t, app, "synthetic-listener")
			workReferenceRequest(t, app, other, http.MethodPatch, "/api/settings", payload, 403, nil)
			workReferenceRequest(t, app, other, http.MethodGet, "/api/settings", "", 403, nil)
			workReferenceRequest(t, app, admin, http.MethodPatch, "/api/settings", payload, 200, nil)
		}
		var settings appSettingsResponse
		workReferenceRequest(t, app, admin, http.MethodGet, "/api/settings", "", 200, &settings)
		route, ok := settings.Proxy.Routes.Sources[7]
		if !ok || route.Mode != "proxy" || strings.Join(route.ProxyIDs, ",") != "example" {
			t.Fatalf("boot %d: OpenList source override missing from Settings readback", boot)
		}
		if len(settings.Proxy.Routes.Sources) != 3 || settings.Proxy.Routes.Sources[8].Mode != "direct" || settings.Proxy.Routes.Sources[9].Mode != "direct" {
			t.Fatal("remote-source filtering did not preserve Kikoeru or exclude local/unknown sources")
		}
		if len(settings.Proxy.Proxies) != 2 || !settings.Proxy.Proxies[0].HasPassword || strings.Contains(mustJSON(settings), "synthetic-password") {
			t.Fatal("Settings must retain and mask the proxy password")
		}
		assertOpenListSampleRange(t, app, admin, 's')
		if authorization.Load() != "Basic "+base64.StdEncoding.EncodeToString([]byte("synthetic-user:synthetic-password")) {
			t.Fatal("saved proxy credentials did not reach the selected proxy")
		}
		if boot == 0 {
			// A round-trip of the masked response omits password and must keep it.
			workReferenceRequest(t, app, admin, http.MethodPatch, "/api/settings", mustJSON(map[string]any{"proxy": settings.Proxy}), 200, nil)
		} else {
			settings.Proxy.Routes.Sources[7] = settings.Proxy.Routes.Sources[8]
			workReferenceRequest(t, app, admin, http.MethodPatch, "/api/settings", mustJSON(map[string]any{"proxy": settings.Proxy}), 200, nil)
			assertOpenListSampleRange(t, app, admin, 'd')
			delete(settings.Proxy.Routes.Sources, 7)
			workReferenceRequest(t, app, admin, http.MethodPatch, "/api/settings", mustJSON(map[string]any{"proxy": settings.Proxy}), 200, nil)
			assertOpenListSampleRange(t, app, admin, 'g')
		}
		app.Close()
		if err = server.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err = db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func openListSampleHandler(endpoint func() string, marker byte) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/fs/get":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"name": "track.wav", "size": 4096, "raw_url": endpoint() + "/audio"}})
		case "/audio":
			w.Header().Set("Content-Type", "audio/wav")
			http.ServeContent(w, r, "track.wav", time.Unix(0, 0), bytes.NewReader(bytes.Repeat([]byte{marker}, 4096)))
		default:
			http.NotFound(w, r)
		}
	})
}

// The proxy serves a synthetic upstream inside CONNECT; it never dials a target.
func newOpenListSampleProxy(t *testing.T, upstream http.Handler, authorization *atomic.Value) *httptest.Server {
	t.Helper()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT required", 400)
			return
		}
		if authorization != nil {
			authorization.Store(r.Header.Get("Proxy-Authorization"))
		}
		conn, buffered, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		_, _ = buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = buffered.Flush()
		inner, err := http.ReadRequest(buffered.Reader)
		if err != nil {
			return
		}
		defer func() { _ = inner.Body.Close() }()
		recorder := httptest.NewRecorder()
		upstream.ServeHTTP(recorder, inner)
		response := recorder.Result()
		defer func() { _ = response.Body.Close() }()
		response.Close = true
		_ = response.Write(conn)
	}))
	t.Cleanup(proxy.Close)
	return proxy
}

func assertOpenListSampleRange(t *testing.T, app *httptest.Server, cookie *http.Cookie, marker byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, app.URL+"/api/source-catalog/1/media?path=track.wav", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(cookie)
	req.Header.Set("Range", "bytes=0-3")
	response, err := app.Client().Do(req)
	if err != nil {
		t.Fatal("local sample request failed")
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, 5))
	if err != nil || response.StatusCode != 206 || !bytes.Equal(body, bytes.Repeat([]byte{marker}, 4)) {
		t.Fatalf("selected source route returned unexpected range: status=%d", response.StatusCode)
	}
}
