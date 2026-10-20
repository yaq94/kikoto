package httpapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	// A release commonly publishes the same readme in several language
	// directories. Six leaves room for a root file plus a handful of
	// translations without turning every card into a broad text read.
	catalogSidecarMaxFiles = 6
	// A sidecar pass is bounded by upstream requests, not cards: each selected
	// file costs one metadata request and one body request. The cap keeps a slow
	// source from occupying the single workflow executor for minutes.
	catalogSidecarBatchRequests = 12
	catalogSidecarBatchCards    = 32
	catalogSidecarPassTimeout   = 20 * time.Second
	// An unreadable file is retried by later passes, not in a tight loop.
	catalogSidecarMaxAttempts = 5
	// A temporary outage must not exhaust the permanent budget, but it also
	// must not be retried on every pass. The wait doubles per failure and is
	// capped so a long outage settles into a slow probe.
	catalogSidecarTransientBase  = 15 * time.Minute
	catalogSidecarTransientMax   = 24 * time.Hour
	catalogSidecarTransientTries = 8
)

// catalogSidecarTextExtensions and catalogSidecarNameHints are the single
// vocabulary for credit files. The reader, the selection order and the
// publication SQL all derive from these lists: a hint added here cannot be
// accepted by one path and ignored by another.
var catalogSidecarTextExtensions = []string{".md", ".markdown", ".nfo", ".txt", ".html", ".htm"}

var catalogSidecarNameHints = []string{
	"readme", "read me", "read_me", "read-me",
	"info", "credit", "staff",
	"介绍", "介紹", "紹介", "説明書", "はじめに",
	"クレジット", "スタッフ",
	"りどみ", "りーどみー", "リードミー",
}

var catalogSidecarReadmeHints = []string{"readme", "read me", "read_me", "read-me"}
var catalogSidecarCreditHints = []string{"credit", "staff", "クレジット", "スタッフ"}
var catalogSidecarIntroductionHints = []string{"介绍", "介紹", "紹介", "説明書", "はじめに"}
var catalogSidecarInfoHints = []string{"info", "りどみ", "りーどみー", "リードミー"}

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

// A release publishes credit text under several extensions. The indexer's
// coarse "text" kind only covers a subset, so the extension and a name hint
// decide which files are worth one bounded read.
func catalogSidecarCandidateFile(file SourceCatalogFile) bool {
	if file.Kind == "folder" {
		return false
	}
	name := strings.ToLower(path.Base(file.Path))
	if !catalogSidecarTextFile(name) {
		return false
	}
	// A Markdown or NFO file can be a release page, but the same formats also
	// hold lyrics, notes or logs. The name is the only signal available before
	// a read, so require a conventional metadata word. A file without one is
	// not read; labelled credits inside it therefore do not justify spending a
	// request on every text file in a release.
	return catalogSidecarNamedCredit(name)
}

func catalogSidecarTextFile(name string) bool {
	extension := path.Ext(name)
	for _, accepted := range catalogSidecarTextExtensions {
		if extension == accepted {
			return true
		}
	}
	return false
}

// catalogSidecarNamedCredit accepts a hint only as a whole word. A bare
// substring test would treat "trackinfo.txt" or "infomation.txt" as credits,
// spending a request on a file that cannot carry them.
func catalogSidecarNamedCredit(name string) bool {
	for _, hint := range catalogSidecarNameHints {
		if catalogSidecarContainsWord(name, hint) {
			return true
		}
	}
	return false
}

// catalogSidecarContainsWord reports whether hint appears delimited from
// adjacent letters or digits. Non-ASCII hints have no case folding concerns and
// are matched directly; separators such as "_" and "-" count as delimiters.
func catalogSidecarContainsWord(name, hint string) bool {
	// CJK names have no word separators; a hint such as 介绍 is meaningful
	// wherever it appears in the name.
	if !catalogSidecarASCII(hint) {
		return strings.Contains(name, hint)
	}
	index := strings.Index(name, hint)
	for index >= 0 {
		before := index == 0 || !catalogSidecarAlphanumeric(rune(name[index-1]))
		afterIndex := index + len(hint)
		after := afterIndex >= len(name) || !catalogSidecarAlphanumeric(rune(name[afterIndex]))
		if before && after {
			return true
		}
		next := strings.Index(name[index+1:], hint)
		if next < 0 {
			return false
		}
		index += 1 + next
	}
	return false
}

