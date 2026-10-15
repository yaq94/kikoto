package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yexca/kikoto/backend/internal/config"
)

func TestSourceAvailabilityOutboundBoundary(t *testing.T) {
	var escaped atomic.Int64
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { escaped.Add(1); w.WriteHeader(200) }))
	defer other.Close()
	for _, kind := range []string{"scheme", "credentials", "private_redirect", "oversized", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			started := make(chan struct{}, 2)
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case started <- struct{}{}:
				default:
				}
				switch kind {
				case "private_redirect":
					http.Redirect(w, r, other.URL+"/synthetic-token", http.StatusFound)
				case "oversized":
					_, _ = io.CopyN(w, zeroReader{}, (32<<20)+1)
				case "cancelled":
					<-r.Context().Done()
				default:
					http.Error(w, "unexpected request", 500)
				}
			}))
			defer remote.Close()
			endpoint := remote.URL
			if kind == "scheme" {
				endpoint = "ftp://source.example.invalid/synthetic-secret"
			}
			if kind == "credentials" {
				parsed, err := url.Parse(remote.URL)
				if err != nil {
					t.Fatal(err)
				}
				parsed.User = url.UserPassword("synthetic-user", "synthetic-secret")
				endpoint = parsed.String()
			}
			db := openMigratedTestDB(t)
			if _, err := db.Exec(`INSERT INTO work(id,primary_code,title) VALUES(1,'RJ00000000','Example Work');
			INSERT INTO file_source(id,code,display_name,source_type) VALUES(1,'example_remote','Example Remote','kikoeru_compatible');`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO file_source_endpoint(file_source_id,api_url,base_url,restrict_outbound_hosts) VALUES(1,?,?,1)`, endpoint, endpoint); err != nil {
				t.Fatal(err)
			}
			server := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", CacheRoot: t.TempDir(), DataRoot: t.TempDir()})
			createTestAdministrator(t, server, "synthetic-user", "synthetic-password")
			handler := server.Routes()
			cookie := loginTestSession(t, handler, "synthetic-user", "synthetic-password")
			request := httptest.NewRequest(http.MethodPost, "/api/works/RJ00000000/source-availability", strings.NewReader(`{"sourceId":1,"force":true}`))
			request.AddCookie(cookie)
			ctx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
			defer cancel()
			request = request.WithContext(ctx)
			if kind == "cancelled" {
				go func() {
					select {
					case <-started:
						cancel()
					case <-ctx.Done():
					}
				}()
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			body := response.Body.String()
			if strings.Contains(body, "synthetic-secret") || strings.Contains(body, remote.URL) || strings.Contains(body, other.URL) {
				t.Fatalf("public error leaked endpoint: %s", body)
			}
			if kind == "cancelled" {
				if response.Code < 400 {
					t.Fatalf("cancelled check succeeded: %d %s", response.Code, body)
				}
			} else if response.Code != http.StatusOK || !strings.Contains(body, `"status":"error"`) {
				t.Fatalf("unsafe check result: %d %s", response.Code, body)
			}
		})
	}
	if escaped.Load() != 0 {
		t.Fatalf("unconfigured private origin received %d requests", escaped.Load())
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }
