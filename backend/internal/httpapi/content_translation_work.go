package httpapi

import (
	"context"
	"slices"

	"github.com/yexca/kikoto/backend/internal/metadatatitles"
)

// Title and description requests need the published prose and protected names,
// not the detail page's source locations, playback, purchases or sync status.
// Reuse the title selector used by that page for editions and authored text.
func (s *Server) loadWorkProseTranslationField(ctx context.Context, in contentTranslationRequest) (readableTranslationField, error) {
	var title, description, snapshot, circle string
	err := s.db.QueryRowContext(ctx, `SELECT w.title,w.description,
		COALESCE((SELECT m.snapshot_json FROM metadata_snapshot m
			JOIN metadata_provider p ON p.id=m.provider_id
			WHERE m.work_id=w.id AND p.code='dlsite' ORDER BY m.fetched_at DESC,m.id DESC LIMIT 1),''),
		COALESCE((SELECT display_name FROM work_primary_circle WHERE work_id=w.id),'')
		FROM work w WHERE w.id=?`, in.WorkID).Scan(&title, &description, &snapshot, &circle)
	if err != nil {
		return readableTranslationField{}, err
	}
	inputs, err := s.loadWorkTitleInputs(ctx, []int64{in.WorkID}, true)
	if err != nil {
		return readableTranslationField{}, err
	}
	input := inputs[in.WorkID]
	overrides, err := s.loadWorkManualOverrides(ctx, in.WorkID)
	if err != nil {
		return readableTranslationField{}, err
	}
	metadata := parseDLsiteSnapshot(snapshot)
	if circle == "" {
		circle = metadata.Circle
	}
	if overrides.Circle != nil {
		circle = overrides.Circle.Name
	}
	names := append([]string{circle}, metadata.VoiceActors...)
	if len(overrides.VoiceActors) > 0 {
		names = append([]string{circle}, manualPeopleNames(overrides.VoiceActors)...)
	} else {
		credits, err := s.loadWorkDetailVoiceCredits(ctx, in.WorkID)
		if err != nil {
			return readableTranslationField{}, err
		}
		for _, credit := range credits {
			names = append(names, credit.DisplayName)
		}
	}
	field := readableTranslationField{Names: names, Original: title}
	selected := metadatatitles.Select(input.Variants, input.Manual, s.viewerMetadataLanguages(ctx), input.Fallback)
	candidates := []string{}
	if in.Field == "title" {
		if input.Manual[""] != "" {
			field.Original = input.Manual[""]
		}
		candidates = append(candidates, selected.Title)
		for _, language := range []string{"", "ja-jp", "zh-cn", "zh-tw", "en-us", "ko-kr", "origin"} {
			choice, _ := metadatatitles.ForLanguage(input.Variants, input.Manual, language, input.Fallback)
			candidates = append(candidates, choice.Title)
		}
		for _, variant := range input.Variants {
			candidates = append(candidates, variant.Title, metadatatitles.Display(variant.Title, !variant.Origin), metadatatitles.ForEdition(variant, input.Manual).Title)
		}
		field.Existing = targetTitleFor(input, in.Text, in.Language, true)
	} else {
		field.Original = description
		candidates = append(candidates, selected.Description)
		if overrides.Description != nil {
			candidates[0] = *overrides.Description
		}
		originDescription := metadatatitles.Original(input.Variants, input.Fallback).Description
		// Manual titles without provider editions also publish a presentation
		// variant carrying the original description, even with a manual summary.
		if len(input.Variants) == 0 && len(input.Manual) > 0 {
			candidates = append(candidates, originDescription)
		}
		for _, variant := range input.Variants {
			candidates = append(candidates, variant.Description)
			if field.Existing == "" && variant.Language == in.Language && variant.Description != "" &&
				(variant.Description == in.Text || in.Text == originDescription) {
				field.Existing = variant.Description
			}
		}
	}
	if slices.Contains(candidates, in.Text) {
		field.Text = in.Text
	}
	return field, nil
}
