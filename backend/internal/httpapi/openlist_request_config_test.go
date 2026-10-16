package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yexca/kikoto/backend/internal/buildinfo"
	"github.com/yexca/kikoto/backend/internal/config"
	"github.com/yexca/kikoto/backend/internal/openlist"
)

const (
	openListConfiguredIdentity = "Kikoto/0.8.3 operator-declared"
	openListChallengeBody      = `<!DOCTYPE html><title>Just a moment...</title><div id="challenge-platform">synthetic https://source.example.invalid/private`
)

// openListRequestConfigApp serves one OpenList source whose identity and header
// options come from the stored configuration, plus one catalog entry and audio
// file so a media request can be driven end to end.
func openListRequestConfigApp(t *testing.T, upstream string, configJSON string) (*Server, http.Handler, *http.Cookie) {
	t.Helper()
	db := openMigratedTestDB(t)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO file_source(id,code,display_name,source_type,config_json) VALUES(7,'example_remote_a','Example Remote A','openlist',?)`, []any{configJSON}},
		{`INSERT INTO file_source_endpoint(file_source_id,api_url,base_url) VALUES(7,?,?)`, []any{upstream, upstream}},
		{`INSERT INTO source_catalog_entry(id,file_source_id,root_path,title) VALUES(1,7,'/Example Work','Example Work')`, nil},
		{`INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES(1,'track.mp3','Example Track','audio',32)`, nil},
	} {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	server := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	createTestAdministrator(t, server, "synthetic-user", "synthetic-password")
	handler := server.Routes()
	return server, handler, loginTestSession(t, handler, "synthetic-user", "synthetic-password")
}

type recordedOpenListRequest struct {
	UserAgent      string
	AcceptLanguage string
	ExtraHeader    string
	Accept         string
}

// TestOpenListMetadataRequestsDeclareIdentityAndConfiguredHeaders covers the
// full outbound path: the catalog scan uses the same configured identity and
// whitelisted headers as every other OpenList request.
func TestOpenListMetadataRequestsDeclareIdentityAndConfiguredHeaders(t *testing.T) {
	var mu sync.Mutex
	var observed []recordedOpenListRequest
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		observed = append(observed, recordedOpenListRequest{
			UserAgent:      r.Header.Get("User-Agent"),
			AcceptLanguage: r.Header.Get("Accept-Language"),
			ExtraHeader:    r.Header.Get("X-Client-Channel"),
			Accept:         r.Header.Get("Accept"),
		})
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": []any{}, "total": 0}})
	}))
	defer remote.Close()
	_, handler, cookie := openListRequestConfigApp(t, remote.URL,
		`{"openListRoots":["/Example Work"],"openListIntervalMs":100,"openListUserAgent":"`+openListConfiguredIdentity+`","openListHeaders":{"Accept-Language":"ja-JP","X-Client-Channel":"operator-configured"}}`)
	reviewRequest(t, handler, cookie, http.MethodPost, "/api/remote-sources/7/catalog/scan", `{}`, 200)
	mu.Lock()
	defer mu.Unlock()
	if len(observed) == 0 {
		t.Fatal("catalog scan sent no source request")
	}
	for _, request := range observed {
		if request.UserAgent != openListConfiguredIdentity {
			t.Fatalf("configured identity not applied: %q", request.UserAgent)
		}
		if request.ExtraHeader != "operator-configured" || request.AcceptLanguage != "ja-JP" {
			t.Fatalf("configured headers not applied: %#v", request)
		}
		if request.Accept != "application/json" {
			t.Fatalf("client Accept header changed: %q", request.Accept)
		}
	}
}

func TestOpenListMetadataRequestsUseDefaultKikotoIdentity(t *testing.T) {
	var mu sync.Mutex
	var observed []string
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		observed = append(observed, r.Header.Get("User-Agent"))
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"name": "track.mp3", "size": 32, "raw_url": "https://example.invalid/track.mp3"}})
	}))
	defer remote.Close()
	server, _, _ := openListRequestConfigApp(t, remote.URL, `{}`)
	source, err := server.loadRemoteSourceForUse(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = server.openListClient(source).Get(context.Background(), "/Example Work/track.mp3"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(observed) != 1 || observed[0] != buildinfo.UserAgent() {
		t.Fatalf("default identity %#v", observed)
	}
}

func TestOpenListConfigurationRejectsDangerousHeaders(t *testing.T) {
	for _, scenario := range []struct{ name, config string }{
		{"host", `{"openListHeaders":{"Host":"attacker.invalid"}}`},
		{"content-length", `{"openListHeaders":{"Content-Length":"10"}}`},
		{"connection", `{"openListHeaders":{"Connection":"keep-alive"}}`},
		{"cookie", `{"openListHeaders":{"Cookie":"session=1"}}`},
		{"accept-encoding", `{"openListHeaders":{"Accept-Encoding":"gzip"}}`},
		{"user-agent header", `{"openListHeaders":{"User-Agent":"Browser/1.0"}}`},
		{"authorization", `{"openListHeaders":{"Authorization":"Bearer synthetic"}}`},
		{"accept header", `{"openListHeaders":{"Accept":"text/html"}}`},
		{"too many", `{"openListHeaders":{"X-A":"1","X-B":"1","X-C":"1","X-D":"1","X-E":"1","X-F":"1","X-G":"1","X-H":"1","X-I":"1"}}`},
		{"newline value", `{"openListHeaders":{"X-A":"one\r\nX-Injected: two"}}`},
		{"empty value", `{"openListHeaders":{"X-A":""}}`},
		{"identity newline", `{"openListUserAgent":"one\ntwo"}`},
		{"identity too long", `{"openListUserAgent":"` + strings.Repeat("a", 257) + `"}`},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			db := openMigratedTestDB(t)
			server := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
			t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
			createTestAdministrator(t, server, "synthetic-user", "synthetic-password")
			handler := server.Routes()
			cookie := loginTestSession(t, handler, "synthetic-user", "synthetic-password")
			body := `{"displayName":"Example Remote A","sourceType":"openlist","enabled":true,"config":` + scenario.config + `,"endpoint":{"apiUrl":"https://source.example.invalid","baseUrl":"https://source.example.invalid"}}`
			request := httptest.NewRequest(http.MethodPost, "/api/file-sources", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			request.AddCookie(cookie)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("unsafe configuration accepted: %d %s", response.Code, response.Body.String())
			}
			if !strings.Contains(response.Body.String(), "openList") {
				t.Fatalf("rejection does not name the setting: %s", response.Body.String())
			}
		})
	}
}

func TestOpenListStoredUnsafeHeadersAreIgnoredOnLoad(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Injected") != "" || r.Header.Get("Cookie") != "" {
			t.Error("unsafe stored header reached the source")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"name": "track.mp3", "size": 32, "raw_url": "https://example.invalid/track.mp3"}})
	}))
	defer remote.Close()
	// Validation exists now, but a row written by an earlier build is still read.
	server, _, _ := openListRequestConfigApp(t, remote.URL, `{"openListHeaders":{"Cookie":"session=1","Host":"attacker.invalid","X-Injected":"one\r\ntwo","X-Kept":"kept"}}`)
	source, err := server.loadRemoteSourceForUse(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(source.Config.OpenListHeaders) != 1 || source.Config.OpenListHeaders["X-Kept"] != "kept" {
		t.Fatalf("unsafe stored headers survived load: %#v", source.Config.OpenListHeaders)
	}
	if _, err = server.openListClient(source).Get(context.Background(), "/Example Work/track.mp3"); err != nil {
		t.Fatal(err)
	}
}

// TestOpenListChallengeIsReportedSanitizedWithoutRetry drives the challenge
// classification through the media path: one upstream attempt and a sanitized
// failure that keeps the challenge page out of the response.
func TestOpenListChallengeIsReportedSanitizedWithoutRetry(t *testing.T) {
	var mu sync.Mutex
	attempts := 0
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		mu.Unlock()
		w.Header().Set("Cf-Mitigated", "challenge")
		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(openListChallengeBody))
	}))
	defer remote.Close()
	_, handler, cookie := openListRequestConfigApp(t, remote.URL, `{}`)
	request := httptest.NewRequest(http.MethodGet, "/api/source-catalog/1/media?path=track.mp3", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("challenge media status %d", response.Code)
	}
	if !strings.Contains(response.Body.String(), "remote source request failed") {
		t.Fatalf("unexpected challenge response: %s", response.Body.String())
	}
	for _, secret := range []string{"challenge-platform", "Just a moment", "source.example.invalid"} {
		if strings.Contains(response.Body.String(), secret) {
			t.Fatalf("challenge detail leaked: %s", secret)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if attempts != 1 {
		t.Fatalf("challenge retried: %d upstream attempts", attempts)
	}
}

func TestOpenListChallengeMarksCatalogDirectoryRestricted(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cf-Mitigated", "challenge")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer remote.Close()
	_, handler, cookie := openListRequestConfigApp(t, remote.URL, `{"openListRoots":["/Example Work"],"openListIntervalMs":100}`)
	result := reviewRequest(t, handler, cookie, http.MethodPost, "/api/remote-sources/7/catalog/scan", `{}`, 200)
	roots, ok := result["roots"].([]any)
	if !ok || len(roots) != 1 {
		t.Fatalf("scan result %#v", result)
	}
	if status := roots[0].(map[string]any)["status"]; status != "restricted" {
		t.Fatalf("challenge root status %v", status)
	}
}

// TestSourceCatalogIndexWorkerUsesConfiguredIdentity covers the full-index
// worker, which builds its own transport rather than reusing openListClient.
func TestSourceCatalogIndexWorkerUsesConfiguredIdentity(t *testing.T) {
	var mu sync.Mutex
	var identities []string
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		identities = append(identities, r.Header.Get("User-Agent"))
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": []any{}, "total": 0}})
	}))
	if _, err := f.server.db.Exec(`UPDATE file_source SET config_json=? WHERE id=1`, mustJSON(map[string]any{"openListUserAgent": openListConfiguredIdentity})); err != nil {
		t.Fatal(err)
	}
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	f.drain(t)
	mu.Lock()
	defer mu.Unlock()
	if len(identities) == 0 {
		t.Fatal("full index sent no source request")
	}
	for _, identity := range identities {
		if identity != openListConfiguredIdentity {
			t.Fatalf("full index identity %q", identity)
		}
	}
}

// TestOpenListPlainPermissionRefusalKeepsItsOwnCategory keeps the challenge
// classification from swallowing an ordinary anonymous-permission refusal.
func TestOpenListPlainPermissionRefusalKeepsItsOwnCategory(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code":403,"message":"private endpoint detail"}`))
	}))
	defer remote.Close()
	server, _, _ := openListRequestConfigApp(t, remote.URL, `{}`)
	source, err := server.loadRemoteSourceForUse(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	_, err = server.openListClient(source).Get(context.Background(), "/Example Work/track.mp3")
	if !errors.Is(err, openlist.ErrRestricted) || errors.Is(err, openlist.ErrChallenged) {
		t.Fatalf("plain refusal classification %v", err)
	}
}
