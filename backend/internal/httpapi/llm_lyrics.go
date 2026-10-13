package httpapi

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/yexca/kikoto/backend/internal/llm"
	"github.com/yexca/kikoto/backend/internal/localfs"
	"github.com/yexca/kikoto/backend/internal/textdecode"
)

type llmLyricsInput struct {
	LocationID int64
	AudioID    int64
	FolderID   int64
	Extension  string
	Hash       string
	Document   llm.LyricsDocument
}

func (s *Server) loadLLMLyrics(ctx context.Context, workID int64, payload llmTranslatePayload, policy llm.Policy) (llmLyricsInput, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result := llmLyricsInput{LocationID: payload.LyricsLocationID, AudioID: payload.AudioMediaItemID, FolderID: payload.FolderID}
	if result.LocationID <= 0 || result.AudioID <= 0 || result.FolderID <= 0 {
		return result, llmInvalid("llm_lyrics_selection", "Select one lyrics file, its audio track and an online local destination folder.")
	}
	family, err := s.lyricsAssignmentFamily(ctx, workID)
	if err != nil {
		return result, err
	}
	target, err := s.lyricsFetchTarget(ctx, family, result.FolderID)
	if err != nil {
		return result, llmInvalid("llm_lyrics_scope", "The selected local destination is unavailable.")
	}
	if err := s.validateLyricsFetchAudio(ctx, target.WorkID, map[int64]string{result.AudioID: "selected"}); err != nil {
		return result, llmInvalid("llm_lyrics_scope", "Choose an audio track in the destination work.")
	}
	var locationWork, sourceID int64
	var kind, locationType, relative, availability, stream, download string
	err = s.db.QueryRowContext(ctx, `SELECT item.work_id,item.kind,location.location_type,location.path,location.availability,COALESCE(location.file_source_id,0),COALESCE(location.stream_url,''),COALESCE(location.download_url,'')
 FROM media_file_location location JOIN media_item item ON item.id=location.media_item_id WHERE location.id=?`, result.LocationID).Scan(&locationWork, &kind, &locationType, &relative, &availability, &sourceID, &stream, &download)
	if err != nil || !family[locationWork] || kind != "text" || availability != "available" && availability != "remote" {
		return result, llmInvalid("llm_lyrics_scope", "The selected lyrics file is outside this work or unavailable.")
	}
	result.Extension = strings.ToLower(filepath.Ext(relative))
	var data []byte
	switch locationType {
	case "remote_stream":
		actor, ok := userFromContext(ctx)
		if !policy.RemoteLyrics || !ok || !userHasPermission(actor, "remote:fetch") {
			return result, llmInvalid("llm_remote_lyrics_disabled", "Remote lyrics translation requires administrator opt-in and remote fetch permission.")
		}
		source, err := s.loadRemotePlaybackSource(ctx, sourceID)
		if err != nil {
			return result, llmInvalid("llm_lyrics_unavailable", "The selected remote lyrics source is unavailable.")
		}
		req, err := http.NewRequestWithContext(ctx, "GET", firstNonEmpty(download, stream), nil)
		if err != nil {
			return result, llmInvalid("llm_lyrics_unavailable", "The selected lyrics file is unavailable.")
		}
		response, err := s.sourceDownloadHTTPClient(source, 30*time.Second).Do(req)
		if err != nil {
			return result, llmInvalid("llm_lyrics_unavailable", "The selected lyrics file could not be read.")
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode < 200 || response.StatusCode >= 300 || response.ContentLength > llm.LyricsLimit {
			return result, llmInvalid("llm_lyrics_unavailable", "The selected lyrics response is unavailable or too large.")
		}
		data, err = io.ReadAll(io.LimitReader(response.Body, llm.LyricsLimit+1))
		if err != nil {
			return result, err
		}
	case "local":
		if err := s.ensureFetchTargetWritable(ctx, relative); err != nil {
			return result, llmInvalid("llm_lyrics_scope", "The lyrics storage pool is offline.")
		}
		root, scopedPath, err := s.openLLMPoolRoot(ctx, relative)
		if err != nil {
			return result, llmInvalid("llm_lyrics_unavailable", "The selected lyrics file is unavailable.")
		}
		defer func() { _ = root.Close() }()
		file, err := root.Open(scopedPath)
		if err != nil {
			return result, llmInvalid("llm_lyrics_unavailable", "The selected lyrics file is unavailable.")
		}
		defer func() { _ = file.Close() }()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() > llm.LyricsLimit {
			return result, llmInvalid("llm_lyrics_unavailable", "The selected lyrics file is unavailable or too large.")
		}
		data, err = io.ReadAll(io.LimitReader(file, llm.LyricsLimit+1))
		if err != nil {
			return result, err
		}
	default:
		return result, llmInvalid("llm_lyrics_scope", "Choose an original local or explicitly enabled remote lyrics file.")
	}
	if len(data) > llm.LyricsLimit {
		return result, llmInvalid("llm_input_too_large", "Lyrics exceed the translation limit.")
	}
	text, err := textdecode.Decode(ctx, data, "")
	if err != nil {
		return result, llmInvalid("llm_lyrics_unavailable", "Lyrics could not be decoded.")
	}
	result.Document, err = llm.ParseLyrics(relative, text)
	if err != nil {
		return result, llmInvalid("llm_lyrics_format", "Lyrics are empty, too large or use an unsupported format.")
	}
	result.Hash = llmValueHash(struct {
		Location, Audio, Folder int64
		Text                    string
	}{result.LocationID, result.AudioID, result.FolderID, text})
	return result, nil
}

func saveLLMLyricsAsset(ctx context.Context, tx *sql.Tx, id string, p llmPreview, userID int64) error {
	text, err := p.Lyrics.Document.Render(p.Result.Lyrics)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO llm_lyrics_asset(id,work_id,source_location_id,audio_media_item_id,folder_id,language,source_hash,value_text,extension,created_by_user_id) VALUES(?,?,?,?,?,?,?,?,?,?)`, id, p.WorkID, p.Lyrics.LocationID, p.Lyrics.AudioID, p.Lyrics.FolderID, p.Target, p.Lyrics.Hash, text, p.Lyrics.Extension, userID)
	return err
}

// The durable database asset precedes filesystem publication. A failed rename
// or database update is recoverable from the asset without another LLM request.
func (s *Server) publishLLMLyricsAsset(ctx context.Context, id string) error {
	var workID, folderID int64
	var text, extension, status string
	if err := s.db.QueryRowContext(ctx, "SELECT work_id,folder_id,value_text,extension,status FROM llm_lyrics_asset WHERE id=?", id).Scan(&workID, &folderID, &text, &extension, &status); err != nil {
		return err
	}
	if status == "accepted" {
		return nil
	}
	if status != "publishing" {
		return errors.New("lyrics asset is not publishable")
	}
	family, err := s.lyricsAssignmentFamily(ctx, workID)
	if err != nil {
		return err
	}
	target, err := s.lyricsFetchTarget(ctx, family, folderID)
	if err != nil {
		return err
	}
	// The server-generated preview token is the whole filename namespace.
	if len(id) != 32 || strings.ContainsAny(id, "/\\.") {
		return errors.New("invalid asset identity")
	}
	filename := "translation" + extension
	folder := path.Join(target.RootPath, "LLM Lyrics - "+id)

	root, scopedFolder, err := s.openLLMPoolRoot(ctx, folder)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if _, err = root.Lstat(scopedFolder); errors.Is(err, os.ErrNotExist) {
		if err := root.MkdirAll(".kikoto-staging", 0o755); err != nil {
			return err
		}
		var nonce [8]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return err
		}
		staging := filepath.Join(".kikoto-staging", "llm-lyrics-"+hex.EncodeToString(nonce[:]))
		if err := root.Mkdir(staging, 0o755); err != nil {
			return err
		}
		defer func() { _ = root.RemoveAll(staging) }()
		file, err := root.OpenFile(filepath.Join(staging, filename), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return err
		}
		_, writeErr := file.Write([]byte(text))
		syncErr := file.Sync()
		closeErr := file.Close()
		if writeErr != nil {
			return writeErr
		}
		if syncErr != nil {
			return syncErr
		}
		if closeErr != nil {
			return closeErr
		}
		if err := s.ensureFetchTargetWritable(ctx, target.RootPath); err != nil {
			return err
		}
		if err := root.Rename(staging, scopedFolder); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	file, err := root.Open(filepath.Join(scopedFolder, filename))
	if err != nil {
		return err
	}
	existing, readErr := io.ReadAll(io.LimitReader(file, llm.LyricsLimit+1))
	info, statErr := file.Stat()
	_ = file.Close()
	if readErr != nil || statErr != nil || string(existing) != text {
		return errors.New("lyrics publication does not match its durable asset")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// Use the same local path identity as indexing, without scanning the work.
	var code string
	if err := tx.QueryRowContext(ctx, "SELECT primary_code FROM work WHERE id=?", target.WorkID).Scan(&code); err != nil {
		return err
	}
	fileInfo := localfs.LocalFile{RelPath: path.Join(folder, filename), WorkRelPath: strings.TrimPrefix(path.Join(folder, filename), target.RootPath+"/"), Title: filename, Extension: extension, SizeBytes: int64(len(text)), FileVersion: localfs.FileVersion(info)}
	mediaID, err := upsertDetectedMediaItem(ctx, tx, target.WorkID, localfs.WorkFolder{Code: code, RelPath: target.RootPath}, fileInfo, "text", 0)
	if err != nil {
		return err
	}
	_, err = upsertDetectedLocation(ctx, tx, mediaID, target.FileSourceID, fileInfo)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "UPDATE llm_lyrics_asset SET status='accepted',relative_path=?,media_item_id=? WHERE id=? AND status='publishing'", path.Join(folder, filename), mediaID, id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("lyrics asset was already published")
	}
	return tx.Commit()
}
func (s *Server) retryLLMLyricsPublication(w http.ResponseWriter, r *http.Request) {
	_, ok := s.requirePermission(w, r, "library:write")
	if !ok {
		return
	}

	if err := s.publishLLMLyricsAsset(r.Context(), r.PathValue("asset")); err != nil {
		writeAPIError(w, 409, "llm_asset_pending", "Translation asset publication is pending. Check the selected storage pool.", false)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) downloadLLMLyricsAsset(w http.ResponseWriter, r *http.Request) {
	if s.cfg.IsDemo() {
		writeAPIError(w, 403, "llm_disabled", "LLM translation is unavailable in Demo.", false)
		return
	}
	if _, ok := s.requirePermission(w, r, "library:read"); !ok {
		return
	}
	var text, extension string
	if err := s.db.QueryRowContext(r.Context(), "SELECT value_text,extension FROM llm_lyrics_asset WHERE id=? AND status='accepted'", r.PathValue("asset")).Scan(&text, &extension); err != nil {
		writeAPIError(w, 404, "llm_asset_not_found", "Translation asset not found.", false)
		return
	}
	setUntrustedFileHeaders(w)
	w.Header().Set("Content-Disposition", "attachment; filename="+`"translation`+extension+`"`)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(text))
}
func (s *Server) listLLMLyricsAssets(w http.ResponseWriter, r *http.Request) {
	if s.cfg.IsDemo() {
		writeAPIError(w, 403, "llm_disabled", "LLM translation is unavailable in Demo.", false)
		return
	}
	if _, ok := s.requirePermission(w, r, "library:write"); !ok {
		return
	}
	workID, err := parseInt64PathValue(r, "id")
	if err != nil {
		writeAPIError(w, 400, "invalid_work_id", "Invalid work id.", false)
		return
	}
	rows, err := s.db.QueryContext(r.Context(), "SELECT id,audio_media_item_id,language,status FROM llm_lyrics_asset WHERE work_id=? ORDER BY created_at DESC LIMIT 64", workID)
	if err != nil {
		writeError(w, err)
		return
	}
	defer func() { _ = rows.Close() }()
	assets := []map[string]any{}
	for rows.Next() {
		var id, language, status string
		var audioID int64
		if err := rows.Scan(&id, &audioID, &language, &status); err != nil {
			writeError(w, err)
			return
		}
		assets = append(assets, map[string]any{"id": id, "audioMediaItemId": audioID, "language": language, "status": status, "downloadUrl": "/api/llm/lyrics-assets/" + id})
	}
	if err := rows.Err(); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, assets)
}

// A rooted pool handle rejects symlink escapes into another pool or out of the library.
func (s *Server) openLLMPoolRoot(ctx context.Context, relative string) (*os.Root, string, error) {
	pool, err := s.fetchTransactionPool(ctx, relative)
	if err != nil {
		return nil, "", err
	}
	base := s.cfg.DataRoot
	if pool != "" {
		base, err = safeDataPath(base, pool)
		if err != nil {
			return nil, "", err
		}
	}
	absolute, err := safeDataPath(s.cfg.DataRoot, relative)
	if err != nil {
		return nil, "", err
	}
	scoped, err := filepath.Rel(base, absolute)
	if err != nil || scoped == ".." || strings.HasPrefix(scoped, ".."+string(filepath.Separator)) {
		return nil, "", errors.New("lyrics path escapes its pool")
	}
	root, err := os.OpenRoot(base)
	return root, scoped, err
}
