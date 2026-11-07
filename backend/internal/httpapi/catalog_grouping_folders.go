package httpapi

import (
	"context"
	"database/sql"
	"path"
)

func stageCatalogGroupingFolders(ctx context.Context, tx *sql.Tx, id int64, in catalogGroupingIndexInput, after string) (string, bool, error) {
	query := `SELECT name FROM source_catalog_observation WHERE file_source_id=? AND directory_path=? AND revision=? AND kind='folder' AND name>? ORDER BY name LIMIT 64`
	args := []any{in.SourceID, in.Root, in.Revision, after}
	if in.ListingID != 0 {
		query = `SELECT name FROM source_catalog_listing_item WHERE listing_id=? AND directory_path=? AND kind='folder' AND name>? AND instr(name,'/')=0 ORDER BY name LIMIT 64`
		args = []any{in.ListingID, in.Root, after}
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return after, false, err
	}
	names := []string{}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
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
	positions, err := catalogReadGroupingBucket(ctx, tx, id, "folder_positions", "all")
	if err != nil {
		return after, false, err
	}
	for _, name := range names {
		canonical, copy := catalogEpisodeFormatCopy(name)
		if !copy {
			canonical = name
		}
		if evidence, ok := catalogEpisodeEvidence(path.Join(in.Root, canonical)); ok && evidence.Position > 0 {
			_, err = tx.ExecContext(ctx, `INSERT INTO source_catalog_grouping_folder(grouping_id,name,canonical,position) VALUES(?,?,?,?)`, id, name, canonical, evidence.Position)
			if err != nil {
				return after, false, err
			}
			if positions.Count == 0 {
				positions.Min = evidence.Position
			}
			positions.Count++
			positions.Min = min(positions.Min, evidence.Position)
			positions.Max = max(positions.Max, evidence.Position)
		}
		after = name
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO source_catalog_grouping_bucket(grouping_id,kind,key,payload) VALUES(?,'folder_positions','all',?) ON CONFLICT(grouping_id,kind,key) DO UPDATE SET payload=excluded.payload`, id, mustJSON(positions))
	return after, len(names) < catalogGroupingRows, err
}
