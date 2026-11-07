package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
)

const catalogGroupingRows = 64

type catalogGroupingObserveKey struct{}

func observeCatalogGrouping(ctx context.Context, phase string, rows int) {
	if observer, ok := ctx.Value(catalogGroupingObserveKey{}).(func(string, int)); ok {
		observer(phase, rows)
	}
}

// A grouping version describes one complete directory revision. The tables
// contain derived evidence, never another work identity or a published card.
type catalogGroupingIndexInput struct {
	SourceID, Generation, ListingID int64
	Root                            string
	Revision                        int
}

func (in catalogGroupingIndexInput) version() string {
	if in.ListingID != 0 {
		return fmt.Sprintf("listing:%d", in.ListingID)
	}
	return fmt.Sprintf("observation:%d:%d", in.Generation, in.Revision)
}

type catalogNameEvidence struct {
	Name, Prefix, Identity, Base, Part, Structured, EpisodeSeries string
	PartRank, StructuredPosition, PrefixPosition, EpisodePosition int
	Episode                                                       catalogLooseEpisode
}

func classifyCatalogName(root, name string) catalogNameEvidence {
	e := catalogNameEvidence{Name: name, Prefix: catalogWorkPrefix(name), Identity: catalogWorkIdentityTitle(name), Base: catalogBaseTitle(name), PrefixPosition: -1, StructuredPosition: -1}
	if neutral, rank, ok := catalogPartTitle(name); ok {
		e.Part, e.PartRank = neutral, rank
	}
	stem := strings.TrimSpace(catalogFileStem(name))
	rest := stem
	if e.Prefix != "" && len(stem) >= len(e.Prefix) && strings.EqualFold(stem[:len(e.Prefix)], e.Prefix) {
		rest = strings.TrimLeft(stem[len(e.Prefix):], " ._-、")
		if m := catalogPrefixedTrack.FindStringSubmatch(stem[len(e.Prefix):]); m != nil {
			e.PrefixPosition, _ = strconv.Atoi(m[1])
		}
	}
	if m := catalogStructuredTrackPrefix.FindStringSubmatch(rest); m != nil {
		if position, err := strconv.Atoi(m[2]); err == nil && position <= 999 {
			e.Structured = e.Prefix + "\n" + strings.ToLower(m[1])
			e.StructuredPosition = position
		}
	}
	base := stem
	if canonical, ok := catalogEpisodeFormatCopy(stem); ok {
		base = canonical
	}
	if evidence, ok := catalogEpisodeEvidence(path.Join("/", base)); ok && evidence.Position > 0 {
		title := evidence.Title
		if title == "" {
			title = path.Base(strings.TrimRight(root, "/"))
		}
		if title != "" {
			e.EpisodeSeries = strings.ToLower(title)
			e.EpisodePosition = evidence.Position
			e.Episode = catalogLooseEpisode{Key: e.EpisodeSeries + "|" + strconv.Itoa(evidence.Position), SeriesKey: e.EpisodeSeries, Title: base, Position: evidence.Position, Evidence: catalogSeriesEvidence{Key: strings.TrimRight(root, "/") + "\n" + e.EpisodeSeries, Title: title, Position: evidence.Position}}
		}
	}
	return e
}

// prepareCatalogGrouping releases the writer between every 64 input names or
// aggregate keys. Its cursor and those rows commit together, including when a
// directory contains one group much larger than a provider page.
func (s *Server) prepareCatalogGrouping(ctx context.Context, in catalogGroupingIndexInput) (int64, error) {
	if err := s.cleanupCatalogGroupingVersions(ctx, in); err != nil {
		return 0, err
	}
	var id int64
	for {
		tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
		if err != nil {
			return 0, err
		}
		if id == 0 {
			err = tx.QueryRowContext(ctx, `INSERT INTO source_catalog_grouping(file_source_id,root_path,version) VALUES(?,?,?) ON CONFLICT(file_source_id,root_path,version) DO UPDATE SET version=excluded.version RETURNING id`, in.SourceID, in.Root, in.version()).Scan(&id)
		}
		var phase, cursor string
		if err == nil {
			err = tx.QueryRowContext(ctx, `SELECT phase,cursor FROM source_catalog_grouping WHERE id=?`, id).Scan(&phase, &cursor)
		}
		if err != nil {
			release()
			return 0, err
		}
		if phase == "ready" {
			release()
			return id, nil
		}
		observeCatalogGrouping(ctx, "phase:"+phase, 0)
		var done bool
		switch phase {
		case "names":
			cursor, done, err = stageCatalogGroupingNames(ctx, tx.Tx, id, in, cursor)
		case "folders":
			cursor, done, err = stageCatalogGroupingFolders(ctx, tx.Tx, id, in, cursor)
		case "folder_buckets":
			cursor, done, err = stageCatalogGroupingBuckets(ctx, tx.Tx, id, "folder", cursor)
		case "part", "structured", "episode", "prefix":
			cursor, done, err = stageCatalogGroupingBuckets(ctx, tx.Tx, id, phase, cursor)
		case "fold":
			cursor, done, err = stageCatalogGroupingFold(ctx, tx.Tx, id, cursor)
		case "mode":
			// Name evidence is immutable for this version. Stream the global
			// sequence predicates without holding SQLite's writer while parsing.
			release()
			readTx, readErr := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
			if readErr != nil {
				return 0, readErr
			}
			var mode string
			mode, err = catalogIndexedSplitMode(ctx, readTx, id)
			_ = readTx.Rollback()
			if err != nil {
				return 0, err
			}
			tx, release, err = beginTxWithDatabaseBusyRetry(ctx, s.db)
			if err != nil {
				return 0, err
			}
			if err == nil {
				_, err = tx.ExecContext(ctx, `UPDATE source_catalog_grouping SET mode=? WHERE id=?`, mode, id)
			}
			done = true
		case "targets":
			cursor, done, err = stageCatalogGroupingTargets(ctx, tx.Tx, id, in.Root, cursor)
		default:
			err = fmt.Errorf("unknown catalog grouping phase %q", phase)
		}
		if err == nil {
			if done {
				phase = nextCatalogGroupingPhase(phase)
				cursor = ""
			}
			_, err = tx.ExecContext(ctx, `UPDATE source_catalog_grouping SET phase=?,cursor=? WHERE id=?`, phase, cursor, id)
		}
		observeCatalogGrouping(ctx, "timing:commit", 0)
		if err == nil {
			err = tx.Commit()
		}
		release()
		if err != nil {
			return 0, err
		}
		if err = pauseCatalogWriter(ctx); err != nil {
			return 0, err
		}
	}
}

