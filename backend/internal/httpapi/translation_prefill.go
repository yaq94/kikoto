package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yexca/kikoto/backend/internal/translation"
)

// The prefill pass walks the published catalog once and fills every display
// language it can. It is deliberately slow and restartable: a run may take
// several nights, and the cursor is committed after each batch so a restart
// resumes instead of starting over.
const (
	translationPrefillBatchSize   = 50
	translationPrefillInterval    = 15 * time.Second
	translationPrefillParallel    = 8
	translationPrefillFailureStop = 3
)

// prefillLanguages are the languages the model can produce. Traditional
// Chinese is derived from Simplified at display time and is not stored twice.
var prefillLanguages = []string{
	translation.SourceEnglish,
	translation.SourceHans,
	translation.SourceJapanese,
	translation.SourceKorean,
}

type translationPrefillLanguage struct {
	Language string `json:"language"`
	Rows     int64  `json:"rows"`
	Missing  int64  `json:"missing"`
}

type translationPrefillView struct {
	Status        string                       `json:"status"`
	Cursor        int64                        `json:"cursor"`
	Total         int64                        `json:"total"`
	Languages     []translationPrefillLanguage `json:"languages"`
	NightWindow   bool                         `json:"nightWindow"`
	LastError     string                       `json:"lastError,omitempty"`
	UpdatedAt     string                       `json:"updatedAt,omitempty"`
	RatePerMinute float64                      `json:"ratePerMinute"`
}

// translationPrefillState reads only the durable cursor/status row. The worker
// uses this projection so a 15-second tick never scans the whole corpus.
func (s *Server) translationPrefillState(ctx context.Context) (translationPrefillView, error) {
	view := translationPrefillView{Status: "idle", Languages: []translationPrefillLanguage{}}
	var updated string
	err := s.db.QueryRowContext(ctx, `SELECT cursor_entry_id,status,last_error,updated_at FROM translation_prefill_state WHERE id=1`).Scan(&view.Cursor, &view.Status, &view.LastError, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		view.Status = "idle"
	} else if err != nil {
		return view, err
	}
	view.UpdatedAt = updated
	return view, nil
}

// translationPrefillStatus reads the durable cursor and per-language coverage.
// It is the operator view; the background worker must use
// translationPrefillState instead.
func (s *Server) translationPrefillStatus(ctx context.Context) (translationPrefillView, error) {
	view, err := s.translationPrefillState(ctx)
	if err != nil {
		return view, err
	}
	view.NightWindow = translation.IsNightWindow(time.Now())
	view.RatePerMinute = s.translationPrefillRate()
	if err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM source_catalog_entry
		WHERE relocated_to_id IS NULL AND availability_status='available' AND trim(title)<>''`).Scan(&view.Total); err != nil {
		return view, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT language, count(*) FROM content_translation
		WHERE entity_kind='entry' AND field='title' GROUP BY language`)
	if err != nil {
		return view, err
	}
	defer rows.Close()
	byLanguage := map[string]int64{}
	for rows.Next() {
		var language string
		var count int64
		if err = rows.Scan(&language, &count); err != nil {
			return view, err
		}
		byLanguage[language] = count
	}
	if err = rows.Err(); err != nil {
		return view, err
	}
	for _, language := range append(prefillLanguages, translation.SourceHant) {
		view.Languages = append(view.Languages, translationPrefillLanguage{Language: language, Rows: byLanguage[language], Missing: max64(view.Total-byLanguage[language], 0)})
	}
	return view, nil
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// translationPrefillRate reports rows written in the last minute. It is a
// process-local gauge: after a restart it starts at zero rather than pretending
// to know the previous process's work.
func (s *Server) translationPrefillRate() float64 {
	s.translationPrefillMu.Lock()
	defer s.translationPrefillMu.Unlock()
	cutoff := time.Now().Add(-time.Minute)
	total := 0
	for _, stamp := range s.translationPrefillWrites {
		if stamp.After(cutoff) {
			total++
		}
	}
	return float64(total)
}

