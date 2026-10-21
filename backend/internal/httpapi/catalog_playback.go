package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Preparing an explicit play of an already registered work gives its exact
// catalog file a media identity. Browsing never registers works or media.
func (s *Server) prepareCatalogPlayback(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePermission(w, r, "playback:use"); !ok {
		return
	}
	id, err := parseInt64PathValue(r, "id")
	if err != nil {
		writeAPIError(w, 400, "invalid_request", "invalid catalog id", false)
		return
	}
	var input struct {
		Path  string   `json:"path"`
		Paths []string `json:"paths"`
	}
	if err = json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&input); err != nil {
		writeAPIError(w, 400, "invalid_request", "invalid playback request", false)
		return
	}
	if input.Path != "" {
		input.Paths = []string{input.Path}
	}
	if len(input.Paths) == 0 || len(input.Paths) > 256 {
		writeAPIError(w, 400, "invalid_request", "playback queue must contain 1 to 256 paths", false)
		return
	}
	entry, _, err := s.loadSourceCatalogEntry(r.Context(), id)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	if entry.WorkID == nil {
		writeAPIError(w, 409, "work_not_registered", "register this work before saving playback progress", false)
		return
	}
	targets, err := s.prepareCatalogPlaybackTargets(r.Context(), entry, input.Paths)
	if errors.Is(err, sql.ErrNoRows) {
		writeAPIError(w, 404, "not_found", "catalog audio file not found", false)
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	response := map[string]any{"targets": targets}
	if input.Path != "" {
		for key, value := range targets[0] {
			response[key] = value
		}
	}
	writeJSON(w, 200, response)
}

type catalogPlaybackFile struct {
	relative string
	title    string
	size     int64
}

