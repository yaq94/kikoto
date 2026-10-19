package httpapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/yexca/kikoto/backend/internal/openlist"
)

// Catalog sidecars are ordinary text files that a release folder publishes
// beside its audio. They are evidence for matching, not a work identity: a
// readme can name a voice actor or a circle, but it never supplies a product
// code and never registers a work.
const (
	catalogSidecarMaxBytes = 256 << 10
	catalogSidecarMaxFiles = 4
)

// A sidecar is fetched at most once per release revision. The cached digest
// lets an unchanged file be reused without another fetch; a missing digest
// (including evidence written by an older release) is read once and then
// cached. Size alone is not a revision: an edit can keep the same length.
type catalogSidecarMeta struct {
	RelativePath string   `json:"relativePath"`
	Size         int64    `json:"size"`
	Digest       string   `json:"digest,omitempty"`
	VoiceActors  []string `json:"voiceActors"`
	Circles      []string `json:"circles"`
	Title        string   `json:"title,omitempty"`
	FetchedAt    string   `json:"fetchedAt"`
}

type catalogSidecarCache struct {
	Sidecars []catalogSidecarMeta `json:"sidecars"`
}

var (
	catalogSidecarVoicePatterns = []*regexp.Regexp{
		// "配音：葉月", "声优: 安里", "CV：逢坂成美", "聲優：..."
		regexp.MustCompile(`(?im)^[\s　*・\-]*(?:配音|聲優|声优|CV|キャスト)\s*[:：]\s*([^\r\n]{1,120})`),
		// "CV / 声优：name" in a single line with separators.
		regexp.MustCompile(`(?im)(?:配音|聲優|声优|CV|キャスト)\s*[:：]\s*([^\r\n]{1,120})`),
	}
	catalogSidecarCirclePatterns = []*regexp.Regexp{
		// "社团：小點聲聆", "サークル: ...", "Circle: ..."
		regexp.MustCompile(`(?im)^[\s　*・\-]*(?:社团|社團|サークル|Circle|ブランド)\s*[:：]\s*([^\r\n]{1,120})`),
	}
	catalogSidecarTitlePatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?im)^[\s　]*[「『]([^」』\r\n]{1,200})[」』]`),
	}
)

// catalogSidecarFiles picks a small, stable set of text files that commonly
// carry release metadata. Subtitle files are excluded: a transcript is not a
// credit list and can be large. The selection is by role, not by a particular
// source's naming scheme.
func catalogSidecarFiles(files []SourceCatalogFile) []SourceCatalogFile {
	selected := []SourceCatalogFile{}
	for _, file := range files {
		if file.Kind != "text" {
			continue
		}
		name := strings.ToLower(path.Base(file.Path))
		switch {
		case strings.Contains(name, "readme"), strings.Contains(name, "info"),
			strings.Contains(name, "credit"), strings.Contains(name, "staff"),
			strings.Contains(name, "介绍"), strings.Contains(name, "介紹"):
			selected = append(selected, file)
		}
		if len(selected) >= catalogSidecarMaxFiles {
			break
		}
	}
	return selected
}

// parseCatalogSidecar extracts only explicitly labelled credits. A free-form
// paragraph mentioning a name is not enough: the label and separator are the
// evidence, so a title that happens to contain "CV" cannot create a credit.
func parseCatalogSidecar(body string) (voices, circles []string, title string) {
	voices = catalogSidecarNames(body, catalogSidecarVoicePatterns)
	circles = catalogSidecarNames(body, catalogSidecarCirclePatterns)
	for _, pattern := range catalogSidecarTitlePatterns {
		if match := pattern.FindStringSubmatch(body); len(match) > 1 {
			title = strings.TrimSpace(match[1])
			break
		}
	}
	return voices, circles, title
}

func catalogSidecarNames(body string, patterns []*regexp.Regexp) []string {
	seen := map[string]bool{}
	names := []string{}
	for _, pattern := range patterns {
		for _, match := range pattern.FindAllStringSubmatch(body, -1) {
			if len(match) < 2 {
				continue
			}
			for _, part := range splitCatalogSidecarNames(match[1]) {
				key := strings.ToLower(part)
				if part == "" || seen[key] {
					continue
				}
				seen[key] = true
				names = append(names, part)
			}
		}
	}
	sort.Strings(names)
	return names
}

// A single credit line can name several people. Split on the separators used
// by release pages, then drop trailing commentary such as a URL or role note.
// Release pages also bracket a name inconsistently ("CV：逢坂成美" and
// "CV：【逢坂成美】"), so a paired closing bracket left on the tail is markup,
// not part of the name.
func splitCatalogSidecarNames(raw string) []string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimRight(raw, "。.,，;；、 ")
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		switch r {
		case '/', '／', '、', ',', '，', '&', '＆', '+':
			return true
		}
		return false
	})
	names := []string{}
	for _, field := range fields {
		field = trimCatalogSidecarBrackets(field)
		if field == "" || len([]rune(field)) > 60 {
			continue
		}
		// A URL or an obvious sentence is not a name.
		if strings.Contains(field, "http") || strings.Contains(field, "：") || strings.Contains(field, ":") {
			continue
		}
		names = append(names, field)
	}
	return names
}

// trimCatalogSidecarBrackets removes trailing markup around a name. A field
// that is entirely bracketed is a name wrapper and is unwrapped. A bracket
// after the name is a role note and is dropped, leaving the name. A stray
// closing bracket is markup and is dropped too. The function never strips
// punctuation from the middle of a name.
func trimCatalogSidecarBrackets(value string) string {
	value = strings.TrimSpace(strings.Trim(value, "　*・-—:："))
	if index := strings.Index(strings.ToLower(value), "http"); index > 0 {
		value = strings.TrimSpace(value[:index])
	}
	pairs := map[rune]rune{'）': '（', ')': '(', '】': '【', '」': '「', '』': '『', '〕': '〔', '］': '［'}
	for {
		runes := []rune(value)
		if len(runes) == 0 {
			return ""
		}
		last := runes[len(runes)-1]
		opener, paired := pairs[last]
		if !paired {
			return strings.TrimSpace(value)
		}
		depth := 0
		matched := -1
		for index := len(runes) - 1; index >= 0; index-- {
			switch runes[index] {
			case last:
				depth++
			case opener:
				depth--
				if depth == 0 {
					matched = index
				}
			}
			if matched >= 0 {
				break
			}
		}
		if matched < 0 {
			// An unmatched closing bracket is stray markup, not a name.
			value = strings.TrimSpace(string(runes[:len(runes)-1]))
			continue
		}
		if matched > 0 {
			// "逢坂成美（CV）": the bracket annotates the name before it.
			return strings.TrimSpace(string(runes[:matched]))
		}
		// "【逢坂成美】": the whole field is a bracketed name.
		value = strings.TrimSpace(string(runes[1 : len(runes)-1]))
	}
}

// loadCatalogSidecars reads cached evidence and fetches only sidecars whose
// file revision changed. A size change is cheap evidence of an edit; an equal
// size still hashes the body, so an in-place edit of the same length is not
// frozen forever. A failed read keeps the previous evidence; it never blocks
// publication of the audio card.
func (s *Server) loadCatalogSidecars(ctx context.Context, entry catalogEntry, files []SourceCatalogFile, source remoteSourceForUse) (catalogSidecarCache, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT sidecar_json FROM source_catalog_entry WHERE id=?`, entry.ID).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return catalogSidecarCache{}, err
	}
	cache := catalogSidecarCache{Sidecars: []catalogSidecarMeta{}}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &cache)
	}
	previous := map[string]catalogSidecarMeta{}
	for _, item := range cache.Sidecars {
		previous[item.RelativePath] = item
	}
	selected := catalogSidecarFiles(files)
	if len(selected) == 0 {
		return catalogSidecarCache{Sidecars: []catalogSidecarMeta{}}, nil
	}
	client := s.openListClient(source)
	next := []catalogSidecarMeta{}
	for _, file := range selected {
		if old, ok := previous[file.Path]; ok && old.Size == file.Size && old.Digest != "" {
			next = append(next, old)
			continue
		}
		full := path.Join(entry.SourceRootOrRoot(), file.Path)
		object, getErr := client.Get(ctx, full)
		if getErr != nil {
			// An unreadable sidecar is not a successful empty read. Keep any
			// previous evidence but do not let this scan claim the file was
			// checked and had no credits.
			continue
		}
		body, readErr := readCatalogSidecar(ctx, client, object.RawURL)
		if readErr != nil {
			if old, ok := previous[file.Path]; ok {
				next = append(next, old)
			}
			continue
		}
		voices, circles, title := parseCatalogSidecar(body)
		digest := sha256.Sum256([]byte(body))
		next = append(next, catalogSidecarMeta{
			RelativePath: file.Path, Size: file.Size,
			Digest:     hex.EncodeToString(digest[:]),
			VoiceActors: voices, Circles: circles, Title: title,
			FetchedAt: time.Now().UTC().Format(time.RFC3339),
		})
	}
	sort.Slice(next, func(i, j int) bool { return next[i].RelativePath < next[j].RelativePath })
	return catalogSidecarCache{Sidecars: next}, nil
}

