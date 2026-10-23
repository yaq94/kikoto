package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/yexca/kikoto/backend/internal/openlist"
)

// Only authored episode markers or an explicitly labelled series directory are
// evidence. A creator, topic, plain numeric track or format name is not.
// An English episode marker is a word plus a number. The Japanese/Chinese
// form needs its counter: "第3話" names an episode, while "第3章" names a
// chapter inside one work and must not create a series from chapter folders.
var catalogEpisodeMarker = regexp.MustCompile(`(?i)^(.*?)\s*(?:\bepisode\s*|\bep\.?\s*)([0-9]{1,6})(?:\s*[-:：._]\s*|\s+)?(.*)$`)
var catalogEpisodeMarkerCJK = regexp.MustCompile(`^(.*?)\s*第\s*([0-9]{1,6})\s*(?:集|話|话|回)(?:\s*[-:：._]\s*|\s+)?(.*)$`)
var catalogSeriesPrefix = regexp.MustCompile(`(?i)^(?:series|系列|シリーズ)\s*[:： -]\s*(.+)$`)
var catalogSeriesSuffix = regexp.MustCompile(`(?i)^(.+?)\s*(?:系列|シリーズ|\sseries)$`)
var catalogNonEpisode = regexp.MustCompile(`(?i)^(?:(?:mp3|flac|wav|m4a|ogg|opus|aac|aiff|dsd|ape|wma)(?:\s*\d+(?:k|bit|hz|kbps).*)?|tracks?|分轨|分軌|音轨|音軌|音声|音聲|audio|disc\s*[._\-]?\s*\d+|cd\s*[._\-]?\s*\d+)$`)

// catalogStructuralPartName reports a directory that numbers a piece of one
// release (Chapter 2, Part 1, Track 3, Disc 1, Scene 4). Such a name never
// identifies an episode, even below a directory that carries a series label.
// Chapter and section markers name a piece of one release as well. The CJK
// forms are written without a space, so the shared prefix pattern cannot see
// them; the episode counters 話/集/回 are deliberately missing here because
// those really are episodes.
var catalogStructuralPartCJK = regexp.MustCompile(`^第\s*[0-9]{1,6}\s*(?:章|節|节|幕|部分)`)

func catalogStructuralPartName(name string) bool {
	name = strings.TrimSpace(name)
	return catalogStructuredTrackPrefix.MatchString(name) || catalogStructuralPartCJK.MatchString(name)
}

type catalogSeriesEvidence struct {
	Key, Title string
	Position   int
	// Explicit is true when the source names the series itself, rather than
	// merely numbering one episode below an ordinary directory.
	Explicit bool
}

