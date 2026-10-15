// Command content-translation-fixture serves the production API over migrated
// SQLite with synthetic public content and a controllable external model.
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
	"github.com/yexca/kikoto/backend/internal/storage"
	"github.com/yexca/kikoto/backend/internal/testfixture"
	"github.com/yexca/kikoto/backend/internal/translation"
	"github.com/yexca/kikoto/backend/migrations"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	root, err := os.MkdirTemp("", "kikoto-content-translation-")
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
	mode := "ok"
	cancelled := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&input) != nil || len(input.Messages) != 1 {
			w.WriteHeader(400)
			return
		}
		prompt := input.Messages[0].Content
		mu.Lock()
		requests = append(requests, prompt)
		current := mode
		mu.Unlock()
		if current == "hold" {
			<-r.Context().Done()
			mu.Lock()
			cancelled++
			mu.Unlock()
			return
		}
		if current == "fail" {
			w.WriteHeader(429)
			return
		}
		parts := strings.SplitN(prompt, "\n\n", 2)
		if len(parts) != 2 {
			w.WriteHeader(400)
			return
		}
		prefix := "ZH: "
		if strings.Contains(parts[0], "日语") {
			prefix = "JA: "
		}
		if strings.Contains(parts[0], "英语") {
			prefix = "EN: "
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": prefix + parts[1]}, "finish_reason": "stop"}}})
	}))
	defer upstream.Close()
	if _, err := db.Exec(`INSERT INTO app_setting (key,value_json) VALUES ('library_mode','"standard"');
 INSERT INTO work (id,primary_code,title,description) VALUES (1,'RJ00000000','Example title by Example Voice','Example description <script>Example text</script>'),(2,'RJ00000001','Another title','Another description');
 INSERT INTO person (id,display_name) VALUES (1,'Example Voice');
 INSERT INTO work_credit (work_id,person_id,role) VALUES (1,1,'voice_actor');
 INSERT INTO file_source (id,code,display_name,source_type) VALUES (1,'example_local','Example Local','local_folder');
 INSERT INTO work_source_presence (work_id,file_source_id,presence_type,availability) VALUES (1,1,'local','available'),(2,1,'local','available');
 INSERT INTO media_item (id,work_id,kind,title) VALUES (1,1,'audio','Example track.wav');
 INSERT INTO media_file_location (id,media_item_id,file_source_id,location_type,path,availability) VALUES (1,1,1,'local','RJ00000000/Example folder/Example track.wav','available');`); err != nil {
		return err
	}
	for ordinal := 2; ordinal < 26; ordinal++ {
		result, err := db.Exec(`INSERT INTO work(primary_code,title) VALUES(?,?)`, testfixture.WorkCode(testfixture.PrefixRJ, ordinal), fmt.Sprintf("Example page title %02d", ordinal))
		if err != nil {
			return err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := db.Exec(`INSERT INTO work_source_presence(work_id,file_source_id,presence_type,availability) VALUES(?,1,'local','available')`, id); err != nil {
			return err
		}
	}
	base := upstream.URL
	minimum := int64(0)
	if _, err := translation.NewStore(db).Update(context.Background(), translation.Patch{BaseURL: &base, MinFreeBytes: &minimum}); err != nil {
		return err
	}
	origin := os.Getenv("PLAYWRIGHT_BASE_URL")
	if origin == "" {
		origin = "http://127.0.0.1:3112"
	}
	app := httpapi.NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", DataRoot: filepath.Join(root, "data"), CacheRoot: filepath.Join(root, "cache"), AllowedOrigins: []string{origin}})
	defer func() { _ = app.Shutdown(context.Background()) }()
	stop := make(chan struct{})
	var once sync.Once
	mux := http.NewServeMux()
	mux.HandleFunc("GET /__fixture/state", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"requests": requests, "cancelled": cancelled})
	})
	mux.HandleFunc("POST /__fixture/mode", func(w http.ResponseWriter, r *http.Request) {
		var value struct {
			Mode string `json:"mode"`
		}
		_ = json.NewDecoder(r.Body).Decode(&value)
		mu.Lock()
		mode = value.Mode
		mu.Unlock()
		w.WriteHeader(204)
	})
	mux.HandleFunc("POST /__fixture/stop", func(w http.ResponseWriter, r *http.Request) { once.Do(func() { close(stop) }); w.WriteHeader(204) })
	mux.Handle("/", app.Routes())
	addr := os.Getenv("CONTENT_TRANSLATION_FIXTURE_ADDR")
	if addr == "" {
		addr = "127.0.0.1:17612"
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	fmt.Println("Content translation fixture ready")
	select {
	case <-stop:
	case <-time.After(time.Hour):
	case err := <-done:
		return err
	}
	_ = app.Shutdown(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return server.Shutdown(ctx)
}
