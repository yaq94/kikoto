// Package catalogprune plans and applies the one-off removal of catalog cards
// that can never pass admission. It is deliberately outside the HTTP API: the
// removal is audited by a dry-run report and runs in bounded batches.
package catalogprune

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/yexca/kikoto/backend/internal/catalogadmission"
)

// WhitelistFromEnv reads the operator-supplied subtree that must survive a
// parent-card deletion. The path belongs to one deployment, so it is not
// committed: the CLI passes it explicitly or through this variable.
func WhitelistFromEnv() []string {
	var roots []string
	for _, item := range strings.Split(os.Getenv("KIKOTO_CATALOG_PRUNE_WHITELIST"), ",") {
		if item = strings.TrimSpace(item); item != "" {
			roots = append(roots, item)
		}
	}
	return roots
}

type Options struct {
	Whitelist  []string
	BatchSize  int
	MaxEntries int
}

type Entry struct {
	EntryID       int64  `json:"entryId"`
	SourceID      int64  `json:"sourceId"`
	Root          string `json:"root"`
	EffectiveRoot string `json:"effectiveRoot"`
	Title         string `json:"title"`
	Files         int    `json:"files"`
	Audio         int    `json:"audio"`
	NonAudio      int    `json:"nonAudio"`
	Reason        string `json:"reason"`
	Observations  int    `json:"observations"`
	Directories   int    `json:"directories"`
	AudioRoots    int    `json:"audioRoots"`
	TransferRefs  int    `json:"transferRefs"`
	// KeptRows counts path rows below the card that a whitelist or a preserved
	// entry keeps. They are reported so a large difference from the raw subtree
	// is visible before apply.
	KeptRows int `json:"keptRows"`
}

type Skipped struct {
	EntryID int64  `json:"entryId"`
	Root    string `json:"root"`
	Reason  string `json:"reason"`
}

type Totals struct {
	Entries      int `json:"entries"`
	Files        int `json:"files"`
	Audio        int `json:"audio"`
	NonAudio     int `json:"nonAudio"`
	Observations int `json:"observations"`
	Directories  int `json:"directories"`
	AudioRoots   int `json:"audioRoots"`
	TransferRefs int `json:"transferRefs"`
	KeptRows     int `json:"keptRows"`
}

type Report struct {
	GeneratedAt string    `json:"generatedAt"`
	Note        string    `json:"note,omitempty"`
	Whitelist   []string  `json:"whitelist"`
	Entries     []Entry   `json:"entries"`
	Skipped     []Skipped `json:"skipped"`
	// Totals describes the plan that Apply will execute. Candidates counts
	// every card that failed admission before the whitelist, series and alias
	// exceptions were applied, so an operator can reconcile the audit number
	// against the smaller deletion plan.
	Candidates Totals `json:"candidateTotals"`
	Totals     Totals `json:"totals"`
}

// BatchResult describes what one transaction removed and verified.
type BatchResult struct {
	Batch        int `json:"batch"`
	Entries      int `json:"entries"`
	Files        int `json:"files"`
	Observations int `json:"observations"`
	Directories  int `json:"directories"`
	AudioRoots   int `json:"audioRoots"`
	TransferRefs int `json:"transferRefs"`
}

type ApplyResult struct {
	Batches []BatchResult `json:"batches"`
	// AlreadyApplied counts report entries that a previous interrupted run
	// had already removed and verified. A rerun with the same report is safe.
	AlreadyApplied int         `json:"alreadyApplied"`
	Totals         BatchResult `json:"totals"`
}

type entryRow struct {
	id, sourceID int64
	root, src    string
	title        string
	files        int
	audio        int
	nonAudio     int
}

type pathKey struct {
	sourceID int64
	path     string
}

type reportBuilder struct {
	entries   []Entry
	entryIdx  map[pathKey]int
	entryIDs  map[int64]bool
	protected map[pathKey]bool
	whitelist []string
}

func normalizeRoots(values []string) []string {
	seen := map[string]bool{}
	roots := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || value == "/" || !strings.HasPrefix(value, "/") {
			continue
		}
		value = path.Clean(value)
		if seen[value] {
			continue
		}
		seen[value] = true
		roots = append(roots, value)
	}
	return roots
}

func effectiveRoot(root, sourceRoot string) string {
	if sourceRoot != "" {
		return sourceRoot
	}
	return root
}

func rootDepth(value string) int { return strings.Count(value, "/") }

