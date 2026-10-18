package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"path"
	"regexp"
	"sort"
	"strings"

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
var catalogLooseNumericStem = regexp.MustCompile(`^[0-9]+$`)
var catalogLooseLeadingTrack = regexp.MustCompile(`^[0-9]{1,4}(?:[\s._\-、]+|$)`)
var catalogLooseTrailingTrack = regexp.MustCompile(`[\s._\-（(【\[]+[0-9]{1,4}[\s._\-）)】\]]*$`)
var catalogLooseTrailingNumber = regexp.MustCompile(`[0-9]{1,4}[\s._\-）)】\]]*$`)

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
	allNumeric := true
	for _, name := range names {
		if !catalogLooseNumericStem.MatchString(catalogFileStem(name)) {
			allNumeric = false
			break
		}
	}
	if allNumeric {
		// 001.mp3..225.mp3 is one audiobook with many episodes, not 225 works.
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
	if len(names) >= 12 {
		distinct := map[string]bool{}
		for _, name := range names {
			distinct[catalogBaseTitle(name)] = true
		}
		if float64(len(distinct))/float64(len(names)) > 0.5 {
			return catalogWorkSplitFile
		}
	}
	return catalogWorkSplitSingle
}

func catalogFileStem(name string) string {
	return strings.TrimSuffix(path.Base(name), path.Ext(name))
}

