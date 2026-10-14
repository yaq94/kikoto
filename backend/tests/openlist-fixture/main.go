// Command openlist-fixture serves synthetic OpenList audio through the real app.
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
	"path/filepath"
	"sync"
	"time"

	"github.com/yexca/kikoto/backend/internal/account"
	"github.com/yexca/kikoto/backend/internal/buildinfo"
	"github.com/yexca/kikoto/backend/internal/config"
	"github.com/yexca/kikoto/backend/internal/httpapi"
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
	root, err := os.MkdirTemp("", "kikoto-openlist-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(root) }()
	db, err := storage.Open(filepath.Join(root, "fixture.db"))
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if err = storage.MigrateFS(db, migrations.Files, buildinfo.Version); err != nil {
		return err
	}
	if _, err = account.NewStore(db).CreateInitialAdministrator(context.Background(), "synthetic-user", "synthetic-password"); err != nil {
		return err
	}
	data := wav()
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&input)
		switch r.URL.Path {
		case "/api/fs/list":
			var content []map[string]any
			switch input.Path {
			case "/Example Work":
				content = []map[string]any{{"name": "MP3", "is_dir": true}}
			case "/Example Work/MP3":
				content = []map[string]any{{"name": "01 opening.wav", "size": len(data)}, {"name": "02 ending.wav", "size": len(data)}}
			case "/RJ00000001":
				content = []map[string]any{{"name": "numbered.wav", "size": len(data)}}
			case "/Archive":
				content = []map[string]any{{"name": "only.zip", "size": 100}}
			default:
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": content, "total": len(content)}})
		case "/api/fs/get":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"name": filepath.Base(input.Path), "raw_url": upstream.URL + "/audio", "size": len(data)}})
		case "/audio":
			w.Header().Set("Content-Type", "audio/wav")
			http.ServeContent(w, r, "audio.wav", time.Unix(0, 0), bytes.NewReader(data))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	_, err = db.Exec(`INSERT INTO app_setting(key,value_json) VALUES('library_mode','"standard"');
		INSERT INTO file_source(id,code,display_name,source_type,config_json) VALUES(1,'example_remote_a','Example Remote A','openlist','{"openListRoots":["/Example Work","/RJ00000001","/Archive"],"openListIntervalMs":100}');
		INSERT INTO file_source_endpoint(file_source_id,base_url,api_url) VALUES(1,?,?);`, upstream.URL, upstream.URL)
	if err != nil {
		return err
	}
	app := httpapi.NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", DataRoot: filepath.Join(root, "data"), CacheRoot: filepath.Join(root, "cache"), AllowedOrigins: []string{"http://127.0.0.1:3117"}})
	defer func() { _ = app.Shutdown(context.Background()) }()
	stop := make(chan struct{})
	var once sync.Once
	mux := http.NewServeMux()
	mux.HandleFunc("POST /__fixture/stop", func(w http.ResponseWriter, r *http.Request) { once.Do(func() { close(stop) }); w.WriteHeader(204) })
	mux.Handle("/", app.Routes())
	listener, err := net.Listen("tcp", "127.0.0.1:17617")
	if err != nil {
		return err
	}
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	fmt.Println("OpenList fixture ready")
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
func wav() []byte {
	const size = 30 * 8000
	data := make([]byte, 44+size)
	copy(data, "RIFF")
	binary.LittleEndian.PutUint32(data[4:], uint32(36+size))
	copy(data[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(data[16:], 16)
	binary.LittleEndian.PutUint16(data[20:], 1)
	binary.LittleEndian.PutUint16(data[22:], 1)
	binary.LittleEndian.PutUint32(data[24:], 8000)
	binary.LittleEndian.PutUint32(data[28:], 8000)
	binary.LittleEndian.PutUint16(data[32:], 1)
	binary.LittleEndian.PutUint16(data[34:], 8)
	copy(data[36:], "data")
	binary.LittleEndian.PutUint32(data[40:], size)
	for i := 44; i < len(data); i++ {
		data[i] = 128
	}
	return data
}