func nextCatalogGroupingPhase(phase string) string {
	switch phase {
	case "names":
		return "folders"
	case "folders":
		return "folder_buckets"
	case "folder_buckets":
		return "part"
	case "part":
		return "structured"
	case "structured":
		return "episode"
	case "episode":
		return "fold"
	case "fold":
		return "prefix"
	case "prefix":
		return "mode"
	case "mode":
		return "targets"
	default:
		return "ready"
	}
}

func stageCatalogGroupingNames(ctx context.Context, tx *sql.Tx, id int64, in catalogGroupingIndexInput, after string) (string, bool, error) {
	query := `SELECT name,size_bytes,content_hash FROM source_catalog_observation WHERE file_source_id=? AND directory_path=? AND revision=? AND kind='audio' AND name>? ORDER BY name LIMIT 64`
	args := []any{in.SourceID, in.Root, in.Revision, after}
	if in.ListingID != 0 {
		query = `SELECT name,size_bytes,content_hash FROM source_catalog_listing_item WHERE listing_id=? AND directory_path=? AND kind='audio' AND name>? AND instr(name,'/')=0 ORDER BY name LIMIT 64`
		args = []any{in.ListingID, in.Root, after}
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return after, false, err
	}
	names := []catalogAudioFile{}
	for rows.Next() {
		var name catalogAudioFile
		if err = rows.Scan(&name.Name, &name.Size, &name.Hash); err != nil {
			break
		}
		names = append(names, name)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return after, false, err
	}
	for _, file := range names {
		name := file.Name
		observeCatalogGrouping(ctx, "timing:classify", 0)
		e := classifyCatalogName(in.Root, name)
		observeCatalogGrouping(ctx, "timing:insert", 0)
		_, err = tx.ExecContext(ctx, `INSERT INTO source_catalog_grouping_name(grouping_id,name,normalized_name,folded_name,size_bytes,content_hash,work_prefix,identity_title,base_title,part_base,part_rank,structured_key,structured_position,prefix_position,episode_series,episode_position,episode_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, name, strings.ToLower(strings.TrimSpace(name)), catalogSimpleFoldKey(name), file.Size, strings.TrimSpace(file.Hash), e.Prefix, e.Identity, e.Base, e.Part, e.PartRank, e.Structured, e.StructuredPosition, e.PrefixPosition, e.EpisodeSeries, e.EpisodePosition, mustJSON(e.Episode))
		if err != nil {
			return after, false, err
		}
		after = name
	}
	observeCatalogGrouping(ctx, "classify", len(names))
	return after, len(names) < catalogGroupingRows, nil
}

type catalogGroupingBucket struct {
	Count        int             `json:"count"`
	Min          int             `json:"min"`
	Max          int             `json:"max"`
	Distinct     int             `json:"distinct"`
	First        json.RawMessage `json:"first"`
	Last         json.RawMessage `json:"last"`
	Second       int             `json:"second"`
	LastPosition int             `json:"last_position"`
}

func catalogReadGroupingBucket(ctx context.Context, tx *sql.Tx, id int64, kind, key string) (catalogGroupingBucket, error) {
	var b catalogGroupingBucket
	if key == "" {
		return b, nil
	}
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT payload FROM source_catalog_grouping_bucket WHERE grouping_id=? AND kind=? AND key=?`, id, kind, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return b, nil
	}
	if err != nil {
		return b, err
	}
	err = json.Unmarshal([]byte(raw), &b)
	return b, err
}

func stageCatalogGroupingFold(ctx context.Context, tx *sql.Tx, id int64, after string) (string, bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT n.name,CASE WHEN json_extract(b.payload,'$.first')=1 AND json_extract(b.payload,'$.second')=1 THEN n.part_base ELSE n.base_title END FROM source_catalog_grouping_name n LEFT JOIN source_catalog_grouping_bucket b ON b.grouping_id=n.grouping_id AND b.kind='part' AND b.key=n.part_base WHERE n.grouping_id=? AND n.name>? ORDER BY n.name LIMIT 64`, id, after)
	if err != nil {
		return after, false, err
	}
	type item struct{ name, base string }
	batch := []item{}
	for rows.Next() {
		var e item
		if err = rows.Scan(&e.name, &e.base); err != nil {
			break
		}
		batch = append(batch, e)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return after, false, err
	}
	for _, e := range batch {
		_, err = tx.ExecContext(ctx, `UPDATE source_catalog_grouping_name SET folded_base=? WHERE grouping_id=? AND name=?`, e.base, id, e.name)
		if err != nil {
			return after, false, err
		}
		after = e.name
	}
	return after, len(batch) < catalogGroupingRows, nil
}

func stageCatalogGroupingTargets(ctx context.Context, tx *sql.Tx, id int64, root, after string) (string, bool, error) {
	var mode string
	if err := tx.QueryRowContext(ctx, `SELECT mode FROM source_catalog_grouping WHERE id=?`, id).Scan(&mode); err != nil {
		return after, false, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT name,work_prefix,part_base,structured_key,episode_series,episode_json FROM source_catalog_grouping_name WHERE grouping_id=? AND name>? ORDER BY name LIMIT 64`, id, after)
	if err != nil {
		return after, false, err
	}
	type item struct{ name, prefix, part, structured, series, episode string }
	batch := []item{}
	for rows.Next() {
		var e item
		if err = rows.Scan(&e.name, &e.prefix, &e.part, &e.structured, &e.series, &e.episode); err != nil {
			break
		}
		batch = append(batch, e)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return after, false, err
	}
	for _, e := range batch {
		g := catalogLooseGrouping{mode: mode, splitPrefixes: map[string]bool{}, partFold: map[string]string{}, structured: map[string]string{}, episodes: map[string]catalogLooseEpisode{}}
		b, err := catalogReadGroupingBucket(ctx, tx, id, "part", e.part)
		if err != nil {
			return after, false, err
		}
		if string(b.First) == "1" && b.Second == 1 {
			g.partFold[e.name] = e.part
		}
		b, err = catalogReadGroupingBucket(ctx, tx, id, "structured", e.structured)
		if err != nil {
			return after, false, err
		}
		if b.Count >= 2 && b.Min <= 1 && b.Distinct == b.Count && b.Max-b.Min+1 == b.Count {
			pieces := strings.SplitN(e.structured, "\n", 2)
			key := pieces[0]
			if key == "" {
				key = "structured:" + pieces[1]
			}
			g.structured[e.name] = key
		}
		b, err = catalogReadGroupingBucket(ctx, tx, id, "episode", e.series)
		if err != nil {
			return after, false, err
		}
		if b.Count >= 2 {
			var episode catalogLooseEpisode
			if err = json.Unmarshal([]byte(e.episode), &episode); err != nil {
				return after, false, err
			}
			episode.Evidence.Explicit = b.Min != b.Max
			g.episodes[e.name] = episode
		}
		b, err = catalogReadGroupingBucket(ctx, tx, id, "prefix", e.prefix)
		if err != nil {
			return after, false, err
		}
		g.splitPrefixes[e.prefix] = string(b.First) != string(b.Last) && (b.Min < 0 || b.Min > 1 || b.Distinct != b.Count || b.Max-b.Min+1 != b.Count)
		key := g.keyFor(e.name)
		target := catalogSplitTarget{Root: root + "::" + key, SourceRoot: root, Title: catalogFileStem(e.name)}
		if episode, ok := g.episodes[e.name]; ok {
			target.Title = episode.Title
			target.Episode = episode.Evidence
			target.HasEpisode = true
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO source_catalog_grouping_target(grouping_id,key,target_json) VALUES(?,?,?) ON CONFLICT DO NOTHING`, id, key, mustJSON(target))
		if err != nil {
			return after, false, err
		}
		_, err = tx.ExecContext(ctx, `UPDATE source_catalog_grouping_name SET group_key=?,target_json=(SELECT target_json FROM source_catalog_grouping_target WHERE grouping_id=? AND key=?) WHERE grouping_id=? AND name=?`, key, id, key, id, e.name)
		if err != nil {
			return after, false, err
		}
		after = e.name
	}
	observeCatalogGrouping(ctx, "target", len(batch))
	return after, len(batch) < catalogGroupingRows, nil
}
