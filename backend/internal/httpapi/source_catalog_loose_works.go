package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/yexca/kikoto/backend/internal/openlist"
)

const (
	catalogWorkSplitSingle = "single"
	catalogWorkSplitPrefix = "prefix"
	catalogWorkSplitFile   = "file"
)

// A creator folder can contain many independent works as loose audio files.
// These patterns identify the numbering schemes those sources use. A code at
// the start of a file name is a work number, not a track number.
var catalogLooseWorkPrefix = regexp.MustCompile(`(?i)^(?:RJ|BJ|VJ|CC)[0-9]{5,8}(?:[^0-9A-Za-z]|$)|^[NSAB][0-9]{1,6}(?:[^0-9A-Za-z]|$)`)
var catalogLooseLeadingTrack = regexp.MustCompile(`^[0-9]{1,4}(?:[\s._\-、]+|$)`)
var catalogLooseTrailingTrack = regexp.MustCompile(`[\s._\-（(【\[]+[0-9]{1,4}[\s._\-）)】\]]*$`)
var catalogLooseTrailingNumber = regexp.MustCompile(`[0-9]{1,4}[\s._\-）)】\]]*$`)
var catalogNumberedTrackPrefix = regexp.MustCompile(`^([0-9]{1,4})(?:[\s._\-、]|$)`)
var catalogPrefixedTrack = regexp.MustCompile(`^[\s._\-、]*([0-9]{1,3})(?:[\s._\-、]|$)`)

// catalogWorkSplitMode decides how one directory's direct audio is published.
// The decision is taken once, when the directory listing first completes.
func catalogWorkSplitMode(files []openlist.File) string {
	names := make([]string, 0, len(files))
	for _, file := range files {
		if file.Kind == "audio" && !strings.Contains(file.Path, "/") {
			names = append(names, file.Path)
		}
	}
	return catalogWorkSplitModeNames(names)
}

func catalogWorkSplitModeNames(names []string) string {
	if len(names) == 0 {
		return catalogWorkSplitSingle
	}
	// 001.mp3..225.mp3 is one audiobook with many episodes, not 225 works. A
	// set of date-only names is also all digits but is not a sequence: 20210104
	// and 20210110 are separate recordings, so require an ordered run rather
	// than testing the stem shape alone.
	if catalogNumberedTrackRun(names) {
		return catalogWorkSplitSingle
	}
	prefixes := map[string]int{}
	for _, name := range names {
		if prefix := catalogWorkPrefix(name); prefix != "" {
			prefixes[prefix]++
		}
	}
	if len(prefixes) >= 2 {
		repeated := false
		for _, count := range prefixes {
			if count >= 2 {
				repeated = true
				break
			}
		}
		// N006-1..N006-3 plus N007 and N008 is three works, not one card.
		// The prefix form must recur in this directory; two unrelated A1/A2
		// parts are not enough evidence for two works.
		if repeated {
			return catalogWorkSplitPrefix
		}
	}
	// A creator folder can hold only a few unrelated recordings, so a minimum
	// file count would fold them into one card. Split when several base titles
	// remain, but keep a numbered track listing together: 01.mp3/02.mp3/03.mp3
	// and 01 Opening/02 Ending are one work with ordered tracks, not three.
	if catalogNumberedTrackRun(names) {
		return catalogWorkSplitSingle
	}
	distinct := map[string]bool{}
	for _, name := range names {
		distinct[catalogBaseTitle(name)] = true
	}
	if len(distinct) > 1 {
		return catalogWorkSplitFile
	}
	return catalogWorkSplitSingle
}