// catalogSeriesLabel reads the explicit label on a directory: "Series: X",
// "系列：X", "X Series" or "X系列". It is the only source of a series name; a
// child marker never invents one.
func catalogSeriesLabel(name string) string {
	if m := catalogSeriesPrefix.FindStringSubmatch(name); m != nil {
		return strings.TrimSpace(m[1])
	}
	if m := catalogSeriesSuffix.FindStringSubmatch(name); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// catalogEpisodeEvidence resolves one directory to a series and an optional
// position. The parent's explicit label owns the series identity for every
// child, so an "Opening" without a marker and an "Episode 1" under the same
// labelled directory share one series. An empty title means the caller should
// use the source display name (the configured root has no directory name).
func catalogEpisodeEvidence(root string) (catalogSeriesEvidence, bool) {
	name := path.Base(root)
	parent := path.Dir(root)
	if catalogNonEpisode.MatchString(name) || catalogWorkPartDirectory(name) {
		return catalogSeriesEvidence{}, false
	}
	// Chapter, Part, Track, Disc and Scene number the pieces of one release.
	// Even below an explicitly labelled series directory they are parts of one
	// work, not episodes: turning them into members would split a single
	// recording into several series entries.
	if catalogStructuralPartName(name) {
		return catalogSeriesEvidence{}, false
	}
	seriesLabel := catalogSeriesLabel(path.Base(parent))
	markerTitle, position := "", 0
	match := catalogEpisodeMarker.FindStringSubmatch(name)
	if match == nil {
		match = catalogEpisodeMarkerCJK.FindStringSubmatch(name)
	}
	if match != nil {
		n, err := strconv.Atoi(match[2])
		if err != nil || n <= 0 {
			return catalogSeriesEvidence{}, false
		}
		// "Episode 1 MP3" is a format copy of episode 1, not a second episode.
		if suffix := strings.TrimSpace(match[3]); suffix != "" &&
			(catalogFormatDirectory(suffix) || catalogAuxiliaryDirectory(suffix)) {
			return catalogSeriesEvidence{}, false
		}
		markerTitle = strings.TrimSpace(match[1])
		position = n
	} else if seriesLabel == "" {
		return catalogSeriesEvidence{}, false
	}
	title := ""
	if seriesLabel != "" {
		// A labelled directory names the series for every child. Using the
		// directory itself keeps an "Opening" track and "Episode 1" in the same
		// series instead of two keys derived from different spellings.
		title = path.Base(parent)
	} else {
		title = markerTitle
	}
	if title == "" {
		title = path.Base(parent)
	}
	if title == "." || title == "/" {
		title = ""
	}
	return catalogSeriesEvidence{
		Key:      parent + "\n" + strings.ToLower(title),
		Title:    title,
		Position: position,
		Explicit: seriesLabel != "",
	}, true
}

// catalogEpisodeFormatCopy reports a directory that names an episode and a
// format copy of it, such as "Episode 1 (MP3)" or "Episode 2 FLAC". It returns
// the episode name without the format marker so the copy can be folded into
// the episode beside it.
func catalogEpisodeFormatCopy(name string) (string, bool) {
	pattern := catalogEpisodeMarker
	match := pattern.FindStringSubmatchIndex(name)
	if match == nil {
		// The CJK marker has one more capture (its counter), so the suffix is
		// always the last group of whichever pattern matched.
		pattern = catalogEpisodeMarkerCJK
		match = pattern.FindStringSubmatchIndex(name)
	}
	if match == nil {
		return "", false
	}
	last := len(match) - 2
	suffix := strings.TrimSpace(name[match[last]:match[last+1]])
	if suffix == "" || (!catalogFormatDirectory(suffix) && !catalogAuxiliaryDirectory(suffix)) {
		return "", false
	}
	// Everything before the format marker is the episode's own name, kept
	// verbatim so "第1集" only folds into "第1集", never into a rebuilt
	// "Episode 1" that the source never published.
	episode := strings.TrimRight(strings.TrimSpace(name[:match[last]]), " \t-:：._")
	if episode == "" {
		return "", false
	}
	return episode, true
}

// catalogEpisodePositionsTx counts distinct episode numbers among the direct
// child folders of one parent. A format variant and its bare sibling count as
// the same position; two format folders for one episode do not create a
// series by themselves.
func catalogEpisodePositionsTx(ctx context.Context, tx *sql.Tx, sourceID, generation int64, parent string) (int, map[string]int, error) {
	if generation == 0 {
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(generation),0)
			FROM source_catalog_directory WHERE file_source_id=? AND path=?`,
			sourceID, parent).Scan(&generation); err != nil {
			return 0, nil, err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT o.name FROM source_catalog_observation o
		JOIN source_catalog_directory d ON d.file_source_id=o.file_source_id AND d.path=o.directory_path
		WHERE o.file_source_id=? AND o.directory_path=? AND o.kind='folder'
			AND o.revision=d.revision AND d.generation=?`, sourceID, parent, generation)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = rows.Close() }()
	positions := map[int]bool{}
	canonicalCounts := map[string]int{}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			return 0, nil, err
		}
		canonical, formatCopy := catalogEpisodeFormatCopy(name)
		if !formatCopy {
			canonical = name
		}
		evidence, ok := catalogEpisodeEvidence(path.Join(parent, canonical))
		if !ok || evidence.Position <= 0 {
			continue
		}
		positions[evidence.Position] = true
		canonicalCounts[canonical]++
	}
	return len(positions), canonicalCounts, rows.Err()
}

// catalogLooseEpisode groups direct audio files that name their episode
// explicitly. A bare numbered run is a track list, not a series, so only the
// authored Episode/Ep./第N集 marker is evidence here. Format variants of one
// episode share a key and therefore one member card.
type catalogLooseEpisode struct {
	Key       string
	SeriesKey string
	Title     string
	Position  int
	Evidence  catalogSeriesEvidence
}

