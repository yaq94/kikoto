package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/yexca/kikoto/backend/internal/config"
)

func TestCatalogSidecarParsesLabelledCreditsOnly(t *testing.T) {
	body := strings.Join([]string{
		"海量免费ASMR资源",
		"「暗戀隔壁的頹廢女學生」Short-time Story 小點聲聆",
		"◆角色介紹",
		"　配音：葉月",
		"　https://example.invalid/voice",
		"社团：小點聲聆",
		"CV：逢坂成美 / 安里",
		"声优：逢坂成美】",
		"CV：【Example Voice】",
		"キャスト：Example Cast（主役）",
		"这是一段提到 CV 但没有标签的普通文字。",
	}, "\r\n")
	voices, circles, title := parseCatalogSidecar(body)
	if strings.Join(voices, "|") != "Example Cast|Example Voice|安里|葉月|逢坂成美" {
		t.Fatalf("voices=%v", voices)
	}
	if strings.Join(circles, "|") != "小點聲聆" {
		t.Fatalf("circles=%v", circles)
	}
	if title != "暗戀隔壁的頹廢女學生" {
		t.Fatalf("title=%q", title)
	}
	// An unlabelled mention must not become a credit.
	voices, _, _ = parseCatalogSidecar("這部作品的 CV 很好。")
	if len(voices) != 0 {
		t.Fatalf("unlabelled mention became a credit: %v", voices)
	}
}

// The same release text can be published as UTF-16 or Shift-JIS. A credit
// label is still a credit label after decoding, so the selection must not
// depend on the file being valid UTF-8.
func TestCatalogSidecarDecodesReleaseTextEncodings(t *testing.T) {
	shiftJIS := []byte{
		0x94, 0x7a, 0x89, 0xb9, 0x81, 0x46, // 配音：
		0x45, 0x78, 0x61, 0x6d, 0x70, 0x6c, 0x65, 0x20, 0x56, 0x6f, 0x69, 0x63, 0x65, // Example Voice in ASCII
	}
	if got := decodeCatalogSidecar(string(shiftJIS)); !strings.Contains(got, "配音：Example Voice") {
		t.Fatalf("Shift-JIS decode = %q", got)
	}
	decoded := decodeCatalogSidecar(string([]byte{0xff, 0xfe, 0x4d, 0x91, 0xf3, 0x97}))
	if decoded != "配音" {
		t.Fatalf("UTF-16 decode = %q", decoded)
	}
}