// catalogNumberedTrackRun reports a directory whose every file starts with an
// ordered track number. The sequence must be the only discriminator: the
// remaining titles may differ. Missing or duplicate positions keep the caller
// on the ordinary base-title rule so a broken listing is not silently merged.
func catalogNumberedTrackRun(names []string) bool {
	if len(names) < 2 {
		return false
	}
	positions := make([]int, 0, len(names))
	for _, name := range names {
		stem := strings.TrimSpace(catalogFileStem(name))
		match := catalogNumberedTrackPrefix.FindStringSubmatch(stem)
		if match == nil {
			return false
		}
		position, err := strconv.Atoi(match[1])
		if err != nil || position <= 0 || position > 999 {
			return false
		}
		positions = append(positions, position)
	}
	sort.Ints(positions)
	if positions[0] != 1 {
		// A year or a high counter is not a track listing. A genuine run
		// starts at the first track; a missing start means the number is
		// part of the title, not the only discriminator.
		return false
	}
	for i, position := range positions {
		if position != i+1 {
			return false
		}
	}
	return true
}

func catalogFileStem(name string) string {
	return strings.TrimSuffix(path.Base(name), path.Ext(name))
}

// catalogBaseTitle removes a leading or trailing track number so 01 Title,
// Title 1 and Title 2 count as the same work. The full title is still kept for
// display. A title that is only a number has no base and keeps its own name.
func catalogBaseTitle(name string) string {
	original := strings.ToLower(strings.TrimSpace(catalogFileStem(name)))
	stem := catalogLooseLeadingTrack.ReplaceAllString(original, "")
	trimmed := strings.TrimSpace(catalogLooseTrailingTrack.ReplaceAllString(stem, ""))
	if trimmed == stem {
		// Titles such as Example Episode1 and Example Episode2 are episodes of
		// one work even without a separator before the number.
		trimmed = strings.TrimSpace(catalogLooseTrailingNumber.ReplaceAllString(stem, ""))
	}
	trimmed = strings.TrimRight(trimmed, " ._-（(【[")
	// A number is not a title. Without this guard a date or a bare counter is
	// reduced to a fragment (20210104 -> 2021, 2020-12-16 -> 12), so unrelated
	// recordings in one creator folder collapse onto the same card. Only strip
	// a track number when a real title remains; otherwise keep the full stem.
	if !catalogHasLetter(trimmed) {
		return strings.Join(strings.Fields(original), " ")
	}
	return strings.Join(strings.Fields(trimmed), " ")
}

