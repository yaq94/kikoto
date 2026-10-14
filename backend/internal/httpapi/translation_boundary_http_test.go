package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTranslationConfigurationRejectsInvalidBodiesAndURLsAtomically(t *testing.T) {
	_, host, admin, _ := translationHTTPFixture(t)
	const path = "/api/settings/translation"
	translationRequest(t, host, admin, "PATCH", path, `{"baseUrl":"https://translation.example/v1","model":"saved-model"}`, 200)
	credentialBody, _ := json.Marshal(map[string]string{"baseUrl": translationCredentialURL("translation.example.invalid")})
	for _, body := range []string{
		`{"baseUrl":"file:///private","model":"bad-model"}`, string(credentialBody),
		`{"baseUrl":"https://translation.example/v1?api_key=synthetic-secret"}`, `{"baseUrl":"https://translation.example/v1#fragment"}`,
		`{"model":""}`, `{"model":" model "}`, `{"timeoutSeconds":0}`, `{"timeoutSeconds":61}`, `{"apiKey":"invalid\r\nheader"}`,
		`{"model":"bad-model"} {}`, `null`, `{"unknown":true}`, `{"apiKey":"` + strings.Repeat("a", 17000) + `"}`,
	} {
		result := translationRequest(t, host, admin, "PATCH", path, body, 400)
		if result["code"] != "translation_invalid_config" {
			t.Fatalf("unexpected error: %#v", result)
		}
		if got := translationRequest(t, host, admin, "GET", path, "", 200); got["model"] != "saved-model" {
			t.Fatalf("invalid patch partially applied: %#v", got)
		}
	}
}

func translationCredentialURL(host string) string {
	fixture := url.URL{Scheme: "http", Host: host, Path: "/v1", User: url.UserPassword("synthetic-user", "synthetic-password")}
	return fixture.String()
}

func TestTranslationTrialRejectsRedirectsWithoutForwardingCredentials(t *testing.T) {
	var escaped atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { escaped.Add(1); w.WriteHeader(200) }))
	defer target.Close()
	var destination string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			escaped.Add(1)
		}
		w.Header().Set("Location", destination)
		w.WriteHeader(307)
	}))
	defer upstream.Close()
	_, host, admin, _ := translationHTTPFixture(t)
	settings, _ := json.Marshal(map[string]any{"baseUrl": upstream.URL + "/v1", "apiKey": "synthetic-secret"})
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", string(settings), 200)
	for _, location := range []string{target.URL + "/steal", upstream.URL + "/same-origin", "http://127.0.0.1:1/private", "http://192.0.2.1/reserved", translationCredentialURL("unconfigured.example.invalid")} {
		destination = location
		result := translationRequest(t, host, admin, "POST", "/api/settings/translation/test", `{"text":"Hello.","targetLanguage":"en"}`, 422)
		if result["code"] != "translation_redirect_blocked" {
			t.Fatalf("unexpected redirect result: %#v", result)
		}
	}
	if escaped.Load() != 0 {
		t.Fatalf("redirect targets received %d requests", escaped.Load())
	}
}

type translationResolver struct {
	calls   atomic.Int32
	address string
}

func (r *translationResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	if r.calls.Add(1) > 1 {
		return []net.IPAddr{{IP: net.ParseIP("0.0.0.0")}}, nil
	}
	return []net.IPAddr{{IP: net.ParseIP(r.address)}}, nil
}

func TestTranslationTrialPinsConfiguredDNSAndRejectsUnusableAddresses(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "192.0.2.1", "0.0.0.0", net.IP{224, 0, 0, 1}.String()} {
		t.Run(address, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"Hello."},"finish_reason":"stop"}]}`)
			}))
			defer upstream.Close()
			s, host, admin, _ := translationHTTPFixture(t)
			resolver := &translationResolver{address: address}
			dials := make(chan string, 2)
			s.translationNetwork.Resolver = resolver
			s.translationNetwork.DialContext = func(ctx context.Context, network, endpoint string) (net.Conn, error) {
				dials <- endpoint
				var dialer net.Dialer
				return dialer.DialContext(ctx, network, strings.TrimPrefix(upstream.URL, "http://"))
			}
			u, _ := url.Parse(upstream.URL)
			settings, _ := json.Marshal(map[string]any{"baseUrl": "http://model.example:" + u.Port() + "/v1"})
			translationRequest(t, host, admin, "PATCH", "/api/settings/translation", string(settings), 200)
			if address == "127.0.0.1" || address == "192.0.2.1" {
				translationRequest(t, host, admin, "POST", "/api/settings/translation/test", `{"text":"Hello.","targetLanguage":"en"}`, 200)
				if got := <-dials; got != net.JoinHostPort(address, u.Port()) {
					t.Fatalf("dialed %s instead of validated address", got)
				}
			} else {
				result := translationRequest(t, host, admin, "POST", "/api/settings/translation/test", `{"text":"Hello.","targetLanguage":"en"}`, 422)
				if result["code"] != "translation_destination_blocked" {
					t.Fatalf("unexpected address result: %#v", result)
				}
				if len(dials) != 0 {
					t.Fatal("unusable address reached dialer")
				}
			}
			if resolver.calls.Load() != 1 {
				t.Fatalf("resolved %d times", resolver.calls.Load())
			}
		})
	}
}

func TestTranslationTrialBoundsConcurrencyAndCancelsOnShutdown(t *testing.T) {
	started, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-started:
		default:
			close(started)
		}
		select {
		case <-r.Context().Done():
			select {
			case <-cancelled:
			default:
				close(cancelled)
			}
		case <-release:
		}
	}))
	defer upstream.Close()
	defer close(release)
	s, host, admin, _ := translationHTTPFixture(t)
	settings, _ := json.Marshal(map[string]any{"baseUrl": upstream.URL, "timeoutSeconds": 3})
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", string(settings), 200)
	finished := make(chan *http.Response, 1)
	req, _ := http.NewRequest("POST", host.URL+"/api/settings/translation/test", strings.NewReader(`{"text":"Hello.","targetLanguage":"en"}`))
	req.AddCookie(admin)
	go func() { res, _ := host.Client().Do(req); finished <- res }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream did not start")
	}
	busy := translationRequest(t, host, admin, "POST", "/api/settings/translation/test", `{"text":"Hello.","targetLanguage":"en"}`, 429)
	if busy["code"] != "translation_busy" {
		t.Fatalf("unexpected busy error: %#v", busy)
	}
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel upstream")
	}
	res := <-finished
	if res == nil {
		t.Fatal("trial did not return a response")
	}
	defer func() { _ = res.Body.Close() }()
	var result map[string]any
	if err := json.NewDecoder(res.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result["code"] != "translation_cancelled" {
		t.Fatalf("unexpected shutdown result: %#v", result)
	}
}
