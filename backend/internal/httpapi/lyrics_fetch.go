package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/yexca/kikoto/backend/internal/buildinfo"
	"github.com/yexca/kikoto/backend/internal/dlsite"
	"github.com/yexca/kikoto/backend/internal/kikoeru"
)

// Lyrics are small text files. These bounds keep one request from turning a
// remote tree into an unbounded download or a long write transaction.
const (
	maxLyricsFetchFiles       = 200
	maxLyricsFetchFileBytes   = 2 << 20
	maxLyricsFetchTotalBytes  = 32 << 20
	lyricsFetchRequestTimeout = 2 * time.Minute
	lyricsFetchFileTimeout    = 30 * time.Second
)

type lyricsFetchRequest struct {
	// SourceID and RemoteCode name the remote edition to download from.
	SourceID   int64  `json:"sourceId"`
	RemoteCode string `json:"remoteCode"`
	// SourceFolderID names another local folder of the family to copy from
	// instead of a remote edition.
	SourceFolderID int64 `json:"sourceFolderId"`
	FolderID       int64 `json:"folderId"`
	// Language declares the language of the lyrics. When it is omitted, the
	// source edition's declared language is used if lyrics can declare it.
	Language    *string                   `json:"language"`
	Files       []string                  `json:"files"`
	Assignments []lyricsFetchAssignmentIn `json:"assignments"`
}

// lyricsFetchAssignmentIn names an added file, by its path in the source, as
// the library lyrics of a local audio file.
type lyricsFetchAssignmentIn struct {
	AudioMediaItemID int64  `json:"audioMediaItemId"`
	Path             string `json:"path"`
}

type lyricsFetchResult struct {
	WorkID   int64  `json:"workId"`
	Folder   string `json:"folder"`
	Language string `json:"language"`
	// Downloaded counts the files this request added; Skipped counts the
	// files the folder already held with the same content.
	Downloaded int `json:"downloaded"`
	Skipped    int `json:"skipped"`
	Assigned   int `json:"assigned"`
}

// lyricsFetchTarget is the existing local work folder that receives the
// lyrics.
type lyricsFetchTarget struct {
	WorkID       int64
	FileSourceID int64
	RootPath     string
}

// lyricsFetchSource supplies the requested files: stage writes one of them,
// named by its path in the source, to a staging destination.
type lyricsFetchSource struct {
	language string
	origin   string
	stage    func(ctx context.Context, sourcePath string, destination string) (int64, error)
}

type lyricsFetchRequestError struct {
	status  int
	message string
}

func (err lyricsFetchRequestError) Error() string { return err.message }

func lyricsFetchBadRequest(message string) error {
	return lyricsFetchRequestError{status: http.StatusBadRequest, message: message}
}

// fetchWorkLyrics adds lyrics files of a family edition to the language's
// lyrics folder inside an existing local work folder. The files come from a
// remote source or from another local folder of the family. Nothing in the
// work folder is replaced: a file that is already there with other content
// rejects the request before anything is written.
func (s *Server) fetchWorkLyrics(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.requirePermission(w, r, "downloads:manage")
	if !ok {
		return
	}
	workID, err := parseInt64PathValue(r, "id")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid work id"})
		return
	}
	var payload lyricsFetchRequest
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	files, assignments, err := normalizeLyricsFetchRequest(payload)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if len(assignments) > 0 && !userHasPermission(actor, "library:write") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "library:write permission is required to assign lyrics"})
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), lyricsFetchRequestTimeout)
	defer cancel()
	result, err := s.runLyricsFetch(ctx, actor.ID, workID, payload, files, assignments)
	if err != nil {
		writeLyricsFetchError(w, workID, payload.SourceID, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// writeLyricsFetchError keeps upstream details in the server log; the response
// never carries a remote URL or a local path.
func writeLyricsFetchError(w http.ResponseWriter, workID int64, sourceID int64, err error) {
	var requestErr lyricsFetchRequestError
	if errors.As(err, &requestErr) {
		writeJSON(w, requestErr.status, map[string]string{"error": requestErr.message})
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "work not found"})
		return
	}
	if writeFetchDestinationError(w, err) {
		return
	}
	slog.Warn("lyrics fetch failed", "work_id", workID, "source_id", sourceID, "error", err)
	writeJSON(w, http.StatusBadGateway, map[string]string{"error": "lyrics could not be downloaded"})
}

