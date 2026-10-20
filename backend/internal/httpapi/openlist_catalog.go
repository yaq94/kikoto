package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sort"
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
	SourceRoot  string `json:"-"`
	WorkID      *int64 `json:"workId"`
	Status      string `json:"status"`
}
type SourceCatalogFile struct {
	Path       string `json:"path"`
	Title      string `json:"title"`
	Kind       string `json:"kind"`
	Size       int64  `json:"sizeBytes"`
	ModifiedAt string `json:"-"`
	Generation int64  `json:"-"`
}

func (s *Server) openListClient(source remoteSourceForUse) *openlist.Client {
	return configuredOpenListClient(source, s.sourceClient(source, 30*time.Second, sourceRequestOpenList))
}

// configuredOpenListClient applies the operator's request options to every
// OpenList client, including the full-index worker's own transport.
func configuredOpenListClient(source remoteSourceForUse, httpClient *http.Client) *openlist.Client {
	client := openlist.NewClient(source.Endpoint.APIURL, httpClient)
	if userAgent := strings.TrimSpace(source.Config.OpenListUserAgent); userAgent != "" {
		client.WithUserAgent(userAgent)
	}
	if len(source.Config.OpenListHeaders) > 0 {
		client.WithRequestHeaders(source.Config.OpenListHeaders)
	}
	return client
}

