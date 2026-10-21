package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"unicode/utf8"

	"github.com/yexca/kikoto/backend/internal/translation"
)

func (s *Server) getTranslationSettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePermission(w, r, "sources:write"); !ok {
		return
	}
	cfg, err := translation.NewStore(s.db).Load(r.Context())
	if err != nil {
		writeTranslationSettingsError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cfg.Settings())
}

func (s *Server) updateTranslationSettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePermission(w, r, "sources:write"); !ok {
		return
	}
	s.translationConfigMu.Lock()
	defer s.translationConfigMu.Unlock()
	var patch translation.Patch
	if err := decodeTranslationBody(w, r, &patch); err != nil {
		writeTranslationSettingsError(w, translation.ErrInvalidConfig)
		return
	}
	settings, err := translation.NewStore(s.db).Update(r.Context(), patch)
	if err != nil {
		writeTranslationSettingsError(w, err)
		return
	}
	cfg, err := translation.NewStore(s.db).Load(r.Context())
	if err != nil {
		writeTranslationSettingsError(w, err)
		return
	}
	s.contentTranslationQueue(cfg).Configure(cfg.Limits)
	writeJSON(w, http.StatusOK, settings)
}

// translationBodyLimit bounds the single-field translation endpoints. A batch
// page is much larger and uses decodeTranslationBodyLimit with its own budget.
const translationBodyLimit = 16 << 10

func decodeTranslationBody(w http.ResponseWriter, r *http.Request, target any) error {
	return decodeTranslationBodyLimit(w, r, target, translationBodyLimit)
}

// decodeTranslationBodyLimit decodes one JSON object with strict field names.
// Callers that accept a page of items pass their own size bound.
func decodeTranslationBodyLimit(w http.ResponseWriter, r *http.Request, target any, limit int64) error {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil || !utf8.Valid(data) || !bytes.HasPrefix(bytes.TrimSpace(data), []byte("{")) {
		return translation.ErrInvalidConfig
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return translation.ErrInvalidConfig
	}
	return nil
}

func writeTranslationSettingsError(w http.ResponseWriter, err error) {
	if errors.Is(err, translation.ErrInvalidConfig) {
		writeAPIError(w, http.StatusBadRequest, "translation_invalid_config", "Invalid translation configuration.", false)
		return
	}
	writeAPIError(w, http.StatusInternalServerError, "translation_unavailable", "Translation configuration is unavailable.", false)
}
