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
	translationPrefillBatchSize = 50
	translationPrefillInterval  = 15 * time.Second
	translationPrefillParallel  = 8
)

type translationPrefillLanguage struct {
	Language string `json:"language"`
	Rows     int64  `json:"rows"`
	Native   int64  `json:"native"`
	Model    int64  `json:"model"`
	Missing  int64  `json:"missing"`
	Stale    int64  `json:"stale"`
	Failed   int64  `json:"failed"`
	Retry    int64  `json:"retry"`
}

type translationPrefillView struct {
	Status         string                       `json:"status"`
	Cursor         int64                        `json:"cursor"`
	CursorLanguage string                       `json:"cursorLanguage,omitempty"`
	CursorKind     string                       `json:"cursorKind,omitempty"`
	CursorField    string                       `json:"cursorField,omitempty"`
	ConfigKey      string                       `json:"configKey"`
	Total          int64                        `json:"total"`
	Languages      []translationPrefillLanguage `json:"languages"`
	NightWindow    bool                         `json:"nightWindow"`
	LastError      string                       `json:"lastError,omitempty"`
	UpdatedAt      string                       `json:"updatedAt,omitempty"`
	RatePerMinute  float64                      `json:"ratePerMinute"`
	OverBudget     bool                         `json:"overBudget"`
	BudgetBytes    int64                        `json:"budgetBytes"`
}

// translationPrefillState reads only the durable cursor/status row. The worker
// uses this projection so a 15-second tick never scans the whole corpus.
func (s *Server) translationPrefillState(ctx context.Context) (translationPrefillView, error) {
	view := translationPrefillView{Status: "idle", Languages: []translationPrefillLanguage{}}
	var updated string
	err := s.db.QueryRowContext(ctx, `SELECT cursor_language,cursor_kind,cursor_field,cursor_id,
		config_key,status,last_error,updated_at FROM translation_prefill_state WHERE id=1`).
		Scan(&view.CursorLanguage, &view.CursorKind, &view.CursorField, &view.Cursor,
			&view.ConfigKey, &view.Status, &view.LastError, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		view.Status = "idle"
	} else if err != nil {
		return view, err
	}
	view.UpdatedAt = updated
	return view, nil
}

// translationPrefillStatus reads the durable cursor and effective coverage.
// It is the operator view; the background worker must use
// translationPrefillState instead.
func (s *Server) translationPrefillStatus(ctx context.Context) (translationPrefillView, error) {
	view, err := s.translationPrefillState(ctx)
	if err != nil {
		return view, err
	}
	view.NightWindow = translation.IsNightWindow(time.Now())
	view.RatePerMinute = s.translationPrefillRate()
	cfg, err := translation.NewStore(s.db).Load(ctx)
	if err != nil {
		return view, err
	}
	coverage, err := s.translationCorpusCoverageFor(ctx, cfg)
	if err != nil {
		return view, err
	}
	view.ConfigKey = translation.ConfigKey(cfg)
	view.Total = coverage.Total
	for _, language := range append(append([]string{}, translationModelLanguages...), translation.SourceHant) {
		item := coverage.Languages[language]
		view.Languages = append(view.Languages, translationPrefillLanguage{
			Language: language, Rows: item.Rows, Native: item.Native, Model: item.Model, Missing: item.Missing,
			Stale: item.Stale, Failed: item.Failed, Retry: item.Retry,
		})
	}
	// A stored complete status is not permission to claim complete coverage:
	// a model change, changed text, failure or retirement leaves gaps.
	if view.Status == "complete" && !coverage.complete() {
		view.Status = "running"
		if view.LastError == "" {
			view.LastError = coverageMessage(coverage)
		}
	}
	overBudget, budget, err := s.translationBudgetReport(ctx, cfg)
	if err != nil {
		return view, err
	}
	view.OverBudget = overBudget
	view.BudgetBytes = budget
	return view, nil
}

