package httpapi

import (
	"context"
	"time"

	"github.com/yexca/kikoto/backend/internal/translation"
)

// translatedContent is the displayable result plus how it was obtained. The
// cache status stays separate from the user-facing status so the UI can show
// "translated" while diagnostics record a hit.
type translatedContent struct {
	TranslatedText string
	CacheStatus    string
}

// translateStoredContent resolves one field from the persistent corpus. A
// native row is written without a model call; a model row is written only after
// a successful call and a successful placeholder restore.
func (s *Server) translateStoredContent(ctx context.Context, in contentTranslationRequest, field readableTranslationField, text string, target translationTarget, kind string, entityID int64, entityKey string) (translatedContent, string, error) {
	cfg, err := translation.NewStore(s.db).Load(ctx)
	if err != nil {
		return translatedContent{}, "", err
	}
	if nativeLanguage, ok := nativeContentLanguage(text, target); ok {
		key := contentTranslationCacheKey(kind, entityID, entityKey, in.Field, nativeLanguage, text, "native", "", "", "", false)
		row := contentTranslationRow{
			CacheKey: key, EntityKind: kind, EntityID: entityID, EntityKey: entityKey,
			Field: in.Field, Language: nativeLanguage, SourceText: text,
			TranslatedText: text, Origin: "native",
		}
		if err = s.saveContentTranslations(ctx, []contentTranslationRow{row}); err != nil {
			return translatedContent{}, "", err
		}
		display, err := displayText(text, nativeLanguage, target)
		if err != nil {
			return translatedContent{}, "", err
		}
		return translatedContent{TranslatedText: display, CacheStatus: "stored"}, "native", nil
	}

	key := contentTranslationCacheKey(kind, entityID, entityKey, in.Field, target.modelLanguage, text, "model", cfg.Model, cfg.BaseURL, translation.TemplateVersion, cfg.DisableThinking)
	if rows, lookupErr := s.lookupContentTranslations(ctx, []string{key}); lookupErr == nil {
		if row, ok := rows[key]; ok && row.Model == cfg.Model && row.TemplateVersion == translation.TemplateVersion {
			display, convertErr := displayText(row.TranslatedText, row.Language, target)
			if convertErr != nil {
				return translatedContent{}, "", convertErr
			}
			return translatedContent{TranslatedText: display, CacheStatus: "hit"}, "translated", nil
		}
	}

	translated, err := s.translateWithQueue(ctx, cfg, key, field.Names, text, target, in.Language)
	if err != nil {
		return translatedContent{}, "", err
	}
	row := contentTranslationRow{
		CacheKey: key, EntityKind: kind, EntityID: entityID, EntityKey: entityKey,
		Field: in.Field, Language: target.modelLanguage, SourceText: text,
		TranslatedText: translated, Origin: "model", Model: cfg.Model,
		TemplateVersion: translation.TemplateVersion,
	}
	if err = s.saveContentTranslations(ctx, []contentTranslationRow{row}); err != nil {
		return translatedContent{}, "", err
	}
	return translatedContent{TranslatedText: translated, CacheStatus: "stored"}, "translated", nil
}

// nativeContentLanguage reports whether the stored text already is the target
// language, so no model call is needed. Traditional Chinese accepts both Han
// variants; anything else is translated.
func nativeContentLanguage(text string, target translationTarget) (string, bool) {
	detected := translation.DetectSourceLanguage(text)
	switch {
	case detected == translation.SourceUnknown:
		return "", false
	case detected == target.modelLanguage:
		return detected, true
	case target.traditional && detected == translation.SourceHant:
		return detected, true
	}
	return "", false
}

// displayText converts a Simplified row for a Traditional viewer. A row that
// already holds Traditional text is returned unchanged.
func displayText(text, language string, target translationTarget) (string, error) {
	if !target.traditional || language == translation.SourceHant {
		return text, nil
	}
	return translation.Traditional(text)
}

// translateWithQueue protects names, calls the model through the interactive
// lane, then restores the protected names. The queue deduplicates readers of
// the same key, so two panels asking for one title share a single call.
func (s *Server) translateWithQueue(ctx context.Context, cfg translation.Config, key string, names []string, text string, target translationTarget, displayLanguage string) (string, error) {
	protected, err := translation.Protect(text, names)
	if err != nil {
		return "", err
	}
	result, err := s.contentTranslationQueue(cfg).Do(ctx, key, time.Duration(cfg.TimeoutSeconds)*time.Second, false, func(callCtx context.Context) (translation.Result, error) {
		if cfg.BaseURL == "" {
			return translation.Result{}, &translation.Error{Code: "translation_not_configured", Message: "Configure a translation service first."}
		}
		client, err := s.newTranslationClient(callCtx, cfg)
		if err != nil {
			return translation.Result{}, err
		}
		defer client.CloseIdleConnections()
		input := translation.Input{Text: protected.Text, TargetLanguage: target.modelLanguage}
		return translation.Translate(callCtx, client, cfg, input)
	})
	if err != nil {
		return "", err
	}
	restored, err := protected.Restore(result.Text)
	if err != nil {
		return "", err
	}
	if target.traditional {
		return translation.Traditional(restored)
	}
	return restored, nil
}

// translateThroughModel is the uncached path for a source-level field that has
// no durable object identity.
func (s *Server) translateThroughModel(ctx context.Context, sourceID int64, field readableTranslationField, text string, target translationTarget, displayLanguage string) (string, error) {
	cfg, err := translation.NewStore(s.db).Load(ctx)
	if err != nil {
		return "", err
	}
	key := contentTranslationCacheKey("source", sourceID, "", "tag", target.modelLanguage, text, "model", cfg.Model, cfg.BaseURL, translation.TemplateVersion, cfg.DisableThinking)
	return s.translateWithQueue(ctx, cfg, key, field.Names, text, target, displayLanguage)
}