func normalizeLyricsFetchRequest(payload lyricsFetchRequest) ([]string, map[int64]string, error) {
	remote := payload.SourceID > 0 && normalizeWorkCode(payload.RemoteCode) != ""
	if payload.FolderID <= 0 || remote == (payload.SourceFolderID > 0) {
		return nil, nil, errors.New("folderId and either sourceId with remoteCode or sourceFolderId are required")
	}
	if payload.Language != nil {
		if _, ok := normalizeLyricsLanguage(*payload.Language); !ok {
			return nil, nil, errors.New("unsupported lyrics language")
		}
	}
	if len(payload.Files) == 0 || len(payload.Files) > maxLyricsFetchFiles {
		return nil, nil, fmt.Errorf("choose between 1 and %d lyrics files", maxLyricsFetchFiles)
	}
	files := make([]string, 0, len(payload.Files))
	seen := map[string]bool{}
	for _, raw := range payload.Files {
		clean := cleanRemoteRelativePath(raw)
		if clean == "" || mediaKindFromPath(clean) != "text" {
			return nil, nil, errors.New("only lyrics text files can be added")
		}
		if !seen[clean] {
			seen[clean] = true
			files = append(files, clean)
		}
	}
	if len(payload.Assignments) > maxLyricsAssignmentChanges {
		return nil, nil, fmt.Errorf("at most %d assignments can be saved at once", maxLyricsAssignmentChanges)
	}
	assignments := map[int64]string{}
	for _, assignment := range payload.Assignments {
		clean := cleanRemoteRelativePath(assignment.Path)
		if assignment.AudioMediaItemID <= 0 || !seen[clean] {
			return nil, nil, errors.New("each assignment must name an audio item and one of the added files")
		}
		if _, duplicate := assignments[assignment.AudioMediaItemID]; duplicate {
			return nil, nil, errors.New("each audio media item can be assigned only once")
		}
		assignments[assignment.AudioMediaItemID] = clean
	}
	return files, assignments, nil
}

func (s *Server) runLyricsFetch(ctx context.Context, userID int64, workID int64, payload lyricsFetchRequest, files []string, assignments map[int64]string) (lyricsFetchResult, error) {
	family, err := s.lyricsAssignmentFamily(ctx, workID)
	if err != nil {
		return lyricsFetchResult{}, err
	}
	target, err := s.lyricsFetchTarget(ctx, family, payload.FolderID)
	if err != nil {
		return lyricsFetchResult{}, err
	}
	if err := s.validateLyricsFetchAudio(ctx, target.WorkID, assignments); err != nil {
		return lyricsFetchResult{}, err
	}
	var source lyricsFetchSource
	if payload.SourceFolderID > 0 {
		source, err = s.lyricsFetchLocalSource(ctx, family, payload.SourceFolderID, target)
	} else {
		source, err = s.lyricsFetchRemoteSource(ctx, family, payload.SourceID, normalizeWorkCode(payload.RemoteCode), files)
	}
	if err != nil {
		return lyricsFetchResult{}, err
	}
	language := source.language
	if payload.Language != nil {
		language, _ = normalizeLyricsLanguage(*payload.Language)
	}
	folder, added, err := s.stageAndPublishLyrics(ctx, target, lyricsLanguageFolderName(language), files, source.stage)
	if err != nil {
		return lyricsFetchResult{}, err
	}
	if err := s.indexLocalMediaForWork(ctx, target.WorkID, target.FileSourceID, target.RootPath); err != nil {
		return lyricsFetchResult{}, err
	}
	assigned, err := s.assignFetchedLyrics(ctx, userID, target.WorkID, folder, language, source.origin, assignments)
	if err != nil {
		return lyricsFetchResult{}, err
	}
	return lyricsFetchResult{
		WorkID: target.WorkID, Folder: folder, Language: language,
		Downloaded: added, Skipped: len(files) - added, Assigned: assigned,
	}, nil
}

