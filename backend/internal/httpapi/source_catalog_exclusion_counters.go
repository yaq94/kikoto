package httpapi

import (
	"context"
	"database/sql"
)

type catalogExclusionCounts struct {
	Directories    int64 `json:"directories"`
	Completed      int64 `json:"completed"`
	Pending        int64 `json:"pending"`
	Failed         int64 `json:"failed"`
	Restricted     int64 `json:"restricted"`
	Empty          int64 `json:"empty"`
	Observed       int64 `json:"observed"`
	Indexed        int64 `json:"indexed"`
	PublishPending int64 `json:"publishPending"`
}

// Counts describe preparation, like source_catalog_scan. Each page and its
// accumulator commit with the job cursor. The public projection changes once,
// after the complete successful count, so retry never adds a page twice.
func catalogExclusionCounterBatch(ctx context.Context, tx *sql.Tx, input catalogExclusionInput, cursor *catalogExclusionCursor) error {
	if input.Generation == 0 {
		cursor.Phase = "publish"
		return nil
	}
	switch cursor.Phase {
	case "counters":
		cursor.Entry = 0
		cursor.Directory, cursor.Name = "", ""
		cursor.Counts = catalogExclusionCounts{}
		cursor.Phase = "count_directories"
		return nil
	case "count_directories":
		return catalogExclusionCountDirectories(ctx, tx, input, cursor)
	case "count_observations":
		return catalogExclusionCountObservations(ctx, tx, input, cursor)
	case "count_publish":
		rows, err := tx.QueryContext(ctx, `SELECT id,last_seen_generation FROM source_catalog_entry NOT INDEXED WHERE file_source_id=? AND id>? ORDER BY id LIMIT 64`, input.SourceID, cursor.Entry)
		if err != nil {
			return err
		}
		count := 0
		for rows.Next() {
			var id, generation int64
			if err = rows.Scan(&id, &generation); err != nil {
				break
			}
			if generation == input.Generation {
				cursor.Counts.Indexed++
			}
			cursor.Entry = id
			cursor.Visited++
			count++
		}
		if err == nil {
			err = rows.Err()
		}
		_ = rows.Close()
		if err != nil {
			return err
		}
		if count == 64 {
			return nil
		}
		c := cursor.Counts
		_, err = tx.ExecContext(ctx, `UPDATE source_catalog_scan SET directories=?,completed=?,pending=?,failed=?,restricted=?,empty=?,observed=?,indexed=?,publish_pending_remaining=?,updated_at=CURRENT_TIMESTAMP WHERE file_source_id=? AND workflow_run_id=?`, c.Directories, c.Completed, c.Pending, c.Failed, c.Restricted, c.Empty, c.Observed, c.Indexed, c.PublishPending, input.SourceID, input.Generation)
		cursor.Phase = "publish"
		return err
	}
	return nil
}

func catalogExclusionCountDirectories(ctx context.Context, tx *sql.Tx, input catalogExclusionInput, cursor *catalogExclusionCursor) error {
	rows, err := tx.QueryContext(ctx, `SELECT path,generation,status,error_code FROM source_catalog_directory WHERE file_source_id=? AND path>? ORDER BY path LIMIT 64`, input.SourceID, cursor.Directory)
	if err != nil {
		return err
	}
	count := 0
	for rows.Next() {
		var p, status, code string
		var generation int64
		if err = rows.Scan(&p, &generation, &status, &code); err != nil {
			break
		}
		cursor.Directory = p
		cursor.Visited++
		count++
		if generation != input.Generation {
			continue
		}
		cursor.Counts.Directories++
		switch status {
		case "complete":
			cursor.Counts.Completed++
		case "pending", "listing_complete":
			cursor.Counts.Pending++
		case "failed":
			cursor.Counts.Failed++
		case "restricted":
			cursor.Counts.Restricted++
		case "empty":
			if code == "not_found" {
				cursor.Counts.Completed++
			} else {
				cursor.Counts.Empty++
			}
		}
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return err
	}
	if count < 64 {
		cursor.Phase, cursor.Directory, cursor.Name = "count_observations", "", ""
	}
	return nil
}

func catalogExclusionCountObservations(ctx context.Context, tx *sql.Tx, input catalogExclusionInput, cursor *catalogExclusionCursor) error {
	// Select the next 64 raw keys before applying generation/revision rules;
	// even a large old-generation prefix therefore releases the write lock.
	rows, err := tx.QueryContext(ctx, `SELECT o.directory_path,o.name,d.generation,o.revision=d.revision,
	 d.status='complete' AND d.catalog_root<>'' AND o.publish_pending=1
	 AND EXISTS(SELECT 1 FROM source_catalog_audio_root r WHERE r.file_source_id=d.file_source_id AND r.generation=d.generation AND r.catalog_root=d.catalog_root)
	 FROM source_catalog_observation o CROSS JOIN source_catalog_directory d
	 ON d.file_source_id=o.file_source_id AND d.path=o.directory_path
	 WHERE o.file_source_id=? AND (o.directory_path,o.name)>(?,?) ORDER BY o.directory_path,o.name LIMIT 64`, input.SourceID, cursor.Directory, cursor.Name)
	if err != nil {
		return err
	}
	count := 0
	for rows.Next() {
		var p, name string
		var generation int64
		var current, pending bool
		if err = rows.Scan(&p, &name, &generation, &current, &pending); err != nil {
			break
		}
		cursor.Directory, cursor.Name = p, name
		cursor.Visited++
		count++
		if generation == input.Generation && current {
			cursor.Counts.Observed++
			if pending {
				cursor.Counts.PublishPending++
			}
		}
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return err
	}
	if count < 64 {
		cursor.Phase = "count_publish"
		cursor.Entry = 0
	}
	return nil
}
