package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/yexca/kikoto/backend/internal/translation"
)

// contentTranslationQueue returns the application-wide scheduler. Its limits
// follow the stored configuration but the queue object survives reloads so
// in-flight work is never dropped by a settings save.
func (s *Server) contentTranslationQueue(cfg translation.Config) *translation.Queue {
	s.translationQueueOnce.Do(func() { s.translationQueue = translation.NewQueue(s.lifetime.ctx, cfg.Limits, s.goTracked) })
	return s.translationQueue
}

// cachedTranslation performs the settings-test call. The trial writes no row:
// it exists to prove the endpoint, credential and model name work, and its text
// is not a public field of any object.
func (s *Server) cachedTranslation(ctx context.Context, cfg translation.Config, input translation.Input, original string, protected translation.ProtectedText) (translation.Result, error) {
	key, err := translation.CacheKey(cfg, input, original, translation.TemplateVersion)
	if err != nil {
		return translation.Result{}, err
	}
	return s.contentTranslationQueue(cfg).Do(ctx, key, time.Duration(cfg.TimeoutSeconds)*time.Second, false, func(callCtx context.Context) (translation.Result, error) {
		if cfg.BaseURL == "" {
			return translation.Result{}, &translation.Error{Code: "translation_not_configured", Message: "Configure a translation service first."}
		}
		client, err := s.newTranslationClient(callCtx, cfg)
		if err != nil {
			return translation.Result{}, err
		}
		defer client.CloseIdleConnections()
		result, err := translation.Translate(callCtx, client, cfg, input)
		if err != nil {
			return result, err
		}
		if _, err := protected.Restore(result.Text); err != nil {
			return translation.Result{}, err
		}
		result.CacheStatus = "uncached"
		return result, nil
	})
}

// translationLanguageCount is one row of the runtime summary.
type translationLanguageCount struct {
	Language string `json:"language"`
	Rows     int64  `json:"rows"`
	Native   int64  `json:"native"`
	Model    int64  `json:"model"`
}

// getTranslationRuntime reports the persistent corpus and scheduler state.
// cacheBytes is the configured model-text budget; the maintenance pass enforces
// it in bounded batches.
func (s *Server) getTranslationRuntime(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePermission(w, r, "sources:write"); !ok {
		return
	}
	cfg, err := translation.NewStore(s.db).Load(r.Context())
	if err != nil {
		writeTranslationSettingsError(w, err)
		return
	}
	counts, err := s.translationLanguageCounts(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	prefill, err := s.translationPrefillStatus(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, struct {
		Languages []translationLanguageCount `json:"languages"`
		SoftBytes int64                      `json:"softBytes"`
		Prefill   translationPrefillView     `json:"prefill"`
		Queue     translation.QueueStats     `json:"queue"`
	}{counts, cfg.CacheBytes, prefill, s.contentTranslationQueue(cfg).Stats()})
}

func (s *Server) translationLanguageCounts(ctx context.Context) ([]translationLanguageCount, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT language, count(*),
		sum(CASE WHEN origin='native' THEN 1 ELSE 0 END),
		sum(CASE WHEN origin='model' THEN 1 ELSE 0 END)
		FROM content_translation GROUP BY language ORDER BY language`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := []translationLanguageCount{}
	for rows.Next() {
		var item translationLanguageCount
		if err = rows.Scan(&item.Language, &item.Rows, &item.Native, &item.Model); err != nil {
			return nil, err
		}
		counts = append(counts, item)
	}
	return counts, rows.Err()
}