// catalogBaseTitle removes a leading or trailing track number so 01 Title,
// Title 1 and Title 2 count as the same work. The full title is still kept for
// display. A title that is only a number has no base and keeps its own name.
func catalogBaseTitle(name string) string {
	stem := strings.ToLower(strings.TrimSpace(catalogFileStem(name)))
	stem = catalogLooseLeadingTrack.ReplaceAllString(stem, "")
	trimmed := strings.TrimSpace(catalogLooseTrailingTrack.ReplaceAllString(stem, ""))
	if trimmed == stem {
		// Titles such as 亦弟亦妹1 and 亦弟亦妹2 are episodes of one work even
		// without a separator before the number.
		trimmed = strings.TrimSpace(catalogLooseTrailingNumber.ReplaceAllString(stem, ""))
	}
	trimmed = strings.TrimRight(trimmed, " ._-（(【[")
	if trimmed != "" {
		stem = trimmed
	}
	if stem == "" {
		stem = strings.ToLower(strings.TrimSpace(catalogFileStem(name)))
	}
	return strings.Join(strings.Fields(stem), " ")
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

// catalogGroupKey is the identity of one work inside a creator folder. A work
// prefix such as N006 wins; otherwise files that share a base title belong to
// one work, so 亦弟亦妹1..6 or Ending1/2/3 stay together instead of becoming
// separate cards.
func catalogGroupKey(name, mode string) string {
	if mode == catalogWorkSplitPrefix {
		return catalogWorkPrefix(name)
	}
	if base := catalogBaseTitle(name); base != "" {
		return base
	}
	return strings.ToLower(strings.TrimSpace(catalogFileStem(name)))
}

// catalogGroupTitles returns the display title of each group from the
// lexicographically first file in that group.
func catalogGroupTitles(names []string, mode string) map[string]string {
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	titles := map[string]string{}
	for _, name := range sorted {
		key := catalogGroupKey(name, mode)
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
func catalogSplitTargetFor(root, name, mode string, titles map[string]string) catalogSplitTarget {
	if mode != catalogWorkSplitPrefix && mode != catalogWorkSplitFile {
		return catalogSplitTarget{Root: root, Title: path.Base(root)}
	}
	key := catalogGroupKey(name, mode)
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
	names := []string{}
	for _, file := range files {
		if file.Kind == "audio" && !strings.Contains(file.Path, "/") {
			names = append(names, file.Path)
		}
	}
	titles := catalogGroupTitles(names, mode)
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
		target := catalogSplitTargetFor(root, file.Path, mode, titles)
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
	// The suffix itself says how the card was grouped: a work prefix such as
	// N006, or a shared base title. A title can look like a prefix ("n006"),
	// so the group key has to match a real file before choosing that mode.
	mode := catalogWorkSplitFile
	upperSuffix := strings.ToUpper(suffix)
	for _, file := range files {
		if file.Kind == "audio" && !strings.Contains(file.Path, "/") && catalogWorkPrefix(file.Path) == upperSuffix {
			mode = catalogWorkSplitPrefix
			break
		}
	}
	result := []openlist.File{}
	for _, file := range files {
		if file.Kind != "audio" || strings.Contains(file.Path, "/") {
			continue
		}
		if strings.EqualFold(catalogGroupKey(file.Path, mode), suffix) {
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
	var existing string
	err := tx.QueryRowContext(ctx, `SELECT work_split FROM source_catalog_directory WHERE file_source_id=? AND path=?`, sourceID, directoryPath).Scan(&existing)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if existing != "" {
		return nil
	}
	var pinned int
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(
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
				_, err = tx.ExecContext(ctx, `UPDATE source_catalog_directory SET work_split=? WHERE file_source_id=? AND path=?`, catalogWorkSplitSingle, sourceID, directoryPath)
				return err
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
	_, err = tx.ExecContext(ctx, `UPDATE source_catalog_directory SET work_split=? WHERE file_source_id=? AND path=?`, mode, sourceID, directoryPath)
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
// change a group title.
func (s *Server) catalogSplitTargetsForRoot(ctx context.Context, tx *sql.Tx, sourceID, generation int64, root string) (map[string]catalogSplitTarget, bool, error) {
	var pinned int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM source_catalog_entry e
		WHERE e.file_source_id=? AND e.root_path=? AND (
			e.work_id IS NOT NULL
			OR EXISTS(SELECT 1 FROM catalog_series_member m WHERE m.catalog_entry_id=e.id)
			OR EXISTS(SELECT 1 FROM source_catalog_entry r WHERE r.relocated_to_id=e.id)))`,
		sourceID, root).Scan(&pinned); err != nil {
		return nil, false, err
	}
	if pinned != 0 {
		return nil, false, nil
	}
	var mode string
	err := tx.QueryRowContext(ctx, `SELECT work_split FROM source_catalog_directory WHERE file_source_id=? AND generation=? AND path=?`, sourceID, generation, root).Scan(&mode)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if mode != catalogWorkSplitPrefix && mode != catalogWorkSplitFile {
		return nil, false, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT o.name FROM source_catalog_observation o
		JOIN source_catalog_directory d ON d.file_source_id=o.file_source_id AND d.path=o.directory_path
		WHERE o.file_source_id=? AND o.directory_path=? AND o.revision=d.revision AND d.generation=? AND o.kind='audio'
		ORDER BY o.name`, sourceID, root, generation)
	if err != nil {
		return nil, false, err
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
		return nil, false, err
	}
	if readErr != nil {
		return nil, false, readErr
	}
	titles := catalogGroupTitles(names, mode)
	targets := map[string]catalogSplitTarget{}
	for _, name := range names {
		target := catalogSplitTargetFor(root, name, mode, titles)
		if target.Root != root {
			targets[name] = target
		}
	}
	var parentNeeded int
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM source_catalog_observation o
		JOIN source_catalog_directory d ON d.file_source_id=o.file_source_id AND d.path=o.directory_path
		WHERE d.file_source_id=? AND d.generation=? AND d.catalog_root=?
			AND o.kind='audio' AND o.directory_path<>?)`,
		sourceID, generation, root, root).Scan(&parentNeeded); err != nil {
		return nil, false, err
	}
	if parentNeeded == 0 {
		for _, name := range names {
			if _, ok := targets[name]; !ok {
				parentNeeded = 1
				break
			}
		}
	}
	return targets, parentNeeded != 0, nil
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
	var child, matched int
	err := tx.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(matched),0) FROM (
		SELECT EXISTS(
			SELECT 1 FROM source_catalog_observation p
			JOIN source_catalog_directory pd ON pd.file_source_id=p.file_source_id AND pd.path=p.directory_path
			WHERE p.file_source_id=? AND p.directory_path=? AND p.kind='audio' AND p.revision=pd.revision
				AND abs(p.size_bytes-o.size_bytes)<=max(1024,o.size_bytes/1000)) AS matched
		FROM source_catalog_observation o
		JOIN source_catalog_directory d ON d.file_source_id=o.file_source_id AND d.path=o.directory_path
		WHERE o.file_source_id=? AND o.directory_path=? AND o.kind='audio' AND o.revision=d.revision)`,
		sourceID, parent, sourceID, directoryPath).Scan(&child, &matched)
	if err != nil {
		return false, err
	}
	// A folder with a handful of shared tracks is a real work with a reused
	// intro; a package copy repeats nearly all of its audio.
	return child >= 20 && matched*10 >= child*9, nil
}
