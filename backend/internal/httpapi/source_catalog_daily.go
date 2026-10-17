package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"
)

const catalogDailySettingPrefix = "source_catalog_daily:"

func (s *Server) catalogTime() time.Time {
	if s.catalogNow != nil {
		return s.catalogNow().UTC()
	}
	return time.Now().UTC()
}

func setCatalogNextUpdateTx(ctx context.Context, tx *sql.Tx, sourceID int64, now time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO app_setting(key,value_json) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value_json=excluded.value_json`, catalogDailySettingPrefix+strconv.FormatInt(sourceID, 10), now.Add(24*time.Hour).Unix())
	return err
}

// The completed scan owns the first due date. Every later dispatch advances it
// in the same transaction as joining/resuming/creating the durable scan. A stop
// between dispatch and execution cannot lose the update or duplicate its run.
func (s *Server) dispatchDueCatalogUpdates(ctx context.Context) error {
	now := s.catalogTime()
	rows, err := s.db.QueryContext(ctx, `SELECT scan.file_source_id FROM source_catalog_scan scan
		JOIN file_source source ON source.id=scan.file_source_id
		JOIN workflow_run run ON run.id=scan.workflow_run_id
		LEFT JOIN app_setting setting ON setting.key=? || scan.file_source_id
		WHERE source.enabled=1 AND source.source_type=? AND
		COALESCE(CAST(setting.value_json AS INTEGER),CASE WHEN run.status='succeeded' THEN unixepoch(run.finished_at)+86400 END)<=?
		ORDER BY scan.file_source_id LIMIT 16`, catalogDailySettingPrefix, sourceTypeOpenList, now.Unix())
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	readErr := rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	if readErr != nil {
		return readErr
	}
	var result error
	for _, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		result = errors.Join(result, s.dispatchCatalogUpdate(ctx, id, now))
	}
	return result
}

func (s *Server) dispatchCatalogUpdate(ctx context.Context, sourceID int64, now time.Time) error {
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return err
	}
	defer release()
	var saved string
	var due sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT scan.options_json,
		COALESCE(CAST(setting.value_json AS INTEGER),CASE WHEN run.status='succeeded' THEN unixepoch(run.finished_at)+86400 END)
		FROM source_catalog_scan scan JOIN workflow_run run ON run.id=scan.workflow_run_id
		JOIN file_source source ON source.id=scan.file_source_id
		LEFT JOIN app_setting setting ON setting.key=? || scan.file_source_id
		WHERE scan.file_source_id=? AND source.enabled=1 AND source.source_type=?`, catalogDailySettingPrefix, sourceID, sourceTypeOpenList).Scan(&saved, &due)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !due.Valid || due.Int64 > now.Unix() {
		return nil
	}
	options := defaultCatalogIndexOptions()
	if err = json.Unmarshal([]byte(saved), &options); err != nil {
		return err
	}
	if err = options.validate(); err != nil {
		return err
	}
	if _, err = s.enqueueSourceCatalogIndexTx(ctx, tx.Tx, sourceID, options, "interval", false); err != nil {
		return err
	}
	if err = setCatalogNextUpdateTx(ctx, tx.Tx, sourceID, now); err != nil {
		return err
	}
	return tx.Commit()
}
