package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/yexca/kikoto/backend/internal/openlist"
)

const sourceTypeOpenList = "openlist"

type catalogEntry struct {
	ID          int64  `json:"catalogEntryId"`
	SourceID    int64  `json:"sourceId"`
	PrimaryCode string `json:"primaryCode"`
	Title       string `json:"title"`
	RootPath    string `json:"-"`
	WorkID      *int64 `json:"workId"`
}
type SourceCatalogFile struct {
	Path  string `json:"path"`
	Title string `json:"title"`
	Kind  string `json:"kind"`
	Size  int64  `json:"sizeBytes"`
}

func (s *Server) openListClient(source remoteSourceForUse) *openlist.Client {
	return openlist.NewClient(source.Endpoint.APIURL, s.sourceClient(source, 30*time.Second, sourceRequestOpenList))
}

func validateOpenListConfig(config fileSourceConfig) error {
	if len(config.OpenListRoots) == 0 || len(config.OpenListRoots) > 8 {
		return errors.New("openListRoots must contain 1 to 8 explicit work directories")
	}
	for i, root := range config.OpenListRoots {
		if !openlist.ValidRoot(root) {
			return errors.New("openListRoots must contain normalized absolute work directories")
		}
		for j := 0; j < i; j++ {
			other := config.OpenListRoots[j]
			if root == other || strings.HasPrefix(root, other+"/") || strings.HasPrefix(other, root+"/") {
				return errors.New("openListRoots must not overlap")
			}
		}
	}
	if n := config.OpenListIntervalMS; n != nil && (*n < 100 || *n > 60000) {
		return errors.New("openListIntervalMs must be between 100 and 60000")
	}
	return nil
}