func (s *Server) noteTranslationPrefillWrite(count int) {
	if count <= 0 {
		return
	}
	s.translationPrefillMu.Lock()
	defer s.translationPrefillMu.Unlock()
	now := time.Now()
	for range count {
		s.translationPrefillWrites = append(s.translationPrefillWrites, now)
	}
	// Keep the gauge bounded: only the last minute can affect the answer.
	cutoff := now.Add(-time.Minute)
	kept := s.translationPrefillWrites[:0]
	for _, stamp := range s.translationPrefillWrites {
		if stamp.After(cutoff) {
			kept = append(kept, stamp)
		}
	}
	s.translationPrefillWrites = kept
}

// noteTranslationPrefillFailure counts consecutive batches in which at least
// one language for the entry failed. A later batch with no failure clears it.
func (s *Server) noteTranslationPrefillFailure(entryID int64) int {
	s.translationPrefillMu.Lock()
	defer s.translationPrefillMu.Unlock()
	if s.translationPrefillFailures == nil {
		s.translationPrefillFailures = map[int64]int{}
	}
	s.translationPrefillFailures[entryID]++
	return s.translationPrefillFailures[entryID]
}

func (s *Server) clearTranslationPrefillFailure(entryID int64) {
	s.translationPrefillMu.Lock()
	defer s.translationPrefillMu.Unlock()
	delete(s.translationPrefillFailures, entryID)
}

func (s *Server) clearTranslationPrefillFailures(entryIDs []int64) {
	s.translationPrefillMu.Lock()
	defer s.translationPrefillMu.Unlock()
	for _, entryID := range entryIDs {
		delete(s.translationPrefillFailures, entryID)
	}
}

// runTranslationPrefillWorker is started with the other background workers.
// It idles when the corpus is complete and resumes automatically when a new
// entry needs rows.
func (s *Server) runTranslationPrefillWorker(ctx context.Context) {
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	retentionTick := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		_, _ = s.runTranslationPrefillBatch(ctx)
		// The corpus limits are a low-frequency maintenance concern. Running
		// them on every 15-second tick would take the writer far more often
		// than the budget can change.
		retentionTick++
		if retentionTick >= 40 {
			retentionTick = 0
			if cfg, err := translation.NewStore(s.db).Load(ctx); err == nil && s.translationDiskPause(cfg) == "" {
				if err = s.pruneContentTranslations(ctx, cfg); err != nil {
					slog.Warn("translation corpus retention failed", "error", err)
				}
			}
		}
		timer.Reset(translationPrefillInterval)
	}
}

