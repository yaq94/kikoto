package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yexca/kikoto/backend/internal/translation"
)

// The HTTP body is the external boundary: settings holds the configuration
// mutex while this client has not finished sending its request.
type delayedTranslationSettingsBody struct {
	io.Reader
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *delayedTranslationSettingsBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.started) })
	<-b.release
	return b.Reader.Read(p)
}

func holdTranslationSettingsRequest(t *testing.T, s *Server, admin *http.Cookie) func() {
	t.Helper()
	body := &delayedTranslationSettingsBody{Reader: strings.NewReader(`{"maxEntries":1}`), started: make(chan struct{}), release: make(chan struct{})}
	req := httptest.NewRequest(http.MethodPatch, "/api/settings/translation", body)
	req.AddCookie(admin)
	req.Header.Set("Content-Type", "application/json")
	done := make(chan struct{})
	response := httptest.NewRecorder()
	go func() { s.Routes().ServeHTTP(response, req); close(done) }()
	var once sync.Once
	release := func() {
		once.Do(func() { close(body.release) })
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("settings request did not release")
		}
	}
	t.Cleanup(release)
	select {
	case <-body.started:
	case <-time.After(time.Second):
		t.Fatal("settings request did not begin body read")
	}
	return release
}

func seedTranslationMaintenanceIdleState(t *testing.T, s *Server) {
	t.Helper()
	cfg := mustTranslationConfig(t, s)
	if _, err := s.db.Exec(`UPDATE translation_prefill_state SET status='complete',config_key=?,
		checked_revision=(SELECT revision FROM translation_corpus_revision WHERE id=1) WHERE id=1`, translation.ConfigKey(cfg)); err != nil {
		t.Fatal(err)
	}
}

func TestTranslationMaintenanceRunnerCancelsDuringSlowSettingsRequest(t *testing.T) {
	s, _, admin, _ := translationHTTPFixture(t)
	seedTranslationMaintenanceIdleState(t, s)
	release := holdTranslationSettingsRequest(t, s, admin)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { s.StartJobRunner(ctx); close(done) }()
	// Let the real first maintenance tick encounter the still-streaming request.
	time.Sleep(5500 * time.Millisecond)
	start := time.Now()
	cancel()
	exited := false
	select {
	case <-done:
		exited = true
	case <-time.After(300 * time.Millisecond):
	}
	t.Logf("runner exit before settings body released=%t elapsed_ms=%.3f", exited, float64(time.Since(start).Microseconds())/1000)
	// Always release the client and join the runner, including the red case.
	release()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runner did not exit after releasing settings request")
	}
	if !exited {
		t.Fatal("background maintenance ignored cancellation while settings request held configuration")
	}
}

func TestTranslationMaintenanceRunnerRetriesSkippedTickAndKeepsCurrentCorpus(t *testing.T) {
	s, host, admin, _ := translationHTTPFixture(t)
	seedPrefillCard(t, s, 1, "Example title")
	cfg := mustTranslationConfig(t, s)
	if status, err := s.saveContentTranslations(context.Background(), []contentTranslationRow{
		{CacheKey: "example-current", EntityKind: "entry", EntityID: 1, Field: "title", Language: "ja", SourceText: "Example title", TranslatedText: "Example current", Origin: "model", ConfigKey: translation.ConfigKey(cfg)},
		{CacheKey: "example-history", EntityKind: "entry", EntityID: 1, Field: "title", Language: "ja", SourceText: "Example old title", TranslatedText: "Example old", Origin: "model", ConfigKey: translation.ConfigKey(cfg)},
		{CacheKey: "example-native", EntityKind: "entry", EntityID: 1, Field: "title", Language: "en", SourceText: "Example title", TranslatedText: "Example title", Origin: "native"},
	}); err != nil || status != contentTranslationStored {
		t.Fatalf("seed translations: %s %v", status, err)
	}
	// Pause only model work; retention remains a lifecycle maintenance concern.
	translationRequest(t, host, admin, http.MethodPost, "/api/settings/translation/prefill", `{"status":"paused"}`, http.StatusOK)
	release := holdTranslationSettingsRequest(t, s, admin)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.StartJobRunner(ctx); close(done) }()
	defer func() {
		cancel()
		release()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("runner did not stop")
		}
	}()
	time.Sleep(5500 * time.Millisecond)
	keys := []string{"example-current", "example-history", "example-native"}
	rows, err := s.lookupContentTranslations(context.Background(), keys)
	if err != nil || len(rows) != 3 {
		t.Fatalf("maintenance ran while config was locked: count=%d err=%v", len(rows), err)
	}
	release()
	start := time.Now()
	deadline := time.NewTimer(17 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(20 * time.Millisecond)
	defer poll.Stop()
	for {
		rows, err = s.lookupContentTranslations(context.Background(), keys)
		if err != nil {
			t.Fatal(err)
		}
		if _, historical := rows["example-history"]; !historical {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("skipped maintenance was not retried on the next tick")
		case <-poll.C:
		}
	}
	if len(rows) != 2 || rows["example-current"].TranslatedText != "Example current" || rows["example-native"].Origin != "native" {
		t.Fatalf("maintenance lost protected translations: %#v", rows)
	}
	t.Logf("maintenance retried after settings release in %.3f ms; current/native retained", float64(time.Since(start).Microseconds())/1000)
}
