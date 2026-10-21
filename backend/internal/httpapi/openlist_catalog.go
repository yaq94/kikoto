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
	"unicode/utf8"

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
	query := r.URL.Query()
	_, hasPath := query["path"]
	_, hasDepth := query["depth"]
	_, hasLimit := query["limit"]
	// Callers that send no track parameters keep the historical complete tree.
	// The scan below walks it in keyset batches so a card is never materialised
	// as one file slice plus one tree at the same time.
	if !hasPath && !hasDepth && !hasLimit {
		builder := newCatalogTrackBuilder(id, "", 0)
		cursor := ""
		for {
			files, more, err := s.loadSourceCatalogFileBatch(r.Context(), id, cursor, 2000)
			if err != nil {
				writeError(w, err)
				return
			}
			for _, file := range files {
				builder.add(file)
			}
			if !more {
				break
			}
			cursor = files[len(files)-1].Path
		}
		writeJSON(w, 200, map[string]any{"catalogEntryId": id, "tracks": builder.render()})
		return
	}

	root := strings.Trim(strings.TrimSpace(query.Get("path")), "/")
	depth := queryInt(r, "depth", 1)
	if depth > maxCatalogTrackDepth {
		depth = maxCatalogTrackDepth
	}
	explicitLimit := queryInt(r, "limit", 0)
	limit := explicitLimit
	if limit <= 0 {
		limit = catalogTrackDefaultLimit
	}
	if limit > catalogTrackLimitCap {
		limit = catalogTrackLimitCap
	}
	total, err := s.countSourceCatalogFiles(r.Context(), id, root)
	if err != nil {
		writeError(w, err)
		return
	}
	// A large card without an explicit limit is a summary: a short preview plus
	// the total. The UI expands a folder by asking for its path.
	summary := false
	if explicitLimit == 0 && total > catalogTrackSummaryThreshold {
		limit = catalogTrackSummaryPreview
		summary = true
	}
	files, truncated, err := s.loadSourceCatalogTrackPage(r.Context(), id, root, depth, limit)
	if err != nil {
		writeError(w, err)
		return
	}
	builder := newCatalogTrackBuilder(id, root, depth)
	returned := 0
	for _, file := range files {
		builder.add(file)
		if file.Kind != "folder" {
			returned++
		}
	}
	writeJSON(w, 200, map[string]any{
		"catalogEntryId": id,
		"path":           root,
		"depth":          depth,
		"tracks":         builder.render(),
		"total":          total,
		"returned":       returned,
		"truncated":      truncated || returned < total,
		"summary":        summary,
	})
}

const (
	catalogTrackDefaultLimit     = 5000
	catalogTrackLimitCap         = 20000
	catalogTrackSummaryThreshold = 5000
	catalogTrackSummaryPreview   = 200
	maxCatalogTrackDepth         = 8
)

func (s *Server) loadSourceCatalogFileBatch(ctx context.Context, id int64, after string, limit int) ([]SourceCatalogFile, bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT relative_path,title,kind,size_bytes,modified_at,observed_generation
		FROM source_catalog_file WHERE catalog_entry_id=? AND relative_path>? ORDER BY relative_path LIMIT ?`, id, after, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = rows.Close() }()
	files := make([]SourceCatalogFile, 0, limit+1)
	for rows.Next() {
		var file SourceCatalogFile
		if err = rows.Scan(&file.Path, &file.Title, &file.Kind, &file.Size, &file.ModifiedAt, &file.Generation); err != nil {
			return nil, false, err
		}
		files = append(files, file)
	}
	if err = rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(files) > limit
	if more {
		files = files[:limit]
	}
	return files, more, nil
}

func (s *Server) countSourceCatalogFiles(ctx context.Context, id int64, root string) (int, error) {
	where, args := catalogTrackPathFilter(root)
	var total int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM source_catalog_file INDEXED BY source_catalog_file_parent WHERE catalog_entry_id=? AND kind<>'folder'`+where,
		append([]any{id}, args...)...).Scan(&total)
	return total, err
}

