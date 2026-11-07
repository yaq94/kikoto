package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path"
	"strings"

	"github.com/yexca/kikoto/backend/internal/openlist"
)

type catalogFiniteOptionsKey struct{}

func (s *Server) catalogFiniteContext(ctx context.Context, sourceID int64) (context.Context, error) {
	options := defaultCatalogIndexOptions()
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT options_json FROM source_catalog_scan WHERE file_source_id=?`, sourceID).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ctx, err
	}
	if err == nil {
		if err = json.Unmarshal([]byte(raw), &options); err != nil {
			return ctx, err
		}
	}
	if err = options.validate(); err != nil {
		return ctx, err
	}
	return context.WithValue(s.catalogPublicationContext(ctx, options), catalogFiniteOptionsKey{}, options), nil
}

const (
	catalogFinitePages       = 100
	catalogFiniteDirectories = 32
	catalogFiniteDepth       = 8
	catalogFinitePageSize    = 100
)

// Finite scans retain their request/depth budgets, but the budget is not a cap
// on the number of independent works in a creator directory. The queue and
// duplicate-name set live in SQLite; the only in-memory collection is one page.
func (s *Server) stageCatalogFiniteListing(ctx context.Context, source remoteSourceForUse, root string) (int64, error) {
	if !openlist.ValidRoot(root) {
		return 0, openlist.ErrInvalid
	}
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return 0, err
	}
	var id int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM source_catalog_listing WHERE file_source_id=? AND directory_path=? AND generation=0 AND observed_version='finite' AND state<>'invalid' ORDER BY id DESC LIMIT 1`, source.ID, root).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, `INSERT INTO source_catalog_listing(file_source_id,directory_path,generation,base_revision,observed_version,options_json) VALUES(?,?,0,0,'finite','{}') RETURNING id`, source.ID, root).Scan(&id)
		if err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO source_catalog_finite_directory(listing_id,path,depth) VALUES(?,?,0)`, id, root)
		}
	}
	if err == nil {
		err = tx.Commit()
	}
	release()
	if err != nil {
		return 0, err
	}
	for {
		if err = ctx.Err(); err != nil {
			return id, err
		}
		var directory string
		var depth, page, expected, received int
		err = s.db.QueryRowContext(ctx, `SELECT path,depth,next_page,expected_total,received FROM source_catalog_finite_directory WHERE listing_id=? AND complete=0 ORDER BY path LIMIT 1`, id).Scan(&directory, &depth, &page, &expected, &received)
		if errors.Is(err, sql.ErrNoRows) {
			_, err = s.execCatalogWrite(ctx, `UPDATE source_catalog_listing SET state='complete' WHERE id=? AND state='fetching'`, id)
			return id, err
		}
		if err != nil {
			return id, err
		}
		var requests, folders int
		if err = s.db.QueryRowContext(ctx, `SELECT next_page-1,(SELECT count(*) FROM source_catalog_finite_directory WHERE listing_id=?) FROM source_catalog_listing WHERE id=?`, id, id).Scan(&requests, &folders); err != nil {
			return id, err
		}
		if depth > catalogFiniteDepth || folders > catalogFiniteDirectories || requests >= catalogFinitePages {
			return id, openlist.ErrLimit
		}
		// A guard attached by the caller is checked before every request and again
		// by the page transaction. No connection is held during remote I/O.
		if guard, _ := ctx.Value(catalogPublicationResourceGuardKey{}).(func() error); guard != nil {
			if err = guard(); err != nil {
				return id, err
			}
		}
		client := s.openListClient(source)
		if options, ok := ctx.Value(catalogFiniteOptionsKey{}).(catalogIndexOptions); ok {
			client.WithResponseLimit(options.MaxResponseBytes)
		}
		data, readErr := client.ListPage(ctx, directory, page, catalogFinitePageSize)
		if readErr != nil {
			return id, readErr
		}
		if (expected >= 0 && expected != data.Total) || received+len(data.Content) > data.Total || (len(data.Content) == 0 && received < data.Total) {
			return id, s.invalidateCatalogFiniteListing(ctx, id, openlist.ErrInvalid)
		}
		tx, release, err = beginTxWithDatabaseBusyRetry(ctx, s.db)
		if err != nil {
			return id, err
		}
		for _, obj := range data.Content {
			if obj.NameDamaged {
				err = openlist.ErrInvalid
				break
			}
			full := path.Join(directory, obj.Name)
			relative := strings.TrimPrefix(full, strings.TrimRight(root, "/")+"/")
			result, writeErr := tx.ExecContext(ctx, `INSERT INTO source_catalog_listing_item(listing_id,directory_path,name,title,kind,size_bytes,content_hash,modified_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`, id, directory, relative, obj.Name, openlist.Kind(obj.Name, obj.IsDir), obj.Size, "", obj.Modified)
			if writeErr != nil {
				err = writeErr
				break
			}
			var n int64
			n, err = result.RowsAffected()
			if err != nil {
				break
			}
			if n != 1 {
				err = openlist.ErrInvalid
				break
			}
			if obj.IsDir {
				_, err = tx.ExecContext(ctx, `INSERT INTO source_catalog_finite_directory(listing_id,path,depth) VALUES(?,?,?)`, id, full, depth+1)
				if err != nil {
					break
				}
			}
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE source_catalog_finite_directory SET next_page=?,expected_total=?,received=?,complete=? WHERE listing_id=? AND path=?`, page+1, data.Total, received+len(data.Content), received+len(data.Content) == data.Total, id, directory)
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE source_catalog_listing SET next_page=next_page+1,received=received+? WHERE id=?`, len(data.Content), id)
		}
		if err == nil {
			err = tx.Commit()
		}
		release()
		if errors.Is(err, openlist.ErrInvalid) {
			return id, s.invalidateCatalogFiniteListing(ctx, id, err)
		}
		if err != nil {
			return id, err
		}
	}
}

func (s *Server) invalidateCatalogFiniteListing(ctx context.Context, id int64, cause error) error {
	_, err := s.execCatalogWrite(ctx, `UPDATE source_catalog_listing SET state='invalid' WHERE id=?`, id)
	return errors.Join(cause, err)
}