func coverageMessage(coverage translationCorpusCoverage) string {
	return fmt.Sprintf("coverage incomplete: %d missing, %d stale, %d failed, %d retry",
		coverage.Missing, coverage.Stale, coverage.Failed, coverage.Retry)
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
	retentionTick := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		_, _ = s.runTranslationPrefillBatch(ctx)
		// The corpus limits are a low-frequency maintenance concern. A pass
		// runs once a minute and may delete several bounded batches; this
		// keeps up with a prefill burst without taking the writer every tick.
		retentionTick++
		if retentionTick >= 4 {
			retentionTick = 0
			if cfg, err := translation.NewStore(s.db).Load(ctx); err == nil {
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
	configKey := translation.ConfigKey(cfg)
	state, err := s.translationPrefillState(ctx)
	if err != nil {
		return false, err
	}
	if state.ConfigKey != configKey {
		// A model, endpoint, template or reasoning change makes the completed
		// prefix meaningless. Restart the scan; coverage still skips every row
		// that is already current under the new key.
		if _, err = s.db.ExecContext(ctx, `UPDATE translation_prefill_state SET
			config_key=?,cursor_language='',cursor_kind='entry',cursor_field='title',cursor_id=0,
			status=CASE WHEN status='complete' THEN 'running' ELSE status END,
			updated_at=CURRENT_TIMESTAMP WHERE id=1`, configKey); err != nil {
			return false, err
		}
		state.ConfigKey = configKey
		state.CursorLanguage, state.CursorKind, state.CursorField, state.Cursor = "", "entry", "title", 0
	}
	if state.Status == "complete" {
		// A complete cursor is only a stored observation. Verify the effective
		// corpus so a model change, changed title or skipped entry resumes work
		// instead of leaving the pass permanently complete.
		coverage, err := s.translationCorpusCoverageFor(ctx, cfg)
		if err != nil {
			return false, err
		}
		if coverage.complete() {
			return false, nil
		}
		if _, err = s.db.ExecContext(ctx, `UPDATE translation_prefill_state
			SET status='running',last_error=?,updated_at=CURRENT_TIMESTAMP WHERE id=1`,
			coverageMessage(coverage)); err != nil {
			return false, err
		}
		state.Status = "running"
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
	// A due retry is taken before new items: a failure must not sit behind a
	// long queue of fresh cards, and its persisted attempt count is the only
	// retry budget.
	retries, err := s.dueTranslationPrefillFailures(ctx, cfg, translationPrefillBatchSize)
	if err != nil {
		return false, err
	}
	pageLimit := translationPrefillBatchSize - len(retries)
	items := []translationCorpusItem{}
	next := translationPrefillCursor{
		Language: state.CursorLanguage, Kind: state.CursorKind, Field: state.CursorField, ID: state.Cursor,
	}
	if pageLimit > 0 {
		items, next, err = s.translationCorpusPage(ctx, next, pageLimit)
		if err != nil {
			return false, err
		}
	}
	// Do not spend the batch on an item that already has a current row. This
	// is also how a model/config change resumes only the missing rows.
	retryKeys := make(map[string]struct{}, len(retries))
	for _, item := range retries {
		retryKeys[translationPrefillItemKey(item)] = struct{}{}
	}
	pending := make([]translationCorpusItem, 0, len(items))
	for _, item := range items {
		if _, retrying := retryKeys[translationPrefillItemKey(item)]; retrying {
			// The retry queue already owns this item for this batch.
			continue
		}
		current, currentErr := s.translationCorpusItemCurrent(ctx, item, configKey)
		if currentErr != nil {
			return false, currentErr
		}
		if !current {
			exhausted, exhaustedErr := s.translationPrefillItemExhausted(ctx, item, configKey)
			if exhaustedErr != nil {
				return false, exhaustedErr
			}
			if exhausted {
				// The item is permanently failed for this source hash. It still
				// keeps coverage incomplete, but retrying it every pass would
				// spend the whole batch on one bad card.
				continue
			}
			pending = append(pending, item)
		}
	}
	// A retry is one item. Expanding it to every model language here would
	// multiply the persisted attempt and lose the field/language granularity,
	// so each due failure is processed as its own item.
	work := append(append([]translationCorpusItem{}, retries...), pending...)
	if len(work) > translationPrefillBatchSize {
		work = work[:translationPrefillBatchSize]
	}
	written, failures, runErr := s.translationPrefillItems(ctx, cfg, work)
	message := ""
	if runErr != nil {
		message = errorText(runErr)
	} else if len(failures) > 0 {
		message = prefillFailureMessage(failures)
	}
	// A page shorter than the requested limit with no retry work means the scan
	// reached its last effective item. Completion and failure are decided from
	// the effective coverage, never from the cursor alone.
	endOfScan := len(items) < pageLimit && len(retries) == 0
	status := "running"
	var coverage translationCorpusCoverage
	// Only the end of the scan can settle the whole-corpus status. A full page
	// mid-scan stays running because later ids may still need rows.
	if endOfScan {
		coverage, err = s.translationCorpusCoverageFor(ctx, cfg)
		if err != nil {
			return false, err
		}
		switch {
		case coverage.complete():
			status, message = "complete", ""
		case len(pending) == 0 && coverage.Failed > 0 && coverage.Missing == 0 && coverage.Stale == 0:
			// This batch had no continuable new item and no due retry, while at
			// least one item exhausted its attempt budget. Report failed so the
			// operator sees the permanent gap instead of a worker that looks
			// busy. The status is an observation, not a terminal state: the next
			// due retry, newly published item or repaired source still runs
			// through this path.
			status, message = "failed", coverageMessage(coverage)
		}
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE translation_prefill_state SET
		cursor_language=?,cursor_kind=?,cursor_field=?,cursor_id=?,
		config_key=?,status=?,last_error=?,updated_at=CURRENT_TIMESTAMP WHERE id=1`,
		next.Language, next.Kind, next.Field, next.ID, configKey, status, message); err != nil {
		return false, err
	}
	s.noteTranslationPrefillWrite(written)
	if status == "complete" {
		return false, nil
	}
	if !endOfScan {
		return len(items) == pageLimit || len(retries) > 0, runErr
	}
	if coverage.Missing+coverage.Stale > 0 && len(items) == 0 {
		// The cursor is past the end but rows below it are missing or stale
		// without a title change trigger. Restart one scan so they are not
		// stranded behind the end position.
		if _, err = s.db.ExecContext(ctx, `UPDATE translation_prefill_state SET
			cursor_language='',cursor_kind='entry',cursor_field='title',cursor_id=0,
			updated_at=CURRENT_TIMESTAMP WHERE id=1`); err != nil {
			return false, err
		}
	}
	// Keep a concrete failure reason visible. Coverage is a summary, not a
	// replacement for the upstream error the operator needs to act on.
	if message == "" && status != "failed" {
		if _, err = s.db.ExecContext(ctx, `UPDATE translation_prefill_state
			SET last_error=?,updated_at=CURRENT_TIMESTAMP WHERE id=1`, coverageMessage(coverage)); err != nil {
			return false, err
		}
	}
	return false, nil
}

func translationPrefillItemKey(item translationCorpusItem) string {
	return fmt.Sprintf("%s/%d/%s/%s", item.EntityKind, item.EntityID, item.Field, item.Language)
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

// prefillFailureMessage summarizes persisted failures without depending on an
// in-process map. It is bounded by the state column's 300-byte budget.
func prefillFailureMessage(failures map[string]error) string {
	keys := make([]string, 0, len(failures))
	for key := range failures {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, min(len(keys), 4))
	for _, key := range keys[:min(len(keys), 4)] {
		parts = append(parts, fmt.Sprintf("%s: %v", key, failures[key]))
	}
	text := fmt.Sprintf("%d failed items; %s", len(keys), strings.Join(parts, "; "))
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

// translationPrefillItems writes native rows where the source already is the
// target language and queues the missing model rows for one batch. Each item
// is one entity kind, field and language, so entry titles, series titles and
// local work title/description fields are all covered by the same path.
func (s *Server) translationPrefillItems(ctx context.Context, cfg translation.Config, items []translationCorpusItem) (int, map[string]error, error) {
	configKey := translation.ConfigKey(cfg)
	type itemTask struct {
		item translationCorpusItem
		key  string
	}
	tasks := make([]itemTask, 0, len(items))
	written := 0
	failures := map[string]error{}
	var failureMu sync.Mutex
	recordFailure := func(item translationCorpusItem, err error) {
		if err == nil {
			return
		}
		failureMu.Lock()
		failures[translationPrefillItemKey(item)] = err
		failureMu.Unlock()
		// The persisted attempt count is the retry budget. A failed write of the
		// failure row is itself surfaced on the next pass by the missing row.
		_, _ = s.recordTranslationPrefillFailure(ctx, item, configKey, err)
	}
	type protectedNames struct {
		once  sync.Once
		names []string
		err   error
	}
	namesLoaded := map[string]*protectedNames{}
	for _, item := range items {
		detected := translation.DetectSourceLanguage(item.SourceText)
		if detected == translation.SourceHant {
			// A Traditional source is authored text. Store it once as native so a
			// zh-tw reader can match it, then let the model produce the other
			// languages (including Simplified).
			hantItem := item
			hantItem.Language = translation.SourceHant
			key := contentTranslationCacheKey(item.EntityKind, item.EntityID, "", item.Field, translation.SourceHant, item.SourceText, "native", "", "", "", false)
			row := contentTranslationRow{CacheKey: key, EntityKind: item.EntityKind, EntityID: item.EntityID,
				Field: item.Field, Language: translation.SourceHant, SourceText: item.SourceText,
				TranslatedText: item.SourceText, Origin: "native"}
			cacheStatus, err := s.saveContentTranslations(ctx, []contentTranslationRow{row})
			if err != nil {
				recordFailure(hantItem, err)
			} else if cacheStatus != contentTranslationStored {
				recordFailure(hantItem, errTranslationCachePaused)
			} else {
				written++
			}
		}
		nativeLanguage := ""
		switch {
		case detected == item.Language:
			nativeLanguage = detected
		}
		key := contentTranslationCacheKey(item.EntityKind, item.EntityID, "", item.Field, item.Language, item.SourceText, "model", cfg.Model, cfg.BaseURL, translation.TemplateVersion, cfg.DisableThinking)
		if nativeLanguage != "" {
			key = contentTranslationCacheKey(item.EntityKind, item.EntityID, "", item.Field, nativeLanguage, item.SourceText, "native", "", "", "", false)
			row := contentTranslationRow{CacheKey: key, EntityKind: item.EntityKind, EntityID: item.EntityID,
				Field: item.Field, Language: nativeLanguage, SourceText: item.SourceText,
				TranslatedText: item.SourceText, Origin: "native"}
			cacheStatus, err := s.saveContentTranslations(ctx, []contentTranslationRow{row})
			if err != nil {
				recordFailure(item, err)
				continue
			}
			if cacheStatus != contentTranslationStored {
				recordFailure(item, errTranslationCachePaused)
				continue
			}
			written++
			if err := s.clearTranslationPrefillFailures(ctx, item, configKey); err != nil {
				recordFailure(item, err)
			}
			continue
		}
		if item.Language == translation.SourceHant {
			// A native Traditional row is authored content, never a model lane.
			continue
		}
		tasks = append(tasks, itemTask{item: item, key: key})
	}
	if len(tasks) == 0 {
		return written, failures, nil
	}
	keys := make([]string, 0, len(tasks))
	for _, task := range tasks {
		keys = append(keys, task.key)
	}
	existing, err := s.lookupContentTranslations(ctx, keys)
	if err != nil {
		return written, failures, err
	}
	sem := make(chan struct{}, translationPrefillParallel)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, task := range tasks {
		if row, ok := existing[task.key]; ok && row.ConfigKey == configKey {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(task itemTask) {
			defer wg.Done()
			defer func() { <-sem }()
			namesKey := fmt.Sprintf("%s/%d", task.item.EntityKind, task.item.EntityID)
			mu.Lock()
			names, ok := namesLoaded[namesKey]
			if !ok {
				names = &protectedNames{}
				namesLoaded[namesKey] = names
			}
			mu.Unlock()
			names.once.Do(func() {
				names.names, names.err = s.translationPrefillProtectedNames(ctx, task.item.EntityKind, task.item.EntityID)
			})
			if names.err != nil {
				recordFailure(task.item, names.err)
				return
			}
			target := translationTarget{modelLanguage: task.item.Language}
			text, translateErr := s.translateInBackground(ctx, cfg, task.key, names.names, task.item.SourceText, target)
			if translateErr != nil {
				recordFailure(task.item, translateErr)
				return
			}
			row := contentTranslationRow{CacheKey: task.key, EntityKind: task.item.EntityKind,
				EntityID: task.item.EntityID, Field: task.item.Field, Language: task.item.Language,
				SourceText: task.item.SourceText, TranslatedText: text, Origin: "model",
				ConfigKey: configKey, Model: cfg.Model, TemplateVersion: translation.TemplateVersion}
			cacheStatus, saveErr := s.saveContentTranslations(ctx, []contentTranslationRow{row})
			if saveErr != nil {
				recordFailure(task.item, saveErr)
				return
			}
			if cacheStatus != contentTranslationStored {
				recordFailure(task.item, errTranslationCachePaused)
				return
			}
			if err := s.clearTranslationPrefillFailures(ctx, task.item, configKey); err != nil {
				recordFailure(task.item, err)
				return
			}
			mu.Lock()
			written++
			mu.Unlock()
		}(task)
	}
	wg.Wait()
	return written, failures, nil
}

// translationPrefillProtectedNames applies the same circle/cast protection as
// an interactive request. A failure is returned so the item waits for a retry
// instead of sending a published name to the model.
func (s *Server) translationPrefillProtectedNames(ctx context.Context, kind string, id int64) ([]string, error) {
	switch kind {
	case "entry":
		entry, _, err := s.loadSourceCatalogEntry(ctx, id)
		if err != nil {
			return nil, err
		}
		return s.catalogEntryProtectedNames(ctx, entry)
	case "series":
		return s.catalogSeriesProtectedNames(ctx, id)
	default:
		work, err := s.loadWorkDetail(ctx, 0, id, false)
		if err != nil {
			return nil, err
		}
		names := append([]string{work.Circle}, work.VoiceActors...)
		for _, credit := range work.VoiceCredits {
			names = append(names, credit.DisplayName)
		}
		return names, nil
	}
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
	cfg, err := translation.NewStore(s.db).Load(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	// The cursor is part of the configuration identity. Changing the model,
	// endpoint, template or reasoning switch starts a fresh queue; otherwise a
	// resumed pass would keep walking a cursor whose completed prefix is stale.
	if _, err := s.db.ExecContext(r.Context(), `UPDATE translation_prefill_state SET
		status=?,last_error='',config_key=?,
		cursor_language=CASE WHEN config_key<>? THEN '' ELSE cursor_language END,
		cursor_kind=CASE WHEN config_key<>? THEN 'entry' ELSE cursor_kind END,
		cursor_field=CASE WHEN config_key<>? THEN 'title' ELSE cursor_field END,
		cursor_id=CASE WHEN config_key<>? THEN 0 ELSE cursor_id END,
		updated_at=CURRENT_TIMESTAMP WHERE id=1`,
		input.Status, translation.ConfigKey(cfg), translation.ConfigKey(cfg), translation.ConfigKey(cfg),
		translation.ConfigKey(cfg), translation.ConfigKey(cfg)); err != nil {
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
		SET cursor_language='',cursor_kind='entry',cursor_field='title',
			cursor_id=CASE WHEN cursor_id=0 THEN 0 ELSE min(cursor_id, ?-1) END,
			status=CASE WHEN status='complete' THEN 'running' ELSE status END,
			updated_at=CURRENT_TIMESTAMP
		WHERE id=1`, lowest)
	return err
}
