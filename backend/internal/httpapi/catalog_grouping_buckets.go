package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
)

type catalogBucketCursor struct {
	Key      string
	Position int
	Name     string
}

// The cursor addresses inputs, not aggregate keys. A single prefix or series
// may contain arbitrarily many names and must release the writer every 64 rows.
func stageCatalogGroupingBuckets(ctx context.Context, tx *sql.Tx, id int64, kind, after string) (string, bool, error) {
	table, key, position, value := "source_catalog_grouping_name", "", "", "''"
	switch kind {
	case "folder":
		table, key, position = "source_catalog_grouping_folder", "canonical", "position"
	case "part":
		key, position = "part_base", "part_rank"
	case "structured":
		key, position = "structured_key", "structured_position"
	case "episode":
		key, position = "episode_series", "episode_position"
	case "prefix":
		key, position, value = "work_prefix", "prefix_position", "folded_base"
	}
	var cursor catalogBucketCursor
	if after != "" {
		if err := json.Unmarshal([]byte(after), &cursor); err != nil {
			return after, false, err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+key+`,`+position+`,name,`+value+` FROM `+table+` WHERE grouping_id=? AND `+key+`<>'' AND (`+key+`,`+position+`,name)>(?,?,?) ORDER BY `+key+`,`+position+`,name LIMIT 64`, id, cursor.Key, cursor.Position, cursor.Name)
	if err != nil {
		return after, false, err
	}
	type item struct {
		catalogBucketCursor
		value string
	}
	batch := []item{}
	for rows.Next() {
		var i item
		if err = rows.Scan(&i.Key, &i.Position, &i.Name, &i.value); err != nil {
			break
		}
		batch = append(batch, i)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return after, false, err
	}
	for _, i := range batch {
		b, e := catalogReadGroupingBucket(ctx, tx, id, kind, i.Key)
		if e != nil {
			return after, false, e
		}
		if b.Count == 0 {
			b.Min, b.Max = i.Position, i.Position
			b.First = json.RawMessage(mustJSON(i.value))
			b.Last = b.First
		}
		if b.Count == 0 || b.LastPosition != i.Position {
			b.Distinct++
		}
		b.Count++
		b.Min = min(b.Min, i.Position)
		b.Max = max(b.Max, i.Position)
		b.LastPosition = i.Position
		switch kind {
		case "part":
			if i.Position == 1 {
				b.First = json.RawMessage("1")
			}
			if i.Position == 2 {
				b.Second = 1
			}
		case "prefix":
			var first, last string
			if e = json.Unmarshal(b.First, &first); e != nil {
				return after, false, e
			}
			if e = json.Unmarshal(b.Last, &last); e != nil {
				return after, false, e
			}
			b.First = json.RawMessage(mustJSON(min(first, i.value)))
			b.Last = json.RawMessage(mustJSON(max(last, i.value)))
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO source_catalog_grouping_bucket(grouping_id,kind,key,payload) VALUES(?,?,?,?) ON CONFLICT(grouping_id,kind,key) DO UPDATE SET payload=excluded.payload`, id, kind, i.Key, mustJSON(b)); e != nil {
			return after, false, e
		}
		cursor = i.catalogBucketCursor
	}
	observeCatalogGrouping(ctx, "aggregate", len(batch))
	return mustJSON(cursor), len(batch) < catalogGroupingRows, nil
}