func catalogLooseEpisodeFiles(root string, names []string) (map[string]catalogLooseEpisode, bool) {
	if len(names) < 2 {
		return nil, false
	}
	episodes := make(map[string]catalogLooseEpisode, len(names))
	positions := map[string]map[int]bool{}
	episodeCounts := map[string]int{}
	for _, name := range names {
		stem := strings.TrimSpace(catalogFileStem(name))
		if stem == "" {
			return nil, false
		}
		base := stem
		if copyName, ok := catalogEpisodeFormatCopy(stem); ok {
			base = copyName
		}
		evidence, ok := catalogEpisodeEvidence(path.Join("/", base))
		if !ok || evidence.Position <= 0 {
			return nil, false
		}
		seriesTitle := evidence.Title
		if seriesTitle == "" {
			seriesTitle = path.Base(strings.TrimRight(root, "/"))
		}
		if seriesTitle == "" {
			return nil, false
		}
		seriesKey := strings.ToLower(seriesTitle)
		key := seriesKey + "|" + strconv.Itoa(evidence.Position)
		episode, exists := episodes[name]
		if !exists {
			episode = catalogLooseEpisode{
				Key:       key,
				SeriesKey: seriesKey,
				Title:     base,
				Position:  evidence.Position,
				Evidence: catalogSeriesEvidence{
					Key:      strings.TrimRight(root, "/") + "\n" + strings.ToLower(seriesTitle),
					Title:    seriesTitle,
					Position: evidence.Position,
				},
			}
			episodes[name] = episode
		}
		if positions[seriesKey] == nil {
			positions[seriesKey] = map[int]bool{}
		}
		positions[seriesKey][evidence.Position] = true
		episodeCounts[seriesKey]++
	}
	complete := true
	for name, episode := range episodes {
		if len(positions[episode.SeriesKey]) < 2 && episodeCounts[episode.SeriesKey] < 2 {
			delete(episodes, name)
			complete = false
		}
	}
	for name, episode := range episodes {
		episode.Evidence.Explicit = len(positions[episode.SeriesKey]) >= 2
		episodes[name] = episode
	}
	if !complete {
		return nil, false
	}
	return episodes, true
}

// catalogLooseEpisodeSubset recognizes explicit episode groups inside a mixed
// creator folder. Unrelated files stay with the ordinary file grouping; a lone
// episode is not a series and is left on the ordinary path.
func catalogLooseEpisodeSubset(root string, names []string) map[string]catalogLooseEpisode {
	if len(names) < 2 {
		return nil
	}
	episodes := map[string]catalogLooseEpisode{}
	positions := map[string]map[int]bool{}
	episodeCounts := map[string]int{}
	for _, name := range names {
		stem := strings.TrimSpace(catalogFileStem(name))
		if stem == "" {
			continue
		}
		base := stem
		if copyName, ok := catalogEpisodeFormatCopy(stem); ok {
			base = copyName
		}
		evidence, ok := catalogEpisodeEvidence(path.Join("/", base))
		if !ok || evidence.Position <= 0 {
			continue
		}
		seriesTitle := evidence.Title
		if seriesTitle == "" {
			seriesTitle = path.Base(strings.TrimRight(root, "/"))
		}
		if seriesTitle == "" {
			continue
		}
		seriesKey := strings.ToLower(seriesTitle)
		key := seriesKey + "|" + strconv.Itoa(evidence.Position)
		if _, exists := episodes[name]; !exists {
			episodes[name] = catalogLooseEpisode{
				Key:       key,
				SeriesKey: seriesKey,
				Title:     base,
				Position:  evidence.Position,
				Evidence: catalogSeriesEvidence{
					Key:      strings.TrimRight(root, "/") + "\n" + strings.ToLower(seriesTitle),
					Title:    seriesTitle,
					Position: evidence.Position,
				},
			}
		}
		if positions[seriesKey] == nil {
			positions[seriesKey] = map[int]bool{}
		}
		positions[seriesKey][evidence.Position] = true
		episodeCounts[seriesKey]++
	}
	for name, episode := range episodes {
		if len(positions[episode.SeriesKey]) < 2 && episodeCounts[episode.SeriesKey] < 2 {
			delete(episodes, name)
		}
	}
	for name, episode := range episodes {
		episode.Evidence.Explicit = len(positions[episode.SeriesKey]) >= 2
		episodes[name] = episode
	}
	return episodes
}

