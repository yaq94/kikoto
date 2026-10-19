package openlist

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yexca/kikoto/backend/internal/buildinfo"
)

func openListTestClient(server *httptest.Server) *Client {
	return NewClient(server.URL, &http.Client{Timeout: 5 * time.Second})
}

func TestOpenListDeclaresKikotoIdentityByDefault(t *testing.T) {
	var observed atomic.Value
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observed.Store(r.Header.Get("User-Agent"))
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"name": "track.mp3", "size": 32, "raw_url": "https://example.invalid/track.mp3"}})
	}))
	defer remote.Close()
	client := openListTestClient(remote)
	if _, err := client.Get(context.Background(), "/Example Work/track.mp3"); err != nil {
		t.Fatal(err)
	}
	if got := observed.Load().(string); got != buildinfo.UserAgent() {
		t.Fatalf("default identity %q, want %q", got, buildinfo.UserAgent())
	}
	if got := observed.Load().(string); strings.HasPrefix(got, "Go-http-client") {
		t.Fatal("Go default identity reached the source")
	}
}

// A companion text read carries the same identity and operator headers as a
// metadata request. CDNs commonly reject the Go default identity, so this path
// must not silently fall back to it.
func TestOpenListCompanionTextUsesConfiguredHeaders(t *testing.T) {
	var identity, language, custom string
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, language, custom = r.Header.Get("User-Agent"), r.Header.Get("Accept-Language"), r.Header.Get("X-Client-Channel")
		_, _ = w.Write([]byte("配音：Example Voice"))
	}))
	defer remote.Close()
	client := openListTestClient(remote).WithUserAgent("Kikoto/synthetic").WithRequestHeaders(map[string]string{
		"Accept-Language":  "ja",
		"X-Client-Channel": "synthetic",
	})
	body, err := client.GetText(context.Background(), remote.URL+"/readme.txt", 256)
	if err != nil {
		t.Fatal(err)
	}
	if body != "配音：Example Voice" {
		t.Fatalf("body = %q", body)
	}
	if identity != "Kikoto/synthetic" || language != "ja" || custom != "synthetic" {
		t.Fatalf("headers = identity %q language %q custom %q", identity, language, custom)
	}
	if _, err = client.GetText(context.Background(), remote.URL+"/readme.txt", 4); !errors.Is(err, ErrLimit) {
		t.Fatalf("oversized companion read = %v, want ErrLimit", err)
	}
}

func TestOpenListOperatorIdentityAndHeadersOverride(t *testing.T) {
	type observedRequest struct {
		UserAgent      string
		AcceptLanguage string
		CustomHeader   string
		ContentType    string
		Accept         string
	}
	var observed []observedRequest
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observed = append(observed, observedRequest{
			UserAgent:      r.Header.Get("User-Agent"),
			AcceptLanguage: r.Header.Get("Accept-Language"),
			CustomHeader:   r.Header.Get("X-Client-Channel"),
			ContentType:    r.Header.Get("Content-Type"),
			Accept:         r.Header.Get("Accept"),
		})
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"name": "track.mp3", "size": 32, "raw_url": "https://example.invalid/track.mp3"}})
	}))
	defer remote.Close()
	client := openListTestClient(remote).
		WithUserAgent("Kikoto/0.8.3 operator-declared").
		WithRequestHeaders(map[string]string{
			"x-client-channel": "operator-configured",
			"accept-language":  "ja-JP",
		})
	if _, err := client.Get(context.Background(), "/Example Work/track.mp3"); err != nil {
		t.Fatal(err)
	}
	if len(observed) != 1 {
		t.Fatalf("requests=%d", len(observed))
	}
	got := observed[0]
	if got.UserAgent != "Kikoto/0.8.3 operator-declared" {
		t.Fatalf("identity override not applied: %q", got.UserAgent)
	}
	if got.CustomHeader != "operator-configured" || got.AcceptLanguage != "ja-JP" {
		t.Fatalf("configured headers not applied: %#v", got)
	}
	// The client keeps ownership of its framing headers.
	if got.ContentType != "application/json" || got.Accept != "application/json" {
		t.Fatalf("client framing headers changed: %#v", got)
	}
}

