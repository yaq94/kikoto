package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yexca/kikoto/backend/internal/library"
	"github.com/yexca/kikoto/backend/internal/openlist"
	"github.com/yexca/kikoto/backend/internal/translation"
	"github.com/yexca/kikoto/backend/internal/workcode"
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

// scanOpenListCatalog uses the durable listing queue for explicit roots;
// it never materializes the whole tree in memory before applying a snapshot.
func (s *Server) scanOpenListCatalog(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePermission(w, r, "sources:write"); !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	id, err := parseInt64PathValue(r, "id")
	if err != nil {
		writeAPIError(w, 400, "invalid_request", "invalid source id", false)
		return
	}
	release, err := s.acquireCatalogRefresh(ctx, id)
	if err != nil {
		if errors.Is(err, errCatalogRefreshBusy) {
			writeAPIError(w, http.StatusConflict, "catalog_busy", err.Error(), true)
		} else {
			writeUpstreamError(w, err)
		}
		return
	}
	defer release()
	source, err := s.loadRemoteSourceForUse(ctx, id)
	if err != nil {
		writeError(w, err)
		return
	}
	if !source.Enabled || source.SourceType != sourceTypeOpenList {
		writeUpstreamError(w, errRemoteSourceNotUsable)
		return
	}
	if err = validateOpenListConfig(source.Config); err != nil {
		writeAPIError(w, 400, "invalid_request", err.Error(), false)
		return
	}
	if len(source.Config.OpenListRoots) == 0 {
		writeAPIError(w, 400, "invalid_request", "select work directories for a finite scan, or start the full index", false)
		return
	}
	ctx, err = s.catalogFiniteContext(ctx, source.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	results := []map[string]any{}
	status := "complete"
	roots := slices.Clone(source.Config.OpenListRoots)
	var owner string
	err = s.db.QueryRowContext(ctx, `SELECT owner FROM source_catalog_publication WHERE file_source_id=? AND state='preparing'`, source.ID).Scan(&owner)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeError(w, err)
		return
	}
	if err == nil {
		// One source has one pending publication. Resume its configured root
		// before starting another root, including after a process restart.
		pending, manual := strings.CutPrefix(owner, "manual:")
		index := slices.Index(roots, pending)
		if !manual || index < 0 {
			writeAPIError(w, http.StatusConflict, "catalog_busy", "resume the pending catalog operation or restore its configured root before scanning", true)
			return
		}
		copy(roots[1:index+1], roots[:index])
		roots[0] = pending
	}
	for _, root := range roots {
		listingID, scanErr := s.stageCatalogFiniteListing(ctx, source, root)
		if ctx.Err() != nil {
			writeAPIError(w, 503, "service_unavailable", "catalog scan cancelled or timed out", true)
			return
		}
		if scanErr != nil {
			status = "partial"
			outcome := "failed"
			if errors.Is(scanErr, openlist.ErrChallenged) || errors.Is(scanErr, openlist.ErrRestricted) {
				outcome = "restricted"
			}
			if errors.Is(scanErr, openlist.ErrLimit) {
				outcome = "limit_exceeded"
			}
			results = append(results, map[string]any{"root": root, "status": outcome})
			continue
		}
		ctx, op, beginErr := s.beginCatalogPublication(ctx, source.ID, "manual:"+root)
		if beginErr != nil {
			writeError(w, beginErr)
			return
		}
		scanErr = s.applyCatalogFiniteListing(ctx, source, root, listingID, op)
		if errors.Is(scanErr, errCatalogNoPlayableFiles) {
			if _, scanErr = s.execCatalogWrite(ctx, `UPDATE source_catalog_listing SET state='applied' WHERE id=?`, listingID); scanErr != nil {
				writeError(w, scanErr)
				return
			}
			if scanErr = s.cleanupCatalogFiniteTargets(ctx, listingID); scanErr != nil {
				writeError(w, scanErr)
				return
			}
			results = append(results, map[string]any{"root": root, "status": "excluded"})
			if scanErr = s.commitCatalogPublication(ctx, op); scanErr != nil {
				writeError(w, scanErr)
				return
			}
			continue
		}
		if scanErr != nil {
			writeError(w, scanErr)
			return
		}
		if scanErr = s.commitCatalogPublication(ctx, op); scanErr != nil {
			writeError(w, scanErr)
			return
		}
		var entryID int64
		_ = s.db.QueryRowContext(ctx, `SELECT id FROM source_catalog_entry WHERE file_source_id=? AND (root_path=? OR source_root=?) ORDER BY root_path LIMIT 1`, source.ID, root, root).Scan(&entryID)
		results = append(results, map[string]any{"root": root, "status": "indexed", "catalogEntryId": entryID})
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": status, "roots": results})
}

func (s *Server) persistOpenListRoot(ctx context.Context, source remoteSourceForUse, root string, files []openlist.File) (result int64, resultErr error) {
	var publicationID int64
	var err error
	_, joined := ctx.Value(catalogPublicationKey{}).(int64)
	if !joined {
		ctx, publicationID, err = s.beginCatalogPublication(ctx, source.ID, "manual:"+root)
		if err != nil {
			return 0, err
		}
	}
	defer func() {
		if resultErr == nil && !joined {
			resultErr = s.commitCatalogPublication(ctx, publicationID)
		}
	}()
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
	id, err := s.persistOpenListSnapshot(ctx, source.ID, root, "", path.Base(root), files)
	if err != nil {
		return 0, err
	}
	if err = s.markStaleSnapshotCards(ctx, source.ID, root, map[int64]bool{id: true}); err != nil {
		return 0, err
	}
	return id, nil
}

// persistCatalogLooseWorkRoots writes one card per prefix group or per file.
// The real directory stays in source_root so playback and refresh keep reading
// the source tree, while root_path becomes the card identity.
func (s *Server) persistCatalogLooseWorkRoots(ctx context.Context, source remoteSourceForUse, root, mode string, groups map[string][]openlist.File, parent []openlist.File) (int64, error) {
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
		id, err := s.persistOpenListSnapshot(ctx, source.ID, groupRoot, root, title, files)
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
		var err error
		if id, err = s.persistOpenListSnapshot(ctx, source.ID, root, "", path.Base(root), parent); err != nil {
			return 0, err
		}
		keep[id] = true
		if first == 0 {
			first = id
		}
		parentKept = true
	}
	if err := s.markStaleSnapshotCards(ctx, source.ID, root, keep); err != nil {
		return 0, err
	}
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return 0, err
	}
	defer release()
	if firstSplit != 0 && !parentKept {
		if err = retireCatalogSplitParentTx(ctx, tx.Tx, source.ID, root, firstSplit); err != nil {
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

const openListEntryUpsertSQL = `INSERT INTO source_catalog_entry(file_source_id,root_path,source_root,primary_code,title)
	VALUES(?,?,?,?,?) ON CONFLICT(file_source_id,root_path) DO UPDATE SET source_root=excluded.source_root,primary_code=excluded.primary_code,title=excluded.title,availability_status='available',status_checked_at=CURRENT_TIMESTAMP,content_fingerprint='',layout_fingerprint='',updated_at=CURRENT_TIMESTAMP
	RETURNING id`

func openListEntryArgs(sourceID any, root, sourceRoot, title string) []any {
	// The same parser the resumable index uses: a code may sit in brackets, be
	// prefixed directly to a title, or be the whole folder name.
	code := catalogEntryCode(root)
	if title == "" {
		title = path.Base(root)
	}
	return []any{sourceID, root, sourceRoot, code, title}
}

func persistOpenListEntryTx(ctx context.Context, tx *sql.Tx, sourceID any, root, sourceRoot, title string) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx, openListEntryUpsertSQL, openListEntryArgs(sourceID, root, sourceRoot, title)...).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, nil
}