func validateOpenListConfig(config fileSourceConfig) error {
	if len(config.OpenListRoots) > 8 {
		return errors.New("openListRoots must contain at most 8 explicit work directories")
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
	return validateOpenListRequestConfig(config)
}

// validateOpenListRequestConfig enforces the operator request options. It runs
// against the raw submitted document, before normalization can drop an unsafe
// value, so a rejected configuration is explained instead of silently changed.
func validateOpenListRequestConfig(config fileSourceConfig) error {
	if userAgent := strings.TrimSpace(config.OpenListUserAgent); userAgent != "" {
		if len(userAgent) > 256 || !openlist.ValidHeaderValue(userAgent) {
			return errors.New("openListUserAgent must be one line of at most 256 characters")
		}
	}
	if len(config.OpenListHeaders) > maxOpenListRequestHeaders {
		return fmt.Errorf("openListHeaders must contain at most %d headers", maxOpenListRequestHeaders)
	}
	for name, value := range config.OpenListHeaders {
		canonical := http.CanonicalHeaderKey(strings.TrimSpace(name))
		if !openlist.ValidHeaderName(canonical) {
			return fmt.Errorf("openListHeaders contains an invalid header name")
		}
		if !openlist.AllowedConfiguredHeader(canonical) {
			return fmt.Errorf("openListHeaders cannot set header %s", canonical)
		}
		if !openlist.ValidHeaderValue(value) {
			return fmt.Errorf("openListHeaders value for %s must be one line of at most 512 characters", canonical)
		}
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
	if len(source.Config.OpenListRoots) == 0 {
		writeAPIError(w, 400, "invalid_request", "select work directories for a finite scan, or start the full index", false)
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
			if errors.Is(err, openlist.ErrChallenged) {
				outcome = "restricted"
			}
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
	mode, err := s.catalogSnapshotSplitMode(ctx, source.ID, root, files)
	if err != nil {
		return 0, err
	}
	groups, parent := catalogSplitSnapshot(root, files, mode)
	if len(groups) > 0 {
		return s.persistCatalogLooseWorkRoots(ctx, source, root, mode, groups, parent)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	id, err := persistOpenListSnapshotTx(ctx, tx, source.ID, root, "", path.Base(root), files)
	if err != nil {
		return 0, err
	}
	// A directory that used to be split can become one work (or lose an
	// episode). The current listing is a complete snapshot, so cards of this
	// root that it no longer publishes are stale.
	if err = markStaleSnapshotCardsTx(ctx, tx, source.ID, root, map[int64]bool{id: true}); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// persistCatalogLooseWorkRoots writes one card per prefix group or per file.
// The real directory stays in source_root so playback and refresh keep reading
// the source tree, while root_path becomes the card identity.
func (s *Server) persistCatalogLooseWorkRoots(ctx context.Context, source remoteSourceForUse, root, mode string, groups map[string][]openlist.File, parent []openlist.File) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	roots := make([]string, 0, len(groups))
	for groupRoot := range groups {
		roots = append(roots, groupRoot)
	}
	sort.Strings(roots)
	var first, firstSplit int64
	parentKept := false
	keep := map[int64]bool{}
	for _, groupRoot := range roots {
		title := strings.TrimPrefix(groupRoot, root+"::")
		files := groups[groupRoot]
		names := catalogDirectAudioNames(files)
		titles := catalogGroupTitles(newCatalogLooseGrouping(names, mode), names)
		if titles[title] != "" {
			title = titles[title]
		} else if len(files) > 0 {
			title = catalogFileStem(files[0].Path)
		}
		id, err := persistOpenListSnapshotTx(ctx, tx, source.ID, groupRoot, root, title, files)
		if err != nil {
			return 0, err
		}
		keep[id] = true
		if firstSplit == 0 {
			firstSplit = id
		}
	}
	if len(parent) > 0 {
		var id int64
		if id, err = persistOpenListSnapshotTx(ctx, tx, source.ID, root, "", path.Base(root), parent); err != nil {
			return 0, err
		}
		keep[id] = true
		if first == 0 {
			first = id
		}
		parentKept = true
	}
	// This scan published a complete snapshot of the root. Split cards from an
	// earlier snapshot that were not republished no longer describe a playable
	// group; leaving them visible would show works the source no longer has.
	if err = markStaleSnapshotCardsTx(ctx, tx, source.ID, root, keep); err != nil {
		return 0, err
	}
	if firstSplit != 0 && !parentKept {
		if err = retireCatalogSplitParentTx(ctx, tx, source.ID, root, firstSplit); err != nil {
			return 0, err
		}
	}
	if first == 0 {
		first = firstSplit
	}
	return first, tx.Commit()
}

func catalogDirectAudioNames(files []openlist.File) []string {
	names := []string{}
	for _, file := range files {
		if file.Kind == "audio" && !strings.Contains(file.Path, "/") {
			names = append(names, file.Path)
		}
	}
	return names
}

func persistOpenListSnapshotTx(ctx context.Context, tx *sql.Tx, sourceID int64, root, sourceRoot, title string, files []openlist.File) (int64, error) {
	// The same parser the resumable index uses: a code may sit in brackets, be
	// prefixed directly to a title, or be the whole folder name.
	code := catalogEntryCode(root)
	if title == "" {
		title = path.Base(root)
	}
	var id int64
	err := tx.QueryRowContext(ctx, `INSERT INTO source_catalog_entry(file_source_id,root_path,source_root,primary_code,title)
				VALUES(?,?,?,?,?) ON CONFLICT(file_source_id,root_path) DO UPDATE SET source_root=excluded.source_root,primary_code=excluded.primary_code,title=excluded.title,availability_status='available',status_checked_at=CURRENT_TIMESTAMP,content_fingerprint='',layout_fingerprint='',updated_at=CURRENT_TIMESTAMP
		RETURNING id`, sourceID, root, sourceRoot, code, title).Scan(&id)
	if err != nil {
		return 0, err
	}
	// Files for a split card are replaced wholesale. The caller passes only the
	// rows that belong to this card, never the whole creator directory.
	if _, err = tx.ExecContext(ctx, "DELETE FROM source_catalog_file WHERE catalog_entry_id=?", id); err != nil {
		return 0, err
	}
	for _, file := range files {
		if _, err = tx.ExecContext(ctx, `INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES(?,?,?,?,?)`, id, file.Path, file.Title, file.Kind, file.Size); err != nil {
			return 0, err
		}
	}
	if sourceRoot == "" {
		// A split card is already one work; do not let a file title become
		// episode evidence for a new series.
		if err = attachCatalogSeriesTx(ctx, tx, id); err != nil {
			return 0, err
		}
	}
	if err = setCatalogEntryStatusTx(ctx, tx, id, "available"); err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM source_catalog_relocation_candidate WHERE catalog_entry_id=?`, id); err != nil {
		return 0, err
	}
	return id, nil
}

// loadSourceCatalogEntry is the shared original-text and source-read boundary.
// It does not register a work, resolve a media URL, or return private user state.
func (s *Server) loadSourceCatalogEntry(ctx context.Context, id int64) (catalogEntry, remoteSourceForUse, error) {
	entry, source, err := s.loadRetainedSourceCatalogEntry(ctx, id)
	if err == nil && (!source.Enabled || source.SourceType != sourceTypeOpenList) {
		err = errRemoteSourceNotUsable
	}
	return entry, source, err
}

func (s *Server) loadSourceCatalogFiles(ctx context.Context, id int64) ([]SourceCatalogFile, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT relative_path,title,kind,size_bytes,modified_at,observed_generation FROM source_catalog_file WHERE catalog_entry_id=? ORDER BY relative_path`, id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	files := []SourceCatalogFile{}
	for rows.Next() {
		var file SourceCatalogFile
		if err = rows.Scan(&file.Path, &file.Title, &file.Kind, &file.Size, &file.ModifiedAt, &file.Generation); err != nil {
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
	entry, source, err := s.loadRetainedSourceCatalogEntry(r.Context(), id)
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
	// Membership normally lives on the surviving card. Historical aliases may
	// still own it after a rename, so follow every alias of this successor
	// instead of losing the "open series" path from an old link.
	if err = s.db.QueryRowContext(r.Context(), `WITH RECURSIVE aliases(id) AS (
		SELECT ?
		UNION ALL
		SELECT e.id FROM source_catalog_entry e JOIN aliases a ON e.relocated_to_id=a.id
	)
	SELECT series_id FROM catalog_series_member WHERE catalog_entry_id IN (SELECT id FROM aliases) LIMIT 1`, entry.ID).Scan(&seriesID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeError(w, err)
		return
	}
	candidates, err := s.catalogRelocationCandidates(r.Context(), entry.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"seriesId": seriesID, "catalogEntryId": entry.ID, "sourceId": entry.SourceID, "sourceName": source.DisplayName, "title": entry.Title, "primaryCode": entry.PrimaryCode, "workId": entry.WorkID, "personalRating": personalRating, "personalNote": note, "status": entry.Status, "relocationCandidates": candidates})
}

func (s *Server) getSourceCatalogTracks(w http.ResponseWriter, r *http.Request) {
	id, err := parseInt64PathValue(r, "id")
	if err != nil {
		writeAPIError(w, 400, "invalid_request", "invalid catalog id", false)
		return
	}
	entry, _, err := s.loadRetainedSourceCatalogEntry(r.Context(), id)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	id = entry.ID
	files, err := s.loadSourceCatalogFiles(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"catalogEntryId": id, "tracks": catalogTracks(id, files, "")})
}

func catalogTracks(id int64, files []SourceCatalogFile, parent string) []map[string]any {
	// A split card owns a work's direct audio and its nested format copies, but
	// the index only publishes the audio rows for that card. Build the tree
	// from paths and synthesize a missing intermediate folder, so a copy under
	// FLAC/ or MP3/ is visible instead of being dropped by the parent filter.
	type node struct {
		file     SourceCatalogFile
		children map[string]*node
		order    []string
	}
	root := &node{children: map[string]*node{}}
	ensure := func(segments []string) *node {
		current := root
		for _, segment := range segments {
			child, ok := current.children[segment]
			if !ok {
				child = &node{file: SourceCatalogFile{Path: segment, Title: segment, Kind: "folder"}, children: map[string]*node{}}
				if current.file.Path != "" {
					child.file.Path = current.file.Path + "/" + segment
				}
				current.children[segment] = child
				current.order = append(current.order, segment)
			}
			current = child
		}
		return current
	}
	for _, file := range files {
		segments := strings.Split(strings.Trim(file.Path, "/"), "/")
		if len(segments) == 0 {
			continue
		}
		parentNode := ensure(segments[:len(segments)-1])
		name := segments[len(segments)-1]
		existing, ok := parentNode.children[name]
		if !ok {
			existing = &node{children: map[string]*node{}}
			parentNode.children[name] = existing
			parentNode.order = append(parentNode.order, name)
		}
		existing.file = file
		if existing.file.Path == "" {
			existing.file.Path = strings.Join(segments, "/")
		}
	}
	var render func(n *node) []map[string]any
	render = func(n *node) []map[string]any {
		tracks := make([]map[string]any, 0, len(n.order))
		for _, name := range n.order {
			child := n.children[name]
			file := child.file
			stream := ""
			if file.Kind == "audio" {
				stream = "/api/source-catalog/" + strconv.FormatInt(id, 10) + "/media?path=" + url.QueryEscape(file.Path)
			}
			children := []map[string]any{}
			if len(child.order) > 0 {
				children = render(child)
			}
			tracks = append(tracks, map[string]any{"type": file.Kind, "title": file.Title, "path": file.Path, "hash": file.Path, "streamUrl": stream, "downloadUrl": "", "sizeBytes": file.Size, "durationSeconds": nil, "children": children})
		}
		return tracks
	}
	return render(root)
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
	from := `FROM (SELECT candidates.*,ROW_NUMBER() OVER (PARTITION BY CASE WHEN work_id IS NOT NULL THEN 'work:' || (SELECT identity_work_id FROM shared_work_identity WHERE work_id=candidates.work_id) WHEN series_id>0 THEN 'series:' || series_id ELSE 'catalog:' || id END ORDER BY series_id,id) AS identity_rank FROM (
		SELECT e.id,0 AS series_id,e.primary_code,e.title,e.work_id,e.updated_at,e.availability_status,
			EXISTS(SELECT 1 FROM source_catalog_file f WHERE f.catalog_entry_id=e.id AND f.kind='audio') AS has_audio FROM source_catalog_entry e
		WHERE e.file_source_id=? AND e.relocated_to_id IS NULL AND NOT EXISTS(SELECT 1 FROM catalog_series_member m WHERE m.catalog_entry_id=e.id AND (SELECT COUNT(*) FROM catalog_series_member sibling WHERE sibling.series_id=m.series_id)>=2)
			AND EXISTS(SELECT 1 FROM source_catalog_file f WHERE f.catalog_entry_id=e.id AND f.kind='audio')
		AND (?='' OR instr(lower(e.title),lower(?))>0 OR instr(lower(e.primary_code),lower(?))>0
			OR EXISTS(SELECT 1 FROM source_catalog_file f WHERE f.catalog_entry_id=e.id AND instr(lower(f.title),lower(?))>0))
		UNION ALL
		SELECT 0,s.id,'',s.title,s.work_id,s.created_at,CASE WHEN EXISTS(
			SELECT 1 FROM catalog_series_member mm JOIN source_catalog_entry ee ON ee.id=mm.catalog_entry_id
			JOIN source_catalog_file ff ON ff.catalog_entry_id=ee.id AND ff.kind='audio'
			WHERE mm.series_id=s.id AND ee.relocated_to_id IS NULL AND ee.availability_status='available'
		) THEN 'available' ELSE 'missing' END,EXISTS(
			SELECT 1 FROM catalog_series_member mm JOIN source_catalog_entry ee ON ee.id=mm.catalog_entry_id
			JOIN source_catalog_file ff ON ff.catalog_entry_id=ee.id AND ff.kind='audio'
			WHERE mm.series_id=s.id AND ee.relocated_to_id IS NULL
		) AS has_audio FROM catalog_series s
		WHERE (SELECT COUNT(*) FROM catalog_series_member all_members WHERE all_members.series_id=s.id)>=2
		AND EXISTS(SELECT 1 FROM catalog_series_member m JOIN source_catalog_entry e ON e.id=m.catalog_entry_id
		WHERE m.series_id=s.id AND e.file_source_id=? AND e.relocated_to_id IS NULL
			AND (?='' OR instr(lower(s.title),lower(?))>0 OR instr(lower(e.title),lower(?))>0 OR instr(lower(e.primary_code),lower(?))>0
			OR EXISTS(SELECT 1 FROM source_catalog_file f WHERE f.catalog_entry_id=e.id AND instr(lower(f.title),lower(?))>0)))
	) AS candidates) AS cards WHERE identity_rank=1`
	args := []any{source.ID, q, q, q, q, source.ID, q, q, q, q, q}
	var total int
	if err := s.db.QueryRowContext(r.Context(), "SELECT COUNT(*) "+from, args...).Scan(&total); err != nil {
		writeError(w, err)
		return
	}
	rows, err := s.db.QueryContext(r.Context(), "SELECT id,series_id,primary_code,title,work_id,updated_at,availability_status,has_audio "+from+" ORDER BY updated_at DESC,series_id DESC,id DESC LIMIT ? OFFSET ?", append(args, pageSize, (page-1)*pageSize)...)
	if err != nil {
		writeError(w, err)
		return
	}
	works := []remoteWorkSummary{}
	for rows.Next() {
		var item remoteWorkSummary
		var availability string
		var hasAudio bool
		if err = rows.Scan(&item.CatalogEntryID, &item.SeriesID, &item.PrimaryCode, &item.Title, &item.WorkID, &item.UpdatedAt, &availability, &hasAudio); err != nil {
			break
		}
		item.RemoteID = strconv.FormatInt(item.CatalogEntryID, 10)
		if item.SeriesID > 0 {
			item.RemoteID = "series:" + strconv.FormatInt(item.SeriesID, 10)
		}
		item.RemoteCode = item.PrimaryCode
		// A retained card still has its identity and personal records, but a
		// completed scan that found the source missing must not advertise
		// playback. An unfinished scan keeps the last known availability.
		item.ImportStatus = "remote"
		item.ListeningStatus = "none"
		item.Tags = []string{}
		item.VoiceActors = []string{}
		item.VoiceRefs = []remoteEntityRef{}
		if item.WorkID != nil {
			item.ImportStatus = "imported"
		}
		// A retained card still has its identity and personal records, but a
		// scan that lost its files must not advertise playback.
		item.RemotePlayable = availability == "available" && hasAudio
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