func catalogSidecarAlphanumeric(value rune) bool {
	return value >= 'a' && value <= 'z' || value >= '0' && value <= '9'
}

func catalogSidecarASCII(value string) bool {
	for _, r := range value {
		if r > 0x7f {
			return false
		}
	}
	return true
}

// catalogSidecarSQLCandidateCondition selects every published text file by its
// real path. The name and extension decision belongs to
// catalogSidecarCandidateFile, which the caller applies to these rows;
// duplicating that decision in SQL is what let the reader and the publication
// path drift apart. This condition is deliberately a superset.
var catalogSidecarSQLCandidateCondition = buildCatalogSidecarSQLCondition()

func buildCatalogSidecarSQLCondition() string {
	extensions := make([]string, 0, len(catalogSidecarTextExtensions))
	for _, extension := range catalogSidecarTextExtensions {
		extensions = append(extensions, "lower(f.relative_path) LIKE '%"+extension+"'")
	}
	return `
	(f.kind <> 'folder'
		AND (` + strings.Join(extensions, " OR ") + `))`
}

// Rank, then path depth, chooses the files closest to the release root.
// The names are conventional labels rather than one provider's layout.
func catalogSidecarFileRank(file SourceCatalogFile) int {
	name := strings.ToLower(path.Base(file.Path))
	switch {
	case catalogSidecarMatchesAny(name, catalogSidecarReadmeHints):
		return 0
	case catalogSidecarMatchesAny(name, catalogSidecarCreditHints):
		return 1
	case catalogSidecarMatchesAny(name, catalogSidecarIntroductionHints):
		return 2
	case catalogSidecarMatchesAny(name, catalogSidecarInfoHints):
		return 3
	default:
		return 4
	}
}

func catalogSidecarMatchesAny(name string, hints []string) bool {
	for _, hint := range hints {
		if catalogSidecarContainsWord(name, hint) {
			return true
		}
	}
	return false
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
	Revision string
	Requests int
	// Reads counts files fetched from the source in this pass. Reused stored
	// evidence is not progress: a card that failed every request but still has
	// older credit text must not look like a productive pass and restart the
	// worker immediately.
	Reads    int
	Failed   bool
	Deferred bool
	// Transient marks at least one failure that heals without the release
	// changing: an outage, refusal, rate limit or timeout. Permanent marks a
	// failure that will not heal: a missing object or a body above the read
	// limit. A card that has both waits on the transient backoff, because the
	// deleted sibling must not turn a recoverable outage into a settled card.
	Transient bool
	Permanent bool
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
			if errors.Is(getErr, openlist.ErrNotFound) {
				result.Permanent = true
			} else {
				result.Transient = true
			}
			keepCatalogSidecarEvidence(&result.Cache, known, file.Path)
			continue
		}
		body, readErr := readCatalogSidecar(ctx, client, object.RawURL)
		result.Requests++
		if readErr != nil {
			result.Failed = true
			if errors.Is(readErr, openlist.ErrNotFound) || errors.Is(readErr, openlist.ErrLimit) ||
				errors.Is(readErr, openlist.ErrInvalid) {
				result.Permanent = true
			} else {
				result.Transient = true
			}
			keepCatalogSidecarEvidence(&result.Cache, known, file.Path)
			continue
		}
		voices, circles, title := parseCatalogSidecar(decodeCatalogSidecar(body))
		result.Reads++
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
	result.Revision = catalogSidecarRevision(files)
	return result
}