func (s *Server) loadSourceCatalogTrackPage(ctx context.Context, id int64, root string, depth, limit int) ([]SourceCatalogFile, bool, error) {
	where, args := catalogTrackPathFilter(root)
	if depth == 1 {
		// parent_path is generated from relative_path and indexed, so the
		// first level reads exactly one level of rows instead of the card.
		where += " AND parent_path=?"
		args = append(args, root)
	} else {
		where += " AND (length(relative_path)-length(replace(relative_path,'/','')))<=?"
		maxSlashes := depth - 1
		if root != "" {
			maxSlashes = strings.Count(root, "/") + depth
		}
		args = append(args, maxSlashes)
	}
	queryArgs := append([]any{id}, args...)
	queryArgs = append(queryArgs, limit+1)
	rows, err := s.db.QueryContext(ctx, `SELECT relative_path,title,kind,size_bytes,modified_at,observed_generation
		FROM source_catalog_file INDEXED BY source_catalog_file_parent WHERE catalog_entry_id=?`+where+` ORDER BY relative_path LIMIT ?`, queryArgs...)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = rows.Close() }()
	files := make([]SourceCatalogFile, 0, limit+1)
	for rows.Next() {
		var file SourceCatalogFile
		if err = rows.Scan(&file.Path, &file.Title, &file.Kind, &file.Size, &file.ModifiedAt, &file.Generation); err != nil {
			return nil, false, err
		}
		files = append(files, file)
	}
	if err = rows.Err(); err != nil {
		return nil, false, err
	}
	truncated := len(files) > limit
	if truncated {
		files = files[:limit]
	}
	return files, truncated, nil
}

// catalogTrackPathFilter restricts rows to the subtree below root. Paths sort
// in byte order, so path+"/" through path+"0" is the exact prefix range.
func catalogTrackPathFilter(root string) (string, []any) {
	if root == "" {
		return "", nil
	}
	return " AND relative_path>=? AND relative_path<?", []any{root + "/", root + "0"}
}

type catalogTrackNode struct {
	file     SourceCatalogFile
	children map[string]*catalogTrackNode
	order    []string
}

type catalogTrackBuilder struct {
	id    int64
	root  string
	depth int
	node  *catalogTrackNode
}

func newCatalogTrackBuilder(id int64, root string, depth int) *catalogTrackBuilder {
	return &catalogTrackBuilder{
		id:    id,
		root:  strings.Trim(root, "/"),
		depth: depth,
		node:  &catalogTrackNode{children: map[string]*catalogTrackNode{}},
	}
}

func (b *catalogTrackBuilder) add(file SourceCatalogFile) {
	relative := strings.Trim(file.Path, "/")
	if b.root != "" {
		if relative == b.root {
			return
		}
		if !strings.HasPrefix(relative, b.root+"/") {
			return
		}
		relative = strings.TrimPrefix(relative, b.root+"/")
	}
	if relative == "" {
		return
	}
	segments := strings.Split(relative, "/")
	truncated := false
	if b.depth > 0 && len(segments) > b.depth {
		segments = segments[:b.depth]
		truncated = true
	}
	current := b.node
	absolute := b.root
	for index, segment := range segments {
		if segment == "" {
			continue
		}
		if absolute == "" {
			absolute = segment
		} else {
			absolute += "/" + segment
		}
		child, ok := current.children[segment]
		if !ok {
			child = &catalogTrackNode{
				file:     SourceCatalogFile{Path: absolute, Title: segment, Kind: "folder"},
				children: map[string]*catalogTrackNode{},
			}
			current.children[segment] = child
			current.order = append(current.order, segment)
		}
		if index == len(segments)-1 {
			if truncated {
				child.file = SourceCatalogFile{Path: absolute, Title: segment, Kind: "folder"}
				child.children = map[string]*catalogTrackNode{}
				child.order = nil
			} else {
				child.file = file
				child.file.Path = strings.Trim(file.Path, "/")
			}
		}
		current = child
	}
}

