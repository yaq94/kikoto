package httpapi

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestOpenListLiveFixtureUsesExplicitSourceProxyAndReadsBothRanges(t *testing.T) {
	var direct atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		direct.Add(1)
		http.Error(w, "synthetic direct refusal", http.StatusForbidden)
	}))
	defer origin.Close()
	var ranges atomic.Int32
	handler := openListSampleHandler(func() string { return origin.URL }, 's')
	proxy := newOpenListSampleProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/audio" {
			ranges.Add(1)
		}
		handler.ServeHTTP(w, r)
	}), nil)
	settings := proxySettingsBody(t, []map[string]any{customProxy("example", proxy.URL)}, map[string]any{
		"sources": map[string]any{"7": map[string]any{"mode": "proxy", "proxyIds": []string{"example"}}},
	})
	// The opt-in contract accepts the proxy object, not the complete Settings body.
	var envelope struct {
		Proxy json.RawMessage `json:"proxy"`
	}
	if err := json.Unmarshal([]byte(settings), &envelope); err != nil {
		t.Fatal(err)
	}
	completed, err := runOpenListRangeSample(t, origin.URL, "/Example Work/track.wav", string(envelope.Proxy))
	if err != nil {
		t.Fatal(err)
	}
	if completed != 2 || ranges.Load() != 2 || direct.Load() != 0 {
		t.Fatal("sample must read both ranges through the explicit source proxy")
	}
}

func TestOpenListLiveFixtureRejectsMetadataOnlySuccess(t *testing.T) {
	var endpoint string
	metadata := openListSampleHandler(func() string { return endpoint }, 's')
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/audio" {
			// A server ignoring Range must not turn a successful fs/get into LIVE success.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("synthetic full response"))
			return
		}
		metadata.ServeHTTP(w, r)
	}))
	defer origin.Close()
	endpoint = origin.URL
	completed, err := runOpenListRangeSample(t, endpoint, "/Example Work/track.wav", "")
	if completed != 0 || err == nil || !strings.Contains(err.Error(), "stage=media status=200 category=range_response bytes=0") {
		t.Fatal("successful metadata with a full media response must fail")
	}
}

func TestOpenListLiveFixtureRejectsInvalidProxyWithoutSourceRequests(t *testing.T) {
	var requests atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer origin.Close()
	completed, err := runOpenListRangeSample(t, origin.URL, "/Example Work/track.wav", `{"proxies":[{"kind":"custom","scheme":"ftp","host":"source.example.invalid","port":21,"password":"synthetic-password"}]}`)
	if completed != 0 || err == nil || err.Error() != "stage=configuration status=400 category=proxy_rejected" || requests.Load() != 0 {
		t.Fatal("invalid opt-in proxy must fail safely before contacting the source")
	}
}

func TestOpenListLiveFixtureReportsSafeTransportFailure(t *testing.T) {
	completed, err := runOpenListRangeSample(t, closedProxyURL(t), "/Example Work/track.wav", "")
	if completed != 0 || err == nil || !strings.Contains(err.Error(), "stage=metadata status=0 category=transport") || strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatal("transport failure must expose only its category, not its destination")
	}
}

func TestOpenListLiveFixtureDoesNotPrintProtectedApplicationLogs(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(previous) })
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{
			"name": "track.wav", "size": 4096, "raw_url": "http://127.0.0.2/synthetic-private-media",
		}})
	}))
	defer origin.Close()
	completed, err := runOpenListRangeSample(t, origin.URL, "/Example Work/track.wav", "")
	if completed != 0 || err == nil {
		t.Fatal("blocked media must fail")
	}
	if slog.Default() != logger {
		t.Fatal("LIVE runner did not restore the caller logger before returning")
	}
	if strings.Contains(logs.String(), "synthetic-private-media") || strings.Contains(logs.String(), "127.0.0.2") {
		t.Fatal("LIVE runner printed a protected application error")
	}
}

func TestOpenListLiveFixtureReportsSafeMetadataFailure(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cf-Mitigated", "challenge")
		http.Error(w, "synthetic-password https://source.example.invalid/private", http.StatusForbidden)
	}))
	defer origin.Close()
	completed, err := runOpenListRangeSample(t, origin.URL, "/Example Work/track.wav", "")
	if completed != 0 || err == nil {
		t.Fatal("metadata refusal must fail before media reads")
	}
	for _, expected := range []string{"stage=metadata", "status=403", "category=restricted", "challenge=true", "route=direct"} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("missing safe diagnostic %s", expected)
		}
	}
	for _, forbidden := range []string{origin.URL, "Example Work", "track.wav", "synthetic-password", "source.example.invalid"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatal("diagnostic exposed private request or response data")
		}
	}
}