// A manual snapshot is the card's current published file set, so it carries
// the same generation as the entry. Admission compares the two; leaving the
// default zero here would make a refreshed card look fileless.
const openListFileUpsertSQL = `INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes,observed_generation)
	VALUES(?,?,?,?,?,(SELECT last_seen_generation FROM source_catalog_entry WHERE id=?)) ON CONFLICT(catalog_entry_id,relative_path) DO UPDATE SET
	title=excluded.title,kind=excluded.kind,size_bytes=excluded.size_bytes,observed_generation=excluded.observed_generation`

func openListFileArgs(entryID int64, file openlist.File) []any {
	return []any{entryID, file.Path, file.Title, file.Kind, file.Size, entryID}
}

func persistOpenListFileTx(ctx context.Context, tx *sql.Tx, entryID int64, file openlist.File) error {
	_, err := tx.ExecContext(ctx, openListFileUpsertSQL, openListFileArgs(entryID, file)...)
	return err
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
	rows, err := s.catalogRead(ctx).QueryContext(ctx, `SELECT relative_path,title,kind,size_bytes,modified_at,observed_generation FROM source_catalog_file WHERE catalog_entry_id=? ORDER BY relative_path`, id)
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

func writeCatalogEntryNotAvailable(w http.ResponseWriter, admission catalogEntryAdmission) {
	writeAPIErrorDetails(w, http.StatusNotFound, "not_found", "catalog entry is not available", map[string]any{
		"reason":   admission.Reason,
		"files":    admission.Files,
		"audio":    admission.Audio,
		"nonAudio": admission.NonAudio,
	}, false)
}

func (s *Server) getSourceCatalogEntry(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	id, err := parseInt64PathValue(r, "id")
	if err != nil {
		writeAPIError(w, 400, "invalid_request", "invalid catalog id", false)
		return
	}
	readCtx, entry, source, done, err := s.beginCatalogEntryRead(r.Context(), id)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	defer done()
	r = r.WithContext(readCtx)
	// A retained missing record is provenance with personal state: its source
	// confirmed the absence, so it has no current published file set to judge.
	// Every other source-backed card, including a metadata-only parent, must
	// pass the same admission rule as browse and tracks.
	if catalogEntryAdmissionRequired(entry) {
		admission, err := catalogPublishedEntryAdmission(r.Context(), s.catalogRead(r.Context()), entry.ID)
		if err != nil {
			writeError(w, err)
			return
		}
		if !admission.Admitted() {
			writeCatalogEntryNotAvailable(w, admission)
			return
		}
	}
	var rating sql.NullInt64
	note := ""
	if entry.WorkID != nil && optionalUserID(r.Context()) > 0 {
		err = s.catalogRead(r.Context()).QueryRowContext(r.Context(), `SELECT rating,note FROM user_work_state WHERE work_id=? AND user_id=?`, *entry.WorkID, optionalUserID(r.Context())).Scan(&rating, &note)
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
	if err = s.catalogRead(r.Context()).QueryRowContext(r.Context(), `WITH RECURSIVE aliases(id) AS (
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
	readCtx, entry, _, done, err := s.beginCatalogEntryRead(r.Context(), id)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	defer done()
	r = r.WithContext(readCtx)
	admission, err := catalogPublishedEntryAdmission(r.Context(), s.catalogRead(r.Context()), entry.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	if !admission.Admitted() {
		// The card exists for provenance but never passed the publish threshold.
		// It has no playable tracks, so it must not answer a track request.
		writeCatalogEntryNotAvailable(w, admission)
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
	catalogTrackDefaultLimit = 5000
	catalogTrackLimitCap     = 20000
	// Admission caps a card at 5,000 files, so the summary must start lower:
	// 1,000 rows of JSON already exceed the 200 KiB first-screen budget.
	catalogTrackSummaryThreshold = 1000
	catalogTrackSummaryPreview   = 200
	maxCatalogTrackDepth         = 8
)

func (s *Server) loadSourceCatalogFileBatch(ctx context.Context, id int64, after string, limit int) ([]SourceCatalogFile, bool, error) {
	rows, err := s.catalogRead(ctx).QueryContext(ctx, `SELECT relative_path,title,kind,size_bytes,modified_at,observed_generation
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
	err := s.catalogRead(ctx).QueryRowContext(ctx, `SELECT COUNT(*) FROM source_catalog_file INDEXED BY source_catalog_file_parent WHERE catalog_entry_id=? AND kind<>'folder'`+where,
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
	rows, err := s.catalogRead(ctx).QueryContext(ctx, `SELECT relative_path,title,kind,size_bytes,modified_at,observed_generation
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
	// A release code or another exact identifier is language-independent. It
	// must still find a card whose text has no target-language row.
	codeQuery := q != "" && workcode.Valid(q)
	var (
		pageSQL   string
		pageArgs  []any
		countSQL  string
		countArgs []any
		// A short translated search builds a page from the trimmed card set and
		// reports the count of that same set, so the page query carries no
		// window total and the count is one cheap statement.
		separateCount bool
	)
	// An empty query stays on the browse path: there is nothing to match, so
	// the display language must not narrow the list.
	targets, languagePath := library.DisplaySearchTargets(r.URL.Query().Get("lang"), q)
	if codeQuery {
		languagePath = false
	}
	if languagePath && q != "" {
		cfg, cfgErr := translation.NewStore(s.db).Load(r.Context())
		if cfgErr != nil {
			writeError(w, cfgErr)
			return
		}
		configKey := translation.ConfigKey(cfg)
		short := utf8.RuneCountInString(q) < 3
		// A 1-2 character query may match the whole source. Each branch's scan is
		// bounded, then the merged, deduplicated cards are trimmed once to
		// catalogShortSearchResultLimit in display order: entry and series cards
		// and every corpus language share one budget, so a keystroke never walks
		// the whole card pipeline. The count reads the same statement one row
		// further, which is how it tells "exactly the budget" from "more than the
		// budget".
		limit := 0
		if short {
			limit = catalogShortSearchResultLimit
		}
		// The page reads one row past the arm bound so a saturated branch is
		// visible to the same set the count reads, while the card budget still
		// trims the merged set to the response limit.
		prefix, from, args := openListCatalogCardsQueryLocalized(source.ID, q, targets, configKey, limit+1, limit)
		pageArgs = append([]any{}, args...)
		pageSQL = catalogCardPageSQL(prefix, from, short)
		pageArgs = append(pageArgs, pageSize, (page-1)*pageSize)
		// The count reads the same cards one row past the budget, so the reported
		// total is the number of cards a reader can actually reach and one extra
		// row proves more exist without a second shape of query.
		countPrefix, countFrom := prefix, from
		countArgs = args
		if short {
			// Both bounds move together: a row past the card budget can only
			// exist if some arm was allowed to read past its own bound, and a
			// single arm has to be able to supply it. One extra row then proves
			// more cards exist without a second query shape.
			countPrefix, countFrom, countArgs = openListCatalogCardsQueryLocalized(
				source.ID, q, targets, configKey, limit+1, limit+1)
			// A saturated branch means the work bound, not the card budget,
			// ended the scan. Report reachable cards and the cut branch.
			countSQL = catalogStatementPrefix(countPrefix) + `SELECT COUNT(*),
					(SELECT EXISTS(SELECT 1 FROM search_hits GROUP BY language,entity_kind HAVING COUNT(*)>` +
				strconv.Itoa(limit) + `)) ` + countFrom
		} else {
			countSQL = catalogStatementPrefix(countPrefix) + "SELECT COUNT(*) " + countFrom
		}
		separateCount = short
	} else {
		prefix, from, args := openListCatalogCardsQuery(source.ID, q)
		pageSQL = catalogCardPageSQL(prefix, from, false)
		pageArgs = append(append([]any{}, args...), pageSize, (page-1)*pageSize)
		countSQL = catalogStatementPrefix(prefix) + "SELECT COUNT(*) " + from
		countArgs = args
	}
	readCtx, done, err := s.beginCatalogRead(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	defer done()
	r = r.WithContext(readCtx)
	rows, err := s.catalogRead(r.Context()).QueryContext(r.Context(), pageSQL, pageArgs...)
	if err != nil {
		writeError(w, err)
		return
	}
	works := []remoteWorkSummary{}
	total := 0
	for rows.Next() {
		var item remoteWorkSummary
		var availability string
		var exact int
		var hasAudio bool
		if err = catalogCardScan(rows, &item, &availability, &exact, &hasAudio, &total, separateCount); err != nil {
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
		// The verdict travels separately from the playability flag: a failed,
		// restricted or unconfirmed range is not the same answer as a confirmed
		// absence. Only a verdict that removes the published file set also
		// removes playback, which is the same rule the detail and track
		// endpoints apply.
		item.Availability = availability
		item.RemotePlayable = catalogVerdictPlayable(availability) && hasAudio
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
	saturated := false
	if separateCount {
		if err = s.catalogRead(r.Context()).QueryRowContext(r.Context(), countSQL, countArgs...).Scan(&total, &saturated); err != nil {
			writeError(w, err)
			return
		}
	}
	if len(works) == 0 && page > 1 && !separateCount {
		// The total rides on the page. An out-of-range page has no row to carry
		// it, so only that page pays for a separate count.
		if err = s.catalogRead(r.Context()).QueryRowContext(r.Context(), countSQL, countArgs...).Scan(&total); err != nil {
			writeError(w, err)
			return
		}
	}
	// A short search reports how many cards fit the result budget. The count read
	// one row past the budget, so a value above it proves more cards exist; the
	// client then renders "first N of more" instead of presenting the budget as
	// the whole result.
	truncated := false
	if separateCount && (total > catalogShortSearchResultLimit || saturated) {
		if total > catalogShortSearchResultLimit {
			total = catalogShortSearchResultLimit
		}
		truncated = true
	}
	writeJSON(w, 200, remoteWorksResponse{SourceID: source.ID, Works: works, Page: page, PageSize: pageSize, Total: total, Status: "ok", Sort: "updated", Direction: "desc", SortApplied: true, Truncated: truncated})
}
