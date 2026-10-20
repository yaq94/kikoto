package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/yexca/kikoto/backend/internal/config"
)

// A split preview read must not take SQLite's write lock. The production DSN
// starts every non-read-only transaction with BEGIN IMMEDIATE, so a GET that
// omits ReadOnly blocks the catalog publisher and every other writer for the
// busy timeout while it only reads a snapshot.
func TestSharedSplitReadDoesNotWaitForTheWriteLock(t *testing.T) {
	pool := openMigratedTestDBWithProductionPool(t)
	path := workMatchingDatabasePath(t, pool)
	server := NewServer(pool, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-editor", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	defer func() { _ = server.Shutdown(context.Background()) }()
	createTestAdministrator(t, server, "synthetic-editor", "synthetic-password")
	handler := server.Routes()
	cookie := loginTestSession(t, handler, "synthetic-editor", "synthetic-password")

	holder := openWorkMatchingWriteDB(t, path, 1)
	held, err := holder.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin lock holder: %v", err)
	}
	defer func() { _ = held.Rollback() }()
	if _, err = held.Exec(`UPDATE work_matching_control SET status='held' WHERE id=1`); err != nil {
		t.Fatalf("hold write lock: %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/shared-splits/1", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	start := time.Now()
	handler.ServeHTTP(response, request)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("split read waited %v for the write lock", elapsed)
	}
	if response.Code != http.StatusNotFound && response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}
