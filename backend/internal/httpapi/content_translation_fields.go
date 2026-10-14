package httpapi

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yexca/kikoto/backend/internal/kikoeru"
	"github.com/yexca/kikoto/backend/internal/metadatatags"
	"github.com/yexca/kikoto/backend/internal/metadatatitles"
)

type readableTranslationField struct {
	Text, Existing, Original string
	Names                    []string
}

func (s *Server) loadTranslationField(ctx context.Context, in contentTranslationRequest) (readableTranslationField, error) {
	if in.SourceID > 0 {
		return s.loadRemoteTranslationField(ctx, in)
	}
	if in.WorkID <= 0 {
		return readableTranslationField{}, sql.ErrNoRows
	}
	work, err := s.loadWorkDetail(ctx, 0, in.WorkID, false)
	if err != nil {
		return readableTranslationField{}, err
	}
	field := readableTranslationField{Names: append([]string{work.Circle}, work.VoiceActors...)}
	for _, credit := range work.VoiceCredits {
		field.Names = append(field.Names, credit.DisplayName)
	}
	switch in.Field {
	case "title", "description":
		candidates := []string{work.Title}
		if in.Field == "description" {
			candidates = []string{work.Description}
		}
		inputs, err := s.loadWorkTitleInputs(ctx, []int64{in.WorkID}, true)
		if err != nil {
			return field, err
		}
		input := inputs[in.WorkID]
		var originalTitle, originalDescription string
		if err := s.db.QueryRowContext(ctx, "SELECT title,description FROM work WHERE id=?", in.WorkID).Scan(&originalTitle, &originalDescription); err != nil {
			return field, err
		}
		field.Original = originalTitle
		if input.Manual[""] != "" {
			field.Original = input.Manual[""]
		}
		if in.Field == "description" {
			field.Original = originalDescription
		}
		for _, v := range work.MetadataView.Variants {
			if in.Field == "title" {
				candidates = append(candidates, v.Title)
			} else {
				candidates = append(candidates, v.Description)
			}
		}
		for _, v := range input.Variants {
			if in.Field == "title" {
				candidates = append(candidates, metadatatitles.Display(v.Title, !v.Origin), v.Title)
			} else {
				candidates = append(candidates, v.Description)
			}
		}
		if in.Field == "title" {
			for _, choice := range work.TitleChoices {
				candidates = append(candidates, choice.Title)
			}
			selected, found := metadatatitles.ForLanguage(input.Variants, input.Manual, in.Language, input.Fallback)
			if found {
				field.Existing = selected.Title
			}
		} else {
			for _, v := range input.Variants {
				if v.Language == in.Language {
					field.Existing = v.Description
					break
				}
			}
		}
		if slices.Contains(candidates, in.Text) {
			field.Text = in.Text
		}
	case "tag":
		if slices.Contains(work.Tags, in.Text) {
			field.Text = in.Text
		}
		tags, err := s.loadWorkMetadataTags(ctx, in.WorkID)
		if err != nil {
			return field, err
		}
		for _, tag := range tags.Tags {
			match := tag.DisplayName == in.Text
			for _, name := range tag.Names {
				if name.Name == in.Text {
					match = true
				}
			}
			if !match {
				continue
			}
			field.Text = in.Text
			originals, err := metadatatags.LocalizedNames(ctx, s.db, []int64{tag.ID}, []string{"origin"})
			if err != nil {
				return field, err
			}
			field.Original = originals[tag.ID]
			for _, name := range tag.Names {
				if name.Language == in.Language {
					field.Existing = name.Name
					break
				}
			}
		}
	case "directory":
		mediaID, err := s.resolveMediaWorkIDForRequest(ctx, in.WorkID)
		if err != nil {
			return field, err
		}
		media, err := s.loadWorkMediaItems(ctx, 0, mediaID)
		if err != nil {
			return field, err
		}
		for _, item := range media {
			if in.MediaID > 0 && item.ID != in.MediaID {
				continue
			}
			if item.Title == in.Text {
				field.Text = in.Text
			}
			for _, location := range item.Locations {
				for _, part := range strings.Split(filepath.ToSlash(location.Path), "/") {
					if part == in.Text {
						field.Text = in.Text
					}
				}
			}
		}
	}
	return field, nil
}

func (s *Server) loadRemoteTranslationField(ctx context.Context, in contentTranslationRequest) (readableTranslationField, error) {
	source, work, err := s.loadRemoteWorkCached(ctx, in.SourceID, in.Code)
	if err != nil {
		return readableTranslationField{}, err
	}
	detail, err := s.remoteWorkDetailWithLanguages(ctx, source, work, s.remoteSourceLanguages(ctx, source))
	if err != nil {
		return readableTranslationField{}, err
	}
	field := readableTranslationField{Names: append([]string{detail.Circle}, detail.VoiceActors...)}
	switch in.Field {
	case "title":
		field.Original = detail.Title
		if in.Text == detail.Title || in.Text == work.Title {
			field.Text = in.Text
		}
		for _, edition := range work.OtherLanguageEditions {
			if edition.Title == in.Text {
				field.Text = in.Text
			}
			if normalizeRemotePresentationLanguage(edition.Language) == in.Language {
				field.Existing = edition.Title
			}
		}
		for _, edition := range normalizedRemoteLanguageEditions(work) {
			if edition.Current && normalizeRemotePresentationLanguage(edition.Language) == in.Language {
				field.Existing = detail.Title
			}
		}
	case "tag":
		for _, tag := range work.Tags {
			match := tag.Name == in.Text
			for _, name := range tag.I18n {
				if name.Name == in.Text {
					match = true
				}
			}
			if !match {
				continue
			}
			field.Text = in.Text
			field.Original = tag.Name
			for language, name := range tag.I18n {
				if normalizeRemotePresentationLanguage(language) == in.Language {
					field.Existing = name.Name
					break
				}
			}
		}
	case "directory":
		_, _, tracks, err := s.loadRemoteWorkTracksCached(ctx, in.SourceID, in.Code)
		if err != nil {
			return field, err
		}
		var visit func([]kikoeru.Track, string)
		visit = func(tracks []kikoeru.Track, parent string) {
			for i, track := range tracks {
				name := remoteTrackName(track.Title, i)
				path := remoteTrackPath(parent, name)
				if name == in.Text && (in.Path == "" || in.Path == path) {
					field.Text = name
				}
				visit(track.Children, path)
			}
		}
		visit(tracks, "")
	}
	return field, nil
}
