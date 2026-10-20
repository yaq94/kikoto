package httpapi

import (
	"context"
	"database/sql"
	"errors"
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

// errFieldAbsent distinguishes a field the resource does not publish from a
// stale selection or an unreachable source. The request names something that
// cannot exist, so the caller reports invalid input instead of asking the
// reader to reload.
var errFieldAbsent = errors.New("content field does not exist for this resource")

func (s *Server) loadTranslationField(ctx context.Context, in contentTranslationRequest) (readableTranslationField, error) {
	// The source selector has no description field. Check before loading the
	// remote listing so the answer does not depend on upstream availability,
	// but only when no higher-priority resource was named.
	if in.SourceID > 0 && in.SeriesID <= 0 && in.CatalogEntryID <= 0 && in.WorkID <= 0 && in.Field == "description" {
		return readableTranslationField{}, errFieldAbsent
	}
	if in.SeriesID > 0 {
		if s.cfg.IsDemo() || in.Field != "title" {
			return readableTranslationField{}, sql.ErrNoRows
		}
		var title string
		err := s.db.QueryRowContext(ctx, `SELECT title FROM catalog_series WHERE id=?`, in.SeriesID).Scan(&title)
		field := readableTranslationField{Text: title, Original: title}
		if err == nil {
			field.Names, err = s.catalogSeriesProtectedNames(ctx, in.SeriesID)
		}
		return field, err
	}
	if in.CatalogEntryID > 0 {
		entry, _, err := s.loadSourceCatalogEntry(ctx, in.CatalogEntryID)
		if err != nil {
			return readableTranslationField{}, err
		}
		names, err := s.catalogEntryProtectedNames(ctx, entry)
		if err != nil {
			return readableTranslationField{}, err
		}
		if in.Field == "title" {
			field := readableTranslationField{Text: entry.Title, Original: entry.Title, Names: names}
			if entry.WorkID != nil {
				existing, err := s.existingWorkTitle(ctx, *entry.WorkID, entry.Title, in.Language)
				if err != nil {
					return readableTranslationField{}, err
				}
				field.Existing = existing
			}
			return field, nil
		}
		if in.Field == "directory" {
			files, err := s.loadSourceCatalogFiles(ctx, entry.ID)
			if err != nil {
				return readableTranslationField{}, err
			}
			for _, file := range files {
				if file.Path == in.Path {
					return readableTranslationField{Text: file.Title, Original: file.Title, Names: names}, nil
				}
			}
		}
		return readableTranslationField{}, sql.ErrNoRows
	}
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
		// An existing target-language text only answers the selection it came
		// from. A title from another edition or a language manual is a separate
		// source text and must not replace the text the caller selected.
		if in.Field == "title" {
			for _, choice := range work.TitleChoices {
				candidates = append(candidates, choice.Title)
			}
			field.Existing = targetTitleFor(input, in.Text, in.Language, true)
		} else {
			originDescription := metadatatitles.Original(input.Variants, input.Fallback).Description
			for _, v := range input.Variants {
				if v.Language != in.Language || v.Description == "" {
					continue
				}
				if v.Description == in.Text || in.Text == originDescription {
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

// existingWorkTitle returns a stored target-language title only when the card's
// text is the work's own origin title. A catalog name that differs from the
// work title is separate source text, so another title in the same language
// cannot answer it.
func (s *Server) existingWorkTitle(ctx context.Context, workID int64, text, language string) (string, error) {
	inputs, err := s.loadWorkTitleInputs(ctx, []int64{workID}, false)
	if err != nil {
		return "", err
	}
	input, ok := inputs[workID]
	if !ok {
		return "", nil
	}
	return targetTitleFor(input, text, language, false), nil
}

// targetTitleFor returns the stored title in the requested language when the
// selected text identifies the work's title. A work request may select a
// translated edition as its own source text, so acceptSelected allows that;
// a card can only stand in for the original title.
func targetTitleFor(input workTitleInputs, text, language string, acceptSelected bool) string {
	origin, _ := metadatatitles.ForLanguage(input.Variants, input.Manual, "origin", input.Fallback)
	if origin.Title == "" || text == "" {
		return ""
	}
	selected, found := metadatatitles.ForLanguage(input.Variants, input.Manual, language, input.Fallback)
	if !found {
		return ""
	}
	if text == origin.Title || (acceptSelected && text == selected.Title) {
		return selected.Title
	}
	return ""
}

// A catalog card and its directory names can mention the circle or a voice
// actor. Those names stay as published, even when no work is registered yet.
func (s *Server) catalogEntryProtectedNames(ctx context.Context, entry catalogEntry) ([]string, error) {
	names := []string{}
	if entry.WorkID == nil {
		return names, nil
	}
	work, err := s.loadWorkDetail(ctx, 0, *entry.WorkID, false)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return names, nil
		}
		return nil, err
	}
	names = append(names, work.Circle)
	names = append(names, work.VoiceActors...)
	for _, credit := range work.VoiceCredits {
		names = append(names, credit.DisplayName)
	}
	return names, nil
}

// A series title belongs to the same work as its episodes, so every member's
// circle and cast are protected names for that title.
func (s *Server) catalogSeriesProtectedNames(ctx context.Context, seriesID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT COALESCE(e.work_id,0) FROM catalog_series_member m
		JOIN source_catalog_entry e ON e.id=m.catalog_entry_id
		WHERE m.series_id=? AND e.work_id IS NOT NULL`, seriesID)
	if err != nil {
		return nil, err
	}
	workIDs := []int64{}
	for rows.Next() {
		var workID int64
		if err = rows.Scan(&workID); err != nil {
			_ = rows.Close()
			return nil, err
		}
		workIDs = append(workIDs, workID)
	}
	readErr := rows.Err()
	_ = rows.Close()
	if readErr != nil {
		return nil, readErr
	}
	// Close the result set before loading each work. The caller may hold the
	// only pooled connection, and a second query while a cursor is open would
	// wait on itself.
	names := []string{}
	for _, workID := range workIDs {
		work, loadErr := s.loadWorkDetail(ctx, 0, workID, false)
		if loadErr != nil {
			if errors.Is(loadErr, sql.ErrNoRows) {
				continue
			}
			return nil, loadErr
		}
		names = append(names, work.Circle)
		names = append(names, work.VoiceActors...)
		for _, credit := range work.VoiceCredits {
			names = append(names, credit.DisplayName)
		}
	}
	return names, nil
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
	case "description":
		// A Kikoeru listing publishes no description. The request names a
		// field this source cannot carry, so it is invalid input rather than a
		// stale selection.
		return readableTranslationField{}, errFieldAbsent
	case "title":
		// The Original is the text that is actually being translated. A
		// language-edition title is its own content; replacing it with the
		// current title would translate the wrong string and report a 409.
		field.Original = in.Text
		// Only the work's own title can reuse a target-language edition. When
		// another edition is selected, that edition is the source text and its
		// siblings are not translations of it.
		sourceTitle := in.Text == detail.Title || in.Text == work.Title
		if sourceTitle {
			field.Text = in.Text
		}
		for _, edition := range work.OtherLanguageEditions {
			if edition.Title == in.Text {
				field.Text = in.Text
				field.Original = in.Text
			}
			if normalizeRemotePresentationLanguage(edition.Language) == in.Language && (sourceTitle || edition.Title == in.Text) {
				field.Existing = edition.Title
			}
		}
		for _, edition := range normalizedRemoteLanguageEditions(work) {
			if edition.Current && normalizeRemotePresentationLanguage(edition.Language) == in.Language && sourceTitle {
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
