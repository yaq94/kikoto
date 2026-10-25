package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yexca/kikoto/backend/internal/buildinfo"
	"github.com/yexca/kikoto/backend/internal/config"
	"github.com/yexca/kikoto/backend/internal/storage"
)

func TestSourceCatalogLifecycleRestartKeepsCandidatesAndIsolatesSources(t *testing.T) {
	var mode atomic.Int32
	remote := func(second bool) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var in struct {
				Path string `json:"path"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			if !second && mode.Load() == 2 {
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 500})
				return
			}
			name := "Original Work"
			if second {
				name = "Other Work"
			} else if mode.Load() == 1 {
				name = "Candidate Work"
			}
			items := []map[string]any{{"name": "track.mp3", "size": 100}}
			if in.Path == "/" {
				items = []map[string]any{{"name": name, "is_dir": true}}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": len(items)}})
		}))
	}
	a, b := remote(false), remote(true)
	defer a.Close()
	defer b.Close()
	filename := filepath.Join(t.TempDir(), "library.db")
	cfg := config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", DatabasePath: filename, DataRoot: t.TempDir(), CacheRoot: t.TempDir()}
	var entryID, workID, otherID float64
	for boot := 0; boot < 3; boot++ {
		db, err := storage.Open(filename)
		if err != nil {
			t.Fatal(err)
		}
		if err = storage.MigrateFS(db, completeMigrationFS(t), buildinfo.Version); err != nil {
			t.Fatal(err)
		}
		s := NewServer(db, cfg)
		if boot == 0 {
			createTestAdministrator(t, s, "synthetic-user", "synthetic-password")
			createAccountManagedTestUser(t, s, "synthetic-listener", "synthetic-password")
		}
		h := s.Routes()
		app := httptest.NewServer(h)
		t.Cleanup(func() { app.Close(); _ = s.Shutdown(context.Background()); _ = db.Close() })
		cookie := workReferenceLogin(t, app, "synthetic-user")
		listener := workReferenceLogin(t, app, "synthetic-listener")
		if boot == 0 {
			for i, url := range []string{a.URL, b.URL} {
				var source map[string]any
				workReferenceRequest(t, app, cookie, http.MethodPost, "/api/file-sources", mustJSON(map[string]any{"displayName": "Example Remote " + strconv.Itoa(i+1), "sourceType": "openlist", "enabled": true, "endpoint": map[string]any{"apiUrl": url, "baseUrl": url}}), 201, &source)
			}
		}
		f := catalogIndexFixture{s, h, cookie, "/api/remote-sources/1"}
		g := catalogIndexFixture{s, h, cookie, "/api/remote-sources/2"}
		switch boot {
		case 0:
			f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
			f.drain(t)
			g.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
			g.drain(t)
			entryID = f.request(t, http.MethodGet, "/works", "", 200)["works"].([]any)[0].(map[string]any)["catalogEntryId"].(float64)
			otherID = g.request(t, http.MethodGet, "/works", "", 200)["works"].([]any)[0].(map[string]any)["catalogEntryId"].(float64)
			var state map[string]any
			workReferenceRequest(t, app, cookie, http.MethodPatch, "/api/source-catalog/"+strconv.Itoa(int(entryID))+"/user-state", `{"rating":4,"note":"Example owner note"}`, 200, &state)
			workID = state["workId"].(float64)
			workReferenceRequest(t, app, listener, http.MethodPatch, "/api/source-catalog/"+strconv.Itoa(int(entryID))+"/user-state", `{"rating":2,"note":"Example private note"}`, 200, nil)
			mode.Store(1)
			f.request(t, http.MethodPost, "/catalog/index", `{}`, 202)
			f.drain(t)
		case 2:
			mode.Store(0)
			f.request(t, http.MethodPost, "/catalog/index", `{}`, 202)
			f.drain(t)
		}
		base := "/api/source-catalog/" + strconv.Itoa(int(entryID))
		var detail map[string]any
		workReferenceRequest(t, app, cookie, http.MethodGet, base, "", 200, &detail)
		if detail["workId"] != workID {
			t.Fatal("restart changed the evaluation object")
		}
		assertReview(t, detail, float64(4), "Example owner note")
		if strings.Contains(mustJSON(detail), "Example private note") {
			t.Fatal("candidate response exposed another user's note")
		}
		candidates := detail["relocationCandidates"].([]any)
		if boot < 2 && (len(candidates) != 1 || candidates[0].(map[string]any)["reason"] != "insufficient_evidence") {
			t.Fatalf("unconfirmed relocation lost across restart: %#v", detail)
		}
		if boot == 2 && (len(candidates) != 0 || detail["status"] != "available") {
			t.Fatalf("restored path retained stale candidates: %#v", detail)
		}
		if boot == 1 {
			mode.Store(2)
			f.request(t, http.MethodPost, "/catalog/index", `{}`, 202)
			f.drain(t)
		}
		var other map[string]any
		workReferenceRequest(t, app, cookie, http.MethodGet, "/api/source-catalog/"+strconv.Itoa(int(otherID)), "", 200, &other)
		if other["status"] != "available" {
			t.Fatal("one source changed another source's status")
		}
		if boot == 2 {
			workReferenceRequest(t, app, cookie, http.MethodDelete, "/api/file-sources/1", "", 200, nil)
			workReferenceRequest(t, app, cookie, http.MethodGet, base, "", 200, &detail)
			if detail["status"] != "missing" {
				t.Fatal("removed source was presented as available")
			}
			assertReview(t, detail, float64(4), "Example owner note")
			workReferenceRequest(t, app, listener, http.MethodGet, base, "", 200, &detail)
			assertReview(t, detail, float64(2), "Example private note")
			workReferenceRequest(t, app, cookie, http.MethodGet, "/api/works/"+strconv.Itoa(int(workID))+"?includeMedia=false", "", 200, nil)
			for _, viewer := range []*http.Cookie{cookie, listener} {
				var library struct {
					Works []struct {
						ID float64 `json:"id"`
					} `json:"works"`
				}
				workReferenceRequest(t, app, viewer, http.MethodGet, "/api/works", "", 200, &library)
				found := false
				for _, work := range library.Works {
					if work.ID == workID {
						found = true
					}
				}
				if !found {
					t.Fatal("source removal hid the reviewed work from the library")
				}
			}
		}
		app.Close()
		if err = s.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err = db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