type catalogEpisodeRootSplit struct {
	Files      []openlist.File
	SourceRoot string
	Title      string
	Evidence   catalogSeriesEvidence
}

// splitCatalogEpisodeRoots partitions an unregistered finite scan only when
// every playable file belongs to an evidenced episode. A bare episode keeps its
// real directory as the card root. Format-only episodes use a virtual canonical
// root below the parent so MP3 and FLAC copies share one member. Loose episode
// files likewise become virtual members below their real directory.
func splitCatalogEpisodeRoots(root string, files []openlist.File) (map[string]catalogEpisodeRootSplit, []openlist.File) {
	directNames := []string{}
	nestedAudio := false
	for _, file := range files {
		if file.Kind != "audio" {
			continue
		}
		parts := strings.SplitN(file.Path, "/", 2)
		if len(parts) == 1 {
			directNames = append(directNames, file.Path)
		} else {
			nestedAudio = true
		}
	}
	if len(directNames) > 0 {
		// A directory may combine loose episode files with cover folders. The
		// direct files decide the series; nested non-audio stays on the parent.
		if nestedAudio {
			return nil, nil
		}
		episodes, ok := catalogLooseEpisodeFiles(root, directNames)
		if !ok {
			return nil, nil
		}
		result := map[string]catalogEpisodeRootSplit{}
		for name, episode := range episodes {
			episodeRoot := root + "::episode:" + episode.Key
			split := result[episodeRoot]
			split.SourceRoot = root
			split.Title = episode.Title
			split.Evidence = episode.Evidence
			for _, file := range files {
				if file.Kind == "audio" && file.Path == name {
					split.Files = append(split.Files, file)
				}
			}
			result[episodeRoot] = split
		}
		parent := []openlist.File{}
		for _, file := range files {
			if file.Kind != "audio" {
				parent = append(parent, file)
			}
		}
		return result, parent
	}

	type childEpisode struct {
		name      string
		canonical string
		evidence  catalogSeriesEvidence
	}
	children := map[string]childEpisode{}
	canonicalChildren := map[string]int{}
	positions := map[int]bool{}
	formatCanonicals := map[string]bool{}
	for _, file := range files {
		if file.Kind != "audio" {
			continue
		}
		parts := strings.SplitN(file.Path, "/", 2)
		if len(parts) != 2 {
			return nil, nil
		}
		if _, seen := children[parts[0]]; seen {
			continue
		}
		canonical, formatCopy := catalogEpisodeFormatCopy(parts[0])
		if !formatCopy {
			canonical = parts[0]
		}
		evidence, ok := catalogEpisodeEvidence(path.Join(root, canonical))
		if !ok {
			return nil, nil
		}
		children[parts[0]] = childEpisode{name: parts[0], canonical: canonical, evidence: evidence}
		canonicalChildren[canonical]++
		positions[evidence.Position] = true
		if formatCopy {
			formatCanonicals[canonical] = true
		}
	}
	if len(children) < 2 {
		return nil, nil
	}
	virtual := map[string]bool{}
	for canonical, count := range canonicalChildren {
		if count > 1 || (len(positions) >= 2 && formatCanonicals[canonical]) {
			virtual[canonical] = true
		}
	}
	result := map[string]catalogEpisodeRootSplit{}
	for _, child := range children {
		episodeRoot := path.Join(root, child.canonical)
		sourceRoot := ""
		if virtual[child.canonical] {
			sourceRoot = root
		}
		split := result[episodeRoot]
		split.SourceRoot = sourceRoot
		split.Title = child.canonical
		split.Evidence = child.evidence
		split.Evidence.Explicit = len(positions) >= 2
		for _, file := range files {
			if !strings.HasPrefix(file.Path, child.name+"/") {
				continue
			}
			copied := file
			if sourceRoot == "" {
				copied.Path = strings.TrimPrefix(file.Path, child.name+"/")
			}
			split.Files = append(split.Files, copied)
		}
		result[episodeRoot] = split
	}
	parent := []openlist.File{}
	for _, file := range files {
		if file.Kind == "folder" {
			continue
		}
		parts := strings.SplitN(file.Path, "/", 2)
		if len(parts) == 2 {
			if _, ok := children[parts[0]]; ok {
				continue
			}
		}
		parent = append(parent, file)
	}
	return result, parent
}