// A card with a readme and an unreadable info file keeps the readme evidence.
// One broken file must not discard the other, and the queue records the
// failure instead of pretending the read succeeded.
func TestCatalogSidecarPartiallyReadableKeepsSuccess(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/fs/list":
			var input struct {
				Path string `json:"path"`
			}
			_ = json.NewDecoder(r.Body).Decode(&input)
			items := []map[string]any{}
			switch input.Path {
			case "/":
				items = append(items, map[string]any{"name": "Example Work", "is_dir": true})
			case "/Example Work":
				items = append(items,
					map[string]any{"name": "track.mp3", "size": 100},
					map[string]any{"name": "readme.txt", "size": 24},
					map[string]any{"name": "info.txt", "size": 12})
			default:
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": len(items)}})
		case "/api/fs/get":
			var input struct {
				Path string `json:"path"`
			}
			_ = json.NewDecoder(r.Body).Decode(&input)
			if path.Base(input.Path) == "info.txt" {
				http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{
				"name": "readme.txt", "size": 24, "raw_url": "http://" + r.Host + "/readme",
			}})
		case "/readme":
			_, _ = w.Write([]byte("配音：Example Voice\r\n"))
		default:
			http.NotFound(w, r)
		}
	})
	f := newCatalogIndexFixture(t, upstream)
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	var raw string
	if err := f.server.db.QueryRow(`SELECT sidecar_json FROM source_catalog_entry WHERE title='Example Work'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var cache catalogSidecarCache
	if err := json.Unmarshal([]byte(raw), &cache); err != nil {
		t.Fatal(err)
	}
	if len(cache.Sidecars) != 1 || strings.Join(cache.Sidecars[0].VoiceActors, "|") != "Example Voice" {
		t.Fatalf("readable readme evidence was discarded: %s", raw)
	}
	var attempts int
	if err := f.server.db.QueryRow(`SELECT sidecar_attempts FROM source_catalog_entry WHERE title='Example Work'`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts == 0 {
		t.Fatal("partially failed read did not record an attempt")
	}
}

// A failure to read the local database is not evidence that the release has no
// credit file. The card stays pending, spends one attempt, and keeps the
// evidence that was already stored.
func TestCatalogSidecarDatabaseReadFailureKeepsEvidence(t *testing.T) {
	db := openMigratedTestDB(t)
	s := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-editor", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	defer func() { _ = s.Shutdown(context.Background()) }()
	stored := `{"sidecars":[{"relativePath":"readme.txt","size":12,"voiceActors":["Example Voice"],"circles":[],"fetchedAt":"2026-01-01T00:00:00Z"}]}`
	metadataReviewExec(t, db, `INSERT INTO source_catalog_entry(id,root_path,title,sidecar_json,sidecar_pending) VALUES(1,'/Example Work','Example Work',?,1)`, stored)
	if _, err := db.Exec(`DROP TABLE source_catalog_file`); err != nil {
		t.Fatal(err)
	}
	outcome := s.refreshCatalogSidecar(context.Background(), nil, catalogSidecarCandidate{ID: 1, RootPath: "/Example Work", Title: "Example Work"}, catalogSidecarBatchRequests)
	if !outcome.Failed {
		t.Fatalf("database read failure was not reported: %#v", outcome)
	}
	var raw string
	var pending, attempts int
	if err := db.QueryRow(`SELECT sidecar_json,sidecar_pending,sidecar_attempts FROM source_catalog_entry WHERE id=1`).Scan(&raw, &pending, &attempts); err != nil {
		t.Fatal(err)
	}
	if raw != stored || pending != 1 || attempts != 1 {
		t.Fatalf("database failure changed the queue or evidence: raw=%s pending=%d attempts=%d", raw, pending, attempts)
	}
}

// The independent worker chooses the next source from the queue, so a pending
// card on a disabled source must not hide work from an enabled source forever.
func TestCatalogSidecarWorkerSkipsDisabledSource(t *testing.T) {
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	metadataReviewExec(t, f.server.db, `INSERT INTO file_source(id,code,display_name,source_type,enabled) VALUES(2,'example_remote_b','Example Remote B','openlist',1);
		INSERT INTO file_source_endpoint(file_source_id,api_url,base_url) SELECT 2,api_url,base_url FROM file_source_endpoint WHERE file_source_id=1;
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,sidecar_pending) VALUES(1,1,'/Disabled','Disabled card',1);
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,sidecar_pending) VALUES(2,2,'/Enabled','Enabled card',1);
		UPDATE file_source SET enabled=0 WHERE id=1;`)
	worked, err := f.server.refreshPendingCatalogSidecars(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if worked {
		t.Fatal("empty enabled source requested an immediate continuation")
	}
	var disabledPending, enabledPending int
	if err := f.server.db.QueryRow(`SELECT sidecar_pending FROM source_catalog_entry WHERE id=1`).Scan(&disabledPending); err != nil {
		t.Fatal(err)
	}
	if err := f.server.db.QueryRow(`SELECT sidecar_pending FROM source_catalog_entry WHERE id=2`).Scan(&enabledPending); err != nil {
		t.Fatal(err)
	}
	if disabledPending != 1 || enabledPending != 0 {
		t.Fatalf("worker starved behind disabled source: disabled=%d enabled=%d", disabledPending, enabledPending)
	}
}

// A source with no usable endpoint cannot be loaded. It must not stay at the
// head of the queue and hide an enabled source behind it on every pass.
func TestCatalogSidecarWorkerSkipsUnusableSource(t *testing.T) {
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/fs/get":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{
				"name": "readme.txt", "size": 24, "raw_url": "http://" + r.Host + "/readme",
			}})
		case "/readme":
			_, _ = w.Write([]byte("配音：Example Voice\r\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	metadataReviewExec(t, f.server.db, `INSERT INTO file_source(id,code,display_name,source_type,enabled) VALUES(2,'example_remote_b','Example Remote B','openlist',1);
		INSERT INTO file_source_endpoint(file_source_id,api_url,base_url) SELECT 2,api_url,base_url FROM file_source_endpoint WHERE file_source_id=1;
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,sidecar_pending) VALUES(1,1,'/Unusable','Unusable card',1);
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,sidecar_pending) VALUES(2,2,'/Usable','Usable card',1);
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes,observed_generation) VALUES(2,'readme.txt','readme.txt','text',24,1);
		UPDATE file_source_endpoint SET api_url='',base_url='' WHERE file_source_id=1;`)
	for range 2 {
		worked, err := f.server.refreshPendingCatalogSidecars(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if worked {
			t.Fatal("empty sidecar queue requested an immediate continuation")
		}
	}
	var pending int
	if err := f.server.db.QueryRow(`SELECT sidecar_pending FROM source_catalog_entry WHERE id=2`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("usable source stayed queued after an unusable source was selected: pending=%d", pending)
	}
}

// The index warm pass and the background worker can run at the same time. One
// pending card must cost one metadata request and one body request.
func TestCatalogSidecarConcurrentPassesReadOneCardOnce(t *testing.T) {
	var gets atomic.Int32
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/fs/get":
			gets.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{
				"name": "readme.txt", "size": 24, "raw_url": "http://" + r.Host + "/readme",
			}})
		case "/readme":
			gets.Add(1)
			_, _ = w.Write([]byte("配音：Example Voice\r\n"))
		default:
			http.NotFound(w, r)
		}
	})
	f := newCatalogIndexFixture(t, upstream)
	metadataReviewExec(t, f.server.db, `INSERT INTO source_catalog_entry(id,file_source_id,root_path,source_root,title,sidecar_pending,last_seen_generation) VALUES(10,1,'/Example Work','/Example','Example Work',1,1);
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes,observed_generation) VALUES(10,'readme.txt','readme.txt','text',24,1);`)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f.server.refreshCatalogSidecars(context.Background(), 1)
		}()
	}
	wg.Wait()
	if got := gets.Load(); got != 2 {
		t.Fatalf("concurrent passes issued %d upstream requests, want one metadata and one body request", got)
	}
	var pending int
	var evidence string
	if err := f.server.db.QueryRow(`SELECT sidecar_pending,sidecar_json FROM source_catalog_entry WHERE id=10`).Scan(&pending, &evidence); err != nil {
		t.Fatal(err)
	}
	if pending != 0 || !strings.Contains(evidence, "Example Voice") {
		t.Fatalf("completed pass left pending=%d evidence=%s", pending, evidence)
	}
}

// A daily rescan republishes the same card. Five failed reads already exhausted
// the attempt budget, so the next publication must keep the card settled rather
// than retrying the same unreadable release every day.
func TestCatalogSidecarRescanKeepsExhaustedAttempts(t *testing.T) {
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	evidence := `{"sidecars":[{"relativePath":"readme.txt","size":24,"voiceActors":["Example Voice"],"circles":[],"fetchedAt":"2026-01-01T00:00:00Z"}]}`
	metadataReviewExec(t, f.server.db, `INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,sidecar_json,sidecar_pending,sidecar_attempts,sidecar_revision)
		VALUES(20,1,'/Example Work','Example Work','`+evidence+`',0,5,'readme.txt|24|');
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes,modified_at,observed_generation)
		VALUES(20,'readme.txt','readme.txt','text',24,'',1);`)
	tx, err := f.server.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = markCatalogSidecarCandidatesTx(context.Background(), tx, []int64{20}, 1); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var pending, attempts int
	if err = f.server.db.QueryRow(`SELECT sidecar_pending,sidecar_attempts FROM source_catalog_entry WHERE id=20`).Scan(&pending, &attempts); err != nil {
		t.Fatal(err)
	}
	if pending != 1 || attempts != 5 {
		t.Fatalf("rescan reset the exhausted budget: pending=%d attempts=%d", pending, attempts)
	}
}

// An unreadable file may become readable without a new release. The stored
// revision lets a rescan distinguish that change from the same exhausted read.
func TestCatalogSidecarRescanRetriesAfterFileRevisionChanges(t *testing.T) {
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	evidence := `{"sidecars":[{"relativePath":"readme.txt","size":24,"voiceActors":["Example Voice"],"circles":[],"fetchedAt":"2026-01-01T00:00:00Z"}]}`
	metadataReviewExec(t, f.server.db, `INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,sidecar_json,sidecar_pending,sidecar_attempts,sidecar_revision)
		VALUES(30,1,'/Example Work','Example Work','`+evidence+`',0,5,'readme.txt|24|2026-01-01T00:00:00Z');
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes,modified_at,observed_generation)
		VALUES(30,'readme.txt','readme.txt','text',48,'2026-02-01T00:00:00Z',1);`)
	tx, err := f.server.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = markCatalogSidecarCandidatesTx(context.Background(), tx, []int64{30}, 1); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var pending, attempts int
	var revision string
	if err = f.server.db.QueryRow(`SELECT sidecar_pending,sidecar_attempts,sidecar_revision FROM source_catalog_entry WHERE id=30`).Scan(&pending, &attempts, &revision); err != nil {
		t.Fatal(err)
	}
	if pending != 1 || attempts != 0 {
		t.Fatalf("changed revision did not reopen the budget: pending=%d attempts=%d revision=%q", pending, attempts, revision)
	}
}

// A card whose credit files were deleted must not keep evidence from files the
// release no longer publishes.
func TestCatalogSidecarDeletedFileDropsStaleEvidence(t *testing.T) {
	var withReadme atomic.Bool
	withReadme.Store(true)
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/fs/list":
			var input struct {
				Path string `json:"path"`
			}
			_ = json.NewDecoder(r.Body).Decode(&input)
			items := []map[string]any{}
			switch input.Path {
			case "/":
				items = append(items, map[string]any{"name": "Example Work", "is_dir": true})
			case "/Example Work":
				items = append(items, map[string]any{"name": "track.mp3", "size": 100})
				if withReadme.Load() {
					items = append(items, map[string]any{"name": "readme.txt", "size": 24})
				}
			default:
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": len(items)}})
		case "/api/fs/get":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{
				"name": "readme.txt", "size": 24, "raw_url": "http://" + r.Host + "/readme",
			}})
		case "/readme":
			_, _ = w.Write([]byte("配音：Example Voice\r\n"))
		default:
			http.NotFound(w, r)
		}
	})
	f := newCatalogIndexFixture(t, upstream)
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	withReadme.Store(false)
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"force":true}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("rescan = %#v", p)
	}
	var raw string
	if err := f.server.db.QueryRow(`SELECT sidecar_json FROM source_catalog_entry WHERE title='Example Work'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "Example Voice") {
		t.Fatalf("stale evidence survived a deleted readme: %s", raw)
	}
}