// BuildReport reads the catalog and produces the audited deletion plan. It
// never writes. Cards referenced by a series or a relocation alias are listed
// as skipped instead of being removed, because those references must survive.
func BuildReport(ctx context.Context, db *sql.DB, options Options) (Report, error) {
	if options.BatchSize <= 0 {
		options.BatchSize = 100
	}
	whitelist := normalizeRoots(options.Whitelist)
	if len(whitelist) == 0 {
		// A deployment that needs a protected subtree passes it explicitly; an
		// empty whitelist protects nothing rather than baking one operator's
		// path into the binary.
		whitelist = normalizeRoots(WhitelistFromEnv())
	}
	report := Report{GeneratedAt: time.Now().UTC().Format(time.RFC3339), Whitelist: whitelist, Entries: []Entry{}, Skipped: []Skipped{}}

	rows, err := db.QueryContext(ctx, `SELECT e.id, COALESCE(e.file_source_id,0), e.root_path, e.source_root, e.title,
		(SELECT count(*) FROM source_catalog_file f WHERE f.catalog_entry_id=e.id),
		(SELECT count(*) FROM source_catalog_file f WHERE f.catalog_entry_id=e.id AND f.kind='audio'),
		(SELECT count(*) FROM source_catalog_file f WHERE f.catalog_entry_id=e.id AND f.kind<>'audio')
		FROM source_catalog_entry e
		WHERE e.relocated_to_id IS NULL AND e.file_source_id IS NOT NULL
		ORDER BY e.id`)
	if err != nil {
		return Report{}, err
	}
	all := []entryRow{}
	for rows.Next() {
		var r entryRow
		if err = rows.Scan(&r.id, &r.sourceID, &r.root, &r.src, &r.title, &r.files, &r.audio, &r.nonAudio); err != nil {
			break
		}
		all = append(all, r)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return Report{}, err
	}

	zeroAudioObservations, err := zeroFileAudioObservations(ctx, db)
	if err != nil {
		return Report{}, err
	}
	candidates := []entryRow{}
	for _, r := range all {
		invalid := r.files > 0 && catalogadmission.Decision(r.files, r.audio, r.nonAudio) != ""
		broken := r.files == 0 && zeroAudioObservations[r.id]
		if invalid || broken {
			candidates = append(candidates, r)
		}
	}
	candidateTotals := Totals{}
	for _, r := range candidates {
		candidateTotals.Entries++
		candidateTotals.Files += r.files
		candidateTotals.Audio += r.audio
		candidateTotals.NonAudio += r.nonAudio
	}
	if options.MaxEntries > 0 && len(candidates) > options.MaxEntries {
		candidates = candidates[:options.MaxEntries]
	}
	candidateIDs := make([]int64, 0, len(candidates))
	for _, r := range candidates {
		candidateIDs = append(candidateIDs, r.id)
	}
	seriesMembers, err := idSet(ctx, db, `SELECT catalog_entry_id FROM catalog_series_member WHERE catalog_entry_id IN (`+placeholders(len(candidateIDs))+`)`, candidateIDs)
	if err != nil {
		return Report{}, err
	}
	relocationTargets, err := idSet(ctx, db, `SELECT DISTINCT relocated_to_id FROM source_catalog_entry WHERE relocated_to_id IN (`+placeholders(len(candidateIDs))+`)`, candidateIDs)
	if err != nil {
		return Report{}, err
	}

	builder := reportBuilder{
		entryIdx:  map[pathKey]int{},
		entryIDs:  map[int64]bool{},
		protected: map[pathKey]bool{},
		whitelist: whitelist,
	}
	deleted := map[int64]bool{}
	for _, r := range candidates {
		if underAnyRoot(effectiveRoot(r.root, r.src), whitelist) {
			// The whitelist is a subtree exception: a card inside it is never
			// removed, even when it would fail admission on its own.
			report.Skipped = append(report.Skipped, Skipped{EntryID: r.id, Root: r.root, Reason: "whitelisted"})
			continue
		}
		switch {
		case seriesMembers[r.id]:
			report.Skipped = append(report.Skipped, Skipped{EntryID: r.id, Root: r.root, Reason: "series_member"})
			continue
		case relocationTargets[r.id]:
			report.Skipped = append(report.Skipped, Skipped{EntryID: r.id, Root: r.root, Reason: "relocation_target"})
			continue
		}
		deleted[r.id] = true
	}
	for _, r := range all {
		if deleted[r.id] {
			continue
		}
		root := effectiveRoot(r.root, r.src)
		if root != "" {
			builder.protected[pathKey{r.sourceID, root}] = true
		}
	}
	for _, root := range whitelist {
		// A whitelist is source-agnostic in configuration. It protects every
		// source whose card sits at that path, which is the safe direction.
		builder.protected[pathKey{0, root}] = true
	}

	for _, r := range candidates {
		if !deleted[r.id] {
			continue
		}
		root := effectiveRoot(r.root, r.src)
		entry := Entry{
			EntryID: r.id, SourceID: r.sourceID, Root: r.root, EffectiveRoot: root, Title: r.title,
			Files: r.files, Audio: r.audio, NonAudio: r.nonAudio,
			Reason: catalogadmission.Decision(r.files, r.audio, r.nonAudio),
		}
		if r.files == 0 {
			entry.Reason = "empty_audio_card"
		}
		builder.entries = append(builder.entries, entry)
		builder.entryIDs[r.id] = true
	}
	// Deepest first, so a nested card owns its own paths instead of its parent.
	sort.Slice(builder.entries, func(i, j int) bool {
		li, lj := rootDepth(builder.entries[i].EffectiveRoot), rootDepth(builder.entries[j].EffectiveRoot)
		if li != lj {
			return li > lj
		}
		return builder.entries[i].EntryID < builder.entries[j].EntryID
	})
	for i := range builder.entries {
		builder.entryIdx[pathKey{builder.entries[i].SourceID, builder.entries[i].EffectiveRoot}] = i
	}

	if err = attributePaths(ctx, db, &builder); err != nil {
		return Report{}, err
	}
	refs, err := transferRefCounts(ctx, db)
	if err != nil {
		return Report{}, err
	}
	for _, r := range candidates {
		candidateTotals.TransferRefs += refs[r.id]
	}
	report.Candidates = candidateTotals
	for i := range builder.entries {
		builder.entries[i].TransferRefs = refs[builder.entries[i].EntryID]
	}
	sort.Slice(builder.entries, func(i, j int) bool { return builder.entries[i].EntryID < builder.entries[j].EntryID })
	report.Entries = builder.entries
	for _, entry := range report.Entries {
		report.Totals.Entries++
		report.Totals.Files += entry.Files
		report.Totals.Audio += entry.Audio
		report.Totals.NonAudio += entry.NonAudio
		report.Totals.Observations += entry.Observations
		report.Totals.Directories += entry.Directories
		report.Totals.AudioRoots += entry.AudioRoots
		report.Totals.TransferRefs += entry.TransferRefs
		report.Totals.KeptRows += entry.KeptRows
	}
	return report, nil
}

