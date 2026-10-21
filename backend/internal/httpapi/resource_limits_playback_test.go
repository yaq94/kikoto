package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// AC-25: a low-disk pause must stop background work without stopping playback
// or reads. The scan and the translation prefill share one disk probe, so this
// run observes both paused states while the same server still serves media
// bytes, the catalog listing and the personal note.
func TestLowDiskPausesScanAndPrefillWithoutStoppingPlayback(t *testing.T) {
	var modelCalls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modelCalls.Add(1)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Example translation"},"finish_reason":"stop"}]}`))
	}))
	defer model.Close()

	wav := []byte("0123456789abcdef")
	// The media URL must reuse the source's own endpoint host: outbound
	// requests to another private address are rejected by design.
	var endpoint atomic.Value
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base, _ := endpoint.Load().(string)
		switch r.URL.Path {
		case "/api/fs/list":
			var input struct {
				Path string `json:"path"`
			}
			_ = json.NewDecoder(r.Body).Decode(&input)
			items := []map[string]any{{"name": "Example", "type": 1}}
			if input.Path != "/" {
				items = []map[string]any{{"name": "published.wav", "type": 3, "size": len(wav)}}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": len(items)}})
		case "/api/fs/get":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"name": "track.wav", "size": len(wav), "raw_url": base + "/audio"}})
		case "/audio":
			w.Header().Set("Content-Type", "audio/wav")
			http.ServeContent(w, r, "track.wav", time.Unix(0, 0), bytes.NewReader(wav))
		default:
			http.NotFound(w, r)
		}
	})
	f := newCatalogIndexFixture(t, handler)
	var apiURL string
	if err := f.server.db.QueryRow(`SELECT api_url FROM file_source_endpoint WHERE file_source_id=1`).Scan(&apiURL); err != nil {
		t.Fatal(err)
	}
	endpoint.Store(apiURL)
	reviewRequest(t, f.handler, f.cookie, http.MethodPatch, "/api/settings/translation",
		mustJSON(map[string]any{"baseUrl": model.URL, "minFreeBytes": 0}), http.StatusOK)
	if _, err := f.server.db.Exec(`
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,availability_status)
			VALUES(91,1,'/Retained','Example retained card','available');
		INSERT INTO source_catalog_audio_root(file_source_id,generation,catalog_root) VALUES(1,900,'/Retained');
		INSERT INTO source_catalog_directory(file_source_id,path,generation,revision,partition_path,catalog_root,status,expected_total)
			VALUES(1,'/Retained',900,1,'/Retained','/Retained','complete',1);
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes)
			VALUES(91,'track.wav','Example track','audio',16);`); err != nil {
		t.Fatal(err)
	}
	reviewRequest(t, f.handler, f.cookie, http.MethodPatch, "/api/source-catalog/91/user-state", `{"rating":4,"note":"Example retained note"}`, http.StatusOK)

	f.server.catalogDiskSpace = func(string) (diskSpaceSnapshot, error) {
		return diskSpaceSnapshot{Available: 0}, nil
	}
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"minFreeBytes":0}`, http.StatusAccepted)
	progress := f.drain(t)
	if progress["status"] != "paused" || progress["pauseReason"] != "low_disk_space" || progress["indexedEntries"] != float64(0) {
		t.Fatalf("low disk did not pause the scan before publishing: %#v", progress)
	}
	if _, err := f.server.runTranslationPrefillBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, status, lastError := readPrefillState(t, f.server); status != "paused" || lastError != "paused_low_disk" {
		t.Fatalf("prefill state = %q/%q, want paused/paused_low_disk", status, lastError)
	}
	if modelCalls.Load() != 0 {
		t.Fatalf("model calls while paused = %d, want 0", modelCalls.Load())
	}

	// Reads keep working while both background passes are parked.
	target := reviewRequest(t, f.handler, f.cookie, http.MethodPost, "/api/source-catalog/91/playback", `{"paths":["track.wav"]}`, http.StatusOK)["targets"].([]any)[0].(map[string]any)
	stream := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/media/%.0f/stream", target["locationId"]), nil)
	stream.AddCookie(f.cookie)
	stream.Header.Set("Range", "bytes=2-5")
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, stream)
	if response.Code != http.StatusPartialContent || response.Body.String() != "2345" {
		t.Fatalf("paused background work broke media Range: %d %q", response.Code, response.Body.String())
	}
	if list := reviewRequest(t, f.handler, f.cookie, http.MethodGet, "/api/works", "", http.StatusOK); list["total"] != float64(1) {
		t.Fatalf("paused scan changed the catalog: %#v", list)
	}
	if tracks := reviewRequest(t, f.handler, f.cookie, http.MethodGet, "/api/source-catalog/91/tracks", "", http.StatusOK)["tracks"].([]any); len(tracks) != 1 {
		t.Fatalf("paused scan hid tracks: %#v", tracks)
	}
	assertReview(t, reviewRequest(t, f.handler, f.cookie, http.MethodGet, "/api/source-catalog/91", "", http.StatusOK), float64(4), "Example retained note")

	// Free space returns: both passes resume instead of reporting completion.
	f.server.catalogDiskSpace = func(string) (diskSpaceSnapshot, error) {
		return diskSpaceSnapshot{Available: 4 << 30}, nil
	}
	if _, err := f.server.runTranslationPrefillBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, status, lastError := readPrefillState(t, f.server); status != "running" || lastError != "" {
		t.Fatalf("prefill did not resume: %q/%q", status, lastError)
	}
	if modelCalls.Load() == 0 {
		t.Fatal("prefill resumed without calling the model")
	}
	f.request(t, http.MethodPost, "/catalog/index", `{"minFreeBytes":0}`, http.StatusAccepted)
	progress = f.drain(t)
	if progress["status"] != "complete" || progress["pauseReason"] != "" || progress["observedEntries"] == float64(0) {
		t.Fatalf("scan did not resume: %#v", progress)
	}
	if list := reviewRequest(t, f.handler, f.cookie, http.MethodGet, "/api/works", "", http.StatusOK); list["total"] != float64(1) {
		t.Fatalf("resumed scan dropped the retained card: %#v", list)
	}
	assertReview(t, reviewRequest(t, f.handler, f.cookie, http.MethodGet, "/api/source-catalog/91", "", http.StatusOK), float64(4), "Example retained note")
}
