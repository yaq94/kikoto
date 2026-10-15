package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"testing"

	"github.com/yexca/kikoto/backend/internal/config"
)

// The independent expected set includes unregistered audio, format children and
// a work deeper than the finite catalog scan. Archives and subtitles are absent.
func TestSourceCatalogIndexCoversCompletePagedTreeWithoutRegistration(t *testing.T) {
	dir := func(name string) map[string]any { return map[string]any{"name": name, "is_dir": true} }
	audio := func(name string) map[string]any { return map[string]any{"name": name, "size": 100} }
	tree := map[string][]map[string]any{
		"/":                                    {dir("Partition A"), dir("Partition B"), dir("Partition C")},
		"/Partition A":                         {dir("RJ00000001"), dir("Example Numberless"), dir("Archives"), dir("Subtitles")},
		"/Partition A/RJ00000001":              {audio("01.mp3"), audio("02.wav"), audio("captions.srt")},
		"/Partition A/Example Numberless":      {dir("MP3"), dir("FLAC")},
		"/Partition A/Example Numberless/MP3":  {audio("original.mp3")},
		"/Partition A/Example Numberless/FLAC": {audio("original.flac")},
		"/Partition A/Archives":                {audio("only.zip")},
		"/Partition A/Subtitles":               {audio("only.srt")},
		"/Partition B":                         {dir("Example Attached")},
		"/Partition B/Example Attached":        {audio("track.ogg"), audio("sub.srt"), audio("extra.zip")},
	}
	deep := "/Partition C"
	for range 10 {
		tree[deep] = []map[string]any{dir("Folder")}
		deep += "/Folder"
	}
	tree[deep] = []map[string]any{dir("Example Deep")}
	tree[deep+"/Example Deep"] = []map[string]any{audio("track.opus")}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/fs/list" {
			t.Errorf("unexpected metadata/media request %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		var input struct {
			Path     string `json:"path"`
			Page     int    `json:"page"`
			PerPage  int    `json:"per_page"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
			return
		}
		if input.Password != "" {
			t.Error("anonymous scan sent a password")
		}
		items, ok := tree[input.Path]
		if !ok {
			t.Errorf("unexpected directory %q", input.Path)
			http.NotFound(w, r)
			return
		}
		start := min(len(items), (input.Page-1)*input.PerPage)
		end := min(len(items), start+input.PerPage)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items[start:end], "total": len(items)}})
	}))
	defer upstream.Close()
	db := openMigratedTestDB(t)
	s := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	defer func() { _ = s.Shutdown(context.Background()) }()
	createTestAdministrator(t, s, "synthetic-user", "synthetic-password")
	h := s.Routes()
	cookie := loginTestSession(t, h, "synthetic-user", "synthetic-password")
	input := mustJSON(map[string]any{"displayName": "Example Remote A", "sourceType": "openlist", "enabled": true, "config": map[string]any{"openListRoots": []string{"/Partition A"}, "openListIntervalMs": 100}, "endpoint": map[string]any{"apiUrl": upstream.URL, "baseUrl": upstream.URL}})
	source := reviewRequest(t, h, cookie, http.MethodPost, "/api/file-sources", input, 201)
	base := "/api/remote-sources/" + strconv.Itoa(int(source["id"].(float64)))
	started := reviewRequest(t, h, cookie, http.MethodPost, base+"/catalog/index", `{"pageSize":2,"pagesPerBatch":2,"intervalMs":100}`, 202)
	joined := reviewRequest(t, h, cookie, http.MethodPost, base+"/catalog/index", `{}`, 202)
	if started["runId"] != joined["runId"] {
		t.Fatal("duplicate scan was created")
	}
	for n := 0; n < 80; n++ {
		if err := s.runNextQueuedWorkflowJob(context.Background()); err != nil {
			t.Fatal(err)
		}
		status := reviewRequest(t, h, cookie, http.MethodGet, base+"/catalog/index", "", 200)
		if status["status"] == "complete" {
			break
		}
		if n == 79 {
			t.Fatalf("scan did not complete: %#v", status)
		}
	}
	list := reviewRequest(t, h, cookie, http.MethodGet, base+"/works", "", 200)
	titles := []string{}
	for _, raw := range list["works"].([]any) {
		entry := raw.(map[string]any)
		if entry["workId"] != nil {
			t.Fatal("scan registered a work")
		}
		titles = append(titles, entry["title"].(string))
		id := strconv.Itoa(int(entry["catalogEntryId"].(float64)))
		reviewRequest(t, h, cookie, http.MethodGet, "/api/source-catalog/"+id+"/tracks", "", 200)
	}
	slices.Sort(titles)
	if !slices.Equal(titles, []string{"Example Attached", "Example Deep", "Example Numberless", "RJ00000001"}) {
		t.Fatalf("qualified set: %v", titles)
	}
	library := reviewRequest(t, h, cookie, http.MethodGet, "/api/works", "", 200)
	if library["total"] != float64(0) {
		t.Fatal("discovery registered library metadata")
	}
}
