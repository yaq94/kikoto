package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yexca/kikoto/backend/internal/sqlutil"
	"github.com/yexca/kikoto/backend/internal/storage"
	"github.com/yexca/kikoto/backend/internal/testfixture"
	"github.com/yexca/kikoto/backend/internal/translation"
)

type translationContentionPath string

const (
	translationContentWrite    translationContentionPath = "content_save"
	translationSourceWrite     translationContentionPath = "source_field_save"
	translationCacheEviction   translationContentionPath = "cache_eviction"
	translationRawWriteControl translationContentionPath = "raw_begin_tx_control"

	translationContentionWriterCount   = storage.FilePoolSize
	translationContentionRetentionRows = translationContentionWriterCount + 2
)

type translationContentionResult struct {
	index    int
	status   string
	affected int64
	err      error
}

// A burst of translation writes must leave a connection available to readers.
// The raw control proves that this fixture can occupy the whole production
// pool: it uses the same immediate transactions as the unfixed write path.
func TestContentTranslationWriteContention(t *testing.T) {
	for _, kind := range []translationContentionPath{
		translationContentWrite,
		translationSourceWrite,
		translationCacheEviction,
		translationRawWriteControl,
	} {
		t.Run(string(kind), func(t *testing.T) {
			runTranslationWriteContention(t, kind, false)
		})
	}
}

// The external lock stays held past the default write budget. Every real write
// path must return a lock/deadline error without changing the cache or leaking
// a writer goroutine. The lock is not released early to make a failure pass.
func TestContentTranslationWriteContentionTimesOut(t *testing.T) {
	for _, kind := range []translationContentionPath{
		translationContentWrite,
		translationSourceWrite,
		translationCacheEviction,
	} {
		t.Run(string(kind), func(t *testing.T) {
			runTranslationWriteContention(t, kind, true)
		})
	}
}

func runTranslationWriteContention(t *testing.T, kind translationContentionPath, holdPastBudget bool) {
	t.Helper()
	db := openMigratedTestDBWithProductionPool(t)
	if got := db.Stats().MaxOpenConnections; got != storage.FilePoolSize {
		t.Fatalf("pool size = %d, want production size %d", got, storage.FilePoolSize)
	}
	prepareTranslationContentionRows(t, db, kind)

	holderDB, err := sql.Open("sqlite", dsnFor(db))
	if err != nil {
		t.Fatalf("open lock holder: %v", err)
	}
	holderDB.SetMaxOpenConns(1)
	defer func() { _ = holderDB.Close() }()
	holder, err := holderDB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin lock holder: %v", err)
	}
	if _, err := holder.Exec(`UPDATE app_setting SET value_json = value_json WHERE key = 'library_mode'`); err != nil {
		_ = holder.Rollback()
		t.Fatalf("take write lock: %v", err)
	}
	var releaseOnce sync.Once
	releaseLock := func() {
		releaseOnce.Do(func() { _ = holder.Rollback() })
	}
	defer releaseLock()

	writerCtx, cancelWriters := context.WithCancel(context.Background())
	defer cancelWriters()
	started := make(chan struct{}, translationContentionWriterCount)
	results := make(chan translationContentionResult, translationContentionWriterCount)
	done := make(chan struct{})
	var writers sync.WaitGroup
	for index := 0; index < translationContentionWriterCount; index++ {
		writers.Go(func() {
			started <- struct{}{}
			result := writeTranslationForContention(writerCtx, db, kind, index)
			result.index = index
			results <- result
		})
	}
	go func() {
		writers.Wait()
		close(done)
	}()
	defer func() {
		releaseLock()
		cancelWriters()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("translation contention writers did not stop")
		}
	}()
	for range translationContentionWriterCount {
		<-started
	}

	// Raw writers stay in the driver's 30-second busy wait. Bounded writers
	// repeatedly release their connections during the same observation window.
	waitForTranslationContention(t, db, kind == translationRawWriteControl, results)
	readCtx, cancelRead := context.WithTimeout(context.Background(), 400*time.Millisecond)
	readStarted := time.Now()
	var value string
	readErr := db.QueryRowContext(readCtx,
		`SELECT value_json FROM app_setting WHERE key = 'library_mode'`).Scan(&value)
	cancelRead()
	if kind == translationRawWriteControl {
		if !errors.Is(readErr, context.DeadlineExceeded) {
			t.Fatalf("raw control read error = %v (elapsed %s, pool %+v), want connection-wait deadline", readErr, time.Since(readStarted), db.Stats())
		}
	} else if readErr != nil {
		t.Fatalf("read while translation writers wait: %v (elapsed %s, pool %+v)", readErr, time.Since(readStarted), db.Stats())
	}

	resultTimeout := 10 * time.Second
	if holdPastBudget {
		// Keep the external lock until every writer has returned. This makes a
		// successful result or unchanged error handling a deterministic failure.
		resultTimeout = sqlutil.DefaultWriteBudget + 2*time.Second
	} else {
		releaseLock()
		if kind == translationRawWriteControl {
			cancelWriters()
		}
	}
	collected := collectTranslationContentionResults(t, results, resultTimeout)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("translation writer group remained active")
	}
	for _, result := range collected {
		switch {
		case holdPastBudget:
			if result.err == nil {
				t.Errorf("writer %d succeeded while the external lock remained held", result.index)
			} else if !errors.Is(result.err, context.DeadlineExceeded) && !sqlutil.IsBusy(result.err) {
				t.Errorf("writer %d error = %v, want a lock or deadline error", result.index, result.err)
			}
		case kind == translationRawWriteControl:
			// The control writers stay inside the driver's busy wait until the
			// test cancels them. modernc/sqlite then reports "interrupted (9)"
			// rather than context.Canceled, so the cancelled observation window
			// is what decides: any error is acceptable once the writers were
			// told to stop, and only an unrelated error is a failure.
			if result.err != nil && !errors.Is(result.err, context.Canceled) &&
				!sqlutil.IsBusy(result.err) && !isInterrupted(result.err) {
				t.Errorf("raw writer %d: %v", result.index, result.err)
			}
		default:
			if result.err != nil {
				t.Errorf("writer %d failed after the external lock was released: %v", result.index, result.err)
			}
			if kind != translationCacheEviction && result.status != contentTranslationStored {
				t.Errorf("writer %d status = %q, want %q", result.index, result.status, contentTranslationStored)
			}
			if kind == translationCacheEviction && result.affected != 1 {
				t.Errorf("writer %d evicted %d rows, want 1", result.index, result.affected)
			}
		}
	}
	verifyTranslationContentionRows(t, db, kind, holdPastBudget)
}