// Release pages spell the same credit with and without brackets. A trailing
// annotation belongs to the markup, so it must not create a second person.
func TestCatalogSidecarNameNormalizationIsNotSampleSpecific(t *testing.T) {
	cases := map[string]string{
		"【Example Voice】":      "Example Voice",
		"Example Voice】":       "Example Voice",
		"Example Voice（主役）":    "Example Voice",
		"Example Voice (Lead)": "Example Voice",
		"「Example Voice」":      "Example Voice",
		"Example Voice":        "Example Voice",
	}
	for raw, want := range cases {
		got := trimCatalogSidecarBrackets(raw)
		if got != want {
			t.Fatalf("trim(%q)=%q want %q", raw, got, want)
		}
	}
	// A bracket that belongs to the middle of the name stays.
	if got := trimCatalogSidecarBrackets("Example (CV) Voice"); got != "Example (CV) Voice" {
		t.Fatalf("middle bracket was rewritten: %q", got)
	}
}

// A numberless card and a registered work with the same original title and the
// same labelled cast are a unique high-confidence candidate even when the
// source publishes no audio hash.
func TestWorkMatchingSidecarCreditsAssociateNumberlessCatalog(t *testing.T) {
	db := openMigratedTestDB(t)
	s := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-editor", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	defer func() { _ = s.Shutdown(context.Background()) }()
	createTestAdministrator(t, s, "synthetic-editor", "synthetic-password")
	metadataReviewExec(t, db, `INSERT INTO work(id,primary_code,title) VALUES(11,'','Example Work');
	INSERT INTO person(id,display_name) VALUES(1,'Example Voice');
	INSERT INTO work_credit(work_id,person_id,role) VALUES(11,1,'voice_actor');
	INSERT INTO file_source(id,code,display_name,source_type) VALUES(7,'example_remote_a','Example Remote A','openlist');
	INSERT INTO file_source_endpoint(file_source_id,api_url,base_url) VALUES(7,'https://source.example.invalid','https://source.example.invalid');
	INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,sidecar_json) VALUES(1,7,'/Example Work','Example Work','{"sidecars":[{"relativePath":"readme.txt","size":12,"voiceActors":["Example Voice"],"circles":[],"fetchedAt":"2026-01-01T00:00:00Z"}]}');`)
	app := httptest.NewServer(s.Routes())
	defer app.Close()
	cookie := workReferenceLogin(t, app, "synthetic-editor")
	var result map[string]any
	workReferenceRequest(t, app, cookie, "POST", "/api/work-matching", `{"kind":"catalog","id":1}`, 200, &result)
	if result["status"] != "completed" || result["rule"] != "title_voice_credits" {
		t.Fatalf("sidecar credit did not associate: %#v", result)
	}
	var entry map[string]any
	workReferenceRequest(t, app, cookie, "GET", "/api/source-catalog/1", "", 200, &entry)
	if entry["workId"] == nil || entry["primaryCode"] != "" {
		t.Fatalf("association changed identity: %#v", entry)
	}
}