func catalogHasLetter(value string) bool {
	for _, r := range value {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

func catalogWorkPrefix(name string) string {
	stem := catalogFileStem(name)
	match := catalogLooseWorkPrefix.FindString(stem)
	if match == "" {
		return ""
	}
	// The pattern consumes one delimiter so a following digit cannot extend
	// the number. The prefix itself ends at the last digit.
	end := len(match)
	for end > 0 && (match[end-1] < '0' || match[end-1] > '9') {
		end--
	}
	if end == 0 {
		return ""
	}
	return strings.ToUpper(match[:end])
}

// catalogLooseGrouping is the work partition of one directory listing. It is
// built once from the direct audio names so refresh and publish see the same
// groups.
type catalogLooseGrouping struct {
	mode          string
	splitPrefixes map[string]bool
}

// newCatalogLooseGrouping decides whether a repeated work prefix names one
// work or several. A prefix such as N006 usually identifies one release, but
// creator folders also reuse B011 for unrelated recordings. Within one prefix,
// files stay together when they share a base title (B20 ...01/02) or when the
// prefix is followed by a track run starting at 1 (N006-1/2/3). Different
// titles without that evidence stay separate.
func newCatalogLooseGrouping(names []string, mode string) catalogLooseGrouping {
	grouping := catalogLooseGrouping{
		mode:          mode,
		splitPrefixes: map[string]bool{},
	}
	if mode == catalogWorkSplitPrefix {
		byPrefix := map[string][]string{}
		for _, name := range names {
			if prefix := catalogWorkPrefix(name); prefix != "" {
				byPrefix[prefix] = append(byPrefix[prefix], name)
			}
		}
		for prefix, group := range byPrefix {
			if !catalogPrefixFilesStayTogether(prefix, group) {
				grouping.splitPrefixes[prefix] = true
			}
		}
	}
	return grouping
}

func catalogPrefixFilesStayTogether(prefix string, names []string) bool {
	bases := map[string]bool{}
	for _, name := range names {
		bases[catalogBaseTitle(name)] = true
	}
	if len(bases) == 1 {
		return true
	}
	positions := make([]int, 0, len(names))
	for _, name := range names {
		stem := strings.TrimSpace(catalogFileStem(name))
		if !strings.HasPrefix(strings.ToUpper(stem), prefix) {
			return false
		}
		remainder := stem[len(prefix):]
		match := catalogPrefixedTrack.FindStringSubmatch(remainder)
		if match == nil {
			return false
		}
		position, err := strconv.Atoi(match[1])
		if err != nil || position <= 0 {
			return false
		}
		positions = append(positions, position)
	}
	sort.Ints(positions)
	if positions[0] != 1 {
		return false
	}
	for i, position := range positions {
		if position != i+1 {
			return false
		}
	}
	return true
}

func (g catalogLooseGrouping) keyFor(name string) string {
	if g.mode == catalogWorkSplitPrefix {
		prefix := catalogWorkPrefix(name)
		if prefix == "" {
			// Direct audio without a work prefix keeps the parent card.
			return ""
		}
		if g.splitPrefixes[prefix] {
			// Keep the prefix in the identity so B011 and B012 with the same
			// title cannot collapse onto one card. The remainder is read from
			// the file stem, not the lowercased base title, so the prefix is
			// removed once and the card key stays readable.
			stem := strings.TrimSpace(catalogFileStem(name))
			remainder := ""
			if len(stem) >= len(prefix) && strings.EqualFold(stem[:len(prefix)], prefix) {
				remainder = strings.TrimSpace(strings.TrimLeft(stem[len(prefix):], " ._-、"))
			}
			if base := catalogBaseTitle(name); base != "" && remainder == "" {
				remainder = base
			}
			if remainder != "" {
				return prefix + " " + remainder
			}
		}
		return prefix
	}
	if base := catalogBaseTitle(name); base != "" {
		return base
	}
	return strings.ToLower(strings.TrimSpace(catalogFileStem(name)))
}

// catalogGroupTitles returns the display title of each group from the
// lexicographically first file in that group.
func catalogGroupTitles(grouping catalogLooseGrouping, names []string) map[string]string {
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	titles := map[string]string{}
	for _, name := range sorted {
		key := grouping.keyFor(name)
		if key == "" {
			continue
		}
		if _, ok := titles[key]; ok {
			continue
		}
		titles[key] = catalogFileStem(name)
	}
	return titles
}

type catalogSplitTarget struct {
	Root       string
	SourceRoot string
	Title      string
}

// catalogSplitTargetFor maps one direct audio file to its card. A file with no
// group key stays on the directory card.
func catalogSplitTargetFor(root, name string, grouping catalogLooseGrouping, titles map[string]string) catalogSplitTarget {
	if grouping.mode != catalogWorkSplitPrefix && grouping.mode != catalogWorkSplitFile {
		return catalogSplitTarget{Root: root, Title: path.Base(root)}
	}
	key := grouping.keyFor(name)
	if key == "" {
		return catalogSplitTarget{Root: root, Title: path.Base(root)}
	}
	return catalogSplitTarget{Root: root + "::" + key, SourceRoot: root, Title: titles[key]}
}

// catalogSplitSnapshot partitions a finite scan. Only direct audio is split;
// the parent keeps direct audio without a work prefix and nested audio from
// part directories, so a disc or format copy is not lost.
func catalogSplitSnapshot(root string, files []openlist.File, mode string) (map[string][]openlist.File, []openlist.File) {
	if mode == catalogWorkSplitSingle {
		return nil, files
	}
	names := catalogDirectAudioNames(files)
	grouping := newCatalogLooseGrouping(names, mode)
	titles := catalogGroupTitles(grouping, names)
	groups := map[string][]openlist.File{}
	parent := []openlist.File{}
	for _, file := range files {
		if file.Kind != "audio" {
			continue
		}
		if strings.Contains(file.Path, "/") {
			parent = append(parent, file)
			continue
		}
		target := catalogSplitTargetFor(root, file.Path, grouping, titles)
		if target.Root == root {
			parent = append(parent, file)
			continue
		}
		groups[target.Root] = append(groups[target.Root], file)
	}
	return groups, parent
}

// catalogSplitCardFiles keeps only the files that belong to one published
// split card. A refresh of that card must not republish the whole creator
// directory into it. Nested audio (discs, format copies) stays on the parent
// card, so split cards only ever own direct files.
func catalogSplitCardFiles(rootPath, sourceRoot string, files []openlist.File) []openlist.File {
	if sourceRoot == "" || !strings.HasPrefix(rootPath, sourceRoot+"::") {
		return files
	}
	suffix := strings.TrimPrefix(rootPath, sourceRoot+"::")
	names := catalogDirectAudioNames(files)
	// Recompute the same partition an index would publish from this listing.
	// The card suffix is only an identity; a title that looks like a prefix
	// ("n006") must not choose the grouping by itself.
	grouping := newCatalogLooseGrouping(names, catalogWorkSplitModeNames(names))
	result := []openlist.File{}
	for _, file := range files {
		if file.Kind != "audio" || strings.Contains(file.Path, "/") {
			continue
		}
		if strings.EqualFold(grouping.keyFor(file.Path), suffix) {
			result = append(result, file)
		}
	}
	return result
}

// catalogEntryCode derives the work code from a split identity such as
// /creator::RJ00000001 as well as from a plain directory.
func catalogEntryCode(root string) string {
	if index := strings.LastIndex(root, "::"); index >= 0 {
		return catalogDirectoryCode(root[index+2:])
	}
	return catalogDirectoryCode(root)
}

// decideCatalogWorkSplitTx records the one-time decision for a directory that
// became a card. A card already linked to a work, a series or a relocated
// child is never split.
func decideCatalogWorkSplitTx(ctx context.Context, tx *sql.Tx, sourceID int64, directoryPath string, revision int) error {
	if directoryPath == "/" {
		// The source root is the operator's scan boundary, not a creator
		// folder. Its loose files keep the root card.
		return nil
	}
	var pinned int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM source_catalog_entry e
		WHERE e.file_source_id=? AND e.root_path=? AND (
			e.work_id IS NOT NULL
			OR EXISTS(SELECT 1 FROM catalog_series_member m WHERE m.catalog_entry_id=e.id)
			OR EXISTS(SELECT 1 FROM source_catalog_entry r WHERE r.relocated_to_id=e.id)))`,
		sourceID, directoryPath).Scan(&pinned); err != nil {
		return err
	}
	mode := catalogWorkSplitSingle
	if pinned == 0 {
		// A directory named with its own work code is an authored release.
		// Its direct files are tracks or versions, never separate works.
		if catalogDirectoryCode(directoryPath) != "" {
			mode = catalogWorkSplitSingle
		} else {
			// A package copy of the parent's audio is one version, not one
			// work per file. Keep it on a single card so the same recordings
			// do not reappear as hundreds of split cards.
			duplicate, dupErr := catalogDuplicateDirectoryTx(ctx, tx, sourceID, directoryPath)
			if dupErr != nil {
				return dupErr
			}
			if duplicate {
				_, execErr := tx.ExecContext(ctx, `UPDATE source_catalog_directory SET work_split=? WHERE file_source_id=? AND path=?`, catalogWorkSplitSingle, sourceID, directoryPath)
				return execErr
			}
			rows, err := tx.QueryContext(ctx, `SELECT name FROM source_catalog_observation WHERE file_source_id=? AND directory_path=? AND revision=? AND kind='audio' ORDER BY name`, sourceID, directoryPath, revision)
			if err != nil {
				return err
			}
			names := []string{}
			for rows.Next() {
				var name string
				if err = rows.Scan(&name); err != nil {
					break
				}
				names = append(names, name)
			}
			readErr := rows.Err()
			_ = rows.Close()
			if err != nil {
				return err
			}
			if readErr != nil {
				return readErr
			}
			mode = catalogWorkSplitModeNames(names)
		}
	}
	// A split decision belongs to the current listing. completeCatalogDirectoryTx
	// runs once per completed revision, so recompute from that revision's names
	// instead of freezing the first choice forever.
	_, err := tx.ExecContext(ctx, `UPDATE source_catalog_directory SET work_split=? WHERE file_source_id=? AND path=?`, mode, sourceID, directoryPath)
	return err
}

// retireCatalogSplitParentTx hides the old creator-folder card once split
// cards exist. Relocation keeps its id and any personal reference resolvable;
// the old file rows are removed because the split cards own those files now.
func retireCatalogSplitParentTx(ctx context.Context, tx *sql.Tx, sourceID int64, parentRoot string, targetID int64) error {
	var id int64
	var workID sql.NullInt64
	var series, relocated int
	err := tx.QueryRowContext(ctx, `SELECT e.id,e.work_id,
		EXISTS(SELECT 1 FROM catalog_series_member m WHERE m.catalog_entry_id=e.id),
		EXISTS(SELECT 1 FROM source_catalog_entry r WHERE r.relocated_to_id=e.id)
		FROM source_catalog_entry e
		WHERE e.file_source_id=? AND e.root_path=? AND e.relocated_to_id IS NULL`,
		sourceID, parentRoot).Scan(&id, &workID, &series, &relocated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if workID.Valid || series != 0 || relocated != 0 || id == targetID {
		return nil
	}
	if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_entry SET file_source_id=NULL,relocated_to_id=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, targetID, id); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM source_catalog_file WHERE catalog_entry_id=?`, id)
	return err
}