// lyricsFetchRemoteSource resolves the requested files in a remote edition's
// tree. The edition must belong to the family; its tree is read without
// recording the edition as a work.
func (s *Server) lyricsFetchRemoteSource(ctx context.Context, family map[int64]bool, sourceID int64, remoteCode string, files []string) (lyricsFetchSource, error) {
	language, err := s.lyricsFetchEditionLanguage(ctx, family, remoteCode)
	if err != nil {
		return lyricsFetchSource{}, err
	}
	remote, tracks, err := s.lyricsFetchRemoteTree(ctx, sourceID, remoteCode)
	if err != nil {
		return lyricsFetchSource{}, err
	}
	urls, err := selectLyricsFetchFiles(tracks, files, remote)
	if err != nil {
		return lyricsFetchSource{}, err
	}
	return lyricsFetchSource{
		language: language,
		origin:   "remote_fetch",
		stage: func(ctx context.Context, sourcePath string, destination string) (int64, error) {
			return s.downloadLyricsFile(ctx, remote, urls[sourcePath], destination)
		},
	}, nil
}

// lyricsFetchEditionLanguage confirms that the remote code is an edition of
// the requested work's family and returns its declared language when lyrics
// can declare it.
func (s *Server) lyricsFetchEditionLanguage(ctx context.Context, family map[int64]bool, remoteCode string) (string, error) {
	var workID int64
	var language string
	err := s.db.QueryRowContext(ctx, `
		SELECT work.id, COALESCE(edition.metadata_language, '')
		FROM work
		LEFT JOIN work_edition AS edition ON edition.work_id = work.id
		WHERE UPPER(work.primary_code) = UPPER(?)
	`, remoteCode).Scan(&workID, &language)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !family[workID]) {
		return "", lyricsFetchBadRequest("the remote edition does not belong to this work")
	}
	if err != nil {
		return "", err
	}
	return editionLyricsLanguage(language), nil
}

// editionLyricsLanguage maps an edition's declared language onto a lyrics
// language, and to the every-language assignment when it has none.
func editionLyricsLanguage(editionLanguage string) string {
	language, ok := normalizeLyricsLanguage(dlsite.EditionMetadataLanguage(editionLanguage))
	if !ok {
		return ""
	}
	return language
}

func (s *Server) lyricsFetchTarget(ctx context.Context, family map[int64]bool, folderID int64) (lyricsFetchTarget, error) {
	var target lyricsFetchTarget
	err := s.db.QueryRowContext(ctx, `
		SELECT folder.work_id, folder.file_source_id, folder.root_path
		FROM work_folder_location AS folder
		INNER JOIN file_source AS source ON source.id = folder.file_source_id
		WHERE folder.id = ? AND folder.state = 'active' AND source.source_type = 'local_folder'
	`, folderID).Scan(&target.WorkID, &target.FileSourceID, &target.RootPath)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !family[target.WorkID]) {
		return lyricsFetchTarget{}, lyricsFetchBadRequest("choose an active local folder of this work")
	}
	if err != nil {
		return lyricsFetchTarget{}, err
	}
	target.RootPath = filepath.ToSlash(filepath.Clean(filepath.FromSlash(target.RootPath)))
	absolute, err := safeDataPath(s.cfg.DataRoot, target.RootPath)
	if err != nil {
		return lyricsFetchTarget{}, lyricsFetchBadRequest("the local folder is outside the library")
	}
	if err := s.ensureFetchTargetWritable(ctx, target.RootPath); err != nil {
		return lyricsFetchTarget{}, err
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.IsDir() || unsafeFetchStagingEntry(info) {
		return lyricsFetchTarget{}, lyricsFetchRequestError{status: http.StatusConflict, message: "the local folder is not available"}
	}
	return target, nil
}

