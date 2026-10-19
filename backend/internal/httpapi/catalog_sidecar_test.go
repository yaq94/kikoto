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
	"time"

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
	// A 503 is a temporary failure: it is recorded as a retry instead of
	// spending the permanent budget, so the card is read once the source
	// recovers.
	var transient int
	if err := f.server.db.QueryRow(`SELECT sidecar_transient_attempts FROM source_catalog_entry WHERE title='Example Work'`).Scan(&transient); err != nil {
		t.Fatal(err)
	}
	if transient == 0 {
		t.Fatal("partially failed read did not record its retry")
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
	var pending, transient int
	if err := db.QueryRow(`SELECT sidecar_json,sidecar_pending,sidecar_transient_attempts FROM source_catalog_entry WHERE id=1`).Scan(&raw, &pending, &transient); err != nil {
		t.Fatal(err)
	}
	// A local database read is an infrastructure failure, not a verdict on
	// the release: the evidence stays and the retry counter moves.
	if raw != stored || pending != 1 || transient != 1 {
		t.Fatalf("database failure changed the queue or evidence: raw=%s pending=%d transient=%d", raw, pending, transient)
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

// A card that never produced evidence has an empty sidecar_revision. The next
// rescan must treat that as "no earlier revision", not as a change, otherwise
// the five-attempt budget restarts forever and the source is read every day.
func TestCatalogSidecarRescanKeepsAttemptsAfterFailedFirstRead(t *testing.T) {
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	metadataReviewExec(t, f.server.db, `INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,sidecar_json,sidecar_pending,sidecar_attempts,sidecar_revision)
		VALUES(50,1,'/Example Work','Example Work','',1,5,'');
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes,modified_at,observed_generation)
		VALUES(50,'readme.txt','readme.txt','text',24,'2026-01-01T00:00:00Z',1);`)
	tx, err := f.server.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = markCatalogSidecarCandidatesTx(context.Background(), tx, []int64{50}, 1); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var pending, attempts int
	var revision string
	if err = f.server.db.QueryRow(`SELECT sidecar_pending,sidecar_attempts,sidecar_revision FROM source_catalog_entry WHERE id=50`).Scan(&pending, &attempts, &revision); err != nil {
		t.Fatal(err)
	}
	if pending != 1 || attempts != 5 {
		t.Fatalf("failed first read restarted the budget: pending=%d attempts=%d", pending, attempts)
	}
	// The observed revision is recorded so the following rescan compares
	// against the release rather than against an empty value again.
	if revision != "readme.txt|24|2026-01-01T00:00:00Z" {
		t.Fatalf("failed first read did not record the observed revision: %q", revision)
	}
}

// A pass that runs out of request budget after reading some files must keep
// those results. Otherwise the next pass requests the same bodies again.
func TestCatalogSidecarDeferredPassKeepsReadEvidence(t *testing.T) {
	var bodies atomic.Int32
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/fs/get":
			var input struct {
				Path string `json:"path"`
			}
			_ = json.NewDecoder(r.Body).Decode(&input)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{
				"name": path.Base(input.Path), "size": 24, "raw_url": "http://" + r.Host + "/" + path.Base(input.Path),
			}})
		case "/readme.txt", "/credit.txt":
			bodies.Add(1)
			_, _ = w.Write([]byte("配音：Example Voice\n"))
		default:
			http.NotFound(w, r)
		}
	})
	f := newCatalogIndexFixture(t, upstream)
	metadataReviewExec(t, f.server.db, `INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,sidecar_pending)
		VALUES(60,1,'/Example Work','Example Work',1);
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes,observed_generation)
		VALUES(60,'readme.txt','readme.txt','text',24,1),(60,'credit.txt','credit.txt','text',24,1);`)
	// A budget of three leaves room for one metadata request and one body
	// request, so the first file is read and the second is deferred.
	source, err := f.server.catalogIndexSource(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	client := f.server.openListClient(source)
	candidate := catalogSidecarCandidate{ID: 60, RootPath: "/Example Work", Title: "Example Work"}
	files, err := f.server.loadSourceCatalogFiles(context.Background(), 60)
	if err != nil {
		t.Fatal(err)
	}
	result := readCatalogSidecars(context.Background(), client, catalogEntry{ID: 60, RootPath: "/Example Work"}, files, catalogSidecarCache{Sidecars: []catalogSidecarMeta{}}, 3)
	if !result.Deferred || len(result.Cache.Sidecars) == 0 {
		t.Fatalf("first file was not read before the budget ran out: %#v", result)
	}
	f.server.finishCatalogSidecar(context.Background(), candidate, catalogSidecarStateRetry, result)
	var raw string
	var pending, attempts int
	if err = f.server.db.QueryRow(`SELECT sidecar_json,sidecar_pending,sidecar_attempts FROM source_catalog_entry WHERE id=60`).Scan(&raw, &pending, &attempts); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, "readme.txt") {
		t.Fatalf("deferred pass discarded the evidence it read: %q", raw)
	}
	// Budget exhaustion is not a source failure: the card stays pending and
	// spends no attempt.
	if pending != 1 || attempts != 0 {
		t.Fatalf("deferred pass changed the queue: pending=%d attempts=%d", pending, attempts)
	}
	if bodies.Load() != 1 {
		t.Fatalf("body reads = %d, want the one file the budget allowed", bodies.Load())
	}
}