// Two different labelled casts are contradictory, not a weaker match.
func TestWorkMatchingSidecarCreditsRejectDifferentCast(t *testing.T) {
	db := openMigratedTestDB(t)
	s := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-editor", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	defer func() { _ = s.Shutdown(context.Background()) }()
	createTestAdministrator(t, s, "synthetic-editor", "synthetic-password")
	metadataReviewExec(t, db, `INSERT INTO work(id,primary_code,title) VALUES(11,'','Example Work');
	INSERT INTO person(id,display_name) VALUES(1,'Example Voice A');
	INSERT INTO work_credit(work_id,person_id,role) VALUES(11,1,'voice_actor');
	INSERT INTO file_source(id,code,display_name,source_type) VALUES(7,'example_remote_a','Example Remote A','openlist');
	INSERT INTO file_source_endpoint(file_source_id,api_url,base_url) VALUES(7,'https://source.example.invalid','https://source.example.invalid');
	INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,sidecar_json) VALUES(1,7,'/Example Work','Example Work','{"sidecars":[{"relativePath":"readme.txt","size":12,"voiceActors":["Example Voice B"],"circles":[],"fetchedAt":"2026-01-01T00:00:00Z"}]}');`)
	app := httptest.NewServer(s.Routes())
	defer app.Close()
	cookie := workReferenceLogin(t, app, "synthetic-editor")
	var result map[string]any
	workReferenceRequest(t, app, cookie, "POST", "/api/work-matching", `{"kind":"catalog","id":1}`, 200, &result)
	if result["status"] != "review" || result["rule"] != "conflicting_evidence" {
		t.Fatalf("different casts matched: %#v", result)
	}
}

