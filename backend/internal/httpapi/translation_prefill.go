package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/yexca/kikoto/backend/internal/translation"
)

// The prefill pass walks the published catalog once and fills every display
// language it can. It is deliberately slow and restartable: a run may take
// several nights, and the cursor is committed after each batch so a restart
// resumes instead of starting over.
const (
	translationPrefillBatchSize = 50
	translationPrefillInterval  = 15 * time.Second
	translationPrefillParallel  = 8
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

// translationPrefillStatus reads the durable cursor and per-language coverage.
func (s *Server) translationPrefillStatus(ctx context.Context) (translationPrefillView, error) {
	view := translationPrefillView{Status: "idle", Languages: []translationPrefillLanguage{}}
	var updated string
	err := s.db.QueryRowContext(ctx, `SELECT cursor_entry_id,status,last_error,updated_at FROM translation_prefill_state WHERE id=1`).Scan(&view.Cursor, &view.Status, &view.LastError, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		view.Status = "idle"
	} else if err != nil {
		return view, err
	}
	view.UpdatedAt = updated
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
	return view, rows.Err()
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

// runTranslationPrefillWorker is started with the other background workers.
// It idles when the corpus is complete and resumes automatically when a new
// entry needs rows.
func (s *Server) runTranslationPrefillWorker(ctx context.Context) {
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		_, _ = s.runTranslationPrefillBatch(ctx)
		timer.Reset(translationPrefillInterval)
	}
}

// runTranslationPrefillBatch fills one bounded batch. It returns whether more
// rows remain.
func (s *Server) runTranslationPrefillBatch(ctx context.Context) (bool, error) {
	state, err := s.translationPrefillStatus(ctx)
	if err != nil {
		return false, err
	}
	if state.Status == "complete" || state.Status == "paused" {
		return false, nil
	}
	entries, err := s.translationPrefillEntries(ctx, state.Cursor)
	if err != nil {
		return false, err
	}
	if len(entries) == 0 {
		_, err = s.db.ExecContext(ctx, `UPDATE translation_prefill_state SET status='complete', last_error='', updated_at=CURRENT_TIMESTAMP WHERE id=1`)
		return false, err
	}
	cfg, err := translation.NewStore(s.db).Load(ctx)
	if err != nil {
		return false, err
	}
	written, runErr := s.translationPrefillFields(ctx, cfg, entries)
	lastID := entries[len(entries)-1].id
	status := "running"
	if runErr != nil {
		status = "running"
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE translation_prefill_state SET cursor_entry_id=?, status=?, last_error=?, updated_at=CURRENT_TIMESTAMP WHERE id=1`, lastID, status, errorText(runErr)); err != nil {
		return false, err
	}
	s.noteTranslationPrefillWrite(written)
	return len(entries) == translationPrefillBatchSize, runErr
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
	rows, err := s.db.QueryContext(ctx, `SELECT id, COALESCE(file_source_id,0), title FROM source_catalog_entry
		WHERE id>? AND relocated_to_id IS NULL AND availability_status='available' AND trim(title)<>''
		ORDER BY id LIMIT ?`, after, translationPrefillBatchSize)
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
// viewer's request is never queued behind a whole backfill.
func (s *Server) translationPrefillFields(ctx context.Context, cfg translation.Config, entries []translationPrefillEntry) (int, error) {
	type task struct {
		entry    translationPrefillEntry
		language string
		key      string
	}
	tasks := []task{}
	written := 0
	for _, entry := range entries {
		detected := translation.DetectSourceLanguage(entry.title)
		for _, language := range prefillLanguages {
			if detected == language {
				key := contentTranslationCacheKey("entry", entry.id, "", "title", language, entry.title, "native", "", "", "", false)
				row := contentTranslationRow{CacheKey: key, EntityKind: "entry", EntityID: entry.id, Field: "title", Language: language, SourceText: entry.title, TranslatedText: entry.title, Origin: "native"}
				if err := s.saveContentTranslations(ctx, []contentTranslationRow{row}); err != nil {
					return written, err
				}
				written++
				continue
			}
			key := contentTranslationCacheKey("entry", entry.id, "", "title", language, entry.title, "model", cfg.Model, cfg.BaseURL, translation.TemplateVersion, cfg.DisableThinking)
			tasks = append(tasks, task{entry: entry, language: language, key: key})
		}
	}
	if len(tasks) == 0 {
		return written, nil
	}
	keys := make([]string, 0, len(tasks))
	for _, item := range tasks {
		keys = append(keys, item.key)
	}
	existing, err := s.lookupContentTranslations(ctx, keys)
	if err != nil {
		return written, err
	}
	sem := make(chan struct{}, translationPrefillParallel)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	for _, item := range tasks {
		if _, ok := existing[item.key]; ok {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(item task) {
			defer wg.Done()
			defer func() { <-sem }()
			names, namesErr := s.catalogEntryProtectedNamesForPrefill(ctx, item.entry.id)
			if namesErr != nil {
				names = nil
			}
			target := translationTarget{modelLanguage: item.language}
			text, translateErr := s.translateWithQueue(ctx, cfg, item.key, names, item.entry.title, target, "")
			if translateErr != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = translateErr
				}
				mu.Unlock()
				return
			}
			row := contentTranslationRow{CacheKey: item.key, EntityKind: "entry", EntityID: item.entry.id, Field: "title", Language: item.language, SourceText: item.entry.title, TranslatedText: text, Origin: "model", Model: cfg.Model, TemplateVersion: translation.TemplateVersion}
			if saveErr := s.saveContentTranslations(ctx, []contentTranslationRow{row}); saveErr != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = saveErr
				}
				mu.Unlock()
				return
			}
			mu.Lock()
			written++
			mu.Unlock()
		}(item)
	}
	wg.Wait()
	return written, firstErr
}

// catalogEntryProtectedNamesForPrefill reuses the same protected-name rules as
// an interactive request. A failure keeps the title translatable: the names are
// protection, not a precondition for the row.
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
	if _, err := s.db.ExecContext(r.Context(), `UPDATE translation_prefill_state SET status=?, updated_at=CURRENT_TIMESTAMP WHERE id=1`, input.Status); err != nil {
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

// noteCatalogEntriesPublished asks the prefill pass to look at newly published
// cards again. The pass walks entry ids in order and stops at the end, so a
// later card with a higher id would be reached anyway; a card republished below
// the cursor needs the cursor moved back to it. The update is a single
// statement and never waits for a translation.
func (s *Server) noteCatalogEntriesPublished(ctx context.Context, entryIDs []int64) {
	if len(entryIDs) == 0 {
		return
	}
	lowest := entryIDs[0]
	for _, id := range entryIDs[1:] {
		if id < lowest {
			lowest = id
		}
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE translation_prefill_state
		SET cursor_entry_id=min(cursor_entry_id, ?-1),
			status=CASE WHEN status='complete' THEN 'running' ELSE status END,
			updated_at=CURRENT_TIMESTAMP
		WHERE id=1`, lowest)
}