// Japanese releases name the same credit file without the English word. The
// candidate set, the rank and the publication query share one vocabulary, so
// these names must be actionable rather than silently skipped.
func TestCatalogSidecarAcceptsJapaneseCreditNames(t *testing.T) {
	for _, name := range []string{"りどみ.txt", "りーどみー.txt", "リードミー.txt", "クレジット.txt", "スタッフ.nfo", "紹介.txt", "説明書.txt", "はじめに.txt", "read me.txt", "Read_me.md"} {
		if !catalogSidecarCandidateFile(SourceCatalogFile{Path: name, Kind: "text"}) {
			t.Errorf("%q was not accepted as credit text", name)
		}
	}
	// The extension still decides, and a name hint inside another word is not
	// a credit file.
	for _, name := range []string{"lyrics.md", "notes.nfo", "trackinfo.txt", "infomation.txt", "りどみ.pdf"} {
		if catalogSidecarCandidateFile(SourceCatalogFile{Path: name, Kind: "text"}) {
			t.Errorf("%q was accepted as credit text", name)
		}
	}
	if rank := catalogSidecarFileRank(SourceCatalogFile{Path: "Readme_TC.txt"}); rank != 0 {
		t.Fatalf("translated readme rank = %d, want the release readme rank", rank)
	}
}

// One release can publish a root readme plus several translations. The limit
// must leave room for the common language set instead of silently dropping the
// traditional-Chinese file.
func TestCatalogSidecarKeepsTraditionalChineseReadme(t *testing.T) {
	files := []SourceCatalogFile{}
	for _, name := range []string{"Readme.txt", "English/Readme_EN.txt", "日本語/Readme_JP.txt", "简体中文/Readme_SC.txt", "繁體中文/Readme_TC.txt"} {
		files = append(files, SourceCatalogFile{Path: name, Kind: "text", Generation: 1})
	}
	selected := catalogSidecarFiles(files)
	paths := map[string]bool{}
	for _, file := range selected {
		paths[file.Path] = true
	}
	if !paths["繁體中文/Readme_TC.txt"] {
		t.Fatalf("traditional Chinese readme was dropped: %#v", selected)
	}
}

// A temporary outage must not spend the permanent budget. The card waits on a
// bounded backoff and stays pending, so a recovered source is read again.
func TestCatalogSidecarTransientFailureWaitsWithoutSpendingBudget(t *testing.T) {
	db := openMigratedTestDB(t)
	s := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-editor", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	defer func() { _ = s.Shutdown(context.Background()) }()
	metadataReviewExec(t, db, `INSERT INTO source_catalog_entry(id,root_path,title,sidecar_pending,sidecar_attempts)
		VALUES(70,'/Example Work','Example Work',1,0);`)
	s.finishCatalogSidecar(context.Background(), catalogSidecarCandidate{ID: 70}, catalogSidecarStateRetry, catalogSidecarReadResult{Failed: true})
	var pending, attempts, transient int
	var retryAt, failure string
	if err := db.QueryRow(`SELECT sidecar_pending,sidecar_attempts,sidecar_transient_attempts,sidecar_retry_at,sidecar_failure FROM source_catalog_entry WHERE id=70`).
		Scan(&pending, &attempts, &transient, &retryAt, &failure); err != nil {
		t.Fatal(err)
	}
	if pending != 1 || attempts != 0 || transient != 1 {
		t.Fatalf("temporary failure spent the permanent budget: pending=%d attempts=%d transient=%d", pending, attempts, transient)
	}
	if retryAt == "" || failure != "temporarily_unavailable" {
		t.Fatalf("temporary failure has no visible backoff: retryAt=%q failure=%q", retryAt, failure)
	}
	// The timestamp must come from SQLite's clock, the same one the due
	// queries compare against; a Go clock would drift in either direction.
	var due int
	if err := db.QueryRow(`SELECT sidecar_retry_at>datetime('now') FROM source_catalog_entry WHERE id=70`).Scan(&due); err != nil {
		t.Fatal(err)
	}
	if due != 1 {
		t.Fatalf("temporary failure is already due: %s", retryAt)
	}
}

