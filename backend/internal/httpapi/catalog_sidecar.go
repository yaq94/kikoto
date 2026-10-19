package httpapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/yexca/kikoto/backend/internal/openlist"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/transform"
)

// Catalog sidecars are ordinary text files that a release folder publishes
// beside its audio. They are evidence for matching, not a work identity: a
// readme can name a voice actor or a circle, but it never supplies a product
// code and never registers a work.
const (
	catalogSidecarMaxBytes = 256 << 10
	catalogSidecarMaxFiles = 4
	// A sidecar pass is bounded by upstream requests, not cards: each selected
	// file costs one metadata request and one body request. The cap keeps a slow
	// source from occupying the single workflow executor for minutes.
	catalogSidecarBatchRequests = 12
	catalogSidecarBatchCards    = 32
	catalogSidecarPassTimeout   = 20 * time.Second
	// An unreadable file is retried by later passes, not in a tight loop.
	catalogSidecarMaxAttempts = 5
)

// The index warm pass and the independent worker can both observe the same
// pending row. One pass per source keeps a card from costing two upstream
// reads without serializing unrelated sources. A process-local lock is enough:
// a single Kikoto process owns the queue, and a restart resumes durable rows.
var catalogSidecarLocks sync.Map

func lockCatalogSidecarSource(sourceID int64) func() {
	value, _ := catalogSidecarLocks.LoadOrStore(sourceID, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	lock.Lock()
	return lock.Unlock
}

// A sidecar is fetched at most once per release revision. The source's
// modification time is the revision; the size and digest keep the comparison
// meaningful when a source omits or reuses that timestamp. A missing digest
// (including evidence written by an older release) is read once and then
// cached.
type catalogSidecarMeta struct {
	RelativePath string   `json:"relativePath"`
	Size         int64    `json:"size"`
	ModifiedAt   string   `json:"modifiedAt,omitempty"`
	Digest       string   `json:"digest,omitempty"`
	VoiceActors  []string `json:"voiceActors"`
	Circles      []string `json:"circles"`
	Title        string   `json:"title,omitempty"`
	FetchedAt    string   `json:"fetchedAt"`
}

type catalogSidecarCache struct {
	Sidecars []catalogSidecarMeta `json:"sidecars"`
}

// catalogSidecarCandidate is the narrow row the queued pass needs. It carries
// no work or user state; the queue exists only to read release text.
type catalogSidecarCandidate struct {
	ID         int64
	RootPath   string
	SourceRoot string
	Title      string
	Primary    string
	Status     string
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

// catalogSidecarFiles picks a small, stable set of files that can carry
// release metadata. Subtitle files are excluded: a transcript is not a credit
// list. Selection prefers files at the release root and names that describe
// credits, so a track-level info file cannot outrank the release readme.
func catalogSidecarFiles(files []SourceCatalogFile) []SourceCatalogFile {
	selected := []SourceCatalogFile{}
	for _, file := range files {
		if !catalogSidecarCandidateFile(file) {
			continue
		}
		selected = append(selected, file)
	}
	sort.SliceStable(selected, func(i, j int) bool {
		left, right := catalogSidecarFileRank(selected[i]), catalogSidecarFileRank(selected[j])
		if left != right {
			return left < right
		}
		if depthI, depthJ := strings.Count(selected[i].Path, "/"), strings.Count(selected[j].Path, "/"); depthI != depthJ {
			return depthI < depthJ
		}
		return selected[i].Path < selected[j].Path
	})
	if len(selected) > catalogSidecarMaxFiles {
		selected = selected[:catalogSidecarMaxFiles]
	}
	return selected
}

// currentCatalogFiles keeps the rows observed in the newest generation. A
// completed rescan removes a deleted readme from the staging tree, but the
// published file row can remain until the lifecycle cleanup pass. Reading that
// stale row would preserve evidence for a file the release no longer has.
func currentCatalogFiles(files []SourceCatalogFile) []SourceCatalogFile {
	var generation int64
	for _, file := range files {
		if file.Generation > generation {
			generation = file.Generation
		}
	}
	current := make([]SourceCatalogFile, 0, len(files))
	for _, file := range files {
		if file.Generation == generation {
			current = append(current, file)
		}
	}
	return current
}

// A release publishes text in several common extensions. The extension list is
// the role signal; the indexer's coarse "text" kind only covers a subset.
func catalogSidecarCandidateFile(file SourceCatalogFile) bool {
	if file.Kind == "folder" {
		return false
	}
	name := strings.ToLower(path.Base(file.Path))
	extension := path.Ext(name)
	switch extension {
	case ".md", ".markdown", ".nfo":
		// These formats almost always describe a release rather than hold a
		// transcript.
		return true
	case ".txt", ".html", ".htm":
		// A plain text file can be a transcript, and a saved web page can be
		// an unrelated store page. Only a name that says it is metadata is
		// treated as a credit source.
		return catalogSidecarNameHints(name)
	default:
		return false
	}
}

func catalogSidecarNameHints(name string) bool {
	for _, hint := range []string{"readme", "info", "credit", "staff", "介绍", "介紹"} {
		if strings.Contains(name, hint) {
			return true
		}
	}
	return false
}

// Rank, then path depth, chooses the four files closest to the release root.
// The names are conventional labels rather than one provider's layout.
func catalogSidecarFileRank(file SourceCatalogFile) int {
	name := strings.ToLower(path.Base(file.Path))
	switch {
	case strings.HasPrefix(name, "readme"):
		return 0
	case strings.Contains(name, "credit"), strings.Contains(name, "staff"):
		return 1
	case strings.Contains(name, "紹介"), strings.Contains(name, "介绍"), strings.Contains(name, "介紹"):
		return 2
	case strings.Contains(name, "info"):
		return 3
	default:
		return 4
	}
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

// catalogSidecarReadResult reports one bounded pass. The cache keeps every file
// that was read successfully, even when another file failed: a broken info
// file must not discard a good readme. Requests is the number of upstream calls
// the pass consumed, so the caller can continue only while its budget lasts.
type catalogSidecarReadResult struct {
	Cache    catalogSidecarCache
	Requests int
	Failed   bool
	Deferred bool
}

func readCatalogSidecars(ctx context.Context, client *openlist.Client, entry catalogEntry, files []SourceCatalogFile, previous catalogSidecarCache, requestBudget int) catalogSidecarReadResult {
	result := catalogSidecarReadResult{Cache: catalogSidecarCache{Sidecars: []catalogSidecarMeta{}}}
	// Evidence for a file the current scan no longer lists is stale. The file
	// rows are the authoritative set for this generation.
	published := map[string]bool{}
	for _, file := range files {
		published[file.Path] = true
	}
	known := map[string]catalogSidecarMeta{}
	for _, item := range previous.Sidecars {
		if published[item.RelativePath] {
			known[item.RelativePath] = item
		}
	}
	files = currentCatalogFiles(files)
	selected := catalogSidecarFiles(files)
	for _, file := range selected {
		if old, ok := known[file.Path]; ok && sidecarRevisionUnchanged(old, file) {
			result.Cache.Sidecars = append(result.Cache.Sidecars, old)
			continue
		}
		if result.Requests+2 > requestBudget {
			result.Deferred = true
			if old, ok := known[file.Path]; ok {
				result.Cache.Sidecars = append(result.Cache.Sidecars, old)
			}
			break
		}
		full := path.Join(entry.SourceRootOrRoot(), file.Path)
		object, getErr := client.Get(ctx, full)
		result.Requests++
		if getErr != nil {
			result.Failed = true
			keepCatalogSidecarEvidence(&result.Cache, known, file.Path)
			continue
		}
		body, readErr := readCatalogSidecar(ctx, client, object.RawURL)
		result.Requests++
		if readErr != nil {
			result.Failed = true
			keepCatalogSidecarEvidence(&result.Cache, known, file.Path)
			continue
		}
		voices, circles, title := parseCatalogSidecar(decodeCatalogSidecar(body))
		digest := sha256.Sum256([]byte(body))
		result.Cache.Sidecars = append(result.Cache.Sidecars, catalogSidecarMeta{
			RelativePath: file.Path, Size: file.Size, ModifiedAt: file.ModifiedAt,
			Digest:      hex.EncodeToString(digest[:]),
			VoiceActors: voices, Circles: circles, Title: title,
			FetchedAt: time.Now().UTC().Format(time.RFC3339),
		})
	}
	sort.Slice(result.Cache.Sidecars, func(i, j int) bool {
		return result.Cache.Sidecars[i].RelativePath < result.Cache.Sidecars[j].RelativePath
	})
	return result
}

func keepCatalogSidecarEvidence(cache *catalogSidecarCache, known map[string]catalogSidecarMeta, relativePath string) {
	if old, ok := known[relativePath]; ok {
		cache.Sidecars = append(cache.Sidecars, old)
	}
}

// The revision is the source's modification time plus the file size. A
// provider that omits the timestamp cannot reveal a same-size edit, so the
// cached digest remains the last checked body instead of an invented change.
func sidecarRevisionUnchanged(old catalogSidecarMeta, file SourceCatalogFile) bool {
	if old.Size != file.Size || old.Digest == "" {
		return false
	}
	if file.ModifiedAt == "" || old.ModifiedAt == "" {
		return true
	}
	return old.ModifiedAt == file.ModifiedAt
}

// Release readmes are commonly Shift-JIS or UTF-16 rather than UTF-8. Decode
// only known BOMs and the Japanese legacy encoding; everything else is already
// text under the parser's ASCII/Unicode labels.
func decodeCatalogSidecar(body string) string {
	raw := []byte(body)
	switch {
	case len(raw) >= 3 && raw[0] == 0xef && raw[1] == 0xbb && raw[2] == 0xbf:
		return string(raw[3:])
	case len(raw) >= 2 && raw[0] == 0xff && raw[1] == 0xfe:
		return decodeUTF16(raw[2:], false)
	case len(raw) >= 2 && raw[0] == 0xfe && raw[1] == 0xff:
		return decodeUTF16(raw[2:], true)
	}
	if utf8.Valid(raw) {
		return body
	}
	decoded, _, err := transform.String(japanese.ShiftJIS.NewDecoder(), body)
	if err == nil && utf8.ValidString(decoded) {
		return decoded
	}
	return body
}

func decodeUTF16(raw []byte, bigEndian bool) string {
	values := make([]uint16, 0, len(raw)/2)
	for index := 0; index+1 < len(raw); index += 2 {
		if bigEndian {
			values = append(values, uint16(raw[index])<<8|uint16(raw[index+1]))
		} else {
			values = append(values, uint16(raw[index+1])<<8|uint16(raw[index]))
		}
	}
	return string(utf16.Decode(values))
}

// loadCatalogSidecars reads the stored evidence for one card. It is shared by
// the bounded worker; the actual fetch budget is applied by the caller.
func (s *Server) loadCatalogSidecars(ctx context.Context, entry catalogEntry) (catalogSidecarCache, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT sidecar_json FROM source_catalog_entry WHERE id=?`, entry.ID).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return catalogSidecarCache{}, err
	}
	cache := catalogSidecarCache{Sidecars: []catalogSidecarMeta{}}
	if raw != "" {
		if err = json.Unmarshal([]byte(raw), &cache); err != nil {
			return catalogSidecarCache{}, err
		}
	}
	return cache, nil
}

func readCatalogSidecar(ctx context.Context, client *openlist.Client, rawURL string) (string, error) {
	return client.GetText(ctx, rawURL, catalogSidecarMaxBytes)
}

func (e catalogEntry) SourceRootOrRoot() string {
	if e.SourceRoot != "" {
		return e.SourceRoot
	}
	return e.RootPath
}

// storeCatalogSidecars persists evidence for the matching worker. The stable
// evidence, not the fetch timestamp, decides whether matching needs a new
// revision: re-reading the same credits must not restart the whole queue.
func (s *Server) storeCatalogSidecars(ctx context.Context, id int64, cache catalogSidecarCache) error {
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return err
	}
	defer release()
	var previous string
	if err = tx.QueryRowContext(ctx, `SELECT sidecar_json FROM source_catalog_entry WHERE id=?`, id).Scan(&previous); err != nil {
		return err
	}
	raw, err := json.Marshal(cache)
	if err != nil {
		return err
	}
	if previous == string(raw) || sameCatalogSidecarEvidence(previous, string(raw)) {
		return tx.Commit()
	}
	if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_entry SET sidecar_json=? WHERE id=?`, string(raw), id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE work_matching_control SET revision=revision+1,status='queued' WHERE id=1`); err != nil {
		return err
	}
	return tx.Commit()
}

// sameCatalogSidecarEvidence compares everything a match decision may read.
// FetchedAt is bookkeeping and is ignored.
func sameCatalogSidecarEvidence(left, right string) bool {
	normalize := func(raw string) string {
		var cache catalogSidecarCache
		if json.Unmarshal([]byte(raw), &cache) != nil {
			return raw
		}
		for index := range cache.Sidecars {
			cache.Sidecars[index].FetchedAt = ""
		}
		stable, err := json.Marshal(cache)
		if err != nil {
			return raw
		}
		return string(stable)
	}
	return normalize(left) == normalize(right)
}

// markCatalogSidecarCandidatesTx queues cards in this publication transaction
// and clears evidence for a readme the release no longer publishes. A card is
// queued only when it contains a file whose role can carry credits.
func markCatalogSidecarCandidatesTx(ctx context.Context, tx *sql.Tx, ids []int64, generation int64) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+2)
	for _, id := range ids {
		args = append(args, id)
	}
	args = append(args, generation)
	queueArgs := make([]any, 0, len(ids)+2)
	for _, id := range ids {
		queueArgs = append(queueArgs, id)
	}
	queueArgs = append(queueArgs, catalogSidecarMaxAttempts, generation)
	// A card whose file set was republished without its credit file loses the
	// evidence with it. Detect this from the same committed file rows rather
	// than from a name at read time, so a deleted readme cannot linger.
	cleared, err := tx.ExecContext(ctx, `UPDATE source_catalog_entry
			SET sidecar_json='',sidecar_pending=0,sidecar_attempts=0
			WHERE id IN (`+placeholders+`) AND sidecar_json<>'' AND NOT EXISTS(
			SELECT 1 FROM source_catalog_file f
			WHERE f.catalog_entry_id=source_catalog_entry.id AND f.kind<>'folder'
				AND f.observed_generation=?
				AND (instr(lower(f.title),'readme')>0 OR instr(lower(f.title),'info')>0
					OR instr(lower(f.title),'credit')>0 OR instr(lower(f.title),'staff')>0
					OR instr(f.title,'介绍')>0 OR instr(f.title,'介紹')>0
					OR lower(f.title) LIKE '%.md' OR lower(f.title) LIKE '%.markdown'
					OR lower(f.title) LIKE '%.nfo'))`, args...)
	if err != nil {
		return err
	}
	if changed, rowsErr := cleared.RowsAffected(); rowsErr == nil && changed > 0 {
		if _, err = tx.ExecContext(ctx, `UPDATE work_matching_control SET revision=revision+1,status='queued' WHERE id=1`); err != nil {
			return err
		}
	}
	// A rescan republishes the same card every day. A card that already spent
	// its attempts stays settled; a changed credit file is queued again through
	// the evidence-clearing branch above. Requeue only cards that still have
	// budget, so a permanently unreadable release is not retried every scan.
	_, err = tx.ExecContext(ctx, `UPDATE source_catalog_entry SET sidecar_pending=1
			WHERE id IN (`+placeholders+`) AND sidecar_attempts<?
			AND EXISTS(
			SELECT 1 FROM source_catalog_file f
			WHERE f.catalog_entry_id=source_catalog_entry.id AND f.kind<>'folder'
				AND f.observed_generation=?
				AND (instr(lower(f.title),'readme')>0 OR instr(lower(f.title),'info')>0
					OR instr(lower(f.title),'credit')>0 OR instr(lower(f.title),'staff')>0
					OR instr(f.title,'介绍')>0 OR instr(f.title,'介紹')>0
					OR lower(f.title) LIKE '%.md' OR lower(f.title) LIKE '%.markdown'
					OR lower(f.title) LIKE '%.nfo'))`, queueArgs...)
	return err
}

// refreshCatalogSidecars drains a bounded batch of queued cards. It runs after
// a publish transaction commits, so a slow readme never holds the write lock or
// delays a playable card. Returning true asks the caller to keep the job
// queued for an immediate continuation; a source that answered nothing is not
// continued at once, because that would spin on a failed origin.
func (s *Server) refreshCatalogSidecars(ctx context.Context, sourceID int64) (more bool) {
	unlock := lockCatalogSidecarSource(sourceID)
	defer unlock()
	source, err := s.catalogIndexSource(ctx, sourceID)
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, catalogSidecarPassTimeout)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT id,root_path,COALESCE(source_root,''),title,COALESCE(primary_code,''),availability_status
		FROM source_catalog_entry WHERE file_source_id=? AND relocated_to_id IS NULL
			AND sidecar_pending=1 AND sidecar_attempts<?
		ORDER BY id LIMIT ?`, sourceID, catalogSidecarMaxAttempts, catalogSidecarBatchCards)
	if err != nil {
		slog.Warn("read catalog sidecar queue", "source_id", sourceID, "error", err)
		return false
	}
	candidates := []catalogSidecarCandidate{}
	for rows.Next() {
		var item catalogSidecarCandidate
		if err = rows.Scan(&item.ID, &item.RootPath, &item.SourceRoot, &item.Title, &item.Primary, &item.Status); err != nil {
			break
		}
		candidates = append(candidates, item)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		slog.Warn("scan catalog sidecar queue", "source_id", sourceID, "error", err)
		return false
	}
	budget := catalogSidecarBatchRequests
	client := s.openListClient(source)
	reads, failures, deferred := 0, 0, false
	for _, item := range candidates {
		if ctx.Err() != nil || budget <= 0 {
			deferred = true
			break
		}
		result := s.refreshCatalogSidecar(ctx, client, item, budget)
		budget -= result.Requests
		reads += result.Reads
		if result.Failed {
			failures++
		}
		deferred = deferred || result.Deferred
	}
	if deferred {
		return true
	}
	// Continue immediately only when this pass made progress and queued cards
	// remain. When every card failed before a single read, the origin is not
	// answering; settle the job and let the normal retry delay apply instead of
	// spinning on the claim query.
	if reads == 0 && failures > 0 {
		return false
	}
	var remaining int
	if err = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM source_catalog_entry
		WHERE file_source_id=? AND relocated_to_id IS NULL
			AND sidecar_pending=1 AND sidecar_attempts<?)`,
		sourceID, catalogSidecarMaxAttempts).Scan(&remaining); err != nil {
		return false
	}
	return remaining != 0
}

type catalogSidecarCardOutcome struct {
	Requests int
	Reads    int
	Failed   bool
	Deferred bool
}

// refreshCatalogSidecar handles exactly one card and reports the requests it
// consumed. A failure is recorded on the card, so the caller can continue with
// the next candidate within the same budget.
func (s *Server) refreshCatalogSidecar(ctx context.Context, client *openlist.Client, item catalogSidecarCandidate, budget int) catalogSidecarCardOutcome {
	entry := catalogEntry{ID: item.ID, RootPath: item.RootPath, SourceRoot: item.SourceRoot, Title: item.Title, PrimaryCode: item.Primary, Status: item.Status}
	files, err := s.loadSourceCatalogFiles(ctx, item.ID)
	if err != nil {
		s.finishCatalogSidecar(ctx, item, catalogSidecarStateFailed, catalogSidecarReadResult{Failed: true})
		return catalogSidecarCardOutcome{Failed: true}
	}
	selected := catalogSidecarFiles(files)
	if len(selected) == 0 {
		// The text rows no longer carry a credit file, for example after a
		// rescan. Clear the queue entry and drop stale evidence for files that
		// no longer exist.
		s.finishCatalogSidecar(ctx, item, catalogSidecarStateEmpty, catalogSidecarReadResult{})
		return catalogSidecarCardOutcome{}
	}
	previous, err := s.loadCatalogSidecars(ctx, entry)
	if err != nil {
		s.finishCatalogSidecar(ctx, item, catalogSidecarStateFailed, catalogSidecarReadResult{Failed: true})
		return catalogSidecarCardOutcome{Failed: true}
	}
	result := readCatalogSidecars(ctx, client, entry, files, previous, budget)
	// A budget stop is not a failed read: the same evidence stays pending for
	// the next pass without consuming an attempt.
	state := catalogSidecarStateComplete
	if result.Failed || result.Deferred {
		state = catalogSidecarStateRetry
	}
	s.finishCatalogSidecar(ctx, item, state, result)
	return catalogSidecarCardOutcome{Requests: result.Requests, Reads: len(result.Cache.Sidecars), Failed: result.Failed, Deferred: result.Deferred}
}

type catalogSidecarState int

const (
	catalogSidecarStateComplete catalogSidecarState = iota
	catalogSidecarStateEmpty
	catalogSidecarStateRetry
	catalogSidecarStateFailed
)

// finishCatalogSidecar records one bounded outcome. Cards with some read files
// store the partial evidence; only a fully read card clears the queue. A
// failed card counts an attempt and is retried by a later pass. When no credit
// file remains, the old evidence is dropped because it describes files that
// are no longer published.
func (s *Server) finishCatalogSidecar(ctx context.Context, item catalogSidecarCandidate, state catalogSidecarState, result catalogSidecarReadResult) {
	if state == catalogSidecarStateRetry || state == catalogSidecarStateFailed {
		if len(result.Cache.Sidecars) > 0 {
			if err := s.storeCatalogSidecars(ctx, item.ID, result.Cache); err != nil {
				slog.Warn("store catalog sidecar evidence", "catalog_entry_id", item.ID, "error", err)
				return
			}
		}
		if state == catalogSidecarStateRetry && result.Deferred && !result.Failed {
			// The pass ran out of request budget, not the source. Keep the
			// card pending without spending an attempt.
			return
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE source_catalog_entry
				SET sidecar_attempts=sidecar_attempts+1,
					sidecar_pending=CASE WHEN sidecar_attempts+1>=? THEN 0 ELSE 1 END
				WHERE id=?`, catalogSidecarMaxAttempts, item.ID); err != nil {
			slog.Warn("record catalog sidecar attempt", "catalog_entry_id", item.ID, "error", err)
		}
		return
	}
	if len(result.Cache.Sidecars) > 0 {
		if err := s.storeCatalogSidecars(ctx, item.ID, result.Cache); err != nil {
			slog.Warn("store catalog sidecar evidence", "catalog_entry_id", item.ID, "error", err)
			return
		}
	} else if err := s.storeCatalogSidecars(ctx, item.ID, catalogSidecarCache{Sidecars: []catalogSidecarMeta{}}); err != nil {
		slog.Warn("clear catalog sidecar evidence", "catalog_entry_id", item.ID, "error", err)
		return
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE source_catalog_entry SET sidecar_pending=0 WHERE id=?`, item.ID); err != nil {
		slog.Warn("clear catalog sidecar queue", "catalog_entry_id", item.ID, "error", err)
	}
}
