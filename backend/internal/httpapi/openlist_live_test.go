package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yexca/kikoto/backend/internal/openlist"
	"github.com/yexca/kikoto/backend/internal/proxyconfig"
)

// This opt-in sample reads two 32-byte ranges of ONE explicitly supplied public
// anonymous audio file. It never discovers directories or downloads a full file.
func TestOpenListLiveRangeSample(t *testing.T) {
	endpoint, fullPath := os.Getenv("KIKOTO_OPENLIST_LIVE_URL"), os.Getenv("KIKOTO_OPENLIST_LIVE_FILE")
	if endpoint == "" || fullPath == "" {
		t.Skip("set KIKOTO_OPENLIST_LIVE_URL and KIKOTO_OPENLIST_LIVE_FILE for one authorized anonymous audio sample")
	}
	if completed, err := runOpenListRangeSample(t, endpoint, fullPath, os.Getenv("KIKOTO_OPENLIST_LIVE_PROXY_CONFIG")); err != nil {
		t.Fatal(err)
	} else if completed != 2 {
		t.Fatal("both media ranges are required")
	}
}

// runOpenListRangeSample is shared by the opt-in LIVE and offline contract tests.
// Proxy JSON exists only in the disposable app's Settings/SQLite and is never logged.
func runOpenListRangeSample(t *testing.T, endpoint, fullPath, proxyJSON string) (int, error) {
	t.Helper()
	// This runner is serial: protected application errors can contain raw URLs.
	// Emit only the explicit sanitized evidence below and restore the caller on return.
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer slog.SetDefault(previousLogger)
	if !openlist.ValidRoot(fullPath) || openlist.Kind(fullPath, false) != "audio" {
		return 0, errors.New("sample must be one absolute audio-file path")
	}
	server, handler, cookie := openListBoundaryApp(t, endpoint)
	app := httptest.NewServer(handler)
	defer app.Close()
	if proxyJSON != "" {
		if err := configureOpenListLiveProxy(app, cookie, proxyJSON); err != nil {
			return 0, err
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	route, err := server.resolveProxyRoute(ctx, proxyconfig.ScopeRemote, 7)
	if err != nil {
		return 0, errors.New("stage=configuration category=proxy_unavailable")
	}
	routeKind := "direct"
	if len(route.proxies) > 0 {
		routeKind = "proxy"
	}
	routeEvidence := fmt.Sprintf("route=%s proxies=%d direct_fallback=%t", routeKind, len(route.proxies), route.directFallback)
	t.Log(routeEvidence)
	source, err := server.loadRemoteSourceForUse(ctx, 7)
	if err != nil {
		return 0, errors.New("stage=configuration category=source_unavailable")
	}
	// Keep the production default pacing even though normal boundary fixtures are faster.
	if _, err = server.db.Exec(`UPDATE file_source SET config_json='{}' WHERE id=7`); err != nil {
		return 0, errors.New("stage=configuration category=storage")
	}
	source.Config.OpenListIntervalMS = nil
	client := server.sourceClient(source, 30*time.Second, sourceRequestOpenList)
	observation := &openListLiveTransport{base: client.Transport}
	client.Transport = observation
	object, err := openlist.NewClient(source.Endpoint.APIURL, client).Get(ctx, fullPath)
	if err != nil {
		return 0, fmt.Errorf("stage=metadata status=%d category=%s challenge=%t %s; no media read performed", observation.status, openListLiveErrorCategory(err), observation.challenge, routeEvidence)
	}
	if object.Size < 2048 {
		return 0, errors.New("sample is too short for the requested offsets")
	}
	if _, err = server.db.Exec(`UPDATE source_catalog_entry SET root_path=? WHERE id=1`, path.Dir(fullPath)); err != nil {
		return 0, errors.New("stage=configuration category=storage")
	}
	if _, err = server.db.Exec(`DELETE FROM source_catalog_file WHERE catalog_entry_id=1`); err != nil {
		return 0, errors.New("stage=configuration category=storage")
	}
	if _, err = server.db.Exec(`INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES(1,?,?,'audio',?)`, path.Base(fullPath), path.Base(fullPath), object.Size); err != nil {
		return 0, errors.New("stage=configuration category=storage")
	}
	identity := sha256.Sum256([]byte(endpoint + "\x00" + fullPath))
	for index, header := range []string{"bytes=0-31", "bytes=1024-1055"} {
		requestCtx, stop := context.WithTimeout(ctx, 30*time.Second)
		req, requestErr := http.NewRequestWithContext(requestCtx, http.MethodGet, app.URL+"/api/source-catalog/1/media?path="+url.QueryEscape(path.Base(fullPath)), nil)
		if requestErr != nil {
			stop()
			return index, errors.New("sample request could not be created")
		}
		req.AddCookie(cookie)
		req.Header.Set("Range", header)
		response, requestErr := app.Client().Do(req)
		if requestErr != nil {
			stop()
			return index, fmt.Errorf("stage=media status=0 category=%s %s", openListLiveErrorCategory(requestErr), routeEvidence)
		}
		// Refuse a full response before consuming its body. Closing the HTTP client
		// response also cancels the application's bounded upstream stream.
		var body []byte
		if response.StatusCode == http.StatusPartialContent {
			body, requestErr = io.ReadAll(io.LimitReader(response.Body, 33))
		}
		_ = response.Body.Close()
		stop()
		prefix := strings.Replace(header, "=", " ", 1) + "/"
		total, totalErr := strconv.ParseInt(strings.TrimPrefix(response.Header.Get("Content-Range"), prefix), 10, 64)
		if requestErr != nil || response.StatusCode != http.StatusPartialContent || len(body) != 32 || !strings.HasPrefix(response.Header.Get("Content-Range"), prefix) || totalErr != nil || total < 1056 {
			return index, fmt.Errorf("stage=media status=%d category=range_response bytes=%d %s", response.StatusCode, len(body), routeEvidence)
		}
		digest := sha256.Sum256(body)
		t.Logf("sample=%s request=%s status=%d bytes=%d sha256=%s", hex.EncodeToString(identity[:]), header, response.StatusCode, len(body), hex.EncodeToString(digest[:]))
	}
	return 2, nil
}

func configureOpenListLiveProxy(app *httptest.Server, cookie *http.Cookie, raw string) error {
	var payload proxySettingsPayload
	if len(raw) > 64<<10 || json.Unmarshal([]byte(raw), &payload) != nil {
		return errors.New("stage=configuration category=invalid_proxy_json")
	}
	body, err := json.Marshal(map[string]any{"proxy": payload})
	if err != nil {
		return errors.New("stage=configuration category=invalid_proxy_json")
	}
	req, err := http.NewRequest(http.MethodPatch, app.URL+"/api/settings", strings.NewReader(string(body)))
	if err != nil {
		return errors.New("stage=configuration category=request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	response, err := app.Client().Do(req)
	if err != nil {
		return errors.New("stage=configuration category=transport")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("stage=configuration status=%d category=proxy_rejected", response.StatusCode)
	}
	return nil
}

type openListLiveTransport struct {
	base      http.RoundTripper
	status    int
	challenge bool
}

func (transport *openListLiveTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := transport.base.RoundTrip(req)
	if response != nil {
		transport.status = response.StatusCode
		transport.challenge = strings.EqualFold(response.Header.Get("Cf-Mitigated"), "challenge")
	}
	return response, err
}

func openListLiveErrorCategory(err error) string {
	var network net.Error
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &network) && network.Timeout():
		return "timeout"
	case errors.Is(err, openlist.ErrRestricted):
		return "restricted"
	case errors.Is(err, openlist.ErrLimit):
		return "limit_exceeded"
	case errors.Is(err, openlist.ErrInvalid):
		return "invalid_response"
	case errors.Is(err, openlist.ErrUnavailable):
		return "unavailable"
	default:
		return "transport"
	}
}