// One deleted sibling must not hide a recoverable outage: the card keeps the
// transient backoff so the readme is retried when the source returns.
func TestCatalogSidecarMixedFailureKeepsTransientRetry(t *testing.T) {
	db := openMigratedTestDB(t)
	s := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-editor", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	defer func() { _ = s.Shutdown(context.Background()) }()
	metadataReviewExec(t, db, `INSERT INTO source_catalog_entry(id,root_path,title,sidecar_pending,sidecar_attempts)
		VALUES(73,'/Example Work','Example Work',1,0);`)
	s.finishCatalogSidecar(context.Background(), catalogSidecarCandidate{ID: 73}, catalogSidecarStateRetry,
		catalogSidecarReadResult{Failed: true, Permanent: true, Transient: true})
	var attempts, transient int
	var failure string
	if err := db.QueryRow(`SELECT sidecar_attempts,sidecar_transient_attempts,sidecar_failure FROM source_catalog_entry WHERE id=73`).
		Scan(&attempts, &transient, &failure); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 || transient != 1 || failure != "temporarily_unavailable" {
		t.Fatalf("mixed failure settled a recoverable card: attempts=%d transient=%d failure=%q", attempts, transient, failure)
	}
}

// A daily rescan republishes unchanged cards. It must not turn a stopped
// transient card back on, because that would probe a dead source every day
// after the operator-visible stop threshold.
func TestCatalogSidecarRescanKeepsStoppedTransientCard(t *testing.T) {
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	metadataReviewExec(t, f.server.db, `INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,sidecar_pending,sidecar_attempts,sidecar_transient_attempts,sidecar_retry_at,sidecar_failure,sidecar_revision)
		VALUES(74,1,'/Example Work','Example Work',0,0,8,'2026-01-01 00:00:00','temporarily_unavailable','readme.txt|24|2026-01-01T00:00:00Z');
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes,modified_at,observed_generation)
		VALUES(74,'readme.txt','readme.txt','text',24,'2026-01-01T00:00:00Z',1);`)
	tx, err := f.server.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = markCatalogSidecarCandidatesTx(context.Background(), tx, []int64{74}, 1); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var pending, transient int
	if err = f.server.db.QueryRow(`SELECT sidecar_pending,sidecar_transient_attempts FROM source_catalog_entry WHERE id=74`).Scan(&pending, &transient); err != nil {
		t.Fatal(err)
	}
	if pending != 0 || transient != 8 {
		t.Fatalf("rescan restarted a stopped transient card: pending=%d transient=%d", pending, transient)
	}
}

// A definitive answer (404/410) spends the permanent budget, so a release that
// really deleted its readme is not requested on every scan.
func TestCatalogSidecarPermanentFailureSpendsBudget(t *testing.T) {
	db := openMigratedTestDB(t)
	s := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-editor", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	defer func() { _ = s.Shutdown(context.Background()) }()
	metadataReviewExec(t, db, `INSERT INTO source_catalog_entry(id,root_path,title,sidecar_pending,sidecar_attempts)
		VALUES(71,'/Example Work','Example Work',1,4);`)
	s.finishCatalogSidecar(context.Background(), catalogSidecarCandidate{ID: 71}, catalogSidecarStateRetry, catalogSidecarReadResult{Failed: true, Permanent: true})
	var pending, attempts int
	var failure string
	if err := db.QueryRow(`SELECT sidecar_pending,sidecar_attempts,sidecar_failure FROM source_catalog_entry WHERE id=71`).Scan(&pending, &attempts, &failure); err != nil {
		t.Fatal(err)
	}
	if pending != 0 || attempts != 5 || failure != "unreadable" {
		t.Fatalf("permanent failure did not settle: pending=%d attempts=%d failure=%q", pending, attempts, failure)
	}
}

// The backoff doubles and is capped; a long outage becomes a slow probe rather
// than a hot loop or a permanent stop.
func TestCatalogSidecarTransientDelayIsBounded(t *testing.T) {
	if got := catalogSidecarTransientDelay(1); got != 15*time.Minute {
		t.Fatalf("first delay = %v", got)
	}
	if got := catalogSidecarTransientDelay(2); got != 30*time.Minute {
		t.Fatalf("second delay = %v", got)
	}
	if got := catalogSidecarTransientDelay(99); got != 24*time.Hour {
		t.Fatalf("capped delay = %v", got)
	}
}

