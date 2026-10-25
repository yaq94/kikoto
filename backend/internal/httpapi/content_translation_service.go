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
		cacheStatus, err := s.saveContentTranslations(ctx, []contentTranslationRow{row})
		if err != nil {
			return translatedContent{}, "", err
		}
		display, err := displayText(text, nativeLanguage, target, field.Names)
		if err != nil {
			return translatedContent{}, "", err
		}
		return translatedContent{TranslatedText: display, CacheStatus: cacheStatus}, "native", nil
	}

	key := contentTranslationCacheKey(kind, entityID, entityKey, in.Field, target.modelLanguage, text, "model", cfg.Model, cfg.BaseURL, translation.TemplateVersion, cfg.DisableThinking)
	configKey := translation.ConfigKey(cfg)
	if rows, lookupErr := s.lookupContentTranslations(ctx, []string{key}); lookupErr == nil {
		if row, ok := rows[key]; ok && row.Origin == "model" && row.ConfigKey == configKey {
			display, convertErr := displayText(row.TranslatedText, row.Language, target, field.Names)
			if convertErr != nil {
				return translatedContent{}, "", convertErr
			}
			return translatedContent{TranslatedText: display, CacheStatus: "hit"}, "translated", nil
		}
	}

	// The model only writes Simplified, so the row stores the lane result
	// unchanged. A Traditional viewer gets the conversion afterwards, inside
	// the same name-protection boundary.
	laneTarget := target
	laneTarget.traditional = false
	translated, err := s.translateWithQueue(ctx, cfg, key, field.Names, text, laneTarget)
	if err != nil {
		return translatedContent{}, "", err
	}
	row := contentTranslationRow{
		CacheKey: key, EntityKind: kind, EntityID: entityID, EntityKey: entityKey,
		Field: in.Field, Language: target.modelLanguage, SourceText: text,
		TranslatedText: translated, Origin: "model", ConfigKey: configKey, Model: cfg.Model,
		TemplateVersion: translation.TemplateVersion,
	}
	cacheStatus, err := s.saveContentTranslations(ctx, []contentTranslationRow{row})
	if err != nil {
		return translatedContent{}, "", err
	}
	display, err := displayText(translated, target.modelLanguage, target, field.Names)
	if err != nil {
		return translatedContent{}, "", err
	}
	return translatedContent{TranslatedText: display, CacheStatus: cacheStatus}, "translated", nil
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
// A stored row keeps the published spelling of circle and cast names, so the
// script conversion has to run inside the same protection boundary as a model
// call. Converting first and restoring afterwards leaves those names verbatim.
func displayText(text, language string, target translationTarget, names []string) (string, error) {
	if !target.traditional || language == translation.SourceHant {
		return text, nil
	}
	protected, err := translation.Protect(text, names)
	if err != nil {
		return "", err
	}
	converted, err := translation.Traditional(protected.Text)
	if err != nil {
		return "", err
	}
	return protected.Restore(converted)
}

// translateWithQueue protects names, calls the model through the interactive
// lane, then restores the protected names. The queue deduplicates readers of
// the same key, so two panels asking for one title share a single call.
func (s *Server) translateWithQueue(ctx context.Context, cfg translation.Config, key string, names []string, text string, target translationTarget) (string, error) {
	return s.translateOnLane(ctx, cfg, key, names, text, target, false)
}

// translateInBackground uses the background lane. A prefill pass must never
// sit in front of a viewer, and the lane already lowers its concurrency during
// the day.
func (s *Server) translateInBackground(ctx context.Context, cfg translation.Config, key string, names []string, text string, target translationTarget) (string, error) {
	return s.translateOnLane(ctx, cfg, key, names, text, target, true)
}

func (s *Server) translateOnLane(ctx context.Context, cfg translation.Config, key string, names []string, text string, target translationTarget, background bool) (string, error) {
	protected, err := translation.Protect(text, names)
	if err != nil {
		return "", err
	}
	queue := s.contentTranslationQueue(cfg)
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	run := func(callCtx context.Context) (translation.Result, error) {
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
	}
	var result translation.Result
	if background {
		result, err = queue.DoBackground(ctx, key, timeout, run)
	} else {
		result, err = queue.Do(ctx, key, timeout, false, run)
	}
	if err != nil {
		return "", err
	}
	// Convert the script while the protected names are still placeholders.
	// Restoring first would hand a published circle or cast name to OpenCC and
	// silently rewrite it.
	output := result.Text
	if target.traditional {
		output, err = translation.Traditional(output)
		if err != nil {
			return "", err
		}
	}
	return protected.Restore(output)
}

// translateThroughModel handles a source-level field that has no durable object
// identity. Its successful output is kept in source_field_translation, which is
// display cache only and never enters the searchable content_translation
// corpus. The key includes the source work, real directory path, original text,
// target language, model and template, so unrelated fields cannot share it.
func (s *Server) translateThroughModel(ctx context.Context, in contentTranslationRequest, field readableTranslationField, text string, target translationTarget) (translatedContent, string, error) {
	cfg, err := translation.NewStore(s.db).Load(ctx)
	if err != nil {
		return translatedContent{}, "", err
	}
	if nativeLanguage, ok := nativeContentLanguage(text, target); ok {
		display, err := displayText(text, nativeLanguage, target, field.Names)
		if err != nil {
			return translatedContent{}, "", err
		}
		return translatedContent{TranslatedText: display}, "native", nil
	}
	key := sourceFieldTranslationCacheKey(in.SourceID, in.Code, in.Field, in.Path, target.modelLanguage, text, cfg.Model, cfg.BaseURL, translation.TemplateVersion, cfg.DisableThinking)
	if rows, lookupErr := s.lookupSourceFieldTranslations(ctx, []string{key}); lookupErr == nil {
		if row, ok := rows[key]; ok && row.Model == cfg.Model && row.TemplateVersion == translation.TemplateVersion {
			display, convertErr := displayText(row.TranslatedText, row.Language, target, field.Names)
			if convertErr != nil {
				return translatedContent{}, "", convertErr
			}
			return translatedContent{TranslatedText: display, CacheStatus: "hit"}, "translated", nil
		}
	}

	// Store the Simplified lane result, never a Traditional derivative. A
	// Traditional viewer converts that stored text at display time.
	laneTarget := target
	laneTarget.traditional = false
	translated, err := s.translateWithQueue(ctx, cfg, key, field.Names, text, laneTarget)
	if err != nil {
		return translatedContent{}, "", err
	}
	row := sourceFieldTranslationRow{
		CacheKey: key, SourceID: in.SourceID, WorkCode: normalizeSourceFieldWorkCode(in.Code),
		Field: in.Field, Path: in.Path, Language: target.modelLanguage,
		SourceText: text, TranslatedText: translated, Model: cfg.Model,
		TemplateVersion: translation.TemplateVersion,
	}
	cacheStatus, err := s.saveSourceFieldTranslations(ctx, []sourceFieldTranslationRow{row})
	if err != nil {
		return translatedContent{}, "", err
	}
	display, err := displayText(translated, target.modelLanguage, target, field.Names)
	if err != nil {
		return translatedContent{}, "", err
	}
	return translatedContent{TranslatedText: display, CacheStatus: cacheStatus}, "translated", nil
}