// retireStaleSplitCardsTx hides split cards for this root that the current
// generation did not publish. A directory that used to hold separate works can
// become one audiobook (or change its grouping); the old cards would otherwise
// stay visible beside the new one. A card with a work, a series member, or a
// relocation target keeps its identity and is left for explicit review. The
// retired row keeps file_source_id NULL and no target: the old card is gone,
// but its id stays resolvable for personal records.
func retireStaleSplitCardsTx(ctx context.Context, tx *sql.Tx, sourceID, generation int64, parentRoot string, keep map[int64]bool) error {
	// The parent card of a former split has source_root='' while its split
	// cards carry the real directory. Include both shapes so a split -> single
	// -> split sequence cannot leave the intermediate parent visible.
	rows, err := tx.QueryContext(ctx, `SELECT id FROM source_catalog_entry
		WHERE file_source_id=? AND (source_root=? OR root_path=?) AND relocated_to_id IS NULL AND last_seen_generation<>?`,
		sourceID, parentRoot, parentRoot, generation)
	if err != nil {
		return err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if keep[id] {
			continue
		}
		var blocked int
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM source_catalog_entry e WHERE e.id=? AND (
			e.work_id IS NOT NULL
			OR EXISTS(SELECT 1 FROM catalog_series_member m WHERE m.catalog_entry_id=e.id)
			OR EXISTS(SELECT 1 FROM source_catalog_entry r WHERE r.relocated_to_id=e.id)))`, id).Scan(&blocked); err != nil {
			return err
		}
		if blocked != 0 {
			continue
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM source_catalog_file WHERE catalog_entry_id=?`, id); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_entry SET file_source_id=NULL,relocated_to_id=NULL,updated_at=CURRENT_TIMESTAMP WHERE id=?`, id); err != nil {
			return err
		}
	}
	return nil
}

// catalogSnapshotSplitMode applies the same decision to a finite scan, with
// the same protection for a card that already owns a work or series.
func (s *Server) catalogSnapshotSplitMode(ctx context.Context, sourceID int64, root string, files []openlist.File) (string, error) {
	if root == "/" {
		return catalogWorkSplitSingle, nil
	}
	var pinned int
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM source_catalog_entry e
		WHERE e.file_source_id=? AND e.root_path=? AND (
			e.work_id IS NOT NULL
			OR EXISTS(SELECT 1 FROM catalog_series_member m WHERE m.catalog_entry_id=e.id)
			OR EXISTS(SELECT 1 FROM source_catalog_entry r WHERE r.relocated_to_id=e.id)))`,
		sourceID, root).Scan(&pinned); err != nil {
		return "", err
	}
	if pinned != 0 {
		return catalogWorkSplitSingle, nil
	}
	if catalogDirectoryCode(root) != "" {
		return catalogWorkSplitSingle, nil
	}
	return catalogWorkSplitMode(files), nil
}

