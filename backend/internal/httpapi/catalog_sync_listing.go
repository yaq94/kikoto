package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"path"
	"time"

	"github.com/yexca/kikoto/backend/internal/openlist"
)

const catalogSyncListingWriteRows = 64

type catalogSyncListing struct {
	ID                               int64
	Page, Expected, Received         int
	State, ApplyCursor, RemoveCursor string
}

type catalogSyncPaused struct{ reason string }

func (e catalogSyncPaused) Error() string { return e.reason }

func (s *Server) checkCatalogSyncBatch(ctx context.Context, options catalogIndexOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if reason, _ := s.pauseCatalogSync(ctx, options); reason != "" {
		return catalogSyncPaused{reason}
	}
	return nil
}

func (s *Server) openCatalogSyncListing(ctx context.Context, sourceID, generation int64, dir catalogSyncDirectoryState, options catalogIndexOptions) (catalogSyncListing, error) {
	var listing catalogSyncListing
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return listing, err
	}
	defer release()
	err = tx.QueryRowContext(ctx, `SELECT id,next_page,expected_total,received,state,apply_cursor,remove_cursor
		FROM source_catalog_listing WHERE file_source_id=? AND directory_path=? AND generation=? AND base_revision=?
		AND observed_version=? AND options_json=? AND state IN ('fetching','complete','applying') ORDER BY id DESC LIMIT 1`,
		sourceID, dir.Path, generation, dir.Revision, dir.ObservedVersion, mustJSON(options)).Scan(&listing.ID, &listing.Page, &listing.Expected, &listing.Received, &listing.State, &listing.ApplyCursor, &listing.RemoveCursor)
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, `INSERT INTO source_catalog_listing(file_source_id,directory_path,generation,base_revision,observed_version,options_json)
			VALUES(?,?,?,?,?,?) RETURNING id,next_page,expected_total,received,state,apply_cursor,remove_cursor`, sourceID, dir.Path, generation, dir.Revision, dir.ObservedVersion, mustJSON(options)).Scan(&listing.ID, &listing.Page, &listing.Expected, &listing.Received, &listing.State, &listing.ApplyCursor, &listing.RemoveCursor)
	}
	if err != nil {
		return listing, err
	}
	return listing, tx.Commit()
}

// One provider page is the maximum resident response. Its rows and next page
// commit together; the provider's configured page limit is at most 500 rows.
func (s *Server) fetchCatalogSyncListingPages(ctx context.Context, client *openlist.Client, sourceID, generation, jobID int64, dir catalogSyncDirectoryState, options catalogIndexOptions) (catalogSyncListing, int, error) {
	listing, err := s.openCatalogSyncListing(ctx, sourceID, generation, dir, options)
	requests := 0
	if err != nil {
		return listing, requests, err
	}
	for listing.State == "fetching" {
		if err = s.checkCatalogSyncBatch(ctx, options); err != nil {
			return listing, requests, err
		}
		if listing.Page > options.MaxPagesPerDirectory {
			return listing, requests, openlist.ErrLimit
		}
		page, requestErr := client.ListPage(ctx, dir.Path, listing.Page, options.PageSize)
		requests++
		if requestErr != nil {
			return listing, requests, requestErr
		}
		if err = s.checkCatalogSyncBatch(ctx, options); err != nil {
			return listing, requests, err
		}
		if err = s.commitCatalogSyncListingPage(ctx, jobID, generation, dir, &listing, page); err != nil {
			if errors.Is(err, openlist.ErrInvalid) {
				_, markErr := s.db.ExecContext(ctx, `UPDATE source_catalog_listing SET state='invalid' WHERE id=?`, listing.ID)
				err = errors.Join(err, markErr)
			}
			return listing, requests, err
		}
	}
	return listing, requests, nil
}

func (s *Server) commitCatalogSyncListingPage(ctx context.Context, jobID, generation int64, dir catalogSyncDirectoryState, listing *catalogSyncListing, page openlist.Page) error {
	if len(page.Content) > 500 || page.Total < 0 || (listing.Expected >= 0 && page.Total != listing.Expected) || listing.Received+len(page.Content) > page.Total || (len(page.Content) == 0 && listing.Received < page.Total) {
		return openlist.ErrInvalid
	}
	started := time.Now()
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return err
	}
	defer release()
	var currentPage int
	if err = tx.QueryRowContext(ctx, `SELECT next_page FROM source_catalog_listing WHERE id=? AND state='fetching'`, listing.ID).Scan(&currentPage); err != nil {
		return err
	}
	if currentPage != listing.Page {
		return context.Canceled
	}
	for _, obj := range page.Content {
		if len(path.Join(dir.Path, obj.Name)) > 4096 {
			return openlist.ErrLimit
		}
		var present bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM source_catalog_listing_item WHERE listing_id=? AND name=?)`, listing.ID, obj.Name).Scan(&present); err != nil {
			return err
		}
		if present {
			return openlist.ErrInvalid
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO source_catalog_listing_item(listing_id,directory_path,name,kind,size_bytes,content_hash,modified_at) VALUES(?,?,?,?,?,?,?)`, listing.ID, dir.Path, obj.Name, openlist.Kind(obj.Name, obj.IsDir), obj.Size, obj.ContentHash(), obj.Modified); err != nil {
			return err
		}
	}
	next := *listing
	next.Page++
	next.Expected = page.Total
	next.Received += len(page.Content)
	if next.Received == next.Expected {
		next.State = "complete"
	}
	if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_listing SET next_page=?,expected_total=?,received=?,state=? WHERE id=?`, next.Page, next.Expected, next.Received, next.State, listing.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE workflow_job SET checkpoint_json=json_set(checkpoint_json,'$.listingGeneration',?,'$.listingId',?,'$.listingPage',?) WHERE id=?`, generation, listing.ID, next.Page, jobID); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	*listing = next
	observeCatalogWrite(ctx, "sync_listing_page", started, len(page.Content))
	return nil
}
