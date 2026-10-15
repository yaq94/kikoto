package httpapi

import (
	"context"
	"database/sql"
	"errors"
)

type catalogEntryQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func resolveCatalogEntry(ctx context.Context, q catalogEntryQueryer, id int64) (catalogEntry, error) {
	for range 8 {
		var e catalogEntry
		var target sql.NullInt64
		err := q.QueryRowContext(ctx, `SELECT id,COALESCE(file_source_id,0),primary_code,title,root_path,work_id,availability_status,relocated_to_id FROM source_catalog_entry WHERE id=?`, id).Scan(&e.ID, &e.SourceID, &e.PrimaryCode, &e.Title, &e.RootPath, &e.WorkID, &e.Status, &target)
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