// catalogSplitTargetsForRoot loads the split plan for one indexed root. Direct
// audio names are read once per publish batch, so a batch boundary cannot
// change a group title. Targets are keyed twice: by exact file name for direct
// audio, and by group key so a nested format copy with another extension still
// reaches its work card.
func (s *Server) catalogSplitTargetsForRoot(ctx context.Context, tx *sql.Tx, sourceID, generation int64, root string) (map[string]catalogSplitTarget, map[string]catalogSplitTarget, catalogLooseGrouping, bool, error) {
	var pinned int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM source_catalog_entry e
		WHERE e.file_source_id=? AND e.root_path=? AND (
			e.work_id IS NOT NULL
			OR EXISTS(SELECT 1 FROM catalog_series_member m WHERE m.catalog_entry_id=e.id)
			OR EXISTS(SELECT 1 FROM source_catalog_entry r WHERE r.relocated_to_id=e.id)))`,
		sourceID, root).Scan(&pinned); err != nil {
		return nil, nil, catalogLooseGrouping{}, false, err
	}
	if pinned != 0 {
		return nil, nil, catalogLooseGrouping{}, false, nil
	}
	var mode string
	err := tx.QueryRowContext(ctx, `SELECT work_split FROM source_catalog_directory WHERE file_source_id=? AND generation=? AND path=?`, sourceID, generation, root).Scan(&mode)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, catalogLooseGrouping{}, false, nil
	}
	if err != nil {
		return nil, nil, catalogLooseGrouping{}, false, err
	}
	if mode != catalogWorkSplitPrefix && mode != catalogWorkSplitFile {
		return nil, nil, catalogLooseGrouping{}, false, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT o.name FROM source_catalog_observation o
		JOIN source_catalog_directory d ON d.file_source_id=o.file_source_id AND d.path=o.directory_path
		WHERE o.file_source_id=? AND o.directory_path=? AND o.revision=d.revision AND d.generation=? AND o.kind='audio'
		ORDER BY o.name`, sourceID, root, generation)
	if err != nil {
		return nil, nil, catalogLooseGrouping{}, false, err
	}
	names := []string{}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			break
		}
		names = append(names, name)
	}
	readErr := rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, nil, catalogLooseGrouping{}, false, err
	}
	if readErr != nil {
		return nil, nil, catalogLooseGrouping{}, false, readErr
	}
	grouping := newCatalogLooseGrouping(names, mode)
	titles := catalogGroupTitles(grouping, names)
	targets := map[string]catalogSplitTarget{}
	targetsByKey := map[string]catalogSplitTarget{}
	for _, name := range names {
		target := catalogSplitTargetFor(root, name, grouping, titles)
		if target.Root != root {
			targets[name] = target
			key := strings.TrimPrefix(target.Root, root+"::")
			if _, exists := targetsByKey[key]; !exists {
				targetsByKey[key] = target
			}
		}
	}
	// Nested audio under a format or track directory belongs to the split card
	// whose work prefix its name carries, just like the direct copy. Counting
	// those files as "parent audio" would leave an empty creator card behind
	// after every split work that also has an MP3/FLAC copy. Only audio that
	// has no split card of its own keeps the parent.
	rows, err = tx.QueryContext(ctx, `SELECT o.name FROM source_catalog_observation o
		JOIN source_catalog_directory d ON d.file_source_id=o.file_source_id AND d.path=o.directory_path
		WHERE d.file_source_id=? AND d.generation=? AND d.catalog_root=? AND o.kind='audio'
		ORDER BY o.directory_path,o.name`, sourceID, generation, root)
	if err != nil {
		return nil, nil, catalogLooseGrouping{}, false, err
	}
	parentNeeded := 0
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			break
		}
		key := grouping.keyFor(name)
		if _, split := targets[name]; split {
			continue
		}
		if _, split := targetsByKey[key]; split || key == "" {
			continue
		}
		parentNeeded = 1
		break
	}
	readErr = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, nil, catalogLooseGrouping{}, false, err
	}
	if readErr != nil {
		return nil, nil, catalogLooseGrouping{}, false, readErr
	}
	return targets, targetsByKey, grouping, parentNeeded != 0, nil
}