func TestOpenListDropsUnsafeConfiguredHeaders(t *testing.T) {
	observed := map[string]string{}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observed["Host"] = r.Host
		observed["Connection"] = r.Header.Get("Connection")
		observed["Cookie"] = r.Header.Get("Cookie")
		observed["X-Kept"] = r.Header.Get("X-Kept")
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"name": "track.mp3", "size": 32, "raw_url": "https://example.invalid/track.mp3"}})
	}))
	defer remote.Close()
	client := openListTestClient(remote).WithRequestHeaders(map[string]string{
		"Host":           "attacker.invalid",
		"Connection":     "close",
		"Cookie":         "session=synthetic",
		"X-Kept":         "kept",
		"X-Bad\r\nExtra": "injected",
		"X-Long":         strings.Repeat("a", 513),
	})
	if _, err := client.Get(context.Background(), "/Example Work/track.mp3"); err != nil {
		t.Fatal(err)
	}
	if observed["X-Kept"] != "kept" {
		t.Fatalf("whitelisted header was dropped: %#v", observed)
	}
	for name, value := range map[string]string{"Host": remote.Listener.Addr().String(), "Cookie": "", "Connection": ""} {
		if observed[name] != value {
			t.Fatalf("header %s reached the source as %q", name, observed[name])
		}
	}
}

func TestOpenListClassifiesChallengeSeparatelyFromPermissionRefusal(t *testing.T) {
	requests := atomic.Int32{}
	challenge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Cf-Mitigated", "challenge")
		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<!DOCTYPE html><title>Just a moment...</title><div id="challenge-platform">synthetic`))
	}))
	defer challenge.Close()
	if _, err := openListTestClient(challenge).Get(context.Background(), "/Example Work/track.mp3"); !errors.Is(err, ErrChallenged) {
		t.Fatalf("challenge classification %v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("challenge detection retried: %d requests", requests.Load())
	}

	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code":403,"message":"private endpoint detail"}`))
	}))
	defer plain.Close()
	_, err := openListTestClient(plain).Get(context.Background(), "/Example Work/track.mp3")
	if !errors.Is(err, ErrRestricted) || errors.Is(err, ErrChallenged) {
		t.Fatalf("plain refusal classification %v", err)
	}
}

func TestOpenListDetectsUndocumentedChallengeMarkup(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("<!DOCTYPE html><html><head><script src=\"/cdn-cgi/challenge-platform/h/b/orchestrate/chl_page/v1\"></script>"))
	}))
	defer remote.Close()
	if _, err := openListTestClient(remote).Get(context.Background(), "/Example Work/track.mp3"); !errors.Is(err, ErrChallenged) {
		t.Fatalf("markup challenge classification %v", err)
	}
}

func TestOpenListHTMLRefusalWithoutChallengeMarkersStaysRestricted(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("<html><body>permission denied</body></html>"))
	}))
	defer remote.Close()
	_, err := openListTestClient(remote).Get(context.Background(), "/Example Work/track.mp3")
	if !errors.Is(err, ErrRestricted) || errors.Is(err, ErrChallenged) {
		t.Fatalf("html refusal classification %v", err)
	}
}

func TestOpenListChallengeOnRateLimitIsNotRetried(t *testing.T) {
	requests := atomic.Int32{}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Cf-Mitigated", "challenge")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer remote.Close()
	if _, err := openListTestClient(remote).ListPage(context.Background(), "/Example Work", 1, 100); !errors.Is(err, ErrChallenged) {
		t.Fatalf("rate-limited challenge classification %v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("rate-limited challenge retried: %d", requests.Load())
	}
}