// prepareCatalogPlaybackTargets keeps the common replay path read-only. A
// transaction only opens when a media item or location row is actually
// missing, and then every missing row is written in one statement.
func (s *Server) prepareCatalogPlaybackTargets(ctx context.Context, entry catalogEntry, paths []string) ([]map[string]any, error) {
	files, err := s.loadCatalogPlaybackFiles(ctx, s.db, entry.ID, paths)
	if err != nil {
		return nil, err
	}
	unique := make([]catalogPlaybackFile, 0, len(files))
	seen := map[string]bool{}
	for _, relative := range paths {
		if seen[relative] {
			continue
		}
		seen[relative] = true
		file, ok := files[relative]
		if !ok {
			return nil, sql.ErrNoRows
		}
		unique = append(unique, file)
	}

	mediaIDs, err := s.lookupCatalogPlaybackMedia(ctx, s.db, *entry.WorkID, entry.ID, unique)
	if err != nil {
		return nil, err
	}
	locationIDs, err := s.lookupCatalogPlaybackLocations(ctx, s.db, entry.SourceID, unique, mediaIDs)
	if err != nil {
		return nil, err
	}
	missingMedia := missingCatalogPlaybackMedia(unique, mediaIDs)
	missingLocations := missingCatalogPlaybackLocations(unique, mediaIDs, locationIDs)
	if len(missingMedia) > 0 || len(missingLocations) > 0 {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		defer func() { _ = tx.Rollback() }()
		if len(missingMedia) > 0 {
			if err = insertCatalogPlaybackMedia(ctx, tx, *entry.WorkID, entry.ID, missingMedia); err != nil {
				return nil, fmt.Errorf("insert catalog media: %w", err)
			}
			if mediaIDs, err = s.lookupCatalogPlaybackMedia(ctx, tx, *entry.WorkID, entry.ID, unique); err != nil {
				return nil, err
			}
			missingLocations = missingCatalogPlaybackLocations(unique, mediaIDs, locationIDs)
		}
		if len(missingLocations) > 0 {
			if err = insertCatalogPlaybackLocations(ctx, tx, entry.SourceID, entry.ID, missingLocations, mediaIDs); err != nil {
				return nil, fmt.Errorf("insert catalog locations: %w", err)
			}
			if locationIDs, err = s.lookupCatalogPlaybackLocations(ctx, tx, entry.SourceID, unique, mediaIDs); err != nil {
				return nil, err
			}
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
	}

	targets := make([]map[string]any, 0, len(paths))
	for _, relative := range paths {
		mediaID, ok := mediaIDs[relative]
		if !ok {
			return nil, sql.ErrNoRows
		}
		locationID, ok := locationIDs[relative]
		if !ok {
			return nil, sql.ErrNoRows
		}
		targets = append(targets, map[string]any{
			"path":        relative,
			"workId":      *entry.WorkID,
			"mediaItemId": mediaID,
			"locationId":  locationID,
			"streamUrl":   catalogPlaybackStreamURL(entry.ID, relative),
		})
	}
	return targets, nil
}

type catalogPlaybackQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (s *Server) loadCatalogPlaybackFiles(ctx context.Context, q catalogPlaybackQueryer, entryID int64, paths []string) (map[string]catalogPlaybackFile, error) {
	placeholders := make([]string, 0, len(paths))
	args := make([]any, 0, len(paths)+1)
	args = append(args, entryID)
	for _, relative := range paths {
		placeholders = append(placeholders, "?")
		args = append(args, relative)
	}
	rows, err := q.QueryContext(ctx, `SELECT relative_path,title,size_bytes FROM source_catalog_file
		WHERE catalog_entry_id=? AND kind='audio' AND relative_path IN (`+strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	files := make(map[string]catalogPlaybackFile, len(paths))
	for rows.Next() {
		var file catalogPlaybackFile
		if err = rows.Scan(&file.relative, &file.title, &file.size); err != nil {
			return nil, err
		}
		files[file.relative] = file
	}
	return files, rows.Err()
}

func catalogPlaybackFingerprint(entryID int64, relative string) string {
	return fmt.Sprintf("catalog:%d:%s", entryID, relative)
}

func catalogPlaybackStreamURL(entryID int64, relative string) string {
	return fmt.Sprintf("/api/source-catalog/%d/media?path=%s", entryID, url.QueryEscape(relative))
}

func (s *Server) lookupCatalogPlaybackMedia(ctx context.Context, q catalogPlaybackQueryer, workID, entryID int64, files []catalogPlaybackFile) (map[string]int64, error) {
	if len(files) == 0 {
		return map[string]int64{}, nil
	}
	placeholders := make([]string, 0, len(files))
	args := make([]any, 0, len(files)+1)
	args = append(args, workID)
	for _, file := range files {
		placeholders = append(placeholders, "?")
		args = append(args, catalogPlaybackFingerprint(entryID, file.relative))
	}
	rows, err := q.QueryContext(ctx, `SELECT id,fingerprint FROM media_item WHERE work_id=? AND fingerprint IN (`+strings.Join(placeholders, ",")+`) ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	ids := make(map[string]int64, len(files))
	byFingerprint := make(map[string]string, len(files))
	for _, file := range files {
		byFingerprint[catalogPlaybackFingerprint(entryID, file.relative)] = file.relative
	}
	for rows.Next() {
		var id int64
		var fingerprint string
		if err = rows.Scan(&id, &fingerprint); err != nil {
			return nil, err
		}
		if relative, ok := byFingerprint[fingerprint]; ok {
			if _, exists := ids[relative]; !exists {
				ids[relative] = id
			}
		}
	}
	return ids, rows.Err()
}

func (s *Server) lookupCatalogPlaybackLocations(ctx context.Context, q catalogPlaybackQueryer, sourceID int64, files []catalogPlaybackFile, mediaIDs map[string]int64) (map[string]int64, error) {
	pairs := make([]catalogPlaybackFile, 0, len(files))
	for _, file := range files {
		if _, ok := mediaIDs[file.relative]; ok {
			pairs = append(pairs, file)
		}
	}
	if len(pairs) == 0 {
		return map[string]int64{}, nil
	}
	placeholders := make([]string, 0, len(pairs))
	args := make([]any, 0, len(pairs)*2+1)
	args = append(args, sourceID)
	for _, file := range pairs {
		placeholders = append(placeholders, "(?,?)")
		args = append(args, mediaIDs[file.relative], file.relative)
	}
	rows, err := q.QueryContext(ctx, `SELECT id,media_item_id,path FROM media_file_location
		WHERE file_source_id=? AND location_type='catalog_stream' AND (media_item_id,path) IN (`+strings.Join(placeholders, ",")+`) ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	locations := make(map[string]int64, len(pairs))
	byMediaPath := make(map[int64]map[string]struct{}, len(pairs))
	for _, file := range pairs {
		mediaID := mediaIDs[file.relative]
		if byMediaPath[mediaID] == nil {
			byMediaPath[mediaID] = map[string]struct{}{}
		}
		byMediaPath[mediaID][file.relative] = struct{}{}
	}
	for rows.Next() {
		var id, mediaID int64
		var relative string
		if err = rows.Scan(&id, &mediaID, &relative); err != nil {
			return nil, err
		}
		if _, ok := byMediaPath[mediaID][relative]; ok {
			if _, exists := locations[relative]; !exists {
				locations[relative] = id
			}
		}
	}
	return locations, rows.Err()
}

func missingCatalogPlaybackMedia(files []catalogPlaybackFile, mediaIDs map[string]int64) []catalogPlaybackFile {
	missing := make([]catalogPlaybackFile, 0, len(files))
	for _, file := range files {
		if _, ok := mediaIDs[file.relative]; !ok {
			missing = append(missing, file)
		}
	}
	return missing
}

func missingCatalogPlaybackLocations(files []catalogPlaybackFile, mediaIDs, locationIDs map[string]int64) []catalogPlaybackFile {
	missing := make([]catalogPlaybackFile, 0, len(files))
	for _, file := range files {
		if _, ok := mediaIDs[file.relative]; !ok {
			continue
		}
		if _, ok := locationIDs[file.relative]; !ok {
			missing = append(missing, file)
		}
	}
	return missing
}

// insertCatalogPlaybackMedia writes every new media row in one statement.
// media_item has no unique fingerprint constraint, so the guard is a NOT EXISTS
// per candidate rather than ON CONFLICT.
func insertCatalogPlaybackMedia(ctx context.Context, tx *sql.Tx, workID, entryID int64, files []catalogPlaybackFile) error {
	if len(files) == 0 {
		return nil
	}
	var statement strings.Builder
	args := make([]any, 0, len(files)*4)
	statement.WriteString(`INSERT INTO media_item(work_id,kind,title,fingerprint,size_bytes)
		SELECT v.work_id,'audio',v.title,v.fingerprint,v.size_bytes FROM (`)
	for index, file := range files {
		if index > 0 {
			statement.WriteString(" UNION ALL ")
		}
		statement.WriteString("SELECT ? AS work_id,? AS title,? AS fingerprint,? AS size_bytes")
		args = append(args, workID, file.title, catalogPlaybackFingerprint(entryID, file.relative), file.size)
	}
	statement.WriteString(`) v WHERE NOT EXISTS (
		SELECT 1 FROM media_item m WHERE m.work_id=v.work_id AND m.fingerprint=v.fingerprint)`)
	_, err := tx.ExecContext(ctx, statement.String(), args...)
	return err
}

func insertCatalogPlaybackLocations(ctx context.Context, tx *sql.Tx, sourceID, entryID int64, files []catalogPlaybackFile, mediaIDs map[string]int64) error {
	if len(files) == 0 {
		return nil
	}
	var statement strings.Builder
	args := make([]any, 0, len(files)*4+2)
	statement.WriteString(`INSERT INTO media_file_location(media_item_id,file_source_id,location_type,path,stream_url,size_bytes,availability)
		SELECT v.media_item_id,?,'catalog_stream',v.path,v.stream_url,v.size_bytes,'available' FROM (`)
	args = append(args, sourceID)
	for index, file := range files {
		if index > 0 {
			statement.WriteString(" UNION ALL ")
		}
		statement.WriteString("SELECT ? AS media_item_id,? AS path,? AS stream_url,? AS size_bytes")
		args = append(args, mediaIDs[file.relative], file.relative, catalogPlaybackStreamURL(entryID, file.relative), file.size)
	}
	statement.WriteString(`) v WHERE NOT EXISTS (
		SELECT 1 FROM media_file_location l WHERE l.media_item_id=v.media_item_id
		AND l.file_source_id=? AND l.location_type='catalog_stream' AND l.path=v.path)`)
	args = append(args, sourceID)
	_, err := tx.ExecContext(ctx, statement.String(), args...)
	return err
}

func (s *Server) streamCatalogLocation(w http.ResponseWriter, r *http.Request, target mediaStreamTarget) {
	parsed, err := url.Parse(target.StreamURL)
	if err != nil || parsed.IsAbs() || parsed.Host != "" {
		writeAPIError(w, 404, "not_found", "catalog media not found", false)
		return
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 4 || parts[0] != "api" || parts[1] != "source-catalog" || parts[3] != "media" {
		writeAPIError(w, 404, "not_found", "catalog media not found", false)
		return
	}
	id, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		writeAPIError(w, 404, "not_found", "catalog media not found", false)
		return
	}
	entry, _, err := s.loadSourceCatalogEntry(r.Context(), id)
	if err != nil || entry.SourceID != target.FileSourceID || parsed.Query().Get("path") != target.RelativePath {
		writeAPIError(w, 404, "not_found", "catalog media not found", false)
		return
	}
	request := r.Clone(r.Context())
	request.SetPathValue("id", parts[2])
	request.URL = parsed
	s.streamSourceCatalogMedia(w, request)
}