// attachCatalogSeriesForRootTx attaches an episode card using the location its
// caller already published. It never re-reads file_source_id or root_path.
func attachCatalogSeriesForRootTx(ctx context.Context, tx *sql.Tx, entryID, sourceID int64, root string) error {
	// A published work code identifies the work; it does not override an
	// explicit series directory. Series membership and work identity are
	// independent levels.
	evidence, ok := catalogEpisodeEvidence(root)
	if !ok {
		return nil
	}
	return attachCatalogSeriesEvidenceTx(ctx, tx, entryID, sourceID, evidence)
}

func attachCatalogSeriesEvidenceTx(ctx context.Context, tx *sql.Tx, entryID, sourceID int64, evidence catalogSeriesEvidence) error {
	var existing int64
	err := tx.QueryRowContext(ctx, `SELECT series_id FROM catalog_series_member WHERE catalog_entry_id=?`, entryID).Scan(&existing)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if !evidence.Explicit {
		parent, _, ok := strings.Cut(evidence.Key, "\n")
		if !ok {
			return nil
		}
		positions, _, err := catalogEpisodePositionsTx(ctx, tx, sourceID, 0, parent)
		if err != nil {
			return err
		}
		if positions < 2 {
			return nil
		}
	}
	title := evidence.Title
	if title == "" {
		// Episodes placed directly at the configured root have no directory
		// name to borrow; the source is the only stable label.
		if err = tx.QueryRowContext(ctx, `SELECT display_name FROM file_source WHERE id=?`, sourceID).Scan(&title); err != nil {
			return err
		}
		title = strings.TrimSpace(title)
	}
	if title == "" {
		return nil
	}
	var seriesID int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM catalog_series WHERE source_id=? AND source_key=?`, sourceID, evidence.Key).Scan(&seriesID)
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, `INSERT INTO catalog_series(title,source_id,source_key) VALUES(?,?,?) RETURNING id`, title, sourceID, evidence.Key).Scan(&seriesID)
	}
	if err != nil {
		return err
	}
	var count, maxPosition int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MAX(position),0) FROM catalog_series_member WHERE series_id=?`, seriesID).Scan(&count, &maxPosition); err != nil {
		return err
	}
	if count >= maxCatalogSeriesMembers {
		return fmt.Errorf("%w: series %d has %d members", errCatalogSeriesTooLarge, seriesID, count)
	}
	position := evidence.Position
	if position == 0 {
		position = maxPosition + 1
	}
	if evidence.Position > 0 {
		// An explicitly numbered episode outranks a label-only child that took
		// the slot while listings arrived in a different order. A second
		// explicit episode with the same number is a genuine duplicate and stays
		// independent for review.
		var occupiedRoot string
		err = tx.QueryRowContext(ctx, `SELECT e.root_path FROM catalog_series_member m
			JOIN source_catalog_entry e ON e.id=m.catalog_entry_id
			WHERE m.series_id=? AND m.position=?`, seriesID, position).Scan(&occupiedRoot)
		switch {
		case err == nil:
			occupied, marked := catalogEpisodeEvidence(occupiedRoot)
			if marked && occupied.Position != 0 {
				// Two distinct directories claim the same explicit number, such
				// as 第1話 and 第1集 or the same label under different parents.
				// They are separate episodes, so keep both addressable instead
				// of dropping the later one from the series listing.
				position = maxPosition + 1
			} else if _, err = tx.ExecContext(ctx, `UPDATE catalog_series_member SET position=? WHERE series_id=? AND position=?`, maxPosition+1, seriesID, position); err != nil {
				return err
			}
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence) VALUES(?,?,?,'directory')`, seriesID, entryID, position)
	return err
}

// Keep one episode transaction below the documented 100-object write bound.
const catalogEpisodeRootsPerBatch = 100

func (s *Server) persistCatalogEpisodeRoots(ctx context.Context, source remoteSourceForUse, root string, files []openlist.File) (int64, bool, error) {
	split, parentFiles := splitCatalogEpisodeRoots(root, files)
	if len(split) == 0 {
		return 0, false, nil
	}
	roots := make([]string, 0, len(split))
	for child := range split {
		roots = append(roots, child)
	}
	sort.Strings(roots)
	existing, pinned, err := s.catalogEpisodeRootState(ctx, source.ID, root)
	if err != nil || pinned {
		return 0, pinned, err
	}
	keep := map[int64]bool{}
	var first int64
	for start := 0; start < len(roots); start += catalogEpisodeRootsPerBatch {
		end := start + catalogEpisodeRootsPerBatch
		if end > len(roots) {
			end = len(roots)
		}
		ids, err := s.persistCatalogEpisodeRootBatch(ctx, source.ID, roots[start:end], split)
		if err != nil {
			return 0, true, err
		}
		for _, id := range ids {
			keep[id] = true
			if first == 0 {
				first = id
			}
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, true, err
	}
	defer func() { _ = tx.Rollback() }()
	// A metadata-only parent keeps the root's artwork, readmes and subtitles.
	// It publishes no playable track, so it does not become a second work card,
	// but its file rows remain available to credit and metadata readers.
	if len(parentFiles) > 0 {
		parentID, persistErr := persistOpenListSnapshotTx(ctx, tx, source.ID, root, "", path.Base(root), parentFiles)
		if persistErr != nil {
			return 0, true, persistErr
		}
		keep[parentID] = true
	}
	// A removed or renumbered episode must not stay visible in the series or
	// as a standalone card. The current listing is a complete snapshot of this
	// directory, so unre-published episode cards are stale.
	if err = markStaleSnapshotCardsTx(ctx, tx, source.ID, root, keep); err != nil {
		return 0, true, err
	}
	if existing != 0 && len(parentFiles) == 0 {
		// The root had no work, series membership or personal record above, so
		// retiring it moves no evaluation object. Its files are now owned by
		// the episode entries. Keep the id as an alias of the first episode so
		// an older /catalog/<id> link still resolves instead of returning 404.
		if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_entry SET file_source_id=NULL,relocated_to_id=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, first, existing); err != nil {
			return 0, true, err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM source_catalog_file WHERE catalog_entry_id=?`, existing); err != nil {
			return 0, true, err
		}
	}
	return first, true, tx.Commit()
}

