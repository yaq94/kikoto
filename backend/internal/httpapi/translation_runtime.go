package httpapi

import (
	"context"
	"net/http"
	"path/filepath"
	"time"

	"github.com/yexca/kikoto/backend/internal/translation"
)

func (s *Server) contentTranslationCache() *translation.Cache {
	s.translationCacheOnce.Do(func() {
		root := ""
		if s.cfg.CacheRoot != "" {
			root = filepath.Join(s.cfg.CacheRoot, "translations")
		}
		s.translationCache = translation.NewCache(root, func(path string) (uint64, error) { space, err := queryDiskSpace(path); return space.Available, err })
	})
	return s.translationCache
}

func (s *Server) contentTranslationQueue(cfg translation.Config) *translation.Queue {
	s.translationQueueOnce.Do(func() { s.translationQueue = translation.NewQueue(s.lifetime.ctx, cfg.Limits, s.goTracked) })
	return s.translationQueue
}

func (s *Server) cachedTranslation(ctx context.Context, cfg translation.Config, input translation.Input, original string, protected translation.ProtectedText) (translation.Result, error) {
	cache := s.contentTranslationCache()
	key, err := translation.CacheKey(cfg, input, original, translation.TemplateVersion)
	if err != nil {
		return translation.Result{}, err
	}
	if result, ok := cache.Get(key, cfg); ok {
		result.CacheStatus = "hit"
		return result, nil
	}
	return s.contentTranslationQueue(cfg).Do(ctx, key, time.Duration(cfg.TimeoutSeconds)*time.Second, false, func(ctx context.Context) (translation.Result, error) {
		if result, ok := cache.Get(key, cfg); ok {
			result.CacheStatus = "hit"
			return result, nil
		}
		if cfg.BaseURL == "" {
			return translation.Result{}, &translation.Error{Code: "translation_not_configured", Message: "Configure a translation service first."}
		}
		client, err := s.newTranslationClient(ctx, cfg)
		if err != nil {
			return translation.Result{}, err
		}
		defer client.CloseIdleConnections()
		result, err := translation.Translate(ctx, client, cfg, input)
		if err != nil {
			return result, err
		}
		if _, err := protected.Restore(result.Text); err != nil {
			return translation.Result{}, err
		}
		if ctx.Err() != nil {
			return translation.Result{}, ctx.Err()
		}
		// An administrator can lower quotas while this request is in flight.
		// Publication and settings changes serialize against the current limits.
		s.translationConfigMu.Lock()
		current, loadErr := translation.NewStore(s.db).Load(ctx)
		if loadErr == nil {
			cfg.Limits = current.Limits
			result.CacheStatus = cache.Put(key, cfg, result)
		} else {
			result.CacheStatus = "unavailable"
		}
		s.translationConfigMu.Unlock()
		return result, nil
	})
}

func (s *Server) getTranslationRuntime(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePermission(w, r, "sources:write"); !ok {
		return
	}
	cfg, err := translation.NewStore(s.db).Load(r.Context())
	if err != nil {
		writeTranslationSettingsError(w, err)
		return
	}
	writeJSON(w, 200, struct {
		translation.CacheStats
		translation.QueueStats
	}{s.contentTranslationCache().Stats(cfg), s.contentTranslationQueue(cfg).Stats()})
}
