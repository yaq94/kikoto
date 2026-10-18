package httpapi

import (
	"context"
	"net/http"
	"path"
)

func (s *Server) streamSourceCatalogMedia(w http.ResponseWriter, r *http.Request) {
	id, err := parseInt64PathValue(r, "id")
	if err != nil {
		writeAPIError(w, 400, "invalid_request", "invalid catalog id", false)
		return
	}
	entry, source, err := s.loadSourceCatalogEntry(r.Context(), id)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	relative := r.URL.Query().Get("path")
	id = entry.ID
	// The supplied path must be an exact previously listed audio file. Display
	// names and arbitrary URLs never participate in file selection.
	var kind string
	if err = s.db.QueryRowContext(r.Context(), "SELECT kind FROM source_catalog_file WHERE catalog_entry_id=? AND relative_path=?", id, relative).Scan(&kind); err != nil || kind != "audio" {
		writeAPIError(w, 404, "not_found", "catalog audio file not found", false)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), remotePlaybackTimeout)
	defer cancel()
	sourceRoot := entry.SourceRoot
	if sourceRoot == "" {
		sourceRoot = entry.RootPath
	}
	for attempt := 0; attempt < 2; attempt++ {
		object, err := s.openListClient(source).Get(ctx, path.Join(sourceRoot, relative))
		if err != nil {
			writeUpstreamError(w, err)
			return
		}
		parsed, err := remotePlaybackURLAllowed(object.RawURL, source)
		if err != nil {
			writeAPIError(w, 502, "remote_media_blocked", "remote media source is not allowed", true)
			return
		}
		response, err := s.openRemotePlaybackResponse(ctx, r, source, parsed)
		if err != nil {
			writeUpstreamError(w, err)
			return
		}
		if attempt == 0 && (response.StatusCode == 401 || response.StatusCode == 403 || response.StatusCode == 410) {
			_ = response.Body.Close()
			continue
		}
		_ = s.serveRemotePlaybackResponse(w, r, response, relative, s.remoteMediaDownloadLimitBytes(r.Context()))
		return
	}
}