// isInterrupted reports the driver's own cancellation result. It is not a
// database state, so it must not be folded into IsBusy, which callers use to
// decide whether a retry is worthwhile.
func isInterrupted(err error) bool {
	for current := err; current != nil; current = errors.Unwrap(current) {
		if strings.Contains(strings.ToLower(current.Error()), "interrupted") {
			return true
		}
	}
	return false
}

func collectTranslationContentionResults(t *testing.T, results <-chan translationContentionResult, timeout time.Duration) []translationContentionResult {
	t.Helper()
	collected := make([]translationContentionResult, translationContentionWriterCount)
	seen := make([]bool, translationContentionWriterCount)
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for range translationContentionWriterCount {
		select {
		case result := <-results:
			if result.index < 0 || result.index >= translationContentionWriterCount || seen[result.index] {
				t.Fatalf("invalid or duplicate writer result: %+v", result)
			}
			seen[result.index] = true
			collected[result.index] = result
		case <-timer.C:
			t.Fatalf("translation writers did not return within %s", timeout)
		}
	}
	return collected
}

func waitForTranslationContention(t *testing.T, db *sql.DB, raw bool, results <-chan translationContentionResult) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		select {
		case result := <-results:
			t.Fatalf("writer returned before the contention probe: %+v", result)
		default:
		}
		if !raw || db.Stats().InUse == storage.FilePoolSize {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("raw writers did not occupy the production pool: %+v", db.Stats())
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Let every writer pass its initial configuration read and reach BEGIN.
	time.Sleep(250 * time.Millisecond)
}

func prepareTranslationContentionRows(t *testing.T, db *sql.DB, kind translationContentionPath) {
	t.Helper()
	var foreignKeys int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		t.Fatalf("foreign key enforcement = %d, err = %v", foreignKeys, err)
	}
	switch kind {
	case translationContentWrite, translationRawWriteControl:
		seedTranslationContentionWorks(t, db, translationContentionWriterCount)
	case translationSourceWrite:
		seedTranslationContentionSource(t, db)
	case translationCacheEviction:
		seedTranslationContentionSource(t, db)
		seedTranslationContentionWorks(t, db, translationContentionRetentionRows)
		seedTranslationContentionCache(t, db)
	default:
		t.Fatalf("unknown contention path %q", kind)
	}
}

