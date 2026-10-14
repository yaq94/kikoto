package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/yexca/kikoto/backend/internal/config"
	"github.com/yexca/kikoto/backend/internal/storage"
)

func TestTranslationConfigurationAndTrialSurviveApplicationRestart(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-restart-secret" {
			t.Error("restarted application lost saved key")
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload["model"] != "restart-model" {
			t.Error("restarted application lost model")
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"今天天气真好。"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()
	path := filepath.Join(t.TempDir(), "restart.db")
	var db *sql.DB
	var server *Server
	var host *httptest.Server
	start := func(initial bool) {
		var err error
		db, err = storage.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := storage.Migrate(db, filepath.Join("..", "..", "migrations")); err != nil {
			t.Fatal(err)
		}
		server = NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "root"})
		if initial {
			createTestAdministrator(t, server, "root", "root-password")
		}
		host = httptest.NewServer(server.Routes())
	}
	stop := func() {
		host.Close()
		if err := server.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
	start(true)
	cookie := loginTestSession(t, server.Routes(), "root", "root-password")
	settings, _ := json.Marshal(map[string]any{"baseUrl": upstream.URL + "/v1", "apiKey": "synthetic-restart-secret", "model": "restart-model", "timeoutSeconds": 30, "disableThinking": false})
	translationRequest(t, host, cookie, "PATCH", "/api/settings/translation", string(settings), 200)
	translationRequest(t, host, cookie, "PATCH", "/api/auth/me", `{"uiLocale":"ja"}`, 200)
	before := translationRequest(t, host, cookie, "GET", "/api/settings/translation", "", 200)
	stop()
	start(false)
	defer stop()
	after := translationRequest(t, host, cookie, "GET", "/api/settings/translation", "", 200)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("restart changed configuration: before=%#v after=%#v", before, after)
	}
	result := translationRequest(t, host, cookie, "POST", "/api/settings/translation/test", `{"text":"The weather is nice today.","targetLanguage":"zh-Hans"}`, 200)
	if result["text"] != "今天天气真好。" {
		t.Fatalf("restarted trial result: %#v", result)
	}
	me := translationRequest(t, host, cookie, "GET", "/api/auth/me", "", 200)
	if me["user"].(map[string]any)["uiLocale"] != "ja" {
		t.Fatal("translation trial changed interface language")
	}
}