func (s *Server) scanOpenListCatalog(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePermission(w, r, "sources:write"); !ok {
		return
	}
	id, err := parseInt64PathValue(r, "id")
	if err != nil {
		writeAPIError(w, 400, "invalid_request", "invalid source id", false)
		return
	}
	source, err := s.loadRemoteSourceForUse(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	if !source.Enabled || source.SourceType != sourceTypeOpenList {
		writeUpstreamError(w, errRemoteSourceNotUsable)
		return
	}
	if err := validateOpenListConfig(source.Config); err != nil {
		writeAPIError(w, 400, "invalid_request", err.Error(), false)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	type rootResult struct {
		Root           string `json:"root"`
		Status         string `json:"status"`
		CatalogEntryID int64  `json:"catalogEntryId,omitempty"`
	}
	results := []rootResult{}
	status := "complete"
	for _, root := range source.Config.OpenListRoots {
		files, err := s.openListClient(source).ScanRoot(ctx, root)
		if ctx.Err() != nil {
			writeAPIError(w, 503, "service_unavailable", "catalog scan cancelled or timed out", true)
			return
		}
		if err != nil {
			outcome := "failed"
			if errors.Is(err, openlist.ErrRestricted) {
				outcome = "restricted"
			}
			if errors.Is(err, openlist.ErrLimit) {
				outcome = "limit_exceeded"
			}
			results = append(results, rootResult{Root: root, Status: outcome})
			status = "partial"
			continue
		}
		playable := false
		for _, file := range files {
			if file.Kind == "audio" {
				playable = true
				break
			}
		}
		if !playable {
			results = append(results, rootResult{Root: root, Status: "excluded"})
			continue
		}
		entryID, err := s.persistOpenListRoot(ctx, source, root, files)
		if err != nil {
			writeError(w, err)
			return
		}
		results = append(results, rootResult{Root: root, Status: "indexed", CatalogEntryID: entryID})
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": status, "roots": results})
}

func (s *Server) persistOpenListRoot(ctx context.Context, source remoteSourceForUse, root string, files []openlist.File) (int64, error) {
	if id, split, err := s.persistCatalogEpisodeRoots(ctx, source, root, files); split || err != nil {
		return id, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	id, err := persistOpenListSnapshotTx(ctx, tx, source.ID, root, files)
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func persistOpenListSnapshotTx(ctx context.Context, tx *sql.Tx, sourceID int64, root string, files []openlist.File) (int64, error) {
	code := normalizeWorkCode(path.Base(root))
	// Product codes may be embedded in an authored work folder name.
	if code == "" {
		for _, word := range strings.FieldsFunc(path.Base(root), func(r rune) bool { return r == '[' || r == ']' || r == ' ' || r == '【' || r == '】' }) {
			if c := normalizeWorkCode(word); c != "" {
				code = c
				break
			}
		}
	}
	var id int64
	err := tx.QueryRowContext(ctx, `INSERT INTO source_catalog_entry(file_source_id,root_path,primary_code,title)
		VALUES(?,?,?,?) ON CONFLICT(file_source_id,root_path) DO UPDATE SET title=excluded.title,updated_at=CURRENT_TIMESTAMP
		RETURNING id`, sourceID, root, code, path.Base(root)).Scan(&id)
	if err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM source_catalog_file WHERE catalog_entry_id=?", id); err != nil {
		return 0, err
	}
	for _, file := range files {
		if _, err = tx.ExecContext(ctx, `INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES(?,?,?,?,?)`, id, file.Path, file.Title, file.Kind, file.Size); err != nil {
			return 0, err
		}
	}
	if err = attachCatalogSeriesTx(ctx, tx, id); err != nil {
		return 0, err
	}
	return id, nil
}

// loadSourceCatalogEntry is the shared original-text and source-read boundary.
// It does not register a work, resolve a media URL, or return private user state.
func (s *Server) loadSourceCatalogEntry(ctx context.Context, id int64) (catalogEntry, remoteSourceForUse, error) {
	var entry catalogEntry
	if s.cfg.IsDemo() {
		return entry, remoteSourceForUse{}, errRemoteWorkNotFound
	}
	err := s.db.QueryRowContext(ctx, `SELECT id,file_source_id,primary_code,title,root_path,work_id FROM source_catalog_entry WHERE id=? AND file_source_id IS NOT NULL`, id).
		Scan(&entry.ID, &entry.SourceID, &entry.PrimaryCode, &entry.Title, &entry.RootPath, &entry.WorkID)
	if err != nil {
		return entry, remoteSourceForUse{}, err
	}
	source, err := s.loadRemoteSourceForUse(ctx, entry.SourceID)
	if err == nil && (!source.Enabled || source.SourceType != sourceTypeOpenList) {
		err = errRemoteSourceNotUsable
	}
	if err == nil && entry.WorkID == nil && entry.PrimaryCode != "" {
		identity, resolveErr := s.resolveWorkCodeIdentity(ctx, entry.PrimaryCode)
		if resolveErr == nil {
			entry.WorkID = &identity.WorkID
		} else if !errors.Is(resolveErr, sql.ErrNoRows) {
			return entry, source, resolveErr
		}
	}
	return entry, source, err
}

func (s *Server) loadSourceCatalogFiles(ctx context.Context, id int64) ([]SourceCatalogFile, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT relative_path,title,kind,size_bytes FROM source_catalog_file WHERE catalog_entry_id=? ORDER BY relative_path`, id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	files := []SourceCatalogFile{}
	for rows.Next() {
		var file SourceCatalogFile
		if err = rows.Scan(&file.Path, &file.Title, &file.Kind, &file.Size); err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return files, rows.Err()
}

func (s *Server) getSourceCatalogEntry(w http.ResponseWriter, r *http.Request) {
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
	var rating sql.NullInt64
	note := ""
	if entry.WorkID != nil && optionalUserID(r.Context()) > 0 {
		err = s.db.QueryRowContext(r.Context(), `SELECT rating,note FROM user_work_state WHERE work_id=? AND user_id=?`, *entry.WorkID, optionalUserID(r.Context())).Scan(&rating, &note)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			writeError(w, err)
			return
		}
	}
	var personalRating any
	if rating.Valid {
		personalRating = rating.Int64
	}
	var seriesID *int64
	if err = s.db.QueryRowContext(r.Context(), `SELECT series_id FROM catalog_series_member WHERE catalog_entry_id=?`, entry.ID).Scan(&seriesID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"seriesId": seriesID, "catalogEntryId": entry.ID, "sourceId": entry.SourceID, "sourceName": source.DisplayName, "title": entry.Title, "primaryCode": entry.PrimaryCode, "workId": entry.WorkID, "personalRating": personalRating, "personalNote": note})
}

func (s *Server) getSourceCatalogTracks(w http.ResponseWriter, r *http.Request) {
	id, err := parseInt64PathValue(r, "id")
	if err != nil {
		writeAPIError(w, 400, "invalid_request", "invalid catalog id", false)
		return
	}
	if _, _, err = s.loadSourceCatalogEntry(r.Context(), id); err != nil {
		writeUpstreamError(w, err)
		return
	}
	files, err := s.loadSourceCatalogFiles(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"catalogEntryId": id, "tracks": catalogTracks(id, files, "")})
}

func catalogTracks(id int64, files []SourceCatalogFile, parent string) []map[string]any {
	tracks := []map[string]any{}
	for _, file := range files {
		dir := path.Dir(file.Path)
		if dir == "." {
			dir = ""
		}
		if dir != parent {
			continue
		}
		stream := ""
		if file.Kind == "audio" {
			stream = "/api/source-catalog/" + strconv.FormatInt(id, 10) + "/media?path=" + url.QueryEscape(file.Path)
		}
		children := []map[string]any{}
		if file.Kind == "folder" {
			children = catalogTracks(id, files, file.Path)
		}
		tracks = append(tracks, map[string]any{"type": file.Kind, "title": file.Title, "path": file.Path, "hash": file.Path, "streamUrl": stream, "downloadUrl": "", "sizeBytes": file.Size, "durationSeconds": nil, "children": children})
	}
	return tracks
}

func (s *Server) listOpenListWorks(w http.ResponseWriter, r *http.Request, source remoteSourceForUse) {
	if s.cfg.IsDemo() {
		writeAPIError(w, 404, "not_found", "source not found", false)
		return
	}
	if !source.Enabled {
		s.writeRemoteWorksDisabled(w, source.ID, r, "")
		return
	}
	page, pageSize := queryInt(r, "page", 1), queryInt(r, "pageSize", 24)
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 24
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	from := `FROM (
		SELECT e.id,0 AS series_id,e.primary_code,e.title,e.work_id,e.updated_at FROM source_catalog_entry e
		WHERE e.file_source_id=? AND NOT EXISTS(SELECT 1 FROM catalog_series_member m WHERE m.catalog_entry_id=e.id)
		AND (?='' OR instr(lower(e.title),lower(?))>0 OR instr(lower(e.primary_code),lower(?))>0)
		UNION ALL
		SELECT 0,s.id,'',s.title,s.work_id,s.created_at FROM catalog_series s
		WHERE EXISTS(SELECT 1 FROM catalog_series_member m JOIN source_catalog_entry e ON e.id=m.catalog_entry_id
		WHERE m.series_id=s.id AND e.file_source_id=? AND (?='' OR instr(lower(s.title),lower(?))>0 OR instr(lower(e.title),lower(?))>0 OR instr(lower(e.primary_code),lower(?))>0))
	) AS cards`
	args := []any{source.ID, q, q, q, source.ID, q, q, q, q}
	var total int
	if err := s.db.QueryRowContext(r.Context(), "SELECT COUNT(*) "+from, args...).Scan(&total); err != nil {
		writeError(w, err)
		return
	}
	rows, err := s.db.QueryContext(r.Context(), "SELECT id,series_id,primary_code,title,work_id,updated_at "+from+" ORDER BY updated_at DESC,series_id DESC,id DESC LIMIT ? OFFSET ?", append(args, pageSize, (page-1)*pageSize)...)
	if err != nil {
		writeError(w, err)
		return
	}
	works := []remoteWorkSummary{}
	for rows.Next() {
		var item remoteWorkSummary
		if err = rows.Scan(&item.CatalogEntryID, &item.SeriesID, &item.PrimaryCode, &item.Title, &item.WorkID, &item.UpdatedAt); err != nil {
			break
		}
		item.RemoteID = strconv.FormatInt(item.CatalogEntryID, 10)
		if item.SeriesID > 0 {
			item.RemoteID = "series:" + strconv.FormatInt(item.SeriesID, 10)
		}
		item.RemoteCode = item.PrimaryCode
		item.RemotePlayable = true
		item.ImportStatus = "remote"
		item.ListeningStatus = "none"
		item.Tags = []string{}
		item.VoiceActors = []string{}
		item.VoiceRefs = []remoteEntityRef{}
		if item.WorkID != nil {
			item.ImportStatus = "imported"
		}
		works = append(works, item)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, remoteWorksResponse{SourceID: source.ID, Works: works, Page: page, PageSize: pageSize, Total: total, Status: "ok", Sort: "updated", Direction: "desc", SortApplied: true})
}
