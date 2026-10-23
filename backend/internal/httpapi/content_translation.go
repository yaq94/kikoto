package httpapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"slices"
	"strconv"

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
	// Version names the model and template that produced the text. A browser
	// cache that stored an older version must not keep showing it after an
	// administrator changes either.
	Version string `json:"version,omitempty"`
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
	resolved, err := s.resolveTranslationResource(r.Context(), in)
	if err != nil {
		writeWorkReadError(w, err)
		return
	}
	in = resolved
	if in.SeriesID <= 0 && in.CatalogEntryID <= 0 && in.SourceID <= 0 {
		if ok, demoErr := s.translationWorkReadable(r.Context(), in.WorkID); demoErr != nil {
			writeError(w, demoErr)
			return
		} else if !ok {
			writeAPIError(w, http.StatusNotFound, "not_found", "work not found", false)
			return
		}
	}
	result, status, err := s.translateContentField(r.Context(), in)
	if err != nil {
		var public *translation.Error
		if errors.As(err, &public) {
			result.Status = "failed"
			result.Code = public.Code
			// A stale selection is a client conflict, not a successful answer:
			// the caller has to reload instead of rendering outdated text.
			if public.Code == "translation_content_changed" {
				writeAPIError(w, http.StatusConflict, public.Code, public.Message, false)
				return
			}
			writeJSON(w, http.StatusOK, result)
			return
		}
		// A field the resource never publishes is invalid input. Retrying or
		// reloading cannot create it, so it must not be reported as an
		// unavailable source.
		if errors.Is(err, errFieldAbsent) {
			writeAPIError(w, http.StatusBadRequest, "translation_invalid_input", "This source has no such field to translate.", false)
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
	result.Version = s.contentTranslationVersion(r.Context())
	writeJSON(w, http.StatusOK, result)
}

// getContentTranslationVersion lets a reader check its browser cache before the
// first translation of a session. It exposes only the opaque version string.
func (s *Server) getContentTranslationVersion(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePermission(w, r, "library:read"); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"version": s.contentTranslationVersion(r.Context())})
}

// contentTranslationVersion identifies everything that changes the bytes a
// model returns: the endpoint, the model name, the reasoning switch and the
// prompt template. The browser only compares it for equality. The endpoint is
// hashed so a viewer never receives a URL or a credential hint in the version.
func (s *Server) contentTranslationVersion(ctx context.Context) string {
	cfg, err := translation.NewStore(s.db).Load(ctx)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256([]byte(cfg.BaseURL))
	return cfg.Model + "|" + strconv.FormatBool(cfg.DisableThinking) + "|" + hex.EncodeToString(sum[:8]) + "|" + translation.TemplateVersion
}

// translateContentField is the shared body of the single and batch endpoints.
// The status it returns is one of original, existing, native or translated.
// resolveTranslationResource turns a work code into the work id the rest of the
// path expects. The single and batch endpoints share it so both apply the same
// identity rules.
func (s *Server) resolveTranslationResource(ctx context.Context, in contentTranslationRequest) (contentTranslationRequest, error) {
	if in.SeriesID <= 0 && in.CatalogEntryID <= 0 && in.WorkID <= 0 && in.SourceID <= 0 && in.Code != "" {
		identity, err := s.resolveWorkCodeIdentity(ctx, in.Code)
		if err != nil {
			return in, err
		}
		in.WorkID = identity.WorkID
	}
	return in, nil
}

// translationWorkReadable applies the demo catalogue boundary to a resolved
// work id. Production accepts every readable work; demo only exposes its
// curated subset.
func (s *Server) translationWorkReadable(ctx context.Context, workID int64) (bool, error) {
	if !s.cfg.IsDemo() {
		return true, nil
	}
	eligible, err := s.demoWorkEligible(ctx, workID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	return eligible, nil
}

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
	if in.Language == "zh-tw" {
		// A reader who authored a Simplified title expects to see that title in
		// Traditional, not a fresh model translation of the original. The
		// selected text was already verified against the live field above, so
		// the Simplified variant only supplies the display text.
		authored := in
		authored.Language = "zh-cn"
		if simplified, loadErr := s.loadTranslationField(ctx, authored); loadErr == nil &&
			simplified.Existing != "" && simplified.Text == text && simplified.Original == original {
			converted, convErr := displayText(simplified.Existing, translation.SourceHans, target, field.Names)
			if convErr != nil {
				return result, "", convErr
			}
			result.Text = converted
			return result, "translated", nil
		}
	}
	kind, entityID, entityKey := translationEntity(in)
	if kind == "" {
		// A source-level field is not part of the searchable corpus. Translate
		// it on demand without persisting a row.
		translated, err := s.translateThroughModel(ctx, in.SourceID, field, text, target)
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
