package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yexca/kikoto/backend/internal/config"
	"github.com/yexca/kikoto/backend/internal/kikoeru"
)

func TestSourceAvailabilityFreshnessAndFailureIsolation(t *testing.T) {
	var failing atomic.Bool
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failing.Load() {
			http.Error(w, "synthetic upstream failure", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(kikoeru.Work{ID: 1, SourceID: "RJ00000000", Title: "Example Work"})
	}))
	defer remote.Close()
	db := openMigratedTestDB(t)
	if _, err := db.Exec(`
		INSERT INTO work (id, primary_code, title) VALUES (1, 'RJ00000000', 'Example Work');
		INSERT INTO file_source (id, code, display_name, source_type) VALUES
		(1, 'example_remote_a', 'Example Remote A', 'kikoeru_compatible'),
		(2, 'example_remote_b', 'Example Remote B', 'kikoeru_compatible');
		INSERT INTO work_source_presence (work_id, file_source_id, presence_type, availability, last_checked_at)
		VALUES (1, 1, 'source', 'available', '2000-01-01 00:00:00');
	`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int{1, 2} {
		if _, err := db.Exec(`INSERT INTO file_source_endpoint (file_source_id, base_url, api_url) VALUES (?, ?, ?)`, id, remote.URL, remote.URL); err != nil {
			t.Fatal(err)
		}
	}
	server := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", CacheRoot: t.TempDir(), DataRoot: t.TempDir()})
	createTestAdministrator(t, server, "synthetic-user", "synthetic-password")
	handler := server.Routes()
	cookie := loginTestSession(t, handler, "synthetic-user", "synthetic-password")
	read := func() []any {
		t.Helper()
		return reviewRequest(t, handler, cookie, http.MethodGet, "/api/works/RJ00000000/source-availability", "", http.StatusOK)["sources"].([]any)
	}
	sources := read()
	first, second := sources[0].(map[string]any), sources[1].(map[string]any)
	if first["status"] != "available" || first["checkedAt"] != "2000-01-01T00:00:00Z" || second["status"] != "unknown" || second["checkedAt"] != "" {
		t.Fatalf("per-source freshness = %#v", sources)
	}
	for _, sourceID := range []string{"1", "2"} {
		result := reviewRequest(t, handler, cookie, http.MethodPost, "/api/works/RJ00000000/source-availability", `{"sourceId":`+sourceID+`}`, http.StatusOK)
		checked := result["sources"].([]any)[0].(map[string]any)
		if checked["status"] != "available" {
			t.Fatalf("check = %#v", checked)
		}
		if _, err := time.Parse(time.RFC3339, checked["checkedAt"].(string)); err != nil {
			t.Fatalf("check timestamp = %#v: %v", checked, err)
		}
	}
	failing.Store(true)
	reviewRequest(t, handler, cookie, http.MethodPost, "/api/works/RJ00000000/source-availability", `{"sourceId":2}`, http.StatusOK)
	sources = read()
	if sources[0].(map[string]any)["status"] != "available" || sources[1].(map[string]any)["status"] != "error" {
		t.Fatalf("failure crossed source boundary = %#v", sources)
	}
	if detail := reviewRequest(t, handler, cookie, http.MethodGet, "/api/works/1?includeMedia=false", "", http.StatusOK); detail["title"] != "Example Work" {
		t.Fatalf("known detail lost = %#v", detail)
	}
	failing.Store(false)
	reviewRequest(t, handler, cookie, http.MethodPost, "/api/works/RJ00000000/source-availability", `{"sourceId":2}`, http.StatusOK)
	if read()[1].(map[string]any)["status"] != "available" {
		t.Fatal("failed source did not recover")
	}
	reviewRequest(t, handler, nil, http.MethodPost, "/api/works/RJ00000000/source-availability", `{}`, http.StatusUnauthorized)
}