// The index reads a readme through the bounded sidecar path, stores only
// labelled credits, and never turns the text into an audio file.
func TestSourceCatalogIndexStoresSidecarCredits(t *testing.T) {
	readme := "「Example Work」\r\n配音：Example Voice\r\n社团：Example Circle\r\n"
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/fs/list":
			var input struct {
				Path string `json:"path"`
			}
			_ = json.NewDecoder(r.Body).Decode(&input)
			items := []map[string]any{}
			switch input.Path {
			case "/":
				items = append(items, map[string]any{"name": "Example Work", "is_dir": true})
			case "/Example Work":
				items = append(items, map[string]any{"name": "track.mp3", "size": 100},
					map[string]any{"name": "readme.txt", "size": len(readme)})
			default:
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": len(items)}})
		case "/api/fs/get":
			var input struct {
				Path string `json:"path"`
			}
			_ = json.NewDecoder(r.Body).Decode(&input)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{
				"name": path.Base(input.Path), "size": len(readme), "raw_url": "http://" + r.Host + "/readme",
			}})
		case "/readme":
			_, _ = w.Write([]byte(readme))
		default:
			http.NotFound(w, r)
		}
	})
	f := newCatalogIndexFixture(t, upstream)
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	var raw string
	if err := f.server.db.QueryRow(`SELECT sidecar_json FROM source_catalog_entry WHERE title='Example Work'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var cache catalogSidecarCache
	if err := json.Unmarshal([]byte(raw), &cache); err != nil {
		t.Fatal(err)
	}
	if len(cache.Sidecars) != 1 || strings.Join(cache.Sidecars[0].VoiceActors, "|") != "Example Voice" ||
		strings.Join(cache.Sidecars[0].Circles, "|") != "Example Circle" {
		t.Fatalf("sidecar cache = %#v", cache)
	}
	var textFiles int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_file WHERE kind='text'`).Scan(&textFiles); err != nil {
		t.Fatal(err)
	}
	if textFiles == 0 {
		t.Fatal("sidecar file was not published as text")
	}
	// The readme is evidence, not a track.
	var entryID int64
	if err := f.server.db.QueryRow(`SELECT id FROM source_catalog_entry WHERE title='Example Work'`).Scan(&entryID); err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(entryID, 10)
	body := reviewRequest(t, f.handler, f.cookie, http.MethodGet, "/api/source-catalog/"+id+"/tracks", "", 200)
	for _, raw := range body["tracks"].([]any) {
		row := raw.(map[string]any)
		if row["type"] == "text" && row["streamUrl"] != "" {
			t.Fatalf("sidecar became a playable track: %#v", row)
		}
	}
}