func readCatalogSidecar(ctx context.Context, client *openlist.Client, rawURL string) (string, error) {
	if rawURL == "" {
		return "", openlist.ErrInvalid
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	response, err := client.HTTP().Do(request)
	if err != nil {
		return "", err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return "", openlist.ErrUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, catalogSidecarMaxBytes+1))
	if err != nil {
		return "", err
	}
	if len(body) > catalogSidecarMaxBytes {
		return "", openlist.ErrLimit
	}
	return string(body), nil
}

func (e catalogEntry) SourceRootOrRoot() string {
	if e.SourceRoot != "" {
		return e.SourceRoot
	}
	return e.RootPath
}

// storeCatalogSidecars persists evidence for the matching worker. The write is
// best-effort relative to the audio publication: a readme failure must not
// remove a playable card.
func (s *Server) storeCatalogSidecars(ctx context.Context, id int64, cache catalogSidecarCache) error {
	raw, err := json.Marshal(cache)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE source_catalog_entry SET sidecar_json=? WHERE id=?`, string(raw), id)
	return err
}

// refreshCatalogSidecars reads sidecars for the cards this batch published.
// The caller passes the ids it just committed, so the cost follows the batch
// rather than the whole generation. A read failure never fails the published
// audio: it is retried on a later revision, not escalated to the scan result.
func (s *Server) refreshCatalogSidecars(ctx context.Context, job workflowJobRecord, sourceID int64, ids []int64) {
	if len(ids) == 0 {
		return
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = strings.TrimRight(placeholders, ",")
	args := make([]any, 0, len(ids)+1)
	args = append(args, sourceID)
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,root_path,COALESCE(source_root,''),title,COALESCE(primary_code,''),work_id,availability_status,sidecar_json
		FROM source_catalog_entry WHERE file_source_id=? AND relocated_to_id IS NULL AND id IN (`+placeholders+`)`, args...)
	if err != nil {
		return
	}
	type candidate struct {
		entry catalogEntry
		files []SourceCatalogFile
		raw   string
	}
	candidates := []candidate{}
	for rows.Next() {
		var item candidate
		var work sql.NullInt64
		if err = rows.Scan(&item.entry.ID, &item.entry.RootPath, &item.entry.SourceRoot, &item.entry.Title, &item.entry.PrimaryCode, &work, &item.entry.Status, &item.raw); err != nil {
			break
		}
		if work.Valid {
			item.entry.WorkID = &work.Int64
		}
		candidates = append(candidates, item)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return
	}
	if len(candidates) == 0 {
		return
	}
	source, err := s.catalogIndexSource(ctx, sourceID)
	if err != nil {
		return
	}
	for _, item := range candidates {
		files, readErr := s.loadSourceCatalogFiles(ctx, item.entry.ID)
		if readErr != nil {
			continue
		}
		if len(catalogSidecarFiles(files)) == 0 {
			continue
		}
		cache, readErr := s.loadCatalogSidecars(ctx, item.entry, files, source)
		if readErr != nil {
			continue
		}
		if len(cache.Sidecars) == 0 {
			// Nothing was readable and nothing was cached before. Keep the
			// column empty so a later successful scan can record evidence
			// without treating an empty result as a completed read.
			continue
		}
		_ = s.storeCatalogSidecars(ctx, item.entry.ID, cache)
	}
}
