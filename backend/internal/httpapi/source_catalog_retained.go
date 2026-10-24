package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type catalogEntryQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// catalogEntryPendingSQL is the shared visibility gate for staged episodes.
// Retained missing records have no file_source_id but no pending row, so they
// stay readable. The parent publication row keeps a ready entry hidden until
// all activation batches finish and one row flips the complete split public.
func catalogEntryPendingSQL(alias string) string {
	return fmt.Sprintf(`NOT EXISTS(
		SELECT 1 FROM source_catalog_episode_pending pending
		WHERE pending.catalog_entry_id=%[1]s.id
			AND NOT EXISTS(
				SELECT 1 FROM source_catalog_episode_publication publication
				WHERE publication.source_id=pending.source_id
					AND publication.parent_root=pending.parent_root
					AND publication.state='published'
			)
	)`, alias)
}

func resolveCatalogEntry(ctx context.Context, q catalogEntryQueryer, id int64) (catalogEntry, error) {
	for range 8 {
		var e catalogEntry
		var target sql.NullInt64
		err := q.QueryRowContext(ctx, `SELECT id,COALESCE(file_source_id,0),primary_code,title,root_path,source_root,work_id,availability_status,relocated_to_id
			FROM source_catalog_entry WHERE id=? AND `+catalogEntryPendingSQL("source_catalog_entry"), id).
			Scan(&e.ID, &e.SourceID, &e.PrimaryCode, &e.Title, &e.RootPath, &e.SourceRoot, &e.WorkID, &e.Status, &target)
		if err != nil {
			return e, err
		}
		if !target.Valid {
			return e, nil
		}
		id = target.Int64
	}
	return catalogEntry{}, sql.ErrNoRows
}

// Saved metadata and private records remain readable when a source is removed
// or disabled. Outbound consumers use loadSourceCatalogEntry's stricter gate.
func (s *Server) loadRetainedSourceCatalogEntry(ctx context.Context, id int64) (catalogEntry, remoteSourceForUse, error) {
	if s.cfg.IsDemo() {
		return catalogEntry{}, remoteSourceForUse{}, errRemoteWorkNotFound
	}
	entry, err := resolveCatalogEntry(ctx, s.db, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return entry, remoteSourceForUse{}, errRemoteWorkNotFound
		}
		return entry, remoteSourceForUse{}, err
	}
	var source remoteSourceForUse
	if entry.SourceID == 0 {
		entry.Status = "missing"
	} else {
		source, err = s.loadRemoteSourceForUse(ctx, entry.SourceID)
		if err != nil {
			return entry, source, err
		}
		if !source.Enabled {
			entry.Status = "disabled"
		}
	}
	if entry.WorkID == nil && entry.PrimaryCode != "" {
		identity, resolveErr := s.resolveWorkCodeIdentity(ctx, entry.PrimaryCode)
		if resolveErr == nil {
			entry.WorkID = &identity.WorkID
		} else if !errors.Is(resolveErr, sql.ErrNoRows) {
			return entry, source, resolveErr
		}
	}
	return entry, source, nil
}

type catalogRelocationView struct {
	ID     int64  `json:"catalogEntryId"`
	Title  string `json:"title"`
	Reason string `json:"reason"`
}

func (s *Server) catalogRelocationCandidates(ctx context.Context, id int64) ([]catalogRelocationView, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT e.id,e.title,c.reason FROM source_catalog_relocation_candidate c JOIN source_catalog_entry e ON e.id=c.candidate_entry_id WHERE c.catalog_entry_id=? AND e.relocated_to_id IS NULL ORDER BY e.id LIMIT 8`, id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := []catalogRelocationView{}
	for rows.Next() {
		var item catalogRelocationView
		if err = rows.Scan(&item.ID, &item.Title, &item.Reason); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