func zeroFileAudioObservations(ctx context.Context, db *sql.DB) (map[int64]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT e.id
		FROM source_catalog_entry e
		JOIN source_catalog_observation o ON o.file_source_id=e.file_source_id AND o.directory_path=e.root_path AND o.kind='audio'
		WHERE NOT EXISTS(SELECT 1 FROM source_catalog_file f WHERE f.catalog_entry_id=e.id)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		result[id] = true
	}
	return result, rows.Err()
}

func idSet(ctx context.Context, db *sql.DB, query string, args []int64) (map[int64]bool, error) {
	if len(args) == 0 {
		return map[int64]bool{}, nil
	}
	rows, err := db.QueryContext(ctx, query, int64Args(args)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		result[id] = true
	}
	return result, rows.Err()
}

func transferRefCounts(ctx context.Context, db *sql.DB) (map[int64]int, error) {
	rows, err := db.QueryContext(ctx, `SELECT catalog_entry_id, count(*) FROM personal_transfer_ref
		WHERE catalog_entry_id IS NOT NULL GROUP BY catalog_entry_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[int64]int{}
	for rows.Next() {
		var id int64
		var n int
		if err = rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		result[id] = n
	}
	return result, rows.Err()
}

// attributePaths assigns every observation, directory and audio-root row to at
// most one card. Rows under a preserved descendant (the audio whitelist) are
// counted as kept and never assigned.
func attributePaths(ctx context.Context, db *sql.DB, builder *reportBuilder) error {
	type tableSpec struct {
		table, column string
		assign        func(*Entry)
	}
	specs := []tableSpec{
		{"source_catalog_observation", "directory_path", func(e *Entry) { e.Observations++ }},
		{"source_catalog_directory", "path", func(e *Entry) { e.Directories++ }},
		{"source_catalog_audio_root", "catalog_root", func(e *Entry) { e.AudioRoots++ }},
	}
	for _, spec := range specs {
		rows, err := db.QueryContext(ctx, `SELECT file_source_id, `+spec.column+` FROM `+spec.table)
		if err != nil {
			return err
		}
		for rows.Next() {
			var sourceID int64
			var value string
			if err = rows.Scan(&sourceID, &value); err != nil {
				rows.Close()
				return err
			}
			idx, ok := builder.owner(sourceID, value)
			if !ok {
				continue
			}
			if _, kept := builder.keptBy(sourceID, value); kept {
				builder.entries[idx].KeptRows++
				continue
			}
			spec.assign(&builder.entries[idx])
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return err
		}
		if err = rows.Close(); err != nil {
			return err
		}
	}
	return nil
}

// owner finds the deepest candidate whose root contains the path.
func (b *reportBuilder) owner(sourceID int64, value string) (int, bool) {
	prefix := value
	for {
		if idx, ok := b.entryIdx[pathKey{sourceID, prefix}]; ok {
			return idx, true
		}
		if prefix == "" || prefix == "/" {
			return 0, false
		}
		next := parentPath(prefix)
		if next == prefix {
			return 0, false
		}
		prefix = next
	}
}

// keptBy reports whether a row under a candidate is protected by a preserved
// descendant entry or the whitelist. The owner root itself is not a protection:
// rows at or below it belong to the card being deleted unless a deeper kept
// entry starts inside it.
func (b *reportBuilder) keptBy(sourceID int64, value string) (int, bool) {
	owner, ok := b.owner(sourceID, value)
	if !ok {
		return 0, false
	}
	prefix := value
	for prefix != b.entries[owner].EffectiveRoot {
		if b.protected[pathKey{sourceID, prefix}] || b.protected[pathKey{0, prefix}] {
			return owner, true
		}
		next := parentPath(prefix)
		if next == prefix {
			break
		}
		prefix = next
	}
	return 0, false
}

func parentPath(value string) string {
	if i := strings.LastIndex(value, "::"); i > 0 && !strings.Contains(value[i+2:], "/") {
		return value[:i]
	}
	if i := strings.LastIndex(value, "/"); i > 0 {
		return value[:i]
	}
	if strings.HasPrefix(value, "/") {
		return "/"
	}
	return value
}

func placeholders(n int) string {
	if n == 0 {
		return "NULL"
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func int64Args(values []int64) []any {
	args := make([]any, len(values))
	for i, value := range values {
		args[i] = value
	}
	return args
}

var errReportChanged = errors.New("catalog changed since the dry-run report; regenerate it")

// Apply verifies the database against the report and deletes one batch per
// transaction. Any mismatch between the live counts and the report aborts
// before a write.
func Apply(ctx context.Context, db *sql.DB, report Report, options Options) (ApplyResult, error) {
	if options.BatchSize <= 0 {
		options.BatchSize = 100
	}
	live, err := BuildReport(ctx, db, options)
	if err != nil {
		return ApplyResult{}, err
	}
	remaining, already, err := verifyReport(ctx, db, report, live)
	if err != nil {
		return ApplyResult{}, err
	}
	paths := map[int64][]pathKey{}
	builder := reportBuilder{
		entries:   remaining,
		entryIdx:  map[pathKey]int{},
		entryIDs:  map[int64]bool{},
		protected: map[pathKey]bool{},
		whitelist: live.Whitelist,
	}
	for i := range builder.entries {
		builder.entryIDs[builder.entries[i].EntryID] = true
	}
	for i := range builder.entries {
		builder.entryIdx[pathKey{builder.entries[i].SourceID, builder.entries[i].EffectiveRoot}] = i
	}
	protected, err := loadProtectedRoots(ctx, db, builder.entryIDs, live.Whitelist)
	if err != nil {
		return ApplyResult{}, err
	}
	builder.protected = protected
	// Recompute the exact path set with the same owner/kept rules as the report.
	for _, spec := range []struct{ table, column string }{
		{"source_catalog_observation", "directory_path"},
		{"source_catalog_directory", "path"},
		{"source_catalog_audio_root", "catalog_root"},
	} {
		rows, err := db.QueryContext(ctx, `SELECT file_source_id, `+spec.column+` FROM `+spec.table)
		if err != nil {
			return ApplyResult{}, err
		}
		for rows.Next() {
			var sourceID int64
			var value string
			if err = rows.Scan(&sourceID, &value); err != nil {
				rows.Close()
				return ApplyResult{}, err
			}
			if idx, ok := builder.owner(sourceID, value); ok {
				if _, kept := builder.keptBy(sourceID, value); kept {
					continue
				}
				entryID := builder.entries[idx].EntryID
				paths[entryID] = append(paths[entryID], pathKey{sourceID, value})
			}
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return ApplyResult{}, err
		}
		if err = rows.Close(); err != nil {
			return ApplyResult{}, err
		}
	}
	result := ApplyResult{Batches: []BatchResult{}, AlreadyApplied: already}
	for start := 0; start < len(remaining); start += options.BatchSize {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		end := start + options.BatchSize
		if end > len(remaining) {
			end = len(remaining)
		}
		batch := remaining[start:end]
		summary, err := applyBatch(ctx, db, batch, paths, live.Whitelist, start/options.BatchSize+1)
		if err != nil {
			return result, fmt.Errorf("batch %d: %w", start/options.BatchSize+1, err)
		}
		result.Batches = append(result.Batches, summary)
		result.Totals.Entries += summary.Entries
		result.Totals.Files += summary.Files
		result.Totals.Observations += summary.Observations
		result.Totals.Directories += summary.Directories
		result.Totals.AudioRoots += summary.AudioRoots
		result.Totals.TransferRefs += summary.TransferRefs
	}
	// Refresh the stored projection after the batches commit. A rerun with an
	// already-applied report also reaches this point, which repairs a stale
	// remainder left by an interrupted process.
	sources := map[int64]bool{}
	for _, entry := range report.Entries {
		sources[entry.SourceID] = true
	}
	if err = reconcileScanCounters(ctx, db, sources); err != nil {
		return result, err
	}
	return result, nil
}

// verifyReport keeps only the entries that are still present. An entry that a
// previous interrupted run already removed is verified to be fully gone, so
// rerunning the same report continues instead of failing on the smaller live
// plan. A new inadmissible card is still an error: Apply never deletes rows a
// reviewed report did not name.
func verifyReport(ctx context.Context, db *sql.DB, want, got Report) ([]Entry, int, error) {
	liveByID := make(map[int64]Entry, len(got.Entries))
	for _, entry := range got.Entries {
		liveByID[entry.EntryID] = entry
	}
	remaining := make([]Entry, 0, len(got.Entries))
	seen := map[int64]bool{}
	already := 0
	for _, w := range want.Entries {
		g, ok := liveByID[w.EntryID]
		if !ok {
			gone, err := entryRemoved(ctx, db, w.EntryID)
			if err != nil {
				return nil, 0, err
			}
			if !gone {
				return nil, 0, fmt.Errorf("%w: entry %d is no longer in the plan but still has rows", errReportChanged, w.EntryID)
			}
			already++
			continue
		}
		if w.Files != g.Files || w.Audio != g.Audio || w.NonAudio != g.NonAudio ||
			w.Observations != g.Observations || w.Directories != g.Directories ||
			w.AudioRoots != g.AudioRoots || w.TransferRefs != g.TransferRefs {
			return nil, 0, fmt.Errorf("%w: entry %d changed (%+v -> %+v)", errReportChanged, w.EntryID, w, g)
		}
		remaining = append(remaining, g)
		seen[w.EntryID] = true
	}
	for _, g := range got.Entries {
		if !seen[g.EntryID] {
			return nil, 0, fmt.Errorf("%w: entry %d appeared after the report", errReportChanged, g.EntryID)
		}
	}
	return remaining, already, nil
}

// reconcileScanCounters refreshes the stored progress projection after rows
// are deleted. Without this the next publisher can see a positive remainder
// for observations that no longer exist and never reach the retirement step.
// The update is skipped on schemas that predate the counters migration.
func reconcileScanCounters(ctx context.Context, db *sql.DB, sources map[int64]bool) error {
	var columns int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('source_catalog_scan') WHERE name='publish_pending_remaining'`).Scan(&columns); err != nil {
		return err
	}
	if columns == 0 {
		return nil
	}
	for sourceID := range sources {
		if _, err := db.ExecContext(ctx, `UPDATE source_catalog_scan SET
			directories=(SELECT count(*) FROM source_catalog_directory d
				WHERE d.file_source_id=source_catalog_scan.file_source_id AND d.generation=source_catalog_scan.workflow_run_id),
			completed=(SELECT count(*) FROM source_catalog_directory d
				WHERE d.file_source_id=source_catalog_scan.file_source_id AND d.generation=source_catalog_scan.workflow_run_id
					AND (d.status='complete' OR (d.status='empty' AND d.error_code='not_found'))),
			pending=(SELECT count(*) FROM source_catalog_directory d
				WHERE d.file_source_id=source_catalog_scan.file_source_id AND d.generation=source_catalog_scan.workflow_run_id
					AND d.status IN ('pending','listing_complete')),
			failed=(SELECT count(*) FROM source_catalog_directory d
				WHERE d.file_source_id=source_catalog_scan.file_source_id AND d.generation=source_catalog_scan.workflow_run_id AND d.status='failed'),
			restricted=(SELECT count(*) FROM source_catalog_directory d
				WHERE d.file_source_id=source_catalog_scan.file_source_id AND d.generation=source_catalog_scan.workflow_run_id AND d.status='restricted'),
			empty=(SELECT count(*) FROM source_catalog_directory d
				WHERE d.file_source_id=source_catalog_scan.file_source_id AND d.generation=source_catalog_scan.workflow_run_id
					AND d.status='empty' AND d.error_code<>'not_found'),
			observed=(SELECT count(*) FROM source_catalog_directory d
				JOIN source_catalog_observation o ON o.file_source_id=d.file_source_id AND o.directory_path=d.path AND o.revision=d.revision
				WHERE d.file_source_id=source_catalog_scan.file_source_id AND d.generation=source_catalog_scan.workflow_run_id),
			indexed=(SELECT count(*) FROM source_catalog_entry e
				WHERE e.file_source_id=source_catalog_scan.file_source_id AND e.last_seen_generation=source_catalog_scan.workflow_run_id),
			publish_pending_remaining=(SELECT count(*) FROM source_catalog_directory d
				JOIN source_catalog_observation o ON o.file_source_id=d.file_source_id AND o.directory_path=d.path AND o.revision=d.revision
				WHERE d.file_source_id=source_catalog_scan.file_source_id AND d.generation=source_catalog_scan.workflow_run_id
					AND d.status='complete' AND d.catalog_root<>'' AND o.publish_pending=1
					AND EXISTS(SELECT 1 FROM source_catalog_audio_root r
						WHERE r.file_source_id=d.file_source_id AND r.generation=d.generation AND r.catalog_root=d.catalog_root)),
			updated_at=CURRENT_TIMESTAMP
		WHERE file_source_id=?`, sourceID); err != nil {
			return err
		}
	}
	return nil
}

