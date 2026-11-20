package httpapi

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/yexca/kikoto/backend/internal/outbound"
	"github.com/yexca/kikoto/backend/internal/proxyconfig"
	"github.com/yexca/kikoto/backend/internal/translation"
)

func (s *Server) testTranslation(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePermission(w, r, "sources:write"); !ok {
		return
	}
	var input translation.Input
	if err := decodeTranslationBody(w, r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, "translation_invalid_input", "Enter a short text and a supported target language.", false)
		return
	}
	if err := input.Validate(); err != nil {
		writeTranslationTrialError(w, err)
		return
	}
	if !s.translationTrialMu.TryLock() {
		writeAPIError(w, http.StatusTooManyRequests, "translation_busy", "A translation trial is already running.", false)
		return
	}
	defer s.translationTrialMu.Unlock()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(s.lifetime.ctx, cancel)
	defer stop()
	cfg, err := translation.NewStore(s.db).Load(r.Context())
	if err != nil {
		writeTranslationSettingsError(w, err)
		return
	}
	if cfg.BaseURL == "" {
		writeTranslationTrialError(w, &translation.Error{Code: "translation_not_configured", Message: "Configure a translation service first."})
		return
	}
	client, err := s.translationModelClient(r.Context(), cfg)
	if err != nil {
		writeTranslationTrialError(w, err)
		return
	}
	result, err := s.contentTranslationQueue(cfg).Do(ctx, "trial", time.Duration(cfg.TimeoutSeconds)*time.Second, true, func(ctx context.Context) (translation.Result, error) {
		return translation.Translate(ctx, client, cfg, input)
	})
	if err != nil {
		writeTranslationTrialError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func writeTranslationTrialError(w http.ResponseWriter, err error) {
	public := translation.PublicError(err)
	status := http.StatusUnprocessableEntity
	switch public.Code {
	case "translation_invalid_input", "translation_empty_text", "translation_text_too_long", "translation_unsupported_language":
		status = http.StatusBadRequest
	case "translation_busy", "translation_queue_full":
		status = http.StatusTooManyRequests
	}
	writeAPIError(w, status, public.Code, public.Message, false)
}

type translationClientKey struct {
	baseURL, proxyRoute string
	timeout             time.Duration
}

// The model has one configured endpoint and one resolved proxy route. Pool its
// connections across all model calls and release idle sockets on a route swap.
func (s *Server) translationModelClient(ctx context.Context, cfg translation.Config) (*http.Client, error) {
	route, err := s.resolveProxyRoute(ctx, proxyconfig.ScopeOther, 0)
	if err != nil {
		return nil, err
	}
	key := translationClientKey{cfg.BaseURL, route.key(), time.Duration(cfg.TimeoutSeconds) * time.Second}
	s.translationClientMu.Lock()
	defer s.translationClientMu.Unlock()
	if cause := context.Cause(s.lifetime.ctx); cause != nil {
		return nil, cause
	}
	if s.translationClient != nil && s.translationClientKey == key {
		return s.translationClient, nil
	}

	// Every transport uses the exact configured origin, including an intentional
	// LAN origin. DNS validation and pinning still apply to each new connection.
	policyFor := func(proxy *url.URL) (*outbound.Policy, error) {
		options := s.translationNetwork
		options.Proxy = proxy
		return outbound.NewPolicy([]outbound.Destination{{URL: cfg.BaseURL, AllowPrivate: true}}, options)
	}
	policy, err := policyFor(nil)
	if err != nil {
		return nil, err
	}
	transport, err := proxiedTransport(route, policyFor)
	if err != nil {
		return nil, err
	}
	client := policy.Client(transport, key.timeout)
	// Never replay a credentialed model POST after a redirect.
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	if s.translationClient != nil {
		s.translationClient.CloseIdleConnections()
	}
	s.translationClientKey, s.translationClient = key, client
	return client, nil
}

func (s *Server) closeTranslationModelClient() {
	s.translationClientMu.Lock()
	defer s.translationClientMu.Unlock()
	if s.translationClient != nil {
		s.translationClient.CloseIdleConnections()
		s.translationClient = nil
	}
}