// lyricsFetchLocalSource reads the requested files from another active local
// folder of the family, such as a translated edition's folder. The paths are
// relative to that folder.
func (s *Server) lyricsFetchLocalSource(ctx context.Context, family map[int64]bool, folderID int64, target lyricsFetchTarget) (lyricsFetchSource, error) {
	var workID int64
	var rootPath, editionLanguage string
	err := s.db.QueryRowContext(ctx, `
		SELECT folder.work_id, folder.root_path, COALESCE(edition.metadata_language, '')
		FROM work_folder_location AS folder
		INNER JOIN file_source AS source ON source.id = folder.file_source_id
		LEFT JOIN work_edition AS edition ON edition.work_id = folder.work_id
		WHERE folder.id = ? AND folder.state = 'active' AND source.source_type = 'local_folder'
	`, folderID).Scan(&workID, &rootPath, &editionLanguage)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !family[workID]) {
		return lyricsFetchSource{}, lyricsFetchBadRequest("choose an active local folder of this work to copy from")
	}
	if err != nil {
		return lyricsFetchSource{}, err
	}
	rootPath = filepath.ToSlash(filepath.Clean(filepath.FromSlash(rootPath)))
	if strings.EqualFold(rootPath, target.RootPath) {
		return lyricsFetchSource{}, lyricsFetchBadRequest("the lyrics are already in this folder")
	}
	absolute, err := safeDataPath(s.cfg.DataRoot, rootPath)
	if err != nil {
		return lyricsFetchSource{}, lyricsFetchBadRequest("the local folder is outside the library")
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.IsDir() || unsafeFetchStagingEntry(info) {
		return lyricsFetchSource{}, lyricsFetchRequestError{status: http.StatusConflict, message: "the local folder is not available"}
	}
	return lyricsFetchSource{
		language: editionLyricsLanguage(editionLanguage),
		origin:   "library_import",
		stage: func(_ context.Context, sourcePath string, destination string) (int64, error) {
			return copyLibraryLyricsFile(absolute, sourcePath, destination)
		},
	}, nil
}

// copyLibraryLyricsFile copies one lyrics file below sourceRoot to a staging
// destination. Every path component must be a regular directory or file, so a
// link inside the folder cannot read from outside it.
func copyLibraryLyricsFile(sourceRoot string, relative string, destination string) (int64, error) {
	missing := lyricsFetchRequestError{status: http.StatusNotFound, message: "a selected lyrics file is no longer in the folder"}
	current := sourceRoot
	parts := strings.Split(relative, "/")
	for index, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return 0, missing
		}
		last := index == len(parts)-1
		if unsafeFetchStagingEntry(info) || info.IsDir() == last || (last && !info.Mode().IsRegular()) {
			return 0, missing
		}
		if last && info.Size() > maxLyricsFetchFileBytes {
			return 0, lyricsFetchBadRequest("a lyrics file is too large")
		}
	}
	input, err := os.Open(current)
	if err != nil {
		return 0, missing
	}
	defer func() { _ = input.Close() }()
	return writeLyricsStagingFile(input, destination)
}

// writeLyricsStagingFile writes a bounded lyrics file to a new staging path.
func writeLyricsStagingFile(input io.Reader, destination string) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return 0, err
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return 0, err
	}
	written, copyErr := io.Copy(output, io.LimitReader(input, maxLyricsFetchFileBytes+1))
	closeErr := output.Close()
	if copyErr != nil {
		return 0, copyErr
	}
	if closeErr != nil {
		return 0, closeErr
	}
	if written > maxLyricsFetchFileBytes {
		return 0, lyricsFetchBadRequest("a lyrics file is too large")
	}
	return written, nil
}

