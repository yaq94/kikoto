package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/yexca/kikoto/backend/internal/config"
	"github.com/yexca/kikoto/backend/internal/storage"
)

func TestContentTranslationCacheAndIndependentPreferencesSurviveRestart(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"Example translated title"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()
	root := t.TempDir()
	var db *sql.DB
	var app *Server
	var host *httptest.Server
	start := func(initial bool) {
		t.Helper()
		var err error
		db, err = storage.Open(filepath.Join(root, "app.db"))
		if err != nil {
			t.Fatal(err)
		}
		if err := storage.Migrate(db, filepath.Join("..", "..", "migrations")); err != nil {
			t.Fatal(err)
		}
		app = NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "root", CacheRoot: filepath.Join(root, "cache")})
		if initial {
			createTestAdministrator(t, app, "root", "root-password")
			createAccountManagedTestUser(t, app, "listener", "listener-password")
			if _, err := db.Exec(`INSERT INTO work (id,primary_code,title) VALUES (1,'RJ00000000','Example title')`); err != nil {
				t.Fatal(err)
			}
		}
		host = httptest.NewServer(app.Routes())
	}
	stop := func() {
		host.Close()
		if err := app.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
	start(true)
	admin := loginTestSession(t, app.Routes(), "root", "root-password")
	listener := loginTestSession(t, app.Routes(), "listener", "listener-password")
	cfg, _ := json.Marshal(map[string]any{"baseUrl": upstream.URL, "minFreeBytes": int64(1 << 40)})
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", string(cfg), 200)
	translationRequest(t, host, admin, "PATCH", "/api/auth/me/preferences", `{"metadataLanguages":["zh-cn","origin"]}`, 200)
	translationRequest(t, host, admin, "PATCH", "/api/auth/me", `{"uiLocale":"en"}`, 200)
	translationRequest(t, host, listener, "PATCH", "/api/auth/me/preferences", `{"metadataLanguages":["ja-jp","origin"]}`, 200)
	translationRequest(t, host, listener, "PATCH", "/api/auth/me", `{"uiLocale":"ko"}`, 200)
	translationRequest(t, host, listener, "PATCH", "/api/works/1/user-state", `{"rating":4,"note":"Private note"}`, 200)
	const body = `{"workId":1,"field":"title","text":"Example title","language":"zh-cn"}`
	state := translationRequest(t, host, admin, "GET", "/api/settings/translation/runtime", "", 200)
	if state["cacheStatus"] != "paused_low_disk" || state["cacheEntries"] != float64(0) {
		t.Fatalf("cold cache did not report low space: %#v", state)
	}
	paused := translationRequest(t, host, admin, "POST", "/api/content-translations", body, 200)
	if paused["cacheStatus"] != "paused_low_disk" {
		t.Fatalf("cold cache ignored low space: %#v", paused)
	}
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", `{"minFreeBytes":0}`, 200)
	stored := translationRequest(t, host, admin, "POST", "/api/content-translations", body, 200)
	if stored["cacheStatus"] != "stored" {
		t.Fatalf("cold cache did not resume storage: %#v", stored)
	}
	stop()
	start(false)
	defer stop()
	cached := translationRequest(t, host, admin, "POST", "/api/content-translations", body, 200)
	if cached["cacheStatus"] != "hit" || calls.Load() != 2 {
		t.Fatalf("restart lost cache: %#v; calls=%d", cached, calls.Load())
	}
	for _, user := range []struct {
		cookie       *http.Cookie
		language, ui string
	}{{admin, "zh-cn", "en"}, {listener, "ja-jp", "ko"}} {
		pref := translationRequest(t, host, user.cookie, "GET", "/api/auth/me/preferences", "", 200)
		if pref["metadataLanguages"].([]any)[0] != user.language {
			t.Fatalf("shared display preferences: %#v", pref)
		}
		me := translationRequest(t, host, user.cookie, "GET", "/api/auth/me", "", 200)
		if me["user"].(map[string]any)["uiLocale"] != user.ui {
			t.Fatal("UI preference changed")
		}
	}
	detail := translationRequest(t, host, listener, "GET", "/api/works/1?includeMedia=false", "", 200)
	if detail["personalRating"] != float64(4) || detail["personalNote"] != "Private note" || detail["id"] != float64(1) {
		t.Fatalf("cache changed durable state: %#v", detail)
	}
}
