// Command source-playback-fixture serves an isolated production API with a
// migrated database, generated audio and controllable external source failures.
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
	root, err := os.MkdirTemp("", "kikoto-source-playback-")
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
	if _, err := account.NewStore(db).CreateManagedUser(context.Background(), account.CreateUserInput{Username: "synthetic-listener", Password: "synthetic-password", Role: "user", Enabled: true, ActorUserID: 1}); err != nil {
		return err
	}
	dataRoot := filepath.Join(root, "data")
	if err := os.MkdirAll(filepath.Join(dataRoot, "RJ00000000"), 0700); err != nil {
		return err
	}
	wav := silentWAV(180)
	for _, name := range []string{"local-a.wav", "local-b.wav"} {
		if err := os.WriteFile(filepath.Join(dataRoot, "RJ00000000", name), wav, 0600); err != nil {
			return err
		}
	}
	var mu sync.Mutex
	requests := []string{}
	failed := true
	revised := false
	blockTracks := false
	tracksGate := make(chan struct{})
	var releaseTracks sync.Once
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.URL.Path)
		fail, block, changed := failed, blockTracks, revised
		mu.Unlock()
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) < 3 {
			http.NotFound(w, r)
			return
		}
		source := parts[0]
		if source == "c" && fail {
			http.Error(w, "synthetic source failure", 500)
			return
		}
		switch parts[2] {
		case "workInfo":
			code := "RJ00000000"
			id := int64(1)
			if len(parts) > 3 && parts[3] == "RJ00000001" {
				code, id = "RJ00000001", 2
			}
			_ = json.NewEncoder(w).Encode(kikoeru.Work{ID: id, SourceID: code, Title: "Example Remote " + strings.ToUpper(source)})
		case "tracks":
			if source == "b" && block {
				select {
				case <-tracksGate:
				case <-r.Context().Done():
					return
				}
			}
			title := "remote-" + source + ".wav"
			if source == "b" && changed {
				title = "remote-b-updated.wav"
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{"type": "audio", "title": title, "hash": "synthetic-" + source, "mediaStreamUrl": upstream.URL + "/" + source + "/api/audio", "size": len(wav), "duration": 180}})
		case "audio":
			w.Header().Set("Content-Type", "audio/wav")
			http.ServeContent(w, r, "synthetic.wav", time.Unix(0, 0), bytes.NewReader(wav))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	if _, err := db.Exec(`
		INSERT INTO app_setting (key,value_json) VALUES ('library_mode','"standard"');
		INSERT INTO work (id,primary_code,title) VALUES (1,'RJ00000000','Example Work'), (2,'RJ00000001','Example Unknown Work');
		INSERT INTO file_source (id,code,display_name,source_type) VALUES
		(1,'example_local','Local','local'),
		(2,'example_remote_a','Example Remote A','kikoeru_compatible'),
		(3,'example_remote_b','Example Remote B','kikoeru_compatible'),
		(4,'example_remote_c','Example Remote C','kikoeru_compatible');
		INSERT INTO work_source_presence (work_id,file_source_id,presence_type,availability,last_checked_at) VALUES
		(1,1,'local','available',CURRENT_TIMESTAMP),
		(1,2,'source','available',CURRENT_TIMESTAMP),
		(1,4,'source','available','2000-01-01 00:00:00');
		INSERT INTO media_item (id,work_id,kind,title,fingerprint,duration_seconds) VALUES
		(1,1,'audio','local-a.wav','synthetic-a',180),
		(2,1,'audio','local-b.wav','synthetic-b',180);
		INSERT INTO media_file_location (id,media_item_id,file_source_id,location_type,path,availability) VALUES
		(1,1,1,'local','RJ00000000/local-a.wav','available'),
		(2,2,1,'local','RJ00000000/local-b.wav','available');
	`); err != nil {
		return err
	}
	for i, source := range []string{"a", "b", "c"} {
		endpoint := upstream.URL + "/" + source
		if _, err := db.Exec(`INSERT INTO file_source_endpoint (file_source_id,base_url,api_url) VALUES (?,?,?)`, i+2, endpoint, endpoint); err != nil {
			return err
		}
	}
	origin := os.Getenv("PLAYWRIGHT_BASE_URL")
	if origin == "" {
		origin = "http://127.0.0.1:3115"
	}
	app := httpapi.NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", DataRoot: dataRoot, CacheRoot: filepath.Join(root, "cache"), AllowedOrigins: []string{origin}})
	defer func() { _ = app.Shutdown(context.Background()) }()
	stop := make(chan struct{})
	var once sync.Once
	mux := http.NewServeMux()
	mux.HandleFunc("POST /__fixture/stop", func(w http.ResponseWriter, r *http.Request) { once.Do(func() { close(stop) }); w.WriteHeader(204) })
	mux.HandleFunc("GET /__fixture/requests", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_ = json.NewEncoder(w).Encode(requests)
	})
	mux.HandleFunc("POST /__fixture/recover", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		failed = false
		mu.Unlock()
		w.WriteHeader(204)
	})
	mux.HandleFunc("POST /__fixture/block-tracks", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		blockTracks = true
		mu.Unlock()
		w.WriteHeader(204)
	})
	mux.HandleFunc("POST /__fixture/release-tracks", func(w http.ResponseWriter, r *http.Request) {
		releaseTracks.Do(func() { close(tracksGate) })
		w.WriteHeader(204)
	})
	mux.Handle("/", app.Routes())
	mux.HandleFunc("POST /__fixture/fail", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		failed = true
		mu.Unlock()
		w.WriteHeader(204)
	})
	mux.HandleFunc("POST /__fixture/revise-tracks", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		revised = true
		mu.Unlock()
		w.WriteHeader(204)
	})
	listener, err := net.Listen("tcp", "127.0.0.1:17615")
	if err != nil {
		return err
	}
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	fmt.Println("Source playback fixture ready")
	select {
	case <-stop:
	case <-time.After(30 * time.Minute):
	case err := <-done:
		return err
	}
	releaseTracks.Do(func() { close(tracksGate) })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return server.Shutdown(ctx)
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