func (s *Server) catalogEpisodeRootState(ctx context.Context, sourceID int64, root string) (existing int64, pinned bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT id FROM source_catalog_entry WHERE file_source_id=? AND root_path=?`, sourceID, root).Scan(&existing)
	switch {
	case err == nil:
		// A root published before series discovery understood the evidence
		// still owns every file below it. Splitting it is safe only while no
		// shared or personal identity points at it; otherwise the root keeps
		// its own card and only its direct files.
		var busy int
		if err = s.db.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1 FROM source_catalog_entry e WHERE e.id=? AND (
				e.work_id IS NOT NULL
				OR EXISTS(SELECT 1 FROM catalog_series_member m WHERE m.catalog_entry_id=e.id)
				OR EXISTS(SELECT 1 FROM source_catalog_entry r WHERE r.relocated_to_id=e.id)))`, existing).Scan(&busy); err != nil {
			return 0, false, err
		}
		// A registered work or series member already owns this root. Its
		// identity and personal records win over later episode evidence;
		// splitting it needs explicit review.
		return existing, busy != 0, nil
	case errors.Is(err, sql.ErrNoRows):
		// The common first scan: only the episode roots get identities.
		return 0, false, nil
	default:
		return 0, false, err
	}
}

func (s *Server) persistCatalogEpisodeRootBatch(ctx context.Context, sourceID int64, roots []string, split map[string]catalogEpisodeRootSplit) ([]int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	ids := make([]int64, 0, len(roots))
	for _, child := range roots {
		part := split[child]
		id, err := persistOpenListSnapshotTx(ctx, tx, sourceID, child, part.SourceRoot, part.Title, part.Files)
		if err != nil {
			return nil, err
		}
		if err = attachCatalogSeriesEvidenceTx(ctx, tx, id, sourceID, part.Evidence); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return ids, nil
}
