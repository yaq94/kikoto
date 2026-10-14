// Command personal-review-fixture serves an isolated production-mode API and
// synthetic upstream for the opt-in full-stack personal-review browser test.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yexca/kikoto/backend/internal/account"
	"github.com/yexca/kikoto/backend/internal/buildinfo"
	"github.com/yexca/kikoto/backend/internal/config"
	"github.com/yexca/kikoto/backend/internal/httpapi"
	"github.com/yexca/kikoto/backend/internal/kikoeru"
	"github.com/yexca/kikoto/backend/internal/storage"
	"github.com/yexca/kikoto/backend/migrations"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	root, err := os.MkdirTemp("", "kikoto-personal-review-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(root) }()
	db, err := storage.Open(filepath.Join(root, "fixture.db"))
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if err := storage.MigrateFS(db, migrations.Files, buildinfo.Version); err != nil {
		return err
	}
	if _, err := account.NewStore(db).CreateInitialAdministrator(context.Background(), "synthetic-user", "synthetic-password"); err != nil {
		return err
	}
	var mu sync.Mutex
	requests := []string{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/api/workInfo/RJ00000001":
			_ = json.NewEncoder(w).Encode(kikoeru.Work{ID: 1, SourceID: "RJ00000001", Title: "Example Remote Work"})
		case "/api/tracks/1":
			_ = json.NewEncoder(w).Encode([]kikoeru.Track{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	if _, err := db.Exec(`
		INSERT INTO app_setting (key, value_json) VALUES ('library_mode', '"standard"');
		INSERT INTO work (id, primary_code, title, rating_average) VALUES (1, 'RJ00000000', 'Example Local Work', 4.25);
		INSERT INTO file_source (id, code, display_name, source_type) VALUES
		(1, 'example_remote_a', 'Example Remote A', 'kikoeru_compatible'),
		(2, 'example_remote_b', 'Example Remote B', 'kikoeru_compatible');
	`); err != nil {
		return err
	}
	for _, id := range []int{1, 2} {
		if _, err := db.Exec(`INSERT INTO file_source_endpoint (file_source_id, base_url, api_url) VALUES (?, ?, ?)`, id, upstream.URL, upstream.URL); err != nil {
			return err
		}
	}
	browserOrigin := strings.TrimSpace(os.Getenv("PLAYWRIGHT_BASE_URL"))
	if browserOrigin == "" {
		browserOrigin = "http://127.0.0.1:3114"
	}
	app := httpapi.NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", DataRoot: filepath.Join(root, "data"), CacheRoot: filepath.Join(root, "cache"), AllowedOrigins: []string{browserOrigin}})
	defer func() { _ = app.Shutdown(context.Background()) }()
	stop := make(chan struct{})
	var once sync.Once
	mux := http.NewServeMux()
	mux.HandleFunc("POST /__fixture/stop", func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(stop) })
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /__fixture/requests", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_ = json.NewEncoder(w).Encode(requests)
	})
	mux.Handle("/", app.Routes())
	addr := strings.TrimSpace(os.Getenv("PERSONAL_REVIEW_FIXTURE_ADDR"))
	if addr == "" {
		addr = "127.0.0.1:17614"
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	fmt.Println("Personal review fixture ready")
	select {
	case <-stop:
	case <-time.After(15 * time.Minute):
	case err := <-done:
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return server.Shutdown(ctx)
}
