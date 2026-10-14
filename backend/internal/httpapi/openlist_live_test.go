package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/yexca/kikoto/backend/internal/openlist"
)

// This opt-in sample reads two 32-byte ranges of ONE explicitly supplied public
// anonymous audio file. It never discovers directories or downloads a full file.
func TestOpenListLiveRangeSample(t *testing.T) {
	endpoint, fullPath := os.Getenv("KIKOTO_OPENLIST_LIVE_URL"), os.Getenv("KIKOTO_OPENLIST_LIVE_FILE")
	if endpoint == "" || fullPath == "" {
		t.Skip("set KIKOTO_OPENLIST_LIVE_URL and KIKOTO_OPENLIST_LIVE_FILE for one authorized anonymous audio sample")
	}
	if !openlist.ValidRoot(fullPath) || openlist.Kind(fullPath, false) != "audio" {
		t.Fatal("sample must be one absolute audio-file path")
	}
	server, handler, cookie := openListBoundaryApp(t, endpoint)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source, err := server.loadRemoteSourceForUse(ctx, 7)
	if err != nil {
		t.Fatal("source configuration failed")
	}
	// Keep the production default pacing even though normal boundary fixtures are faster.
	if _, err = server.db.Exec(`UPDATE file_source SET config_json='{}' WHERE id=7`); err != nil {
		t.Fatal(err)
	}
	source.Config.OpenListIntervalMS = nil
	object, err := server.openListClient(source).Get(ctx, fullPath)
	if err != nil {
		t.Fatal("anonymous file metadata was refused or failed; no media read performed")
	}
	if object.Size < 2048 {
		t.Fatal("sample is too short for the requested offsets")
	}
	if _, err = server.db.Exec(`UPDATE source_catalog_entry SET root_path=? WHERE id=1;
		DELETE FROM source_catalog_file WHERE catalog_entry_id=1;
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES(1,?,?,'audio',?)`, path.Dir(fullPath), path.Base(fullPath), path.Base(fullPath), object.Size); err != nil {
		t.Fatal(err)
	}
	identity := sha256.Sum256([]byte(endpoint + "\x00" + fullPath))
	app := httptest.NewServer(handler)
	defer app.Close()
	for _, header := range []string{"bytes=0-31", "bytes=1024-1055"} {
		requestCtx, stop := context.WithTimeout(ctx, 30*time.Second)
		req, requestErr := http.NewRequestWithContext(requestCtx, http.MethodGet, app.URL+"/api/source-catalog/1/media?path="+url.QueryEscape(path.Base(fullPath)), nil)
		if requestErr != nil {
			stop()
			t.Fatal("sample request could not be created")
		}
		req.AddCookie(cookie)
		req.Header.Set("Range", header)
		response, requestErr := app.Client().Do(req)
		if requestErr != nil {
			stop()
			t.Fatal("sample media request failed")
		}
		// Refuse a full response before consuming its body. Closing the HTTP client
		// response also cancels the application's bounded upstream stream.
		var body []byte
		if response.StatusCode == http.StatusPartialContent {
			body, requestErr = io.ReadAll(io.LimitReader(response.Body, 33))
		}
		_ = response.Body.Close()
		stop()
		if requestErr != nil || response.StatusCode != http.StatusPartialContent || len(body) != 32 || !strings.HasPrefix(response.Header.Get("Content-Range"), strings.Replace(header, "=", " ", 1)+"/") {
			t.Fatalf("range sample failed: status=%d bytes=%d", response.StatusCode, len(body))
		}
		digest := sha256.Sum256(body)
		t.Logf("sample=%s request=%s status=%d content-range=%s content-type=%s bytes=%d sha256=%s", hex.EncodeToString(identity[:]), header, response.StatusCode, response.Header.Get("Content-Range"), response.Header.Get("Content-Type"), len(body), hex.EncodeToString(digest[:]))
	}
}