func (s *Server) validateLyricsFetchAudio(ctx context.Context, workID int64, assignments map[int64]string) error {
	for audioID := range assignments {
		var audioWorkID int64
		var kind string
		err := s.db.QueryRowContext(ctx, "SELECT work_id, kind FROM media_item WHERE id = ?", audioID).Scan(&audioWorkID, &kind)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && (kind != "audio" || audioWorkID != workID)) {
			return lyricsFetchBadRequest("assignments must target audio in the chosen local folder's work")
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// lyricsFetchRemoteTree reads the edition's remote tree without recording the
// edition as a work: browsing a sibling edition is not a request to track it.
func (s *Server) lyricsFetchRemoteTree(ctx context.Context, sourceID int64, remoteCode string) (remoteSourceForUse, []kikoeru.Track, error) {
	source, _, tracks, err := s.loadInstanceRemoteWorkTracksCached(ctx, sourceID, remoteCode)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || isNotFoundLikeError(err) {
			return remoteSourceForUse{}, nil, lyricsFetchRequestError{status: http.StatusNotFound, message: "the edition was not found on this source"}
		}
		return remoteSourceForUse{}, nil, err
	}
	if !source.Enabled || !isKikoeruSourceType(source.SourceType) {
		return remoteSourceForUse{}, nil, lyricsFetchBadRequest("source is not an enabled compatible remote source")
	}
	return source, tracks, nil
}

// selectLyricsFetchFiles resolves each requested path in the remote tree to
// its URL and checks that URL against the source's outbound policy before any
// download.
func selectLyricsFetchFiles(tracks []kikoeru.Track, paths []string, source remoteSourceForUse) (map[string]string, error) {
	available := map[string]string{}
	collectLyricsTrackURLs(tracks, "", available)
	result := make(map[string]string, len(paths))
	for _, remotePath := range paths {
		rawURL, ok := available[remotePath]
		if !ok {
			return nil, lyricsFetchRequestError{status: http.StatusNotFound, message: "a selected lyrics file is no longer on the source"}
		}
		parsed, err := url.Parse(rawURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || !remotePreviewURLAllowed(parsed, source) {
			return nil, lyricsFetchRequestError{status: http.StatusBadGateway, message: "a lyrics file URL is not allowed by the source policy"}
		}
		result[remotePath] = parsed.String()
	}
	return result, nil
}

func collectLyricsTrackURLs(nodes []kikoeru.Track, basePath string, result map[string]string) {
	for index, node := range nodes {
		nodePath := remoteTrackPath(basePath, remoteTrackName(node.Title, index))
		if len(node.Children) > 0 || remoteTrackKindForPath(node.Type, nodePath) == "folder" {
			collectLyricsTrackURLs(node.Children, nodePath, result)
			continue
		}
		if mediaKindFromPath(nodePath) != "text" {
			continue
		}
		if value := firstNonEmpty(node.MediaDownloadURL, node.MediaStreamURL); value != "" {
			result[nodePath] = value
		}
	}
}

// stageAndPublishLyrics stages every file on the target's storage pool, then
// adds the staged files to the lyrics folder. It returns the folder relative
// to the data root and how many files it added.
func (s *Server) stageAndPublishLyrics(
	ctx context.Context,
	target lyricsFetchTarget,
	folderName string,
	files []string,
	stage func(ctx context.Context, sourcePath string, destination string) (int64, error),
) (string, int, error) {
	poolPath, err := s.fetchTransactionPool(ctx, target.RootPath)
	if err != nil {
		return "", 0, err
	}
	stagingParent := path.Join(poolPath, ".kikoto-staging")
	stagingRoot, err := s.createLyricsStaging(stagingParent)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = os.RemoveAll(stagingRoot) }()

	total := int64(0)
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return "", 0, err
		}
		written, err := stage(ctx, file, filepath.Join(stagingRoot, filepath.FromSlash(file)))
		if err != nil {
			return "", 0, err
		}
		total += written
		if total > maxLyricsFetchTotalBytes {
			return "", 0, lyricsFetchBadRequest("the selected lyrics files are too large")
		}
	}
	return s.publishLyricsFolder(stagingRoot, target.RootPath, folderName, files)
}

func (s *Server) createLyricsStaging(stagingParent string) (string, error) {
	parent, err := safeDataPath(s.cfg.DataRoot, stagingParent)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", err
	}
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return "", err
	}
	root := filepath.Join(parent, "lyrics-"+hex.EncodeToString(suffix))
	return root, os.Mkdir(root, 0o755)
}

func (s *Server) downloadLyricsFile(ctx context.Context, source remoteSourceForUse, remoteURL string, destination string) (int64, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, remoteURL, nil)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Accept", "text/plain,text/*,*/*;q=0.5")
	request.Header.Set("User-Agent", buildinfo.UserAgent()+" Kikoeru-compatible client")
	request.Header.Set("Accept-Language", s.instanceRemoteSourceAcceptLanguage(ctx, source))
	response, err := s.sourceDownloadHTTPClient(source, lyricsFetchFileTimeout).Do(request)
	if err != nil {
		return 0, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return 0, fmt.Errorf("lyrics download returned HTTP %d", response.StatusCode)
	}
	if response.ContentLength > maxLyricsFetchFileBytes {
		return 0, lyricsFetchBadRequest("a lyrics file is too large")
	}
	return writeLyricsStagingFile(response.Body, destination)
}

// lyricsFolderConflict is the fixed message for a file or folder name that is
// already taken by something this request must not replace.
var lyricsFolderConflict = lyricsFetchRequestError{
	status:  http.StatusConflict,
	message: "a different file with the same name is already in this language's lyrics folder",
}