func (b *catalogTrackBuilder) render() []map[string]any {
	var render func(*catalogTrackNode) []map[string]any
	render = func(n *catalogTrackNode) []map[string]any {
		tracks := make([]map[string]any, 0, len(n.order))
		for _, name := range n.order {
			child := n.children[name]
			file := child.file
			stream := ""
			if file.Kind == "audio" {
				stream = "/api/source-catalog/" + strconv.FormatInt(b.id, 10) + "/media?path=" + url.QueryEscape(file.Path)
			}
			children := []map[string]any{}
			if len(child.order) > 0 {
				children = render(child)
			}
			tracks = append(tracks, map[string]any{
				"type": file.Kind, "title": file.Title, "path": file.Path, "hash": file.Path,
				"streamUrl": stream, "downloadUrl": "", "sizeBytes": file.Size,
				"durationSeconds": nil, "children": children,
			})
		}
		return tracks
	}
	return render(b.node)
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
	prefix, from, args := openListCatalogCardsQuery(source.ID, q)
	rows, err := s.db.QueryContext(r.Context(), prefix+`SELECT id,series_id,primary_code,title,work_id,updated_at,availability_status,has_audio,COUNT(*) OVER () AS total `+from+` ORDER BY updated_at DESC,series_id DESC,id DESC LIMIT ? OFFSET ?`, append(args, pageSize, (page-1)*pageSize)...)
	if err != nil {
		writeError(w, err)
		return
	}
	works := []remoteWorkSummary{}
	total := 0
	for rows.Next() {
		var item remoteWorkSummary
		var availability string
		var hasAudio bool
		if err = rows.Scan(&item.CatalogEntryID, &item.SeriesID, &item.PrimaryCode, &item.Title, &item.WorkID, &item.UpdatedAt, &availability, &hasAudio, &total); err != nil {
			break
		}
		item.RemoteID = strconv.FormatInt(item.CatalogEntryID, 10)
		if item.SeriesID > 0 {
			item.RemoteID = "series:" + strconv.FormatInt(item.SeriesID, 10)
		}
		item.RemoteCode = item.PrimaryCode
		item.ImportStatus = "remote"
		item.ListeningStatus = "none"
		item.Tags = []string{}
		item.VoiceActors = []string{}
		item.VoiceRefs = []remoteEntityRef{}
		if item.WorkID != nil {
			item.ImportStatus = "imported"
		}
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
	if len(works) == 0 && page > 1 {
		// COUNT(*) OVER () rides on the page. An out-of-range page has no row
		// to carry it, so only that page pays for a separate count.
		countPrefix, countFrom, countArgs := openListCatalogCardsQuery(source.ID, q)
		if err = s.db.QueryRowContext(r.Context(), countPrefix+"SELECT COUNT(*) "+countFrom, countArgs...).Scan(&total); err != nil {
			writeError(w, err)
			return
		}
	}
	writeJSON(w, 200, remoteWorksResponse{SourceID: source.ID, Works: works, Page: page, PageSize: pageSize, Total: total, Status: "ok", Sort: "updated", Direction: "desc", SortApplied: true})
}

// openListCatalogCardsQuery builds the deduplicated card source once. Empty
// search omits both the FTS CTE and the per-file instr predicates, so browsing
// without a query never touches file titles.
func openListCatalogCardsQuery(sourceID int64, query string) (string, string, []any) {
	query = strings.TrimSpace(query)
	prefix := ""
	args := []any{}
	entrySearch := ""
	entryArgs := []any{}
	seriesSearch := ""
	seriesArgs := []any{}
	if query != "" {
		if utf8.RuneCountInString(query) >= 3 {
			prefix = `WITH search_hits AS MATERIALIZED (
	SELECT d.catalog_entry_id, d.series_id
	FROM source_catalog_search s JOIN source_catalog_search_doc d ON d.id=s.rowid
	WHERE source_catalog_search MATCH ? AND (d.entity_kind='series' OR d.file_source_id=?)
) `
			args = append(args, ftsPhrase(query), sourceID)
			entrySearch = ` AND (e.id IN (SELECT catalog_entry_id FROM search_hits WHERE catalog_entry_id<>0)
	OR EXISTS(SELECT 1 FROM search_hits sh JOIN catalog_series_member sm ON sm.series_id=sh.series_id WHERE sh.series_id<>0 AND sm.catalog_entry_id=e.id))`
			seriesSearch = ` AND (s.id IN (SELECT series_id FROM search_hits WHERE series_id<>0)
	OR EXISTS(SELECT 1 FROM search_hits sh JOIN catalog_series_member sm ON sm.catalog_entry_id=sh.catalog_entry_id WHERE sh.catalog_entry_id<>0 AND sm.series_id=s.id))`
		} else {
			// A trigram needs three characters. One- and two-character queries
			// keep the old substring behaviour rather than silently matching
			// nothing.
			entrySearch = ` AND (instr(lower(e.title),lower(?))>0 OR instr(lower(e.primary_code),lower(?))>0
	OR EXISTS(SELECT 1 FROM source_catalog_file f WHERE f.catalog_entry_id=e.id AND instr(lower(f.title),lower(?))>0))`
			entryArgs = []any{query, query, query}
			seriesSearch = ` AND (instr(lower(s.title),lower(?))>0 OR EXISTS(
	SELECT 1 FROM catalog_series_member m JOIN source_catalog_entry se ON se.id=m.catalog_entry_id
	WHERE m.series_id=s.id AND (instr(lower(se.title),lower(?))>0 OR instr(lower(se.primary_code),lower(?))>0
	OR EXISTS(SELECT 1 FROM source_catalog_file sf WHERE sf.catalog_entry_id=se.id AND instr(lower(sf.title),lower(?))>0))))`
			seriesArgs = []any{query, query, query, query, query}
		}
	}
	from := `FROM (SELECT candidates.*,ROW_NUMBER() OVER (PARTITION BY CASE WHEN work_id IS NOT NULL THEN 'work:' || (SELECT identity_work_id FROM shared_work_identity WHERE work_id=candidates.work_id) WHEN series_id>0 THEN 'series:' || series_id ELSE 'catalog:' || id END ORDER BY series_id,id) AS identity_rank FROM (
		SELECT e.id,0 AS series_id,e.primary_code,e.title,e.work_id,e.updated_at,e.availability_status,
			EXISTS(SELECT 1 FROM source_catalog_file f WHERE f.catalog_entry_id=e.id AND f.kind='audio') AS has_audio FROM source_catalog_entry e INDEXED BY source_catalog_entry_live_root
		WHERE e.file_source_id=? AND e.relocated_to_id IS NULL AND NOT EXISTS(SELECT 1 FROM catalog_series_member m WHERE m.catalog_entry_id=e.id AND (SELECT COUNT(*) FROM catalog_series_member sibling WHERE sibling.series_id=m.series_id)>=2)
			AND EXISTS(SELECT 1 FROM source_catalog_file f WHERE f.catalog_entry_id=e.id AND f.kind='audio')` + entrySearch + `
		UNION ALL
		SELECT 0,s.id,'',s.title,s.work_id,s.created_at,CASE WHEN EXISTS(
			SELECT 1 FROM catalog_series_member mm JOIN source_catalog_entry ee ON ee.id=mm.catalog_entry_id
			JOIN source_catalog_file ff INDEXED BY source_catalog_file_parent ON ff.catalog_entry_id=ee.id AND ff.kind='audio'
			WHERE mm.series_id=s.id AND ee.relocated_to_id IS NULL AND ee.availability_status='available'
		) THEN 'available' ELSE 'missing' END,EXISTS(
			SELECT 1 FROM catalog_series_member mm JOIN source_catalog_entry ee ON ee.id=mm.catalog_entry_id
			JOIN source_catalog_file ff INDEXED BY source_catalog_file_parent ON ff.catalog_entry_id=ee.id AND ff.kind='audio'
			WHERE mm.series_id=s.id AND ee.relocated_to_id IS NULL
		) AS has_audio FROM catalog_series s
		WHERE (SELECT COUNT(*) FROM catalog_series_member all_members WHERE all_members.series_id=s.id)>=2
		AND EXISTS(SELECT 1 FROM catalog_series_member m JOIN source_catalog_entry e ON e.id=m.catalog_entry_id
		WHERE m.series_id=s.id AND e.file_source_id=? AND e.relocated_to_id IS NULL` + seriesSearch + `)
	) AS candidates) AS cards WHERE identity_rank=1`
	args = append(args, sourceID)
	args = append(args, entryArgs...)
	args = append(args, sourceID)
	args = append(args, seriesArgs...)
	return prefix, from, args
}

func ftsPhrase(query string) string {
	return `"` + strings.ReplaceAll(query, `"`, `""`) + `"`
}
