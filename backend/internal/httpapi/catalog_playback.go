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

	"github.com/yexca/kikoto/backend/internal/sqlutil"
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
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(r.Context(), "UPDATE source_catalog_entry SET id=id WHERE id=?", id); err != nil {
		writeError(w, err)
		return
	}
	targets := []map[string]any{}
	for _, relative := range input.Paths {
		target, err := prepareCatalogFile(r.Context(), tx, entry, relative)
		if errors.Is(err, sql.ErrNoRows) {
			writeAPIError(w, 404, "not_found", "catalog audio file not found", false)
			return
		}
		if err != nil {
			writeError(w, err)
			return
		}
		targets = append(targets, target)
	}
	if err = tx.Commit(); err != nil {
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

func prepareCatalogFile(ctx context.Context, tx *sql.Tx, entry catalogEntry, relative string) (map[string]any, error) {
	var title string
	var size int64
	if err := tx.QueryRowContext(ctx, "SELECT title,size_bytes FROM source_catalog_file WHERE catalog_entry_id=? AND relative_path=? AND kind='audio'", entry.ID, relative).Scan(&title, &size); err != nil {
		return nil, err
	}
	fingerprint := fmt.Sprintf("catalog:%d:%s", entry.ID, relative)
	var mediaID, locationID int64
	err := tx.QueryRowContext(ctx, "SELECT id FROM media_item WHERE work_id=? AND fingerprint=?", *entry.WorkID, fingerprint).Scan(&mediaID)
	if errors.Is(err, sql.ErrNoRows) {
		mediaID, err = sqlutil.InsertID(ctx, tx, "INSERT INTO media_item(work_id,kind,title,fingerprint,size_bytes) VALUES(?,'audio',?,?,?)", *entry.WorkID, title, fingerprint, size)
	}
	if err != nil {
		return nil, err
	}
	stream := fmt.Sprintf("/api/source-catalog/%d/media?path=%s", entry.ID, url.QueryEscape(relative))
	err = tx.QueryRowContext(ctx, "SELECT id FROM media_file_location WHERE media_item_id=? AND file_source_id=? AND location_type='catalog_stream' AND path=?", mediaID, entry.SourceID, relative).Scan(&locationID)
	if errors.Is(err, sql.ErrNoRows) {
		locationID, err = sqlutil.InsertID(ctx, tx, "INSERT INTO media_file_location(media_item_id,file_source_id,location_type,path,stream_url,size_bytes,availability) VALUES(?,?,'catalog_stream',?,?,?,'available')", mediaID, entry.SourceID, relative, stream, size)
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"path": relative, "workId": *entry.WorkID, "mediaItemId": mediaID, "locationId": locationID, "streamUrl": stream}, nil
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
