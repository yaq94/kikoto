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
var catalogEpisodeMarker = regexp.MustCompile(`(?i)^(.*?)\s*(?:episode\s*|ep\.?\s*|第\s*)([0-9]{1,6})(?:\s*(?:集|話|话|回))?(?:\s*[-:：._ ]\s*.*)?$`)
var catalogSeriesPrefix = regexp.MustCompile(`(?i)^(?:series|系列|シリーズ)\s*[:： -]\s*(.+)$`)
var catalogSeriesSuffix = regexp.MustCompile(`(?i)^(.+?)\s*(?:系列|シリーズ|\sseries)$`)
var catalogNonEpisode = regexp.MustCompile(`(?i)^(?:(?:mp3|flac|wav|m4a|ogg|opus|aac|aiff|dsd|ape|wma)(?:\s*\d+(?:k|bit|hz|kbps).*)?|tracks?|分轨|分軌|音轨|音軌|音声|音聲|audio|disc\s*\d+|cd\s*\d+)$`)

type catalogSeriesEvidence struct {
	Key, Title string
	Position   int
}

func catalogEpisodeEvidence(root string) (catalogSeriesEvidence, bool) {
	name := path.Base(root)
	parent := path.Dir(root)
	if catalogNonEpisode.MatchString(name) {
		return catalogSeriesEvidence{}, false
	}
	if match := catalogEpisodeMarker.FindStringSubmatch(name); match != nil {
		n, err := strconv.Atoi(match[2])
		if err != nil || n <= 0 {
			return catalogSeriesEvidence{}, false
		}
		title := strings.TrimSpace(match[1])
		if title == "" {
			title = path.Base(parent)
		}
		if title == "." || title == "/" || catalogNonEpisode.MatchString(title) {
			return catalogSeriesEvidence{}, false
		}
		return catalogSeriesEvidence{Key: parent + "\n" + strings.ToLower(title), Title: title, Position: n}, true
	}
	title := ""
	if m := catalogSeriesPrefix.FindStringSubmatch(path.Base(parent)); m != nil {
		title = strings.TrimSpace(m[1])
	}
	if m := catalogSeriesSuffix.FindStringSubmatch(path.Base(parent)); m != nil {
		title = strings.TrimSpace(m[1])
	}
	if title != "" {
		return catalogSeriesEvidence{Key: parent + "\n" + strings.ToLower(title), Title: title}, true
	}
	return catalogSeriesEvidence{}, false
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
	if len(children) < 2 || len(children) > maxCatalogSeriesMembers {
		return nil
	}
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

// attachCatalogSeriesTx is shared by finite and resumable publication. It is
// deliberately local: no provider calls, work registration or private writes.
func attachCatalogSeriesTx(ctx context.Context, tx *sql.Tx, entryID int64) error {
	var sourceID int64
	var root, code string
	if err := tx.QueryRowContext(ctx, `SELECT file_source_id,root_path,primary_code FROM source_catalog_entry WHERE id=?`, entryID).Scan(&sourceID, &root, &code); err != nil {
		return err
	}
	if code != "" {
		return nil
	}
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
	var seriesID int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM catalog_series WHERE source_id=? AND source_key=?`, sourceID, evidence.Key).Scan(&seriesID)
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, `INSERT INTO catalog_series(title,source_id,source_key) VALUES(?,?,?) RETURNING id`, evidence.Title, sourceID, evidence.Key).Scan(&seriesID)
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
	// Duplicate episode numbers are ambiguous editions, not permission to merge.
	_, err = tx.ExecContext(ctx, `INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence) VALUES(?,?,?,'directory') ON CONFLICT DO NOTHING`, seriesID, entryID, position)
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
	var existing int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM source_catalog_entry WHERE file_source_id=? AND root_path=?`, source.ID, root).Scan(&existing)
	if err == nil {
		return 0, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, true, err
	}
	roots := make([]string, 0, len(split))
	for child := range split {
		roots = append(roots, child)
	}
	sort.Strings(roots)
	var first int64
	for _, child := range roots {
		id, err := persistOpenListSnapshotTx(ctx, tx, source.ID, child, split[child])
		if err != nil {
			return 0, true, err
		}
		if first == 0 {
			first = id
		}
	}
	return first, true, tx.Commit()
}
