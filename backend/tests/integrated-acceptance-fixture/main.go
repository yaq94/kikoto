// Command integrated-acceptance-fixture serves a production API, local audio,
// synthetic OpenList directories and a controllable external model in one
// disposable database for the opt-in cross-feature browser test.
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yexca/kikoto/backend/internal/account"
	"github.com/yexca/kikoto/backend/internal/buildinfo"
	"github.com/yexca/kikoto/backend/internal/config"
	"github.com/yexca/kikoto/backend/internal/httpapi"
	"github.com/yexca/kikoto/backend/internal/storage"
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
	root, err := os.MkdirTemp("", "kikoto-integrated-acceptance-")
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

	dataRoot := filepath.Join(root, "data", "RJ00000000")
	if err := os.MkdirAll(dataRoot, 0700); err != nil {
		return err
	}
	audio := silentWAV(180)
	for _, name := range []string{"local-a.wav", "local-b.wav"} {
		if err := os.WriteFile(filepath.Join(dataRoot, name), audio, 0600); err != nil {
			return err
		}
	}

	var mu sync.Mutex
	requests := []string{}
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/chat/completions":
			var input struct {
				Messages []struct {
					Content string `json:"content"`
				} `json:"messages"`
			}
			if json.NewDecoder(r.Body).Decode(&input) != nil || len(input.Messages) != 1 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			parts := strings.SplitN(input.Messages[0].Content, "\n\n", 2)
			if len(parts) != 2 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []any{map[string]any{
					"message":       map[string]string{"content": translated(parts[1])},
					"finish_reason": "stop",
				}},
			})
		case "/api/fs/list":
			var input struct {
				Path    string `json:"path"`
				Page    int    `json:"page"`
				PerPage int    `json:"per_page"`
			}
			if json.NewDecoder(r.Body).Decode(&input) != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			content := []map[string]any{}
			switch input.Path {
			case "/Example Work":
				content = []map[string]any{{"name": "listening wind.wav", "size": len(audio)}}
			case "/Example Work Alternate":
				content = []map[string]any{{"name": "alternate wind.wav", "size": len(audio)}}
			default:
				http.NotFound(w, r)
				return
			}
			if input.Page > 0 && input.PerPage > 0 {
				start := min(len(content), (input.Page-1)*input.PerPage)
				content = content[start:min(len(content), start+input.PerPage)]
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": http.StatusOK,
				"data": map[string]any{"content": content, "total": 1},
			})
		case "/api/fs/get":
			var input struct {
				Path string `json:"path"`
			}
			if json.NewDecoder(r.Body).Decode(&input) != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": http.StatusOK,
				"data": map[string]any{
					"name":    path.Base(input.Path),
					"raw_url": upstream.URL + "/audio",
					"size":    len(audio),
				},
			})
		case "/audio":
			w.Header().Set("Content-Type", "audio/wav")
			http.ServeContent(w, r, "integrated.wav", time.Unix(0, 0), bytes.NewReader(audio))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	if _, err := db.Exec(`
		INSERT INTO app_setting (key,value_json) VALUES ('library_mode','"standard"');
		INSERT INTO work (id,primary_code,title,description) VALUES (1,'RJ00000000','Example conversion title','Example conversion description');
		INSERT INTO file_source (id,code,display_name,source_type,enabled,priority) VALUES
			(1,'example_local','Local','local',1,0),
			(2,'example_remote_a','Example Remote A','openlist',1,10);
		INSERT INTO work_source_presence (work_id,file_source_id,presence_type,availability) VALUES (1,1,'local','available');
		INSERT INTO media_item (id,work_id,kind,title,fingerprint,duration_seconds) VALUES
			(1,1,'audio','local-a.wav','synthetic-local-a',180),
			(2,1,'audio','local-b.wav','synthetic-local-b',180);
		INSERT INTO media_file_location (id,media_item_id,file_source_id,location_type,path,availability) VALUES
			(1,1,1,'local','RJ00000000/local-a.wav','available'),
			(2,2,1,'local','RJ00000000/local-b.wav','available');
		INSERT INTO source_catalog_entry (id,file_source_id,root_path,primary_code,title) VALUES
			(2,2,'/Example Work','','Numberless Forest Music'),
			(3,2,'/Example Work Alternate','','Numberless Forest Music');
		INSERT INTO source_catalog_file (catalog_entry_id,relative_path,title,kind,size_bytes) VALUES
			(2,'listening wind.wav','listening wind.wav','audio',?),
			(3,'alternate wind.wav','alternate wind.wav','audio',?);
	`, len(audio), len(audio)); err != nil {
		return err
	}
	if _, err := db.Exec(`INSERT INTO file_source_endpoint (file_source_id,base_url,api_url) VALUES (?,?,?)`, 2, upstream.URL, upstream.URL); err != nil {
		return err
	}
	base := upstream.URL
	minimum := int64(0)
	if _, err := translation.NewStore(db).Update(context.Background(), translation.Patch{BaseURL: &base, MinFreeBytes: &minimum}); err != nil {
		return err
	}

	origin := strings.TrimSpace(os.Getenv("PLAYWRIGHT_BASE_URL"))
	if origin == "" {
		origin = "http://127.0.0.1:3147"
	}
	app := httpapi.NewServer(db, config.Config{
		Mode: config.ModeProduction, RootUsername: "synthetic-user",
		DatabasePath: filepath.Join(root, "fixture.db"),
		DataRoot:     filepath.Join(root, "data"), CacheRoot: filepath.Join(root, "cache"),
		AllowedOrigins: []string{origin},
	})
	defer func() { _ = app.Shutdown(context.Background()) }()

	stop := make(chan struct{})
	var once sync.Once
	mux := http.NewServeMux()
	mux.HandleFunc("POST /__fixture/stop", func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(stop) })
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /__fixture/resources", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_ = json.NewEncoder(w).Encode(requests)
	})
	mux.Handle("/", app.Routes())
	addr := strings.TrimSpace(os.Getenv("INTEGRATED_ACCEPTANCE_ADDR"))
	if addr == "" {
		addr = "127.0.0.1:17647"
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	fmt.Println("Integrated acceptance fixture ready")
	select {
	case <-stop:
	case <-time.After(30 * time.Minute):
	case err := <-done:
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return server.Shutdown(ctx)
}

func translated(text string) string {
	switch {
	case strings.Contains(text, "Example conversion title"):
		return "听见森林里的音乐"
	case strings.Contains(text, "Numberless Forest Music"):
		return "无码森林音乐"
	case strings.Contains(text, "listening wind.wav"):
		return "听见风声.wav"
	default:
		return "ZH: " + text
	}
}

func silentWAV(seconds int) []byte {
	const rate = 8000
	size := seconds * rate * 2
	b := make([]byte, 44+size)
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+size))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], rate)
	binary.LittleEndian.PutUint32(b[28:], rate*2)
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(size))
	return b
}