// catalogDuplicateDirectoryTx reports a child folder that repeats the parent's
// own audio, such as a "279 works 14G" package placed beside the loose files it
// copies. Publishing it would create a second card for every work inside it,
// while the parent already carries the same recordings.
//
// The check needs a large, almost entirely size-identical subset, so a real
// disc, a differently mastered edition or a partial bonus folder keeps its own
// card. Duplicates stay unpublished rather than being merged into the parent,
// because the parent already holds the original files.
func catalogDuplicateDirectoryTx(ctx context.Context, tx *sql.Tx, sourceID int64, directoryPath string) (bool, error) {
	parent := path.Dir(directoryPath)
	if directoryPath == "/" || parent == "." || parent == directoryPath || parent == "/" {
		return false, nil
	}
	child, err := catalogAudioSizesTx(ctx, tx, sourceID, directoryPath)
	if err != nil || len(child) < 20 {
		return false, err
	}
	parentSizes, err := catalogAudioSizesTx(ctx, tx, sourceID, parent)
	if err != nil {
		return false, err
	}
	if len(parentSizes) < 20 {
		return false, nil
	}
	sort.Slice(parentSizes, func(i, j int) bool { return parentSizes[i] < parentSizes[j] })
	matched := 0
	for _, size := range child {
		tolerance := max(int64(1024), size/1000)
		index := sort.Search(len(parentSizes), func(i int) bool { return parentSizes[i] >= size-tolerance })
		if index < len(parentSizes) && parentSizes[index] <= size+tolerance {
			matched++
		}
	}
	// A folder with a handful of shared tracks is a real work with a reused
	// intro; a package copy repeats nearly all of its audio.
	return matched*10 >= len(child)*9, nil
}

func catalogAudioSizesTx(ctx context.Context, tx *sql.Tx, sourceID int64, directoryPath string) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT o.size_bytes FROM source_catalog_observation o
		JOIN source_catalog_directory d ON d.file_source_id=o.file_source_id AND d.path=o.directory_path
		WHERE o.file_source_id=? AND o.directory_path=? AND o.kind='audio' AND o.revision=d.revision`,
		sourceID, directoryPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	sizes := []int64{}
	for rows.Next() {
		var size int64
		if err = rows.Scan(&size); err != nil {
			return nil, err
		}
		sizes = append(sizes, size)
	}
	return sizes, rows.Err()
}
