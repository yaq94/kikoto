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
	SeriesID       int64  `json:"seriesId"`
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
	targets := map[string]string{"en-us": "en", "zh-cn": "zh-Hans", "zh-tw": "zh-Hans", "ja-jp": "ja", "ko-kr": "ko"}
	if in.Language != "origin" && in.Language != "zh-tw" && targets[in.Language] == "" {
		writeTranslationTrialError(w, &translation.Error{Code: "translation_invalid_input", Message: "Select a supported display language."})
		return
	}
	if _, ok := s.requirePermission(w, r, "library:read"); !ok {
		return
	}
	if in.SeriesID <= 0 && in.CatalogEntryID <= 0 && in.WorkID <= 0 && in.SourceID <= 0 && in.Code != "" {
		identity, err := s.resolveWorkCodeIdentity(r.Context(), in.Code)
		if err != nil {
			writeWorkReadError(w, err)
			return
		}
		in.WorkID = identity.WorkID
	}
	if in.SeriesID <= 0 && in.CatalogEntryID <= 0 && in.SourceID <= 0 && !s.requireDemoWork(w, r, in.WorkID) {
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
	// Original is presentation only: the canonical text behind "show original".
	// The selected text is the only source that may reach the model or the cache
	// key, so an edition or manual variant is never replaced by the canonical
	// text and is never served another variant's cached result.
	original := field.Original
	if original == "" {
		original = text
	}
	result := contentTranslationResponse{Text: text, Original: original, Language: in.Language, Status: "original"}
	if in.Language == "origin" {
		result.Text = original
		writeJSON(w, 200, result)
		return
	}
	if field.Existing != "" {
		result.Text = field.Existing
		result.Status = "existing"
		writeJSON(w, 200, result)
		return
	}
	source := text
	authoredSimplified := false
	if in.Language == "zh-tw" {
		// Reuse the same field-selection rules as a Simplified request, including
		// edition, remote title and system-tag choices. Recheck the resource.
		simplifiedRequest := in
		simplifiedRequest.Language = "zh-cn"
		simplified, loadErr := s.loadTranslationField(r.Context(), simplifiedRequest)
		if loadErr != nil || simplified.Text != in.Text || simplified.Original != field.Original {
			writeAPIError(w, 409, "translation_content_changed", "Reload the content before translating.", false)
			return
		}
		if simplified.Existing != "" {
			source = simplified.Existing
			authoredSimplified = true
		}
	}
	protected, err := translation.Protect(source, field.Names)
	if err != nil {
		result.Status = "failed"
		result.Code = translation.PublicError(err).Code
		writeJSON(w, 200, result)
		return
	}
	translated := translation.Result{Text: protected.Text}
	if !authoredSimplified {
		cfg, loadErr := translation.NewStore(s.db).Load(r.Context())
		if loadErr != nil {
			writeTranslationSettingsError(w, loadErr)
			return
		}
		input := translation.Input{Text: protected.Text, TargetLanguage: targets[in.Language]}
		translated, err = s.cachedTranslation(r.Context(), cfg, input, text, protected)
	}
	if err == nil && in.Language == "zh-tw" {
		translated.Text, err = translation.Traditional(translated.Text)
	}
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
