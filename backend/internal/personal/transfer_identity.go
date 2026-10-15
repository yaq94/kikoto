package personal

import (
	"context"
	"database/sql"
	"errors"
)

func exportIdentity(ctx context.Context, tx *sql.Tx, work int64) (*BackupIdentity, error) {
	i := &BackupIdentity{}
	err := tx.QueryRowContext(ctx, `SELECT r.ref,w.title,CASE
	 WHEN EXISTS(SELECT 1 FROM catalog_series WHERE work_id=w.id) THEN 'series'
	 WHEN EXISTS(SELECT 1 FROM source_catalog_entry c JOIN catalog_series_member m ON m.catalog_entry_id=c.id WHERE c.work_id=w.id) THEN 'episode'
	 ELSE r.object_kind END FROM work w JOIN personal_transfer_ref r ON r.work_id=w.id AND r.is_primary=1 WHERE w.id=?`, work).Scan(&i.Ref, &i.Title, &i.Kind)
	if err != nil {
		return nil, err
	}
	err = scanRows(ctx, tx, `SELECT r.ref FROM personal_transfer_ref r
	 LEFT JOIN source_catalog_entry c ON c.id=r.catalog_entry_id
	 LEFT JOIN catalog_series s ON s.id=r.series_id
	 WHERE (r.work_id=? OR c.work_id=? OR s.work_id=?) AND r.ref<>? ORDER BY r.ref LIMIT 101`, []any{work, work, work, i.Ref}, func(rows *sql.Rows) error {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			return err
		}
		i.References = append(i.References, ref)
		return nil
	})
	if len(i.References) > 100 {
		return nil, ErrLimit
	}
	return i, err
}

type identityMatch struct {
	work    int64
	catalog int64
	series  int64
	problem string
}

func sharedIdentity(ctx context.Context, tx *sql.Tx, work int64) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT identity_work_id FROM shared_work_identity WHERE work_id=?`, work).Scan(&id)
	return id, err
}

// Exact portable references win over a code's canonical edition projection.
// Every known piece of evidence must agree; unknown foreign references never
// cause a title/path match or rewrite an existing source association.
func resolveTransferIdentity(ctx context.Context, tx *sql.Tx, i *BackupIdentity, code string) (identityMatch, error) {
	m := identityMatch{}
	refs := append([]string{i.Ref}, i.References...)
	for _, ref := range refs {
		var work, catalog, series sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT COALESCE(r.work_id,c.work_id,s.work_id),r.catalog_entry_id,r.series_id
		 FROM personal_transfer_ref r LEFT JOIN source_catalog_entry c ON c.id=r.catalog_entry_id LEFT JOIN catalog_series s ON s.id=r.series_id WHERE r.ref=?`, ref).Scan(&work, &catalog, &series)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return m, err
		}
		if !work.Valid {
			// A known but unregistered source is not permission to replace its identity.
			if m.catalog != 0 && m.catalog != catalog.Int64 || m.series != 0 && m.series != series.Int64 {
				m.problem = "identity_conflict"
				return m, nil
			}
			m.catalog, m.series = catalog.Int64, series.Int64
			continue
		}
		if m.work != 0 && m.work != work.Int64 {
			m.problem = "identity_conflict"
			return m, nil
		} else {
			m.work = work.Int64
		}
	}
	if code != "" {
		var exact int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM work WHERE primary_code=?`, code).Scan(&exact)
		if errors.Is(err, sql.ErrNoRows) {
			exact, err = resolveCode(ctx, tx, code)
		}
		if err != nil {
			return m, err
		}
		if m.work == 0 {
			m.work = exact
		} else if exact != 0 && exact != m.work {
			// A different explicit edition is contradictory even if its review happens
			// to be shared. It cannot be substituted for playback or list membership.
			m.problem = "identity_conflict"
			return m, nil
		}
		if m.work != 0 {
			var actual string
			if err := tx.QueryRowContext(ctx, `SELECT primary_code FROM work WHERE id=?`, m.work).Scan(&actual); err != nil {
				return m, err
			}
			if actual != "" && actual != code && exact != m.work {
				m.problem = "identity_conflict"
				return m, nil
			}
		}
	}
	if m.work != 0 {
		current, err := exportIdentity(ctx, tx, m.work)
		if err != nil {
			return m, err
		}
		if current.Kind != i.Kind {
			m.problem = "identity_conflict"
		}
	}
	if m.catalog != 0 || m.series != 0 {
		m.problem = "unregistered_source"
	}
	return m, nil
}

func registerTransferWork(ctx context.Context, tx *sql.Tx, w BackupWork) (int64, error) {
	result, err := tx.ExecContext(ctx, `INSERT INTO work(primary_code,title) VALUES(?,?)`, w.PrimaryCode, w.Identity.Title)
	if err != nil {
		return 0, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	result, err = tx.ExecContext(ctx, `INSERT INTO logical_work(canonical_work_id,canonical_code) VALUES(?,?)`, id, w.PrimaryCode)
	if err != nil {
		return 0, err
	}
	logical, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO work_edition(work_id,logical_work_id,primary_code,is_canonical) VALUES(?,?,?,1)`, id, logical, w.PrimaryCode); err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE personal_transfer_ref SET object_kind=? WHERE work_id=? AND is_primary=1`, w.Identity.Kind, id); err != nil {
		return 0, err
	}
	if w.Identity.Kind == "series" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO catalog_series(title,work_id) VALUES(?,?)`, w.Identity.Title, id); err != nil {
			return 0, err
		}
	}
	return id, nil
}

func saveTransferReferences(ctx context.Context, tx *sql.Tx, work int64, i *BackupIdentity) error {
	for _, ref := range append([]string{i.Ref}, i.References...) {
		// Only missing aliases are remembered; known catalog/series locations keep
		// their exact ownership and are never reassigned by a personal import.
		if _, err := tx.ExecContext(ctx, `INSERT INTO personal_transfer_ref(ref,work_id,object_kind) VALUES(?,?,?) ON CONFLICT(ref) DO NOTHING`, ref, work, i.Kind); err != nil {
			return err
		}
	}
	return nil
}
