package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
	"slices"

	"github.com/yexca/kikoto/backend/internal/translation"
)

// Requests identify a readable public field. Client text is only a stale-read
// guard and must exactly match that field before it can reach the model.
type contentTranslationRequest struct {
	CatalogEntryID int64  `json:"catalogEntryId"`
	WorkID         int64  `json:"workId"`
	SourceID       int64  `json:"sourceId"`
	Code           string `json:"code"`
	Field          string `json:"field"`
	Text           string `json:"text"`
	Language       string `json:"language"`
	MediaID        int64  `json:"mediaId"`
	Path           string `json:"path"`
}

type contentTranslationResponse struct {
	Text        string `json:"text"`
	Original    string `json:"original"`
	Language    string `json:"language"`
	Status      string `json:"status"`
	CacheStatus string `json:"cacheStatus,omitempty"`
	Code        string `json:"code,omitempty"`
}

func (s *Server) translateContent(w http.ResponseWriter, r *http.Request) {
	if user, ok := userFromContext(r.Context()); !ok || user.ID <= 0 {
		writeAPIError(w, 401, "authentication_required", "Sign in to translate content.", false)
		return
	}
	var in contentTranslationRequest
	if decodeTranslationBody(w, r, &in) != nil || !slices.Contains([]string{"title", "description", "tag", "directory"}, in.Field) {
		writeTranslationTrialError(w, &translation.Error{Code: "translation_invalid_input", Message: "Select a public content field."})
		return
	}
	targets := map[string]string{"en-us": "en", "zh-cn": "zh-Hans", "ja-jp": "ja", "ko-kr": "ko"}
	if in.Language != "origin" && in.Language != "zh-tw" && targets[in.Language] == "" {
		writeTranslationTrialError(w, &translation.Error{Code: "translation_invalid_input", Message: "Select a supported display language."})
		return
	}
	if _, ok := s.requirePermission(w, r, "library:read"); !ok {
		return
	}
	if in.CatalogEntryID <= 0 && in.WorkID <= 0 && in.SourceID <= 0 && in.Code != "" {
		identity, err := s.resolveWorkCodeIdentity(r.Context(), in.Code)
		if err != nil {
			writeWorkReadError(w, err)
			return
		}
		in.WorkID = identity.WorkID
	}
	if in.CatalogEntryID <= 0 && in.SourceID <= 0 && !s.requireDemoWork(w, r, in.WorkID) {
		return
	}
	field, err := s.loadTranslationField(r.Context(), in)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAPIError(w, 404, "not_found", "Content is unavailable.", false)
		} else {
			writeAPIError(w, 503, "translation_content_unavailable", "Content is unavailable. Try reloading.", true)
		}
		return
	}
	text := field.Text
	if text == "" || text != in.Text {
		writeAPIError(w, 409, "translation_content_changed", "Reload the content before translating.", false)
		return
	}
	if field.Original != "" {
		text = field.Original
	}
	result := contentTranslationResponse{Text: text, Original: text, Language: in.Language, Status: "original"}
	if in.Language == "origin" {
		writeJSON(w, 200, result)
		return
	}
	if field.Existing != "" {
		result.Text = field.Existing
		result.Status = "existing"
		writeJSON(w, 200, result)
		return
	}
	// Authored Traditional fields are usable before the conversion pipeline is
	// available. Never send Traditional requests as another target language.
	if targets[in.Language] == "" {
		result.Status = "failed"
		result.Code = "translation_invalid_input"
		writeJSON(w, 200, result)
		return
	}
	protected, err := translation.Protect(text, field.Names)
	if err != nil {
		result.Status = "failed"
		result.Code = translation.PublicError(err).Code
		writeJSON(w, 200, result)
		return
	}
	cfg, err := translation.NewStore(s.db).Load(r.Context())
	if err != nil {
		writeTranslationSettingsError(w, err)
		return
	}
	input := translation.Input{Text: protected.Text, TargetLanguage: targets[in.Language]}
	translated, err := s.cachedTranslation(r.Context(), cfg, input, text, protected)
	if err != nil {
		result.Status = "failed"
		result.Code = translation.PublicError(err).Code
	} else {
		restored, restoreErr := protected.Restore(translated.Text)
		if restoreErr != nil {
			result.Status = "failed"
			result.Code = translation.PublicError(restoreErr).Code
		} else {
			result.Text = restored
			result.Status = "translated"
			result.CacheStatus = translated.CacheStatus
		}
	}
	writeJSON(w, 200, result)
}
