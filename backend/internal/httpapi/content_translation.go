package httpapi

import (
	"context"
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

// translationTarget maps a display language to the language the model writes
// and, for Traditional Chinese, to the conversion applied after the model.
type translationTarget struct {
	modelLanguage string
	traditional   bool
}

var translationTargets = map[string]translationTarget{
	"en-us": {modelLanguage: translation.SourceEnglish},
	"zh-cn": {modelLanguage: translation.SourceHans},
	"zh-tw": {modelLanguage: translation.SourceHans, traditional: true},
	"ja-jp": {modelLanguage: translation.SourceJapanese},
	"ko-kr": {modelLanguage: translation.SourceKorean},
}

// translationEntity names the evaluation object a field belongs to. The key is
// empty for titles and carries the sub-path for directories, so two folders of
// one card do not share a row. A request that only names a source has no
// object in the persistent store and is translated without one.
func translationEntity(in contentTranslationRequest) (kind string, id int64, key string) {
	switch {
	case in.SeriesID > 0:
		return "series", in.SeriesID, ""
	case in.CatalogEntryID > 0:
		if in.Field == "directory" {
			return "entry", in.CatalogEntryID, in.Path
		}
		return "entry", in.CatalogEntryID, ""
	case in.WorkID > 0:
		return "work", in.WorkID, ""
	default:
		return "", 0, ""
	}
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
	if in.Language != "origin" && translationTargets[in.Language].modelLanguage == "" {
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
	result, status, err := s.translateContentField(r.Context(), in)
	if err != nil {
		var public *translation.Error
		if errors.As(err, &public) {
			result.Status = "failed"
			result.Code = public.Code
			writeJSON(w, http.StatusOK, result)
			return
		}
		if errors.Is(err, sql.ErrNoRows) {
			writeAPIError(w, 404, "not_found", "Content is unavailable.", false)
			return
		}
		writeAPIError(w, 503, "translation_content_unavailable", "Content is unavailable. Try reloading.", true)
		return
	}
	result.Status = status
	writeJSON(w, http.StatusOK, result)
}

// translateContentField is the shared body of the single and batch endpoints.
// The status it returns is one of original, existing, native or translated.
func (s *Server) translateContentField(ctx context.Context, in contentTranslationRequest) (contentTranslationResponse, string, error) {
	field, err := s.loadTranslationField(ctx, in)
	if err != nil {
		return contentTranslationResponse{}, "", err
	}
	text := field.Text
	if text == "" || text != in.Text {
		return contentTranslationResponse{}, "", &translation.Error{Code: "translation_content_changed", Message: "Reload the content before translating."}
	}
	original := field.Original
	if original == "" {
		original = text
	}
	result := contentTranslationResponse{Text: text, Original: original, Language: in.Language}
	if in.Language == "origin" {
		result.Text = original
		return result, "original", nil
	}
	if field.Existing != "" {
		result.Text = field.Existing
		return result, "existing", nil
	}
	target := translationTargets[in.Language]
	kind, entityID, entityKey := translationEntity(in)
	if kind == "" {
		// A source-level field is not part of the searchable corpus. Translate
		// it on demand without persisting a row.
		translated, err := s.translateThroughModel(ctx, in.SourceID, field, text, target, in.Language)
		if err != nil {
			return result, "", err
		}
		result.Text = translated
		result.CacheStatus = "uncached"
		return result, "translated", nil
	}
	row, status, err := s.translateStoredContent(ctx, in, field, text, target, kind, entityID, entityKey)
	if err != nil {
		return result, "", err
	}
	result.Text = row.TranslatedText
	result.CacheStatus = row.CacheStatus
	return result, status, nil
}
