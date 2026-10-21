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

type catalogSeriesEvidence struct {
	Key, Title string
	Position   int
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
	if catalogNonEpisode.MatchString(name) || catalogFormatDirectory(name) || catalogAuxiliaryDirectory(name) {
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
	return catalogSeriesEvidence{Key: parent + "\n" + strings.ToLower(title), Title: title, Position: position}, true
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

// splitCatalogEpisodeRoots partitions an unregistered finite scan only when
// every playable file belongs to an evidenced direct child. Real file paths
// are rebased below that child; format and track directories stay in it.
func splitCatalogEpisodeRoots(root string, files []openlist.File) map[string][]openlist.File {
	children := map[string]bool{}
	for _, file := range files {
		if file.Kind != "audio" {
			continue
		}
		parts := strings.SplitN(file.Path, "/", 2)
		if len(parts) != 2 {
			return nil
		}
		if _, ok := catalogEpisodeEvidence(path.Join(root, parts[0])); !ok {
			return nil
		}
		children[parts[0]] = true
	}
	if len(children) < 2 {
		// A lone episode is not a series yet; the entry keeps its own card.
		return nil
	}
	// More than maxCatalogSeriesMembers children are all split into independent
	// entries. attachCatalogSeriesForRootTx stops adding members at the limit, so the
	// extra episodes stay addressable instead of collapsing into one root card.
	result := map[string][]openlist.File{}
	for child := range children {
		for _, file := range files {
			if !strings.HasPrefix(file.Path, child+"/") {
				continue
			}
			file.Path = strings.TrimPrefix(file.Path, child+"/")
			result[path.Join(root, child)] = append(result[path.Join(root, child)], file)
		}
	}
	return result
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
	var existing int64
	err := tx.QueryRowContext(ctx, `SELECT series_id FROM catalog_series_member WHERE catalog_entry_id=?`, entryID).Scan(&existing)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
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
		return nil
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

func (s *Server) persistCatalogEpisodeRoots(ctx context.Context, source remoteSourceForUse, root string, files []openlist.File) (int64, bool, error) {
	split := splitCatalogEpisodeRoots(root, files)
	if len(split) == 0 {
		return 0, false, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, true, err
	}
	defer func() { _ = tx.Rollback() }()
	roots := make([]string, 0, len(split))
	for child := range split {
		roots = append(roots, child)
	}
	sort.Strings(roots)
	var first int64
	var existing int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM source_catalog_entry WHERE file_source_id=? AND root_path=?`, source.ID, root).Scan(&existing)
	switch {
	case err == nil:
		// A root published before series discovery understood the evidence
		// still owns every file below it. Splitting it is safe only while no
		// shared or personal identity points at it; otherwise the root keeps
		// its own card and only its direct files.
		var pinned int
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1 FROM source_catalog_entry e WHERE e.id=? AND (
				e.work_id IS NOT NULL
				OR EXISTS(SELECT 1 FROM catalog_series_member m WHERE m.catalog_entry_id=e.id)
				OR EXISTS(SELECT 1 FROM source_catalog_entry r WHERE r.relocated_to_id=e.id)))`, existing).Scan(&pinned); err != nil {
			return 0, true, err
		}
		if pinned != 0 {
			// A registered work or series member already owns this root. Its
			// identity and personal records win over later episode evidence;
			// splitting it needs explicit review.
			return 0, false, nil
		}
	case errors.Is(err, sql.ErrNoRows):
		// The common first scan: only the episode roots get identities.
	default:
		return 0, true, err
	}
	keep := map[int64]bool{}
	for _, child := range roots {
		id, err := persistOpenListSnapshotTx(ctx, tx, source.ID, child, "", path.Base(child), split[child])
		if err != nil {
			return 0, true, err
		}
		keep[id] = true
		if first == 0 {
			first = id
		}
	}
	// A removed or renumbered episode must not stay visible in the series or
	// as a standalone card. The current listing is a complete snapshot of this
	// directory, so unre-published episode cards are stale.
	if err = markStaleSnapshotCardsTx(ctx, tx, source.ID, root, keep); err != nil {
		return 0, true, err
	}
	if existing != 0 {
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
