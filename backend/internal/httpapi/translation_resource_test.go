package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yexca/kikoto/backend/internal/config"
	"github.com/yexca/kikoto/backend/internal/storage"
	"github.com/yexca/kikoto/backend/internal/translation"
)

// Coverage is a database boundary with a fixed scan budget, regardless of the
// number of reported languages. This catches the repeated corpus materialization
// that made an already complete 42k-card library busy every worker tick.
func TestTranslationCoverageUsesOneCorpusRead(t *testing.T) {
	db, counter := openCatalogCountingTestDB(t)
	s := NewServer(db, config.Config{})
	seedPrefillCard(t, s, 1, "Example title")
	cfg := mustTranslationConfig(t, s)
	counter.reset()
	coverage, err := s.translationCorpusCoverageFor(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if coverage.Total != 1 || coverage.Missing != 4 {
		t.Fatalf("coverage = %#v", coverage)
	}
	reads := 0
	for _, statement := range counter.snapshot() {
		if strings.Contains(strings.ToLower(statement), "corpus as materialized") {
			reads++
		}
	}
	if reads != 1 {
		t.Fatalf("full corpus reads = %d, want 1", reads)
	}
}

func TestTranslationRevisionRollbackAndUnrelatedWritesKeepSnapshot(t *testing.T) {
	db, counter := openCatalogCountingTestDB(t)
	s := NewServer(db, config.Config{})
	seedPrefillCard(t, s, 1, "Example title")
	cfg := mustTranslationConfig(t, s)
	if _, err := s.cachedTranslationOperatorSnapshot(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE source_catalog_entry SET title='Rollback title' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE source_catalog_entry SET updated_at=CURRENT_TIMESTAMP WHERE id=1;
		UPDATE source_catalog_file SET size_bytes=999 WHERE catalog_entry_id=1;
		UPDATE file_source SET display_name='Example renamed source' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	counter.reset()
	for range 3 {
		if _, err := s.cachedTranslationOperatorSnapshot(context.Background(), cfg); err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range counter.snapshot() {
		if strings.Contains(statement, "MATERIALIZED") || strings.Contains(statement, "dbstat") {
			t.Fatal("rollback/unrelated write invalidated snapshot")
		}
	}
}

func TestTranslationCompleteRevisionSurvivesRestartAndFindsNewEntity(t *testing.T) {
	image, err := migratedTestDatabaseImage()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "translation.db")
	if err := os.WriteFile(path, image, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(db, config.Config{})
	var calls atomic.Int32
	model := successfulPrefillModel(t, &calls)
	defer model.Close()
	url, minFree := model.URL, int64(0)
	if _, err := translation.NewStore(db).Update(context.Background(), translation.Patch{BaseURL: &url, MinFreeBytes: &minFree}); err != nil {
		t.Fatal(err)
	}
	seedPrefillCard(t, s, 1, "Example title")
	if _, err := s.runTranslationPrefillBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := calls.Load()
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	s = NewServer(db, config.Config{})
	defer func() { _ = s.Shutdown(context.Background()) }()
	if _, err := s.runTranslationPrefillBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != before {
		t.Fatal("restart repeated completed model calls")
	}
	seedPrefillCard(t, s, 2, "Example second title")
	if _, err := s.runTranslationPrefillBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != before+3 {
		t.Fatalf("new entity calls = %d, want %d", calls.Load(), before+3)
	}
	_, status, _ := readPrefillState(t, s)
	if status != "complete" {
		t.Fatalf("restarted prefill status = %s", status)
	}
}

func TestTranslationCoverageInvalidatesEveryCorpusDependency(t *testing.T) {
	for _, tc := range []struct {
		name, mutation        string
		total, stale, missing int64
	}{
		{"source text", `UPDATE source_catalog_entry SET title='Changed title' WHERE id=1`, 1, 4, 0},
		{"source disabled", `UPDATE file_source SET enabled=0 WHERE id=1`, 0, 0, 0},
		{"source type", `UPDATE file_source SET source_type='localfs' WHERE id=1`, 0, 0, 0},
		{"entity removed", `DELETE FROM source_catalog_entry WHERE id=1`, 0, 0, 0},
		{"admission files", `UPDATE source_catalog_file SET kind='other' WHERE catalog_entry_id=1`, 0, 0, 0},
		{"admission summary", `INSERT INTO source_catalog_entry_admission(catalog_entry_id,generation,files,audio,non_audio) VALUES(1,0,5001,1,5000) ON CONFLICT(catalog_entry_id) DO UPDATE SET generation=0,files=5001,audio=1,non_audio=5000`, 0, 0, 0},
		{"generation", `UPDATE source_catalog_entry SET last_seen_generation=9 WHERE id=1`, 0, 0, 0},
		{"relocated", `INSERT INTO source_catalog_entry(id,file_source_id,root_path,title) VALUES(2,1,'/Other',''); UPDATE source_catalog_entry SET relocated_to_id=2 WHERE id=1`, 0, 0, 0},
		{"work inserted", `INSERT INTO work(primary_code,title) VALUES('RJ00000000','Example Work')`, 2, 0, 4},
		{"translation removed", `DELETE FROM content_translation WHERE language='ja'`, 1, 0, 1},
		{"publication pending", `INSERT INTO source_catalog_episode_pending(source_id,parent_root,catalog_entry_root,catalog_entry_id) VALUES(1,'/Parent','/Example',1)`, 0, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, host, admin, _ := translationHTTPFixture(t)
			seedPrefillCard(t, s, 1, "Example title")
			cfg := mustTranslationConfig(t, s)
			for _, lang := range translationModelLanguages {
				if _, err := s.saveContentTranslations(context.Background(), []contentTranslationRow{{CacheKey: "example:" + lang, EntityKind: "entry", EntityID: 1, Field: "title", Language: lang, SourceText: "Example title", TranslatedText: "Example translation", Origin: "model", ConfigKey: translation.ConfigKey(cfg)}}); err != nil {
					t.Fatal(err)
				}
			}
			translationRequest(t, host, admin, "GET", "/api/settings/translation/prefill", "", 200)
			if _, err := s.db.Exec(tc.mutation); err != nil {
				t.Fatal(err)
			}
			view := translationRequest(t, host, admin, "GET", "/api/settings/translation/prefill", "", 200)
			var stale, missing int64
			for _, language := range view["languages"].([]any) {
				item := language.(map[string]any)
				if item["language"] != "zh-Hant" {
					stale += int64(item["stale"].(float64))
					missing += int64(item["missing"].(float64))
				}
			}
			if view["total"] != float64(tc.total) || stale != tc.stale || missing != tc.missing {
				t.Fatalf("total/stale/missing = %v/%d/%d, want %d/%d/%d", view["total"], stale, missing, tc.total, tc.stale, tc.missing)
			}
		})
	}
}

func TestTranslationPrefillConcurrentEntityChangeDoesNotLoseEarlierWork(t *testing.T) {
	s, host, admin, _ := translationHTTPFixture(t)
	seedPrefillCard(t, s, 1, "Example title")
	started, resume := make(chan struct{}, 1), make(chan struct{})
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-resume
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"Example translation"},"finish_reason":"stop"}]}`)
	}))
	defer model.Close()
	configurePrefillModel(t, host, admin, model.URL)
	done := make(chan error, 1)
	go func() { _, err := s.runTranslationPrefillBatch(context.Background()); done <- err }()
	<-started
	if _, err := s.db.Exec(`UPDATE source_catalog_entry SET title='Changed title' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	close(resume)
	if err := <-done; err != nil && !errors.Is(err, errTranslationCorpusChanged) {
		t.Fatal(err)
	}
	view := translationRequest(t, host, admin, "GET", "/api/settings/translation/prefill", "", 200)
	if view["status"] == "complete" {
		t.Fatal("in-flight old text reported complete")
	}
	for range 3 {
		if _, err := s.runTranslationPrefillBatch(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	view = translationRequest(t, host, admin, "GET", "/api/settings/translation/prefill", "", 200)
	if view["status"] != "complete" {
		t.Fatalf("changed text did not recover: %v", view["status"])
	}
}

func TestTranslationRetentionCoalescesWritesAndRetriesAfterCancellation(t *testing.T) {
	db, counter := openCatalogCountingTestDB(t)
	s := NewServer(db, config.Config{})
	cfg := mustTranslationConfig(t, s)
	for range 10000 {
		s.pruneTranslationCacheAfterWrite(context.Background(), cfg, 32)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.pruneContentTranslations(ctx, cfg); err == nil {
		t.Fatal("cancelled maintenance succeeded")
	}
	if err := s.pruneContentTranslations(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	counter.reset()
	for range 3 {
		if err := s.pruneContentTranslations(context.Background(), cfg); err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range counter.snapshot() {
		if strings.Contains(statement, "DELETE") || strings.Contains(statement, "sum(") {
			t.Fatal("idle maintenance rescanned translations")
		}
	}
	if s.translationRetentionWrites.Load() != 0 {
		t.Fatal("maintenance did not drain coalesced writes")
	}
}

func TestTranslationCoverageTracksSeriesMembershipAndPublication(t *testing.T) {
	s, host, admin, _ := translationHTTPFixture(t)
	seedPrefillCard(t, s, 1, "Example first title")
	seedPrefillCard(t, s, 2, "Example second title")
	if _, err := s.db.Exec(`INSERT INTO catalog_series(id,title) VALUES(1,'Example Series');
		INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence) VALUES(1,1,1,'manual'),(1,2,2,'manual')`); err != nil {
		t.Fatal(err)
	}
	readTotal := func(want float64) {
		t.Helper()
		view := translationRequest(t, host, admin, "GET", "/api/settings/translation/prefill", "", 200)
		if view["total"] != want {
			t.Fatalf("total=%v, want %v", view["total"], want)
		}
	}
	readTotal(3)
	ctx, operation, err := s.beginCatalogPublication(context.Background(), 1, "synthetic:coverage-members")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.execCatalogWrite(ctx, `DELETE FROM catalog_series_member WHERE catalog_entry_id=2`); err != nil {
		t.Fatal(err)
	}
	readTotal(3)
	if err := s.commitCatalogPublication(ctx, operation); err != nil {
		t.Fatal(err)
	}
	readTotal(2)
	if _, err := s.db.Exec(`INSERT INTO source_catalog_episode_pending(source_id,parent_root,catalog_entry_root,catalog_entry_id) VALUES(1,'/Parent','/Example',2)`); err != nil {
		t.Fatal(err)
	}
	readTotal(1)
	if _, err := s.db.Exec(`INSERT INTO source_catalog_episode_publication(source_id,parent_root,state) VALUES(1,'/Parent','published')`); err != nil {
		t.Fatal(err)
	}
	readTotal(2)
	if _, err := s.db.Exec(`UPDATE source_catalog_episode_publication SET state='pending' WHERE source_id=1`); err != nil {
		t.Fatal(err)
	}
	readTotal(1)
}

func TestTranslationPrefillConfigurationSwitchDuringModelCallCannotCompleteOldConfig(t *testing.T) {
	s, host, admin, _ := translationHTTPFixture(t)
	seedPrefillCard(t, s, 1, "Example title")
	started, resume := make(chan struct{}, 1), make(chan struct{})
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-resume
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"Example translation"},"finish_reason":"stop"}]}`)
	}))
	defer model.Close()
	configurePrefillModel(t, host, admin, model.URL)
	done := make(chan error, 1)
	go func() { _, err := s.runTranslationPrefillBatch(context.Background()); done <- err }()
	<-started
	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", `{"model":"Example changed model"}`, 200)
	close(resume)
	if err := <-done; err != nil && !errors.Is(err, errTranslationCorpusChanged) {
		t.Fatal(err)
	}
	view := translationRequest(t, host, admin, "GET", "/api/settings/translation/prefill", "", 200)
	if view["status"] == "complete" {
		t.Fatal("old generation marked new config complete")
	}
	for range 3 {
		if _, err := s.runTranslationPrefillBatch(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	view = translationRequest(t, host, admin, "GET", "/api/settings/translation/prefill", "", 200)
	if view["status"] != "complete" {
		t.Fatalf("new config did not complete: %v", view["status"])
	}
}

func TestTranslationCompleteWorkerDoesNotRescanUnchangedCorpus(t *testing.T) {
	db, counter := openCatalogCountingTestDB(t)
	s := NewServer(db, config.Config{})
	seedPrefillCard(t, s, 1, "Example title")
	model := successfulPrefillModel(t, &atomic.Int32{})
	defer model.Close()
	url, minFree := model.URL, int64(0)
	if _, err := translation.NewStore(db).Update(context.Background(), translation.Patch{BaseURL: &url, MinFreeBytes: &minFree}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.runTranslationPrefillBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, status, _ := readPrefillState(t, s)
	if status != "complete" {
		t.Fatalf("status = %s", status)
	}
	// Rebuildable source display rows affect the budget but not corpus coverage.
	if _, err := s.saveSourceFieldTranslations(context.Background(), []sourceFieldTranslationRow{{CacheKey: "example-display", SourceID: 1, WorkCode: "RJ00000000", Field: "tag", Language: "ja", SourceText: "Example tag", TranslatedText: "Example text"}}); err != nil {
		t.Fatal(err)
	}
	counter.reset()
	for range 3 {
		if _, err := s.runTranslationPrefillBatch(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range counter.snapshot() {
		if strings.Contains(strings.ToLower(statement), "corpus as materialized") {
			t.Fatal("complete worker rescanned unchanged corpus")
		}
	}
}

// A binary's template version can change without any setting/database write.
// The persisted config key must invalidate completion even at the same revision.
func TestTranslationCompleteRevisionCannotHideConfigKeyChange(t *testing.T) {
	s, host, admin, _ := translationHTTPFixture(t)
	seedPrefillCard(t, s, 1, "Example title")
	model := successfulPrefillModel(t, &atomic.Int32{})
	defer model.Close()
	configurePrefillModel(t, host, admin, model.URL)
	if _, err := s.runTranslationPrefillBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE content_translation SET config_key='previous-template' WHERE origin='model';
		UPDATE translation_prefill_state SET config_key='previous-template',status='complete',
		checked_revision=(SELECT revision FROM translation_corpus_revision WHERE id=1) WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.runTranslationPrefillBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	view := translationRequest(t, host, admin, "GET", "/api/settings/translation/prefill", "", 200)
	if view["status"] != "complete" {
		t.Fatalf("configuration-key change left stale completed prefix: %v", view["status"])
	}
}

func TestTranslationRetentionRetryAfterWriterTimeoutKeepsCurrentCorpus(t *testing.T) {
	s := NewServer(openMigratedTestDBWithProductionPool(t), config.Config{})
	seedPrefillCard(t, s, 1, "Example title")
	cfg := mustTranslationConfig(t, s)
	if _, err := s.saveContentTranslations(context.Background(), []contentTranslationRow{
		{CacheKey: "example-current", EntityKind: "entry", EntityID: 1, Field: "title", Language: "ja", SourceText: "Example title", TranslatedText: "Example current", Origin: "model", ConfigKey: translation.ConfigKey(cfg)},
		{CacheKey: "example-history", EntityKind: "entry", EntityID: 1, Field: "title", Language: "ja", SourceText: "Example old title", TranslatedText: "Example old", Origin: "model", ConfigKey: translation.ConfigKey(cfg)},
	}); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE work SET title=title`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	start := time.Now()
	err = s.pruneContentTranslations(ctx, cfg)
	cancel()
	if time.Since(start) > time.Second {
		t.Fatal("maintenance held a connection for the driver's full busy timeout")
	}
	if err == nil {
		t.Fatal("maintenance ignored locked writer")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := s.pruneContentTranslations(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	rows, err := s.lookupContentTranslations(context.Background(), []string{"example-current", "example-history"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows["example-current"].TranslatedText != "Example current" {
		t.Fatalf("retry failed to retain current row and remove history: %v", rows)
	}
}

func TestTranslationWritesDoNotRunRetentionScans(t *testing.T) {
	db, counter := openCatalogCountingTestDB(t)
	s := NewServer(db, config.Config{})
	seedPrefillCard(t, s, 1, "Example title")
	rows := make([]contentTranslationRow, 32)
	for i := range rows {
		rows[i] = contentTranslationRow{CacheKey: strings.Repeat("x", i+1), EntityKind: "entry", EntityID: 1,
			Field: "tag", Language: "ja", SourceText: "Example tag", TranslatedText: "Example translation", Origin: "model"}
	}
	counter.reset()
	if status, err := s.saveContentTranslations(context.Background(), rows); err != nil || status != contentTranslationStored {
		t.Fatalf("save = %s, %v", status, err)
	}
	for _, statement := range counter.snapshot() {
		lower := strings.ToLower(statement)
		if strings.Contains(lower, "sum(byte_count)") || strings.Contains(lower, "from dbstat") || strings.Contains(lower, "delete from content_translation") {
			t.Fatal("interactive save ran a full retention query")
		}
	}
	stored, err := s.lookupContentTranslations(context.Background(), []string{rows[0].CacheKey})
	if err != nil || len(stored) != 1 {
		t.Fatalf("saved result unavailable: %v", err)
	}
}