func entryRemoved(ctx context.Context, db *sql.DB, id int64) (bool, error) {
	var entries, files, refs int
	if err := db.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM source_catalog_entry WHERE id=?),
		(SELECT count(*) FROM source_catalog_file WHERE catalog_entry_id=?),
		(SELECT count(*) FROM personal_transfer_ref WHERE catalog_entry_id=?)`,
		id, id, id).Scan(&entries, &files, &refs); err != nil {
		return false, err
	}
	return entries == 0 && files == 0 && refs == 0, nil
}

func applyBatch(ctx context.Context, db *sql.DB, batch []Entry, paths map[int64][]pathKey, whitelist []string, number int) (BatchResult, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return BatchResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	summary := BatchResult{Batch: number, Entries: len(batch)}
	ids := make([]int64, 0, len(batch))
	for _, entry := range batch {
		ids = append(ids, entry.EntryID)
	}
	args := int64Args(ids)
	in := placeholders(len(ids))

	// Refuse to delete a row that a series or an alias still references.
	var seriesRefs, aliasRefs int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM catalog_series_member WHERE catalog_entry_id IN (`+in+`)`, args...).Scan(&seriesRefs); err != nil {
		return BatchResult{}, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM source_catalog_entry WHERE relocated_to_id IN (`+in+`)`, args...).Scan(&aliasRefs); err != nil {
		return BatchResult{}, err
	}
	if seriesRefs != 0 || aliasRefs != 0 {
		return BatchResult{}, fmt.Errorf("batch %d still has %d series members and %d aliases", number, seriesRefs, aliasRefs)
	}

	before, err := whitelistCounts(ctx, tx, whitelist)
	if err != nil {
		return BatchResult{}, err
	}
	for _, entry := range batch {
		var files, refs int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM source_catalog_file WHERE catalog_entry_id=?`, entry.EntryID).Scan(&files); err != nil {
			return BatchResult{}, err
		}
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM personal_transfer_ref WHERE catalog_entry_id=?`, entry.EntryID).Scan(&refs); err != nil {
			return BatchResult{}, err
		}
		if files != entry.Files || refs != entry.TransferRefs {
			return BatchResult{}, fmt.Errorf("entry %d counts changed: files %d->%d refs %d->%d", entry.EntryID, entry.Files, files, entry.TransferRefs, refs)
		}
	}
	var refRows int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM personal_transfer_ref WHERE catalog_entry_id IN (`+in+`)`, args...).Scan(&refRows); err != nil {
		return BatchResult{}, err
	}

	if _, err = tx.ExecContext(ctx, `CREATE TEMP TABLE IF NOT EXISTS prune_paths(source_id INTEGER NOT NULL, path TEXT NOT NULL, PRIMARY KEY(source_id,path)) WITHOUT ROWID`); err != nil {
		return BatchResult{}, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM prune_paths`); err != nil {
		return BatchResult{}, err
	}
	insertPath, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO prune_paths(source_id,path) VALUES(?,?)`)
	if err != nil {
		return BatchResult{}, err
	}
	for _, entry := range batch {
		for _, key := range paths[entry.EntryID] {
			if _, err = insertPath.ExecContext(ctx, key.sourceID, key.path); err != nil {
				_ = insertPath.Close()
				return BatchResult{}, err
			}
		}
	}
	if err = insertPath.Close(); err != nil {
		return BatchResult{}, err
	}
	var observationRows, directoryRows, audioRootRows int64
	result, err := tx.ExecContext(ctx, `DELETE FROM source_catalog_observation
		WHERE (file_source_id,directory_path) IN (SELECT source_id,path FROM prune_paths)`)
	if err != nil {
		return BatchResult{}, err
	}
	observationRows, _ = result.RowsAffected()
	result, err = tx.ExecContext(ctx, `DELETE FROM source_catalog_directory
		WHERE (file_source_id,path) IN (SELECT source_id,path FROM prune_paths)`)
	if err != nil {
		return BatchResult{}, err
	}
	directoryRows, _ = result.RowsAffected()
	result, err = tx.ExecContext(ctx, `DELETE FROM source_catalog_audio_root
		WHERE (file_source_id,catalog_root) IN (SELECT source_id,path FROM prune_paths)`)
	if err != nil {
		return BatchResult{}, err
	}
	audioRootRows, _ = result.RowsAffected()

	result, err = tx.ExecContext(ctx, `DELETE FROM source_catalog_file WHERE catalog_entry_id IN (`+in+`)`, args...)
	if err != nil {
		return BatchResult{}, err
	}
	fileRows, _ := result.RowsAffected()
	// A catalog alias can outlive the card when the card owns a work. Keep the
	// opaque reference and repoint it at that work; aliases for unregistered
	// source-only cards have no surviving object and are removed with the card.
	if _, err = tx.ExecContext(ctx, `UPDATE personal_transfer_ref
		SET work_id=(SELECT e.work_id FROM source_catalog_entry e WHERE e.id=personal_transfer_ref.catalog_entry_id),
			catalog_entry_id=NULL,
			object_kind='work',
			is_primary=0
		WHERE catalog_entry_id IN (`+in+`)
			AND (SELECT e.work_id FROM source_catalog_entry e WHERE e.id=personal_transfer_ref.catalog_entry_id) IS NOT NULL`, args...); err != nil {
		return BatchResult{}, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM personal_transfer_ref WHERE catalog_entry_id IN (`+in+`)`, args...); err != nil {
		return BatchResult{}, err
	}
	result, err = tx.ExecContext(ctx, `DELETE FROM source_catalog_entry WHERE id IN (`+in+`)`, args...)
	if err != nil {
		return BatchResult{}, err
	}
	entryRows, _ := result.RowsAffected()

	if entryRows != int64(len(batch)) {
		return BatchResult{}, fmt.Errorf("deleted %d entries, want %d", entryRows, len(batch))
	}
	if err = foreignKeyCheck(ctx, tx); err != nil {
		return BatchResult{}, err
	}
	after, err := whitelistCounts(ctx, tx, whitelist)
	if err != nil {
		return BatchResult{}, err
	}
	if after != before {
		return BatchResult{}, fmt.Errorf("whitelist changed: %+v -> %+v", before, after)
	}
	if err = tx.Commit(); err != nil {
		return BatchResult{}, err
	}
	summary.Files = int(fileRows)
	summary.Observations = int(observationRows)
	summary.Directories = int(directoryRows)
	summary.AudioRoots = int(audioRootRows)
	summary.TransferRefs = int(refRows)
	return summary, nil
}

type whitelistSnapshot struct {
	Entries      int
	Files        int
	Observations int
}

func whitelistCounts(ctx context.Context, tx *sql.Tx, whitelist []string) (whitelistSnapshot, error) {
	var snapshot whitelistSnapshot
	for _, root := range whitelist {
		var entries, files, observations int
		if err := tx.QueryRowContext(ctx, `SELECT
			(SELECT count(*) FROM source_catalog_entry e WHERE e.root_path=? OR substr(e.root_path,1,length(?)+1)=? || '/')
			 + (SELECT count(*) FROM source_catalog_entry e WHERE e.source_root=? OR substr(e.source_root,1,length(?)+1)=? || '/')`,
			root, root, root, root, root, root).Scan(&entries); err != nil {
			return snapshot, err
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM source_catalog_file f
			WHERE f.catalog_entry_id IN (
				SELECT id FROM source_catalog_entry e WHERE e.root_path=? OR substr(e.root_path,1,length(?)+1)=? || '/'
					OR e.source_root=? OR substr(e.source_root,1,length(?)+1)=? || '/')`,
			root, root, root, root, root, root).Scan(&files); err != nil {
			return snapshot, err
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM source_catalog_observation o
			WHERE o.directory_path=? OR substr(o.directory_path,1,length(?)+1)=? || '/'`,
			root, root, root).Scan(&observations); err != nil {
			return snapshot, err
		}
		snapshot.Entries += entries
		snapshot.Files += files
		snapshot.Observations += observations
	}
	return snapshot, nil
}

func foreignKeyCheck(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		var table string
		var rowID sql.NullInt64
		var parent string
		var constraint int
		if err = rows.Scan(&table, &rowID, &parent, &constraint); err != nil {
			return err
		}
		return fmt.Errorf("foreign key check failed: table %s row %v references %s", table, rowID, parent)
	}
	return rows.Err()
}

// RenderMarkdown writes the audited report. The full per-card table is
// included because the operator compares batch counts against it.
func RenderMarkdown(report Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Catalog prune dry-run\n\n")
	fmt.Fprintf(&b, "- Generated: %s\n", report.GeneratedAt)
	if report.Note != "" {
		fmt.Fprintf(&b, "- Note: %s\n", report.Note)
	}
	fmt.Fprintf(&b, "- Candidate cards (failed admission): %d\n", report.Candidates.Entries)
	fmt.Fprintf(&b, "- Candidate files: %d (audio %d, non-audio %d)\n", report.Candidates.Files, report.Candidates.Audio, report.Candidates.NonAudio)
	fmt.Fprintf(&b, "- Candidate transfer refs: %d\n", report.Candidates.TransferRefs)
	fmt.Fprintf(&b, "- Cards planned for deletion: %d\n", report.Totals.Entries)
	fmt.Fprintf(&b, "- Files planned for deletion: %d (audio %d, non-audio %d)\n", report.Totals.Files, report.Totals.Audio, report.Totals.NonAudio)
	fmt.Fprintf(&b, "- Observations: %d\n", report.Totals.Observations)
	fmt.Fprintf(&b, "- Directories: %d\n", report.Totals.Directories)
	fmt.Fprintf(&b, "- Audio roots: %d\n", report.Totals.AudioRoots)
	fmt.Fprintf(&b, "- Transfer refs: %d\n", report.Totals.TransferRefs)
	fmt.Fprintf(&b, "- Kept path rows (whitelist or preserved entry): %d\n", report.Totals.KeptRows)
	fmt.Fprintf(&b, "- Whitelist: %s\n", strings.Join(report.Whitelist, ", "))
	if len(report.Skipped) > 0 {
		fmt.Fprintf(&b, "\n## Skipped\n\n| entry | root | reason |\n| --- | --- | --- |\n")
		for _, item := range report.Skipped {
			fmt.Fprintf(&b, "| %d | `%s` | %s |\n", item.EntryID, item.Root, item.Reason)
		}
	}
	fmt.Fprintf(&b, "\n## Cards\n\n| entry | root | files | audio | non-audio | obs | dirs | audio roots | transfer | reason |\n")
	fmt.Fprintf(&b, "| ---: | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |\n")
	for _, entry := range report.Entries {
		fmt.Fprintf(&b, "| %d | `%s` | %d | %d | %d | %d | %d | %d | %d | %s |\n",
			entry.EntryID, entry.Root, entry.Files, entry.Audio, entry.NonAudio,
			entry.Observations, entry.Directories, entry.AudioRoots, entry.TransferRefs, entry.Reason)
	}
	return b.String()
}

// loadProtectedRoots returns every root of an entry this plan keeps, plus the
// configured whitelist (source id 0 is the wildcard used by keptBy).
func loadProtectedRoots(ctx context.Context, db *sql.DB, deleted map[int64]bool, whitelist []string) (map[pathKey]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, file_source_id, root_path, source_root FROM source_catalog_entry WHERE file_source_id IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	protected := map[pathKey]bool{}
	for rows.Next() {
		var id, sourceID int64
		var root, src string
		if err = rows.Scan(&id, &sourceID, &root, &src); err != nil {
			return nil, err
		}
		if deleted[id] {
			continue
		}
		if value := effectiveRoot(root, src); value != "" {
			protected[pathKey{sourceID, value}] = true
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for _, root := range whitelist {
		protected[pathKey{0, root}] = true
	}
	return protected, nil
}

func underAnyRoot(value string, roots []string) bool {
	for _, root := range roots {
		if value == root || strings.HasPrefix(value, root+"/") {
			return true
		}
	}
	return false
}