// A readme on only one side is not two independent facts. The card stays for
// review and the source text is not promoted into a work credit.
func TestWorkMatchingSidecarRequiresCreditsOnBothSides(t *testing.T) {
	db := openMigratedTestDB(t)
	s := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-editor", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	defer func() { _ = s.Shutdown(context.Background()) }()
	createTestAdministrator(t, s, "synthetic-editor", "synthetic-password")
	metadataReviewExec(t, db, `INSERT INTO work(id,primary_code,title) VALUES(11,'','Example Work');
	INSERT INTO file_source(id,code,display_name,source_type) VALUES(7,'example_remote_a','Example Remote A','openlist');
	INSERT INTO file_source_endpoint(file_source_id,api_url,base_url) VALUES(7,'https://source.example.invalid','https://source.example.invalid');
	INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,sidecar_json) VALUES(1,7,'/Example Work','Example Work','{"sidecars":[{"relativePath":"readme.txt","size":12,"voiceActors":["Example Voice"],"circles":[],"fetchedAt":"2026-01-01T00:00:00Z"}]}');`)
	app := httptest.NewServer(s.Routes())
	defer app.Close()
	cookie := workReferenceLogin(t, app, "synthetic-editor")
	var result map[string]any
	workReferenceRequest(t, app, cookie, "POST", "/api/work-matching", `{"kind":"catalog","id":1}`, 200, &result)
	if result["status"] != "review" || result["rule"] != "insufficient_evidence" {
		t.Fatalf("one-sided readme associated: %#v", result)
	}
}

// A sidecar read failure must not remove the playable card or turn the text
// into audio. The index still publishes the audio file and records no credit.
func TestSourceCatalogSidecarFailureKeepsPlayableCard(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/fs/list":
			var input struct {
				Path string `json:"path"`
			}
			_ = json.NewDecoder(r.Body).Decode(&input)
			items := []map[string]any{}
			switch input.Path {
			case "/":
				items = append(items, map[string]any{"name": "Example Work", "is_dir": true})
			case "/Example Work":
				items = append(items, map[string]any{"name": "track.mp3", "size": 100},
					map[string]any{"name": "readme.txt", "size": 20})
			default:
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": len(items)}})
		case "/api/fs/get":
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
		default:
			http.NotFound(w, r)
		}
	})
	f := newCatalogIndexFixture(t, upstream)
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	var audio, cached int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_file WHERE kind='audio'`).Scan(&audio); err != nil {
		t.Fatal(err)
	}
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_entry WHERE sidecar_json<>''`).Scan(&cached); err != nil {
		t.Fatal(err)
	}
	if audio != 1 || cached != 0 {
		t.Fatalf("audio=%d cached=%d; want the card kept and no invented credit", audio, cached)
	}
}

// A sidecar URL is untrusted source output. The shared outbound policy still
// applies: a redirect to another origin is refused and the card keeps its
// audio without recording a credit from the redirect target.
func TestSourceCatalogSidecarRedirectStaysOnTheSourceOrigin(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("配音：Redirected Voice\r\n"))
	}))
	defer target.Close()
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/fs/list":
			var input struct {
				Path string `json:"path"`
			}
			_ = json.NewDecoder(r.Body).Decode(&input)
			items := []map[string]any{}
			switch input.Path {
			case "/":
				items = append(items, map[string]any{"name": "Example Work", "is_dir": true})
			case "/Example Work":
				items = append(items, map[string]any{"name": "track.mp3", "size": 100},
					map[string]any{"name": "readme.txt", "size": 20})
			default:
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": items, "total": len(items)}})
		case "/api/fs/get":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{
				"name": "readme.txt", "size": 20, "raw_url": target.URL + "/readme",
			}})
		default:
			http.NotFound(w, r)
		}
	})
	f := newCatalogIndexFixture(t, upstream)
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	var raw string
	if err := f.server.db.QueryRow(`SELECT sidecar_json FROM source_catalog_entry WHERE title='Example Work'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "Redirected Voice") {
		t.Fatalf("cross-origin sidecar was accepted: %s", raw)
	}
	var audio int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_file WHERE kind='audio'`).Scan(&audio); err != nil {
		t.Fatal(err)
	}
	if audio != 1 {
		t.Fatalf("audio card lost after refused redirect: %d", audio)
	}
}