// catalogSidecarRevision is the cheap revision of the published credit files.
// It is stored beside the evidence so a rescan can tell an exhausted, unchanged
// file from one the release replaced. One function owns the selected set and
// its order; a second copy of this rule drifted from the reader once already.
func catalogSidecarRevision(files []SourceCatalogFile) string {
	selected := catalogSidecarFiles(currentCatalogFiles(files))
	if len(selected) == 0 {
		return ""
	}
	parts := make([]string, 0, len(selected))
	for _, file := range selected {
		parts = append(parts, fmt.Sprintf("%s|%d|%s", file.Path, file.Size, file.ModifiedAt))
	}
	return strings.Join(parts, "\n")
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
func (s *Server) storeCatalogSidecars(ctx context.Context, id int64, cache catalogSidecarCache, revision string) error {
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return err
	}
	defer release()
	var previous string
	var previousRevision string
	if err = tx.QueryRowContext(ctx, `SELECT sidecar_json,sidecar_revision FROM source_catalog_entry WHERE id=?`, id).Scan(&previous, &previousRevision); err != nil {
		return err
	}
	raw, err := json.Marshal(cache)
	if err != nil {
		return err
	}
	if previous == string(raw) {
		// A release can rewrite the same bytes with a new timestamp. Recording
		// the current revision keeps the next rescan from reading it again.
		if revision == previousRevision {
			return tx.Commit()
		}
		if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_entry SET sidecar_revision=? WHERE id=?`, revision, id); err != nil {
			return err
		}
		return tx.Commit()
	}
	unchanged := sameCatalogSidecarEvidence(previous, string(raw))
	if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_entry SET sidecar_json=?,sidecar_revision=? WHERE id=?`, string(raw), revision, id); err != nil {
		return err
	}
	if unchanged {
		// Only bookkeeping changed: the credits a match decision reads are the
		// same, so the matching revision must not restart the queue.
		return tx.Commit()
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
	placeholders := sqlPlaceholders(len(ids))
	// A card whose file set was republished without its credit file loses the
	// evidence with it. Detect this from the same committed file rows rather
	// than from a name at read time, so a deleted readme cannot linger.
	// The SQL condition is only an extension filter; the reader's own candidate
	// rule decides whether a published file can carry credits. Ask it about the
	// committed rows instead of writing a second name rule in SQL.
	rows, err := tx.QueryContext(ctx, `SELECT e.id,f.relative_path,COALESCE(f.title,''),f.kind,f.size_bytes,COALESCE(f.modified_at,''),f.observed_generation
		FROM source_catalog_entry e
		JOIN source_catalog_file f ON f.catalog_entry_id=e.id
		WHERE e.id IN (`+placeholders+`) AND e.sidecar_json<>'' AND f.observed_generation=?
			AND (`+catalogSidecarSQLCandidateCondition+`)
		ORDER BY e.id`, append(int64Args(ids), generation)...)
	if err != nil {
		return err
	}
	byEntry := map[int64][]SourceCatalogFile{}
	for rows.Next() {
		var id int64
		var file SourceCatalogFile
		if err = rows.Scan(&id, &file.Path, &file.Title, &file.Kind, &file.Size, &file.ModifiedAt, &file.Generation); err != nil {
			_ = rows.Close()
			return err
		}
		byEntry[id] = append(byEntry[id], file)
	}
	evidenceErr := rows.Err()
	_ = rows.Close()
	if evidenceErr != nil {
		return evidenceErr
	}
	clearedIDs := []int64{}
	for _, id := range ids {
		if len(catalogSidecarFiles(byEntry[id])) == 0 {
			clearedIDs = append(clearedIDs, id)
		}
	}
	if len(clearedIDs) > 0 {
		cleared, clearErr := tx.ExecContext(ctx, `UPDATE source_catalog_entry
				SET sidecar_json='',sidecar_pending=0,sidecar_attempts=0,sidecar_revision=''
				WHERE id IN (`+sqlPlaceholders(len(clearedIDs))+`) AND sidecar_json<>''`, int64Args(clearedIDs)...)
		if clearErr != nil {
			return clearErr
		}
		if changed, rowsErr := cleared.RowsAffected(); rowsErr == nil && changed > 0 {
			if _, err = tx.ExecContext(ctx, `UPDATE work_matching_control SET revision=revision+1,status='queued' WHERE id=1`); err != nil {
				return err
			}
		}
	}
	// A rescan republishes the same card every day. Requeue a card with budget,
	// and also requeue an exhausted card when the credit file's revision changed
	// since the last read. Without the second case a release that was briefly
	// unavailable would never be read again after five failed scans.
	// Compare each card's current credit revision with the one stored with its
	// evidence. Reading inside the transaction keeps the choice consistent with
	// the publication rows just written.
	rows, err = tx.QueryContext(ctx, `SELECT id,sidecar_attempts,sidecar_transient_attempts,sidecar_revision FROM source_catalog_entry
		WHERE id IN (`+placeholders+`)`, int64Args(ids)...)
	if err != nil {
		return err
	}
	type queueRow struct {
		id, attempts, transient int64
		revision                string
	}
	queued := []queueRow{}
	for rows.Next() {
		var item queueRow
		if err = rows.Scan(&item.id, &item.attempts, &item.transient, &item.revision); err != nil {
			_ = rows.Close()
			return err
		}
		queued = append(queued, item)
	}
	readErr := rows.Err()
	_ = rows.Close()
	if readErr != nil {
		return readErr
	}
	for _, item := range queued {
		current, revisionErr := publishedCatalogSidecarRevision(ctx, tx, item.id, generation)
		if revisionErr != nil {
			return revisionErr
		}
		if current == "" {
			continue
		}
		// An empty stored revision means the card never completed a read. It is
		// not a change: resetting here would make the five-attempt budget
		// restart on every rescan, so a permanently broken release would be
		// retried forever. Store the observed revision so the next rescan has a
		// baseline to compare, but keep the attempts and the backoff.
		unchanged := item.revision == current || (item.revision == "" && (item.attempts > 0 || item.transient > 0))
		if unchanged {
			// A rescan is not a read. A card with remaining permanent budget
			// stays queued, a permanently exhausted card stays settled, and a
			// transient card that reached the stop threshold stays stopped
			// until an operator reopens it.
			if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_entry
					SET sidecar_revision=CASE WHEN sidecar_revision='' THEN ? ELSE sidecar_revision END,
						sidecar_pending=CASE
							WHEN sidecar_transient_attempts>=? AND sidecar_failure='temporarily_unavailable'
							THEN sidecar_pending ELSE 1 END,
						sidecar_attempts=?
					WHERE id=?`, current, catalogSidecarTransientTries, item.attempts, item.id); err != nil {
				return err
			}
			continue
		}
		// The release published new credit files. Clear the temporary backoff
		// so the new revision is read now rather than after the old wait.
		if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_entry
				SET sidecar_revision=?,sidecar_pending=1,sidecar_attempts=?,sidecar_transient_attempts=0,sidecar_retry_at='',sidecar_failure=''
				WHERE id=?`, current, 0, item.id); err != nil {
			return err
		}
	}
	return nil
}

func publishedCatalogSidecarRevision(ctx context.Context, tx *sql.Tx, id, generation int64) (string, error) {
	// Read the rows and reuse the reader's own selection. A second SQL copy of
	// the rank, depth and file-count rules had already ordered the same set
	// differently, so an unchanged release looked changed on every rescan.
	rows, err := tx.QueryContext(ctx, `SELECT f.relative_path,f.size_bytes,COALESCE(f.modified_at,''),f.kind,f.observed_generation
		FROM source_catalog_file f
		WHERE f.catalog_entry_id=? AND f.kind<>'folder' AND f.observed_generation=?
			AND (`+catalogSidecarSQLCandidateCondition+`)
		ORDER BY f.relative_path`, id, generation)
	if err != nil {
		return "", err
	}
	files := []SourceCatalogFile{}
	for rows.Next() {
		var file SourceCatalogFile
		if err = rows.Scan(&file.Path, &file.Size, &file.ModifiedAt, &file.Kind, &file.Generation); err != nil {
			_ = rows.Close()
			return "", err
		}
		file.Title = path.Base(file.Path)
		files = append(files, file)
	}
	readErr := rows.Err()
	_ = rows.Close()
	if readErr != nil {
		return "", readErr
	}
	return catalogSidecarRevision(files), nil
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
	passCtx, cancel := context.WithTimeout(ctx, catalogSidecarPassTimeout)
	defer cancel()
	rows, err := s.db.QueryContext(passCtx, `SELECT id,root_path,COALESCE(source_root,''),title,COALESCE(primary_code,''),availability_status
		FROM source_catalog_entry WHERE file_source_id=? AND relocated_to_id IS NULL
			AND sidecar_pending=1 AND sidecar_attempts<?
			AND (sidecar_retry_at='' OR sidecar_retry_at<=CURRENT_TIMESTAMP)
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
		if passCtx.Err() != nil || budget <= 0 {
			deferred = true
			break
		}
		result := s.refreshCatalogSidecar(passCtx, client, item, budget)
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
			AND sidecar_pending=1 AND sidecar_attempts<?
			AND (sidecar_retry_at='' OR sidecar_retry_at<=CURRENT_TIMESTAMP))`,
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
		// A local read failure says nothing about the release, so it must not
		// spend the permanent budget.
		s.finishCatalogSidecar(ctx, item, catalogSidecarStateFailed, catalogSidecarReadResult{Failed: true, Transient: true})
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
		s.finishCatalogSidecar(ctx, item, catalogSidecarStateFailed, catalogSidecarReadResult{Failed: true, Transient: true})
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
	return catalogSidecarCardOutcome{Requests: result.Requests, Reads: result.Reads, Failed: result.Failed, Deferred: result.Deferred}
}

type catalogSidecarState int

const (
	catalogSidecarStateComplete catalogSidecarState = iota
	catalogSidecarStateEmpty
	catalogSidecarStateRetry
	catalogSidecarStateFailed
)

// catalogSidecarFailureReason is the stored category shown to an operator. It
// keeps the distinction that decides retry behaviour, so an administrator can
// tell an outage from a deleted file without reading the raw error.
func catalogSidecarFailureReason(result catalogSidecarReadResult) string {
	switch {
	case result.Transient:
		return "temporarily_unavailable"
	case result.Permanent:
		return "unreadable"
	case result.Deferred:
		return "request_budget"
	case result.Failed:
		// An unclassified failure is not evidence of a deleted file. Report it
		// as temporary so the permanent budget is not spent on it.
		return "temporarily_unavailable"
	default:
		return ""
	}
}

// catalogSidecarTransientFailure reports whether the card should wait on the
// temporary backoff. A mixed result (one deleted file, one outage) is
// transient: the deleted file is already permanent, but settling the card now
// would drop the file that can still be read when the source recovers.
func catalogSidecarTransientFailure(result catalogSidecarReadResult) bool {
	return result.Failed && (!result.Permanent || result.Transient)
}

// catalogSidecarTransientDelay grows the wait between temporary retries. The
// first retry waits 15 minutes, then doubles up to a daily probe. It is a pure
// function so the policy is testable without sleeping.
func catalogSidecarTransientDelay(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	delay := catalogSidecarTransientBase
	for i := 1; i < attempts && delay < catalogSidecarTransientMax; i++ {
		delay *= 2
	}
	if delay > catalogSidecarTransientMax {
		return catalogSidecarTransientMax
	}
	return delay
}

// catalogSidecarRetryOffset renders a duration as a SQLite modifier so the
// retry timestamp is produced by the same clock (CURRENT_TIMESTAMP) that the
// queue compares against.
func catalogSidecarRetryOffset(delay time.Duration) string {
	return fmt.Sprintf("+%d seconds", int64(delay/time.Second))
}

// finishCatalogSidecar records one bounded outcome. Cards with some read files
// store the partial evidence; only a fully read card clears the queue. A
// failed card counts an attempt and is retried by a later pass. When no credit
// file remains, the old evidence is dropped because it describes files that
// are no longer published.
func (s *Server) finishCatalogSidecar(ctx context.Context, item catalogSidecarCandidate, state catalogSidecarState, result catalogSidecarReadResult) {
	if state == catalogSidecarStateRetry || state == catalogSidecarStateFailed {
		if len(result.Cache.Sidecars) > 0 {
			if err := s.storeCatalogSidecars(ctx, item.ID, result.Cache, result.Revision); err != nil {
				slog.Warn("store catalog sidecar evidence", "catalog_entry_id", item.ID, "error", err)
				return
			}
		}
		if result.Revision != "" {
			// Record the revision even when no file could be read. A failed
			// first read otherwise leaves an empty revision that the next
			// rescan mistakes for a change, restarting the budget.
			if _, err := s.db.ExecContext(ctx, `UPDATE source_catalog_entry SET sidecar_revision=? WHERE id=?`, result.Revision, item.ID); err != nil {
				slog.Warn("record catalog sidecar revision", "catalog_entry_id", item.ID, "error", err)
				return
			}
		}
		if state == catalogSidecarStateRetry && result.Deferred && !result.Failed {
			// The pass ran out of request budget, not the source. Any files it
			// did read were stored above; keep the card pending without
			// spending an attempt so the next pass continues from that partial
			// evidence instead of requesting the same files again.
			return
		}
		if catalogSidecarTransientFailure(result) {
			// An outage, credential refusal, rate limit or timeout heals
			// without the release changing. Wait on a bounded backoff and do
			// not spend the permanent budget, so a recovered source is read
			// again instead of staying settled forever.
			var transient int
			if err := s.db.QueryRowContext(ctx, `SELECT sidecar_transient_attempts FROM source_catalog_entry WHERE id=?`, item.ID).Scan(&transient); err != nil {
				slog.Warn("read catalog sidecar retry count", "catalog_entry_id", item.ID, "error", err)
				return
			}
			transient++
			pending := 1
			if transient >= catalogSidecarTransientTries {
				// A source that stays down for a week stops automatic probing;
				// the operator can reopen it without editing the file.
				pending = 0
			}
			if _, err := s.db.ExecContext(ctx, `UPDATE source_catalog_entry
					SET sidecar_transient_attempts=?,sidecar_retry_at=datetime('now',?),sidecar_failure=?,sidecar_pending=?
					WHERE id=?`, transient, catalogSidecarRetryOffset(catalogSidecarTransientDelay(transient)),
				catalogSidecarFailureReason(result), pending, item.ID); err != nil {
				slog.Warn("record catalog sidecar retry", "catalog_entry_id", item.ID, "error", err)
			}
			return
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE source_catalog_entry
				SET sidecar_attempts=sidecar_attempts+1,sidecar_failure=?,
					sidecar_transient_attempts=0,sidecar_retry_at='',
					sidecar_pending=CASE WHEN sidecar_attempts+1>=? THEN 0 ELSE 1 END
				WHERE id=?`, catalogSidecarFailureReason(result), catalogSidecarMaxAttempts, item.ID); err != nil {
			slog.Warn("record catalog sidecar attempt", "catalog_entry_id", item.ID, "error", err)
		}
		return
	}
	if len(result.Cache.Sidecars) > 0 {
		if err := s.storeCatalogSidecars(ctx, item.ID, result.Cache, result.Revision); err != nil {
			slog.Warn("store catalog sidecar evidence", "catalog_entry_id", item.ID, "error", err)
			return
		}
	} else if err := s.storeCatalogSidecars(ctx, item.ID, catalogSidecarCache{Sidecars: []catalogSidecarMeta{}}, result.Revision); err != nil {
		slog.Warn("clear catalog sidecar evidence", "catalog_entry_id", item.ID, "error", err)
		return
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE source_catalog_entry
			SET sidecar_pending=0,sidecar_transient_attempts=0,sidecar_retry_at='',sidecar_failure=''
			WHERE id=?`, item.ID); err != nil {
		slog.Warn("clear catalog sidecar queue", "catalog_entry_id", item.ID, "error", err)
	}
}
