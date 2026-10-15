package httpapi

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yexca/kikoto/backend/internal/config"
	"github.com/yexca/kikoto/backend/internal/storage"
)

// Pause after SQLite commits the final update, before ExecContext returns to
// the migration. The real database remains readable on its other connections.
// This forces the publication window without a production test hook.
type migrationCompletionConnector struct {
	base           driver.Driver
	dsn            string
	committed      chan struct{}
	release        chan struct{}
	beforeComplete func(context.Context)
}

func (c *migrationCompletionConnector) Driver() driver.Driver {
	return c.base
}

func (c *migrationCompletionConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.base.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &migrationCompletionConn{Conn: conn, gate: c}, nil
}

type migrationCompletionConn struct {
	driver.Conn
	gate *migrationCompletionConnector
}

func (c *migrationCompletionConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	completion := strings.Contains(query, "SET status = 'completed', phase = 'completed'")
	if completion && c.gate.beforeComplete != nil {
		c.gate.beforeComplete(ctx)
	}
	result, err := c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
	if err == nil && completion && c.gate.committed != nil {
		close(c.gate.committed)
		select {
		case <-c.gate.release:
		case <-ctx.Done():
		}
	}
	return result, err
}

func TestLibraryMigrationCompletionFailureKeepsMaintenanceAndRecovers(t *testing.T) {
	for _, failure := range []string{"database write", "cancelled"} {
		t.Run(failure, func(t *testing.T) {
			path, original := openRestartableMigrationDB(t)
			entered := make(chan struct{})
			release := make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			gate := &migrationCompletionConnector{
				base: original.Driver(), dsn: path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)",
				beforeComplete: func(ctx context.Context) {
					close(entered)
					select {
					case <-release:
					case <-ctx.Done():
					}
				},
			}
			if err := original.Close(); err != nil {
				t.Fatal(err)
			}
			db := sql.OpenDB(gate)
			t.Cleanup(func() { _ = db.Close() })
			seedLibraryMigrationCompletedScan(t, db)
			if failure == "database write" {
				if _, err := db.Exec(`CREATE TRIGGER reject_migration_completion BEFORE UPDATE OF status ON library_layout_migration
					WHEN NEW.status = 'completed' BEGIN SELECT RAISE(ABORT, 'synthetic completion write failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			cfg := config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", DataRoot: t.TempDir()}
			server := NewServer(db, cfg)
			t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
			if err := server.EnsureLocalSource(context.Background()); err != nil {
				t.Fatal(err)
			}
			createTestAdministrator(t, server, "synthetic-user", "synthetic-password")
			httpServer := httptest.NewServer(server.Routes())
			t.Cleanup(httpServer.Close)
			cookie := workReferenceLogin(t, httpServer, "synthetic-user")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			done := make(chan struct{})
			go func() {
				server.ResumeLibraryMigration(ctx)
				close(done)
			}()
			defer func() {
				unblock()
				cancel()
				<-done
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("migration did not attempt its final database update")
			}
			assertLibraryMigrationMaintenance(t, httpServer, cookie, true)
			if failure == "cancelled" {
				cancel()
			}
			unblock()
			<-done
			wantStatus := "failed"
			if failure == "cancelled" {
				wantStatus = "running"
			}
			var status layoutMigrationStatus
			workReferenceRequest(t, httpServer, cookie, http.MethodGet, "/api/library/migration", "", http.StatusOK, &status)
			if status.Status != wantStatus || status.Phase != "scan" {
				t.Fatalf("interrupted completion = %+v, want %s in scan phase", status, wantStatus)
			}
			assertLibraryMigrationMaintenance(t, httpServer, cookie, true)
			httpServer.Close()
			if err := server.Shutdown(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := storage.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			restarted := NewServer(reopened, cfg)
			t.Cleanup(func() { _ = restarted.Shutdown(context.Background()) })
			if err := restarted.PrepareLibraryLayout(context.Background()); err != nil {
				t.Fatal(err)
			}
			restartedHTTP := httptest.NewServer(restarted.Routes())
			t.Cleanup(restartedHTTP.Close)
			assertLibraryMigrationMaintenance(t, restartedHTTP, cookie, true)
			workReferenceRequest(t, restartedHTTP, cookie, http.MethodGet, "/api/library/migration", "", http.StatusOK, &status)
			if status.Status != wantStatus || status.Phase != "scan" {
				t.Fatalf("restart lost the recovery checkpoint: %+v", status)
			}
			if failure == "database write" {
				restarted.ResumeLibraryMigration(context.Background())
				assertLibraryMigrationMaintenance(t, restartedHTTP, cookie, true)
				if _, err := reopened.Exec(`DROP TRIGGER reject_migration_completion`); err != nil {
					t.Fatal(err)
				}
				workReferenceRequest(t, restartedHTTP, cookie, http.MethodPost, "/api/library/migration/retry", "", http.StatusAccepted, nil)
				waitForTestLibraryMigration(t, restarted)
			} else {
				restarted.ResumeLibraryMigration(context.Background())
			}
			workReferenceRequest(t, restartedHTTP, cookie, http.MethodGet, "/api/library/migration", "", http.StatusOK, &status)
			if status.Status != "completed" || status.Phase != "completed" {
				t.Fatalf("recovered completion = %+v", status)
			}
			assertLibraryMigrationMaintenance(t, restartedHTTP, cookie, false)
		})
	}
}

func assertLibraryMigrationMaintenance(t *testing.T, server *httptest.Server, cookie *http.Cookie, maintenance bool) {
	t.Helper()
	var public struct {
		Maintenance bool `json:"maintenance"`
	}
	workReferenceRequest(t, server, nil, http.MethodGet, "/api/library/migration/public", "", http.StatusOK, &public)
	if public.Maintenance != maintenance {
		t.Fatalf("public maintenance = %t, want %t", public.Maintenance, maintenance)
	}
	status := http.StatusOK
	if maintenance {
		status = http.StatusServiceUnavailable
	}
	workReferenceRequest(t, server, cookie, http.MethodGet, "/api/library/layout", "", status, nil)
}

func seedLibraryMigrationCompletedScan(t *testing.T, db *sql.DB) {
	t.Helper()
	saveTestLibraryMigration(t, db, layoutMigrationPlan{}, "scan")
	if _, err := db.Exec(`INSERT INTO workflow_run (id, workflow_code, display_name, status, trigger_type)
		VALUES (1, 'local_library_scan', 'Example Scan', 'succeeded', 'manual');
		UPDATE library_layout_migration SET scan_run_id = 1 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
}

func TestLibraryMigrationCompletionPublishesAfterMaintenanceEnds(t *testing.T) {
	path, original := openRestartableMigrationDB(t)
	gate := &migrationCompletionConnector{
		base: original.Driver(), dsn: path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)",
		committed: make(chan struct{}), release: make(chan struct{}),
	}
	if err := original.Close(); err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(gate)
	t.Cleanup(func() { _ = db.Close() })
	seedLibraryMigrationCompletedScan(t, db)
	server := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", DataRoot: t.TempDir()})
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	createTestAdministrator(t, server, "synthetic-user", "synthetic-password")
	detailStarted := make(chan struct{})
	routes := server.Routes()
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/library/migration" {
			close(detailStarted)
		}
		routes.ServeHTTP(w, r)
	}))
	t.Cleanup(httpServer.Close)
	cookie := workReferenceLogin(t, httpServer, "synthetic-user")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	release := sync.OnceFunc(func() { close(gate.release) })
	defer release()
	done := make(chan struct{})
	go func() {
		server.ResumeLibraryMigration(ctx)
		close(done)
	}()
	defer func() {
		release()
		cancel()
		<-done
	}()
	select {
	case <-gate.committed:
	case <-ctx.Done():
		t.Fatal("migration did not reach its final database update")
	}
	var public struct {
		Maintenance bool `json:"maintenance"`
	}
	workReferenceRequest(t, httpServer, nil, http.MethodGet, "/api/library/migration/public", "", http.StatusOK, &public)
	if !public.Maintenance {
		t.Fatal("maintenance ended before final publication")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, httpServer.URL+"/api/library/migration", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(cookie)
	type responseResult struct {
		response *http.Response
		err      error
	}
	responses := make(chan responseResult, 1)
	go func() {
		response, err := httpServer.Client().Do(request)
		responses <- responseResult{response, err}
	}()
	select {
	case <-detailStarted:
	case <-ctx.Done():
		t.Fatal("migration detail request did not reach the HTTP server")
	}
	select {
	case result := <-responses:
		if result.err != nil {
			t.Fatal(result.err)
		}
		defer func() { _ = result.response.Body.Close() }()
		var status layoutMigrationStatus
		if err := json.NewDecoder(result.response.Body).Decode(&status); err != nil {
			t.Fatal(err)
		}
		t.Fatalf("migration detail escaped final publication while maintenance is active: HTTP %d, status %q", result.response.StatusCode, status.Status)
	case <-time.After(100 * time.Millisecond):
	}
	release()
	result := <-responses
	if result.err != nil {
		t.Fatal(result.err)
	}
	defer func() { _ = result.response.Body.Close() }()
	var status layoutMigrationStatus
	if err := json.NewDecoder(result.response.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if result.response.StatusCode != http.StatusOK || status.Status != "completed" || status.Phase != "completed" {
		t.Fatalf("completed migration = HTTP %d, %+v", result.response.StatusCode, status)
	}
	workReferenceRequest(t, httpServer, nil, http.MethodGet, "/api/library/migration/public", "", http.StatusOK, &public)
	if public.Maintenance {
		t.Fatal("completed migration still reports maintenance")
	}
	workReferenceRequest(t, httpServer, cookie, http.MethodGet, "/api/library/layout", "", http.StatusOK, nil)
}