// runTranslationPrefillBatch fills one bounded batch. It returns whether more
// rows remain.
func (s *Server) runTranslationPrefillBatch(ctx context.Context) (bool, error) {
	cfg, err := translation.NewStore(s.db).Load(ctx)
	if err != nil {
		return false, err
	}
	state, err := s.translationPrefillState(ctx)
	if err != nil {
		return false, err
	}
	if state.Status == "complete" {
		return false, nil
	}
	if state.Status == "paused" {
		// A manual pause waits for the operator. A low-disk pause is a
		// resource condition: resume by itself once the reserve is available
		// again, without guessing whether the operator meant to keep it off.
		if state.LastError == "" || s.translationDiskPause(cfg) != "" {
			return false, nil
		}
		if _, err = s.db.ExecContext(ctx, `UPDATE translation_prefill_state SET status='running', last_error='', updated_at=CURRENT_TIMESTAMP WHERE id=1`); err != nil {
			return false, err
		}
		state.Status = "running"
	}
	if pause := s.translationDiskPause(cfg); pause != "" {
		_, err = s.db.ExecContext(ctx, `UPDATE translation_prefill_state SET status='paused', last_error=?, updated_at=CURRENT_TIMESTAMP WHERE id=1`, pause)
		return false, err
	}
	entries, err := s.translationPrefillEntries(ctx, state.Cursor)
	if err != nil {
		return false, err
	}
	if len(entries) == 0 {
		_, err = s.db.ExecContext(ctx, `UPDATE translation_prefill_state SET status='complete', last_error='', updated_at=CURRENT_TIMESTAMP WHERE id=1`)
		return false, err
	}
	lastID := entries[len(entries)-1].id
	// A skipped entry can fall back into the window when a lower failure pulls
	// the cursor back. Do not spend the batch on it again; the count already
	// proved it is not making progress.
	active := make([]translationPrefillEntry, 0, len(entries))
	alreadySkipped := []int64{}
	for _, entry := range entries {
		if s.translationPrefillFailureCount(entry.id) >= translationPrefillFailureStop {
			alreadySkipped = append(alreadySkipped, entry.id)
			continue
		}
		active = append(active, entry)
	}
	written := 0
	var failed map[int64]error
	var runErr error
	if len(active) > 0 {
		written, failed, runErr = s.translationPrefillFields(ctx, cfg, active)
	}
	cursor := lastID
	message := ""
	if runErr != nil {
		// A batch-level failure was not attributed to an entry. Keep the
		// cursor before this batch so the next tick retries it.
		cursor = active[0].id - 1
		message = errorText(runErr)
	} else {
		retrying := []int64{}
		skipped := append([]int64(nil), alreadySkipped...)
		failedIDs := make([]int64, 0, len(failed))
		for id := range failed {
			failedIDs = append(failedIDs, id)
		}
		sort.Slice(failedIDs, func(i, j int) bool { return failedIDs[i] < failedIDs[j] })
		for _, id := range failedIDs {
			attempts := s.noteTranslationPrefillFailure(id)
			if attempts < translationPrefillFailureStop {
				retrying = append(retrying, id)
				if id-1 < cursor {
					cursor = id - 1
				}
				continue
			}
			skipped = append(skipped, id)
		}
		for _, entry := range active {
			if _, ok := failed[entry.id]; !ok {
				s.clearTranslationPrefillFailure(entry.id)
			}
		}
		sort.Slice(skipped, func(i, j int) bool { return skipped[i] < skipped[j] })
		message = translationPrefillFailureMessage(failed, retrying, skipped)
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE translation_prefill_state SET cursor_entry_id=?, status='running', last_error=?, updated_at=CURRENT_TIMESTAMP WHERE id=1`, cursor, message); err != nil {
		return false, err
	}
	s.noteTranslationPrefillWrite(written)
	return len(entries) == translationPrefillBatchSize, runErr
}

// translationDiskPause keeps the background translation pass from consuming
// the last free bytes of the volume that holds SQLite and its WAL. The cache
// reserve is a floor, so the batch leaves the same headroom for a write that
// the configured minimum promises to the operator.
func (s *Server) translationDiskPause(cfg translation.Config) string {
	root := filepath.Dir(s.cfg.DatabasePath)
	if s.cfg.DatabasePath == "" {
		root = s.cfg.CacheRoot
	}
	measure := s.catalogDiskSpace
	if measure == nil {
		measure = queryDiskSpace
	}
	snapshot, err := measure(root)
	if err != nil {
		return "disk_space_unknown"
	}
	const writeHeadroom = 64 << 20
	if snapshot.Available < uint64(cfg.MinFreeBytes)+writeHeadroom {
		return "paused_low_disk"
	}
	return ""
}

// translationPrefillFailureCount is the process-local consecutive-failure
// count for one entry.
func (s *Server) translationPrefillFailureCount(entryID int64) int {
	s.translationPrefillMu.Lock()
	defer s.translationPrefillMu.Unlock()
	return s.translationPrefillFailures[entryID]
}

// translationPrefillFailureMessage keeps skipped ids/count at the front so
// truncation to the column's 300-byte budget never hides what was skipped.
func translationPrefillFailureMessage(failed map[int64]error, retrying, skipped []int64) string {
	parts := []string{}
	if len(skipped) > 0 {
		ids := make([]string, 0, len(skipped))
		for _, id := range skipped {
			ids = append(ids, fmt.Sprintf("%d", id))
		}
		parts = append(parts, fmt.Sprintf("skipped %d entries after %d failures: %s", len(skipped), translationPrefillFailureStop, strings.Join(ids, ",")))
		for _, id := range skipped {
			if err := failed[id]; err != nil {
				parts = append(parts, fmt.Sprintf("entry %d: %v", id, err))
				break
			}
		}
	}
	if len(retrying) > 0 {
		reasons := make([]string, 0, len(retrying))
		for _, id := range retrying {
			if err := failed[id]; err != nil {
				reasons = append(reasons, fmt.Sprintf("entry %d: %v", id, err))
			}
		}
		if len(reasons) > 0 {
			parts = append(parts, "retrying "+strings.Join(reasons, "; "))
		}
	}
	text := strings.Join(parts, "; ")
	if len(text) > 300 {
		text = text[:300]
	}
	return text
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	if len(text) > 300 {
		text = text[:300]
	}
	return text
}

type translationPrefillEntry struct {
	id       int64
	title    string
	sourceID int64
}

func (s *Server) translationPrefillEntries(ctx context.Context, after int64) ([]translationPrefillEntry, error) {
	// The background pass has no viewer to scope it. It must honour the same
	// source boundary as the reader: a disabled source, or a source that is no
	// longer an admitted catalog, is not sent to the model.
	rows, err := s.db.QueryContext(ctx, `SELECT e.id, COALESCE(e.file_source_id,0), e.title FROM source_catalog_entry e
		JOIN file_source source ON source.id=e.file_source_id
		WHERE e.id>? AND e.relocated_to_id IS NULL AND e.availability_status='available' AND trim(e.title)<>''
			AND source.enabled=1 AND source.source_type=?
		ORDER BY e.id LIMIT ?`, after, sourceTypeOpenList, translationPrefillBatchSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []translationPrefillEntry{}
	for rows.Next() {
		var entry translationPrefillEntry
		if err = rows.Scan(&entry.id, &entry.sourceID, &entry.title); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

// translationPrefillFields writes the native row and queues the missing model
// rows for one batch. The work is bounded and shares the background lane, so a
// viewer's request is never queued behind a whole backfill. The returned map is
// the set of entries with at least one failed language; the batch caller owns
// cursor movement and retry accounting.
func (s *Server) translationPrefillFields(ctx context.Context, cfg translation.Config, entries []translationPrefillEntry) (int, map[int64]error, error) {
	type entryTasks struct {
		entry    translationPrefillEntry
		once     sync.Once
		names    []string
		namesErr error
	}
	type task struct {
		work     *entryTasks
		language string
		key      string
	}
	tasks := []task{}
	failed := map[int64]error{}
	written := 0
	for _, entry := range entries {
		detected := translation.DetectSourceLanguage(entry.title)
		work := &entryTasks{entry: entry}
		// A Traditional title is already the text a Traditional reader wants.
		// Recording it as a native row keeps the four model languages from
		// re-translating it and makes the coverage report truthful.
		if detected == translation.SourceHant {
			key := contentTranslationCacheKey("entry", entry.id, "", "title", translation.SourceHant, entry.title, "native", "", "", "", false)
			row := contentTranslationRow{CacheKey: key, EntityKind: "entry", EntityID: entry.id, Field: "title", Language: translation.SourceHant, SourceText: entry.title, TranslatedText: entry.title, Origin: "native"}
			if err := s.saveContentTranslations(ctx, []contentTranslationRow{row}); err != nil {
				failed[entry.id] = err
				continue
			}
			written++
		}
		for _, language := range prefillLanguages {
			if detected == language {
				key := contentTranslationCacheKey("entry", entry.id, "", "title", language, entry.title, "native", "", "", "", false)
				row := contentTranslationRow{CacheKey: key, EntityKind: "entry", EntityID: entry.id, Field: "title", Language: language, SourceText: entry.title, TranslatedText: entry.title, Origin: "native"}
				if err := s.saveContentTranslations(ctx, []contentTranslationRow{row}); err != nil {
					if _, ok := failed[entry.id]; !ok {
						failed[entry.id] = err
					}
					continue
				}
				written++
				continue
			}
			key := contentTranslationCacheKey("entry", entry.id, "", "title", language, entry.title, "model", cfg.Model, cfg.BaseURL, translation.TemplateVersion, cfg.DisableThinking)
			tasks = append(tasks, task{work: work, language: language, key: key})
		}
	}
	if len(tasks) == 0 {
		return written, failed, nil
	}
	keys := make([]string, 0, len(tasks))
	for _, item := range tasks {
		keys = append(keys, item.key)
	}
	existing, err := s.lookupContentTranslations(ctx, keys)
	if err != nil {
		return written, failed, err
	}
	sem := make(chan struct{}, translationPrefillParallel)
	var wg sync.WaitGroup
	var mu sync.Mutex
	recordFailure := func(entryID int64, err error) {
		if err == nil {
			return
		}
		mu.Lock()
		if _, ok := failed[entryID]; !ok {
			failed[entryID] = err
		}
		mu.Unlock()
	}
	for _, item := range tasks {
		if _, ok := existing[item.key]; ok {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(item task) {
			defer wg.Done()
			defer func() { <-sem }()
			item.work.once.Do(func() {
				item.work.names, item.work.namesErr = s.catalogEntryProtectedNamesForPrefill(ctx, item.work.entry.id)
			})
			// A protected-name lookup that failed cannot be told apart from an
			// empty list. Sending the title without them would hand a published
			// circle or cast name to the model, so the entry waits for a retry.
			if item.work.namesErr != nil {
				recordFailure(item.work.entry.id, item.work.namesErr)
				return
			}
			target := translationTarget{modelLanguage: item.language}
			text, translateErr := s.translateInBackground(ctx, cfg, item.key, item.work.names, item.work.entry.title, target)
			if translateErr != nil {
				recordFailure(item.work.entry.id, translateErr)
				return
			}
			row := contentTranslationRow{CacheKey: item.key, EntityKind: "entry", EntityID: item.work.entry.id, Field: "title", Language: item.language, SourceText: item.work.entry.title, TranslatedText: text, Origin: "model", Model: cfg.Model, TemplateVersion: translation.TemplateVersion}
			if saveErr := s.saveContentTranslations(ctx, []contentTranslationRow{row}); saveErr != nil {
				recordFailure(item.work.entry.id, saveErr)
				return
			}
			mu.Lock()
			written++
			mu.Unlock()
		}(item)
	}
	wg.Wait()
	return written, failed, nil
}

// catalogEntryProtectedNamesForPrefill reuses the same protected-name rules as
// an interactive request. A failure is returned to the caller, which skips the
// entry: sending a title without its names would hand a published circle or
// cast name to the model.
func (s *Server) catalogEntryProtectedNamesForPrefill(ctx context.Context, entryID int64) ([]string, error) {
	entry, _, err := s.loadSourceCatalogEntry(ctx, entryID)
	if err != nil {
		return nil, err
	}
	return s.catalogEntryProtectedNames(ctx, entry)
}

// getTranslationPrefill serves the operator view.
func (s *Server) getTranslationPrefill(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePermission(w, r, "sources:write"); !ok {
		return
	}
	view, err := s.translationPrefillStatus(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// setTranslationPrefill lets an operator pause or resume the pass without a
// redeploy.
func (s *Server) setTranslationPrefill(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePermission(w, r, "sources:write"); !ok {
		return
	}
	var input struct {
		Status string `json:"status"`
	}
	if decodeTranslationBody(w, r, &input) != nil || (input.Status != "running" && input.Status != "paused") {
		writeAPIError(w, http.StatusBadRequest, "translation_invalid_input", "Choose running or paused.", false)
		return
	}
	if _, err := s.db.ExecContext(r.Context(), `UPDATE translation_prefill_state SET status=?, last_error='', updated_at=CURRENT_TIMESTAMP WHERE id=1`, input.Status); err != nil {
		writeError(w, err)
		return
	}
	view, err := s.translationPrefillStatus(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// noteCatalogEntriesPublishedTx asks the prefill pass to look at newly
// published cards again. The pass walks entry ids in order and stops at the
// end, so a later card with a higher id would be reached anyway; a card
// republished below the cursor needs the cursor moved back to it. The caller
// runs this in the publication transaction so a committed card and its cursor
// move cannot diverge.
func noteCatalogEntriesPublishedTx(ctx context.Context, tx *sql.Tx, entryIDs []int64) error {
	if len(entryIDs) == 0 {
		return nil
	}
	lowest := entryIDs[0]
	for _, id := range entryIDs[1:] {
		if id < lowest {
			lowest = id
		}
	}
	_, err := tx.ExecContext(ctx, `UPDATE translation_prefill_state
		SET cursor_entry_id=min(cursor_entry_id, ?-1),
			status=CASE WHEN status='complete' THEN 'running' ELSE status END,
			updated_at=CURRENT_TIMESTAMP
		WHERE id=1`, lowest)
	return err
}