// publishLyricsFolder adds the staged files to the language's lyrics folder
// of the work folder, creating the folder when it is missing. It never
// replaces anything: a file the folder already holds with the same content is
// left alone, and one that differs rejects the request before any file is
// moved. A failed move removes what this call added.
func (s *Server) publishLyricsFolder(stagingRoot string, rootPath string, folderName string, files []string) (string, int, error) {
	rootAbsolute, err := safeDataPath(s.cfg.DataRoot, rootPath)
	if err != nil {
		return "", 0, err
	}
	folderAbsolute := filepath.Join(rootAbsolute, folderName)
	pending := make([]string, 0, len(files))
	for _, file := range files {
		present, err := lyricsFileAlreadyPublished(folderAbsolute, file, filepath.Join(stagingRoot, filepath.FromSlash(file)))
		if err != nil {
			return "", 0, err
		}
		if !present {
			pending = append(pending, file)
		}
	}
	folder := path.Join(rootPath, folderName)
	if len(pending) == 0 {
		return folder, 0, nil
	}

	moved := make([]string, 0, len(pending))
	createdDirectories := []string{}
	rollback := func() {
		for index := len(moved) - 1; index >= 0; index-- {
			_ = os.Remove(moved[index])
		}
		for index := len(createdDirectories) - 1; index >= 0; index-- {
			_ = os.Remove(createdDirectories[index])
		}
	}
	for _, file := range pending {
		destination := filepath.Join(folderAbsolute, filepath.FromSlash(file))
		created, err := mkdirAllRecording(rootAbsolute, filepath.Dir(destination))
		createdDirectories = append(createdDirectories, created...)
		if err != nil {
			rollback()
			return "", 0, err
		}
		if _, err := os.Lstat(destination); err == nil {
			rollback()
			return "", 0, lyricsFolderConflict
		}
		if err := os.Rename(filepath.Join(stagingRoot, filepath.FromSlash(file)), destination); err != nil {
			rollback()
			return "", 0, err
		}
		moved = append(moved, destination)
	}
	return folder, len(moved), nil
}

// lyricsFileAlreadyPublished reports whether the lyrics folder already holds
// file with the staged content. Anything else in the way, including a link or
// a directory on the path, is a conflict.
func lyricsFileAlreadyPublished(folderAbsolute string, file string, stagedPath string) (bool, error) {
	current := folderAbsolute
	parts := append([]string{""}, strings.Split(file, "/")...)
	for index, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		last := index == len(parts)-1
		if unsafeFetchStagingEntry(info) || info.IsDir() == last || (last && !info.Mode().IsRegular()) {
			return false, lyricsFolderConflict
		}
	}
	existing, err := os.ReadFile(current)
	if err != nil {
		return false, err
	}
	staged, err := os.ReadFile(stagedPath)
	if err != nil {
		return false, err
	}
	if !bytes.Equal(existing, staged) {
		return false, lyricsFolderConflict
	}
	return true, nil
}

// mkdirAllRecording creates directory below base and returns the directories it
// created, outermost first, so a rollback removes only those.
func mkdirAllRecording(base string, directory string) ([]string, error) {
	relative, err := filepath.Rel(base, directory)
	if err != nil || relative == "." {
		return nil, err
	}
	created := []string{}
	current := base
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		err := os.Mkdir(current, 0o755)
		if err == nil {
			created = append(created, current)
			continue
		}
		if !errors.Is(err, os.ErrExist) {
			return created, err
		}
	}
	return created, nil
}

// assignFetchedLyrics stores the requested assignments, in the lyrics'
// language, for the files the rescan indexed. A file the scan did not index is
// skipped, not an error: the files themselves were added.
func (s *Server) assignFetchedLyrics(ctx context.Context, userID int64, workID int64, folder string, language string, origin string, assignments map[int64]string) (int, error) {
	if len(assignments) == 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	assigned := 0
	for audioID, sourcePath := range assignments {
		var lyricsID int64
		err := tx.QueryRowContext(ctx, `
			SELECT item.id
			FROM media_file_location AS location
			INNER JOIN media_item AS item ON item.id = location.media_item_id
			WHERE item.work_id = ?
				AND location.location_type = 'local'
				AND location.availability = 'available'
				AND location.path = ?
			ORDER BY location.id
			LIMIT 1
		`, workID, path.Join(folder, sourcePath)).Scan(&lyricsID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if err := upsertLyricsAssignment(ctx, tx, audioID, language, lyricsID, origin, userID); err != nil {
			return 0, err
		}
		assigned++
	}
	return assigned, tx.Commit()
}
