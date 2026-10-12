package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/yexca/kikoto/backend/internal/llm"
	"github.com/yexca/kikoto/backend/internal/outbound"
	"github.com/yexca/kikoto/backend/internal/proxyconfig"
)

type llmProviderResponse struct {
	ID           string      `json:"id"`
	PresetID     string      `json:"presetId"`
	Name         string      `json:"name"`
	Kind         string      `json:"kind"`
	BaseURL      string      `json:"baseUrl"`
	Boundary     string      `json:"boundary"`
	OllamaScope  string      `json:"ollamaScope,omitempty"`
	Enabled      bool        `json:"enabled"`
	HasAPIKey    bool        `json:"hasApiKey"`
	DefaultModel string      `json:"defaultModel"`
	Models       []llm.Model `json:"models"`
}

func (s *Server) llmResponse(p llm.Provider) llmProviderResponse {
	return llmProviderResponse{p.ID, p.PresetID, p.Name, p.Preset().Kind, p.Endpoint(s.cfg.HostProxyAddress()), p.Boundary, p.OllamaScope, p.Enabled, p.APIKey != "", p.DefaultModel, p.Models}
}

func (s *Server) listLLMProviders(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePermission(w, r, "sources:write"); !ok {
		return
	}
	providers, err := s.llmStore.List(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	result := []llmProviderResponse{}
	for _, p := range providers {
		result = append(result, s.llmResponse(p))
	}
	policy, err := s.llmStore.Policy(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": result, "presets": llm.Presets, "policy": policy})
}

func (s *Server) getLLMPolicy(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePermission(w, r, "sources:write"); !ok {
		return
	}
	policy, err := s.llmStore.Policy(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, policy)
}

func (s *Server) updateLLMPolicy(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePermission(w, r, "sources:write"); !ok {
		return
	}
	var policy llm.Policy
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&policy); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_llm_policy", "Invalid LLM safety policy.", false)
		return
	}
	policy = policy.Normalized()
	if err := s.llmStore.SavePolicy(r.Context(), policy); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, policy)
}

type llmProviderPayload struct {
	PresetID     string  `json:"presetId"`
	Name         string  `json:"name"`
	BaseURL      string  `json:"baseUrl"`
	APIKey       *string `json:"apiKey"`
	Boundary     string  `json:"boundary"`
	OllamaScope  string  `json:"ollamaScope"`
	Enabled      *bool   `json:"enabled"`
	DefaultModel string  `json:"defaultModel"`
}

func (s *Server) saveLLMProvider(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePermission(w, r, "sources:write"); !ok {
		return
	}
	var body llmProviderPayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil {
		writeAPIError(w, 400, "invalid_llm_config", "Invalid LLM configuration.", false)
		return
	}
	p := llm.Provider{PresetID: body.PresetID, Name: body.Name, BaseURL: body.BaseURL, Boundary: body.Boundary, OllamaScope: body.OllamaScope, DefaultModel: body.DefaultModel}
	if id := r.PathValue("id"); id != "" {
		current, loadErr := s.llmProvider(r.Context(), id)
		if errors.Is(loadErr, llm.ErrNotFound) {
			writeAPIError(w, 404, "llm_not_found", "LLM provider not found.", false)
			return
		}
		if loadErr != nil {
			writeError(w, loadErr)
			return
		}
		p = current
		if body.PresetID != "" {
			p.PresetID = body.PresetID
		}
		if body.Name != "" {
			p.Name = body.Name
		}
		if body.BaseURL != "" {
			p.BaseURL = body.BaseURL
		}
		if body.Boundary != "" {
			p.Boundary = body.Boundary
		}
		if body.OllamaScope != "" {
			p.OllamaScope = body.OllamaScope
		}
		if body.Enabled != nil {
			p.Enabled = *body.Enabled
		}
		if body.DefaultModel != "" {
			p.DefaultModel = body.DefaultModel
		}
	}
	if body.Enabled != nil && r.PathValue("id") == "" {
		p.Enabled = *body.Enabled
	}
	saved, err := s.llmStore.Mutate(r.Context(), r.PathValue("id"), p, body.APIKey, false)
	if errors.Is(err, llm.ErrNotFound) {
		writeAPIError(w, 404, "llm_not_found", "LLM provider not found.", false)
		return
	}
	if err != nil {
		writeAPIError(w, 400, "invalid_llm_config", "Check the provider, endpoint, location and model settings.", false)
		return
	}
	writeJSON(w, http.StatusOK, s.llmResponse(saved))
}

func (s *Server) deleteLLMProvider(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePermission(w, r, "sources:write"); !ok {
		return
	}
	_, err := s.llmStore.Mutate(r.Context(), r.PathValue("id"), llm.Provider{}, nil, true)
	if err != nil {
		writeAPIError(w, 404, "llm_not_found", "LLM provider not found.", false)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) llmProvider(ctx context.Context, id string) (llm.Provider, error) {
	providers, err := s.llmStore.List(ctx)
	if err != nil {
		return llm.Provider{}, err
	}
	for _, p := range providers {
		if p.ID == id {
			return p, nil
		}
	}
	return llm.Provider{}, llm.ErrNotFound
}

func (s *Server) fetchLLMModels(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePermission(w, r, "sources:write"); !ok {
		return
	}
	p, err := s.llmProvider(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIError(w, 404, "llm_not_found", "LLM provider not found.", false)
		return
	}
	if !s.acquireLLMSlot(w, r) {
		return
	}
	defer func() { <-s.llmRequestSlots }()
	client, closeClient, err := s.llmClient(r.Context(), p, 20*time.Second)
	if err != nil {
		writeAPIError(w, 502, "llm_models_failed", "Models could not be fetched.", false)
		return
	}
	defer closeClient()
	models, err := client.Models(r.Context())
	if err != nil {
		writeAPIError(w, 502, "llm_models_failed", "Models could not be fetched. Check the endpoint and key.", false)
		return
	}
	p, err = s.llmStore.Models(r.Context(), p, models)
	if err != nil {
		writeAPIError(w, 409, "llm_config_changed", "Provider settings changed. Fetch models again.", false)
		return
	}
	writeJSON(w, 200, s.llmResponse(p))
}

func (s *Server) acquireLLMSlot(w http.ResponseWriter, r *http.Request) bool {
	select {
	case s.llmRequestSlots <- struct{}{}:
		return true
	default:
		writeAPIError(w, 429, "llm_busy", "Translation service is busy. Try again shortly.", false)
		return false
	}
}

// Only the administrator-configured origin is allowed, including its private
// address exception. Discovery and translation refuse every redirect.
func (s *Server) llmClient(ctx context.Context, p llm.Provider, timeout time.Duration) (llm.Client, func(), error) {
	base := p.Endpoint(s.cfg.HostProxyAddress())
	route, err := s.resolveProxyRoute(ctx, proxyconfig.ScopeOther, 0)
	if err != nil {
		return llm.Client{}, nil, err
	}
	transport, err := proxiedTransport(route, func(proxy *url.URL) (*outbound.Policy, error) {
		return outbound.NewPolicy([]outbound.Destination{{URL: base, AllowPrivate: true}}, outbound.Options{Proxy: proxy})
	})
	if err != nil {
		return llm.Client{}, nil, err
	}
	client := &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return llm.Client{HTTP: client, Provider: p, Host: s.cfg.HostProxyAddress()}, func() { closeIdleConnections(transport) }, nil
}