func seedTranslationContentionWorks(t *testing.T, db *sql.DB, count int) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	for index := range count {
		if _, err := tx.Exec(`INSERT INTO work(id,primary_code,title) VALUES(?,?,?)`,
			index+1, testfixture.WorkCodeAt(index), "Example Work"); err != nil {
			t.Fatalf("seed work %d: %v", index, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func seedTranslationContentionSource(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO file_source(id,code,display_name,source_type)
		VALUES(1,'example_remote_a','Example Remote A','openlist')`); err != nil {
		t.Fatalf("seed source identity: %v", err)
	}
}

func seedTranslationContentionCache(t *testing.T, db *sql.DB) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	for index := range translationContentionRetentionRows {
		contentTime := fmt.Sprintf("2026-01-01 00:00:%02d", 2*index)
		if _, err := tx.Exec(`INSERT INTO content_translation(
			cache_key,entity_kind,entity_id,field,language,source_text,translated_text,
			origin,model,template_version,updated_at)
				VALUES(?, 'entry', ?, 'title', 'en', 'Example Work', 'Example translation',
			'model', 'test-model', 'v1', ?)`,
			fmt.Sprintf("contention-retention-content-%02d", index), 100000+index, contentTime); err != nil {
			t.Fatalf("seed content cache row %d: %v", index, err)
		}
		sourceTime := fmt.Sprintf("2026-01-01 00:00:%02d", 2*index+1)
		if _, err := tx.Exec(`INSERT INTO source_field_translation(
			cache_key,source_id,work_code,field,path,language,source_text,translated_text,
			model,template_version,updated_at)
			VALUES(?,1,?,'title','','en','Example Work','Example translation',
			'test-model','v1',?)`,
			fmt.Sprintf("contention-retention-source-%02d", index), testfixture.WorkCodeAt(index), sourceTime); err != nil {
			t.Fatalf("seed source cache row %d: %v", index, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func writeTranslationForContention(ctx context.Context, db *sql.DB, kind translationContentionPath, index int) translationContentionResult {
	server := &Server{db: db}
	switch kind {
	case translationContentWrite, translationRawWriteControl:
		status, err := writeContentTranslationForContention(ctx, db, index, kind == translationRawWriteControl)
		return translationContentionResult{status: status, err: err}
	case translationSourceWrite:
		status, err := server.saveSourceFieldTranslations(ctx, []sourceFieldTranslationRow{{
			CacheKey:        fmt.Sprintf("contention-source-%02d", index),
			SourceID:        1,
			WorkCode:        testfixture.WorkCodeAt(index),
			Field:           "title",
			Language:        "en",
			SourceText:      "Example Work",
			TranslatedText:  "Example Work translation",
			Model:           "test-model",
			TemplateVersion: "v1",
		}})
		return translationContentionResult{status: status, err: err}
	case translationCacheEviction:
		affected, err := server.deleteOldestTranslationRows(ctx, translation.Config{}, 1)
		return translationContentionResult{affected: affected, err: err}
	default:
		return translationContentionResult{err: fmt.Errorf("unknown contention path %q", kind)}
	}
}

func verifyTranslationContentionRows(t *testing.T, db *sql.DB, kind translationContentionPath, holdPastBudget bool) {
	t.Helper()
	switch kind {
	case translationContentWrite:
		want := translationContentionWriterCount
		if holdPastBudget {
			want = 0
		}
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM content_translation
			WHERE cache_key LIKE 'contention-content-%'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Errorf("saved content rows = %d, want %d", count, want)
		}
	case translationSourceWrite:
		want := translationContentionWriterCount
		if holdPastBudget {
			want = 0
		}
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM source_field_translation
			WHERE cache_key LIKE 'contention-source-%'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Errorf("saved source field rows = %d, want %d", count, want)
		}
	case translationCacheEviction:
		wantPerTable := translationContentionRetentionRows
		if !holdPastBudget {
			wantPerTable -= translationContentionWriterCount / 2
		}
		var contentRows, sourceRows int
		if err := db.QueryRow(`SELECT
			(SELECT count(*) FROM content_translation WHERE cache_key LIKE 'contention-retention-content-%'),
			(SELECT count(*) FROM source_field_translation WHERE cache_key LIKE 'contention-retention-source-%')`).Scan(&contentRows, &sourceRows); err != nil {
			t.Fatal(err)
		}
		if contentRows != wantPerTable || sourceRows != wantPerTable {
			t.Errorf("retained content/source rows = %d/%d, want %d/%d",
				contentRows, sourceRows, wantPerTable, wantPerTable)
		}
	}
}

func writeContentTranslationForContention(ctx context.Context, db *sql.DB, index int, raw bool) (string, error) {
	row := contentTranslationRow{
		CacheKey:        fmt.Sprintf("contention-content-%02d", index),
		EntityKind:      "work",
		EntityID:        int64(index + 1),
		Field:           "title",
		Language:        "en",
		SourceText:      "Example Work",
		TranslatedText:  "Example Work translation",
		Origin:          "model",
		Model:           "test-model",
		TemplateVersion: "v1",
	}
	if !raw {
		return (&Server{db: db}).saveContentTranslations(ctx, []contentTranslationRow{row})
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `INSERT INTO content_translation(
		cache_key,entity_kind,entity_id,entity_key,field,language,
		source_text,translated_text,origin,model,template_version)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`, row.CacheKey, row.EntityKind, row.EntityID,
		row.EntityKey, row.Field, row.Language, row.SourceText, row.TranslatedText,
		row.Origin, row.Model, row.TemplateVersion)
	if err != nil {
		return "", err
	}
	return "", tx.Commit()
}