// The operator route reopens settled cards without touching evidence or
// personal records, and a normal user cannot use it.
func TestCatalogSidecarRetryRouteReopensSettledCards(t *testing.T) {
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	metadataReviewExec(t, f.server.db, `INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,sidecar_pending,sidecar_attempts,sidecar_failure)
		VALUES(72,1,'/Settled','Settled card',0,5,'unreadable');
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes,observed_generation)
		VALUES(72,'readme.txt','readme.txt','text',24,1);`)
	result := f.request(t, http.MethodPost, "/catalog/sidecars/retry", `{}`, 200)
	if result["reopened"] != float64(1) {
		t.Fatalf("retry response = %#v", result)
	}
	var pending, attempts int
	if err := f.server.db.QueryRow(`SELECT sidecar_pending,sidecar_attempts FROM source_catalog_entry WHERE id=72`).Scan(&pending, &attempts); err != nil {
		t.Fatal(err)
	}
	if pending != 1 || attempts != 0 {
		t.Fatalf("retry route left the card settled: pending=%d attempts=%d", pending, attempts)
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

// The revision is produced once and compared with itself. Building it a second
// time in SQL ordered the same files differently, so an unchanged release
// looked changed on every rescan and the attempt budget never settled.
func TestCatalogSidecarRevisionMatchesRescanQuery(t *testing.T) {
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	metadataReviewExec(t, f.server.db, `INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,sidecar_pending,sidecar_attempts,sidecar_revision)
		VALUES(40,1,'/Example Work','Example Work',0,5,'');
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes,modified_at,observed_generation)
		VALUES(40,'readme.txt','readme.txt','text',10,'m1',1),
			(40,'aaa_credit.txt','aaa_credit.txt','text',20,'m2',1),
			(40,'01.mp3','01.mp3','audio',100,'m3',1);`)
	tx, err := f.server.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	queryRevision, err := publishedCatalogSidecarRevision(context.Background(), tx, 40, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	files, err := f.server.loadSourceCatalogFiles(context.Background(), 40)
	if err != nil {
		t.Fatal(err)
	}
	if want := catalogSidecarRevision(files); queryRevision != want {
		t.Fatalf("rescan revision = %q, reader revision = %q", queryRevision, want)
	}
	metadataReviewExec(t, f.server.db, `UPDATE source_catalog_entry SET sidecar_revision=? WHERE id=40`, queryRevision)
	// Both credit files are selected and the audio file is not, in the
	// reader's own order.
	if !strings.Contains(queryRevision, "readme.txt|10|m1\naaa_credit.txt|20|m2") {
		t.Fatalf("revision = %q", queryRevision)
	}
	tx, err = f.server.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = markCatalogSidecarCandidatesTx(context.Background(), tx, []int64{40}, 1); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var pending, attempts int
	if err = f.server.db.QueryRow(`SELECT sidecar_pending,sidecar_attempts FROM source_catalog_entry WHERE id=40`).Scan(&pending, &attempts); err != nil {
		t.Fatal(err)
	}
	if pending != 1 || attempts != 5 {
		t.Fatalf("unchanged revision reset the budget: pending=%d attempts=%d", pending, attempts)
	}
}

// Rewriting identical bytes with a newer timestamp is a new revision for the
// reader, so the stored revision must follow it; otherwise every rescan reads
// the same unchanged file again after the budget was already spent.
func TestCatalogSidecarStoresRevisionWhenOnlyTheTimestampChanges(t *testing.T) {
	f := newCatalogIndexFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	cache := catalogSidecarCache{Sidecars: []catalogSidecarMeta{{
		RelativePath: "readme.txt", Size: 24, ModifiedAt: "2026-01-01T00:00:00Z",
		Digest: "abc", VoiceActors: []string{"Example Voice"}, Circles: []string{},
		FetchedAt: "2026-01-01T00:00:00Z",
	}}}
	raw, err := json.Marshal(cache)
	if err != nil {
		t.Fatal(err)
	}
	metadataReviewExec(t, f.server.db, `INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,sidecar_json,sidecar_attempts,sidecar_revision)
		VALUES(41,1,'/Example Work','Example Work',?,5,'readme.txt|24|2026-01-01T00:00:00Z')`, string(raw))
	before := 0
	if err = f.server.db.QueryRow(`SELECT revision FROM work_matching_control WHERE id=1`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err = f.server.storeCatalogSidecars(context.Background(), 41, cache, "readme.txt|24|2026-02-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	var revision string
	if err = f.server.db.QueryRow(`SELECT sidecar_revision FROM source_catalog_entry WHERE id=41`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != "readme.txt|24|2026-02-01T00:00:00Z" {
		t.Fatalf("stored revision = %q", revision)
	}
	after := 0
	if err = f.server.db.QueryRow(`SELECT revision FROM work_matching_control WHERE id=1`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("bookkeeping-only change restarted matching: %d -> %d", before, after)
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
