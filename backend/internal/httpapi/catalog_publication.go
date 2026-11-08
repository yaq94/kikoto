package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/yexca/kikoto/backend/internal/sqlutil"
)

type catalogPublicationKey struct{}
type catalogPublicationResourceGuardKey struct{}
type catalogWriteTimingKey struct{}

func (s *Server) catalogPublicationContext(ctx context.Context, options catalogIndexOptions) context.Context {
	return context.WithValue(ctx, catalogPublicationResourceGuardKey{}, func() error {
		return s.checkCatalogSyncBatch(ctx, options)
	})
}

type catalogWriteTx struct {
	*sqlutil.BoundedTx
	publication bool
	ctx         context.Context
	started     time.Time
	observe     func(time.Duration)
	observed    sync.Once
}

func (tx *catalogWriteTx) recordDuration() {
	if tx.observe != nil {
		tx.observed.Do(func() { tx.observe(time.Since(tx.started)) })
	}
}

func (tx *catalogWriteTx) Rollback() error {
	err := tx.BoundedTx.Rollback()
	tx.recordDuration()
	return err
}

func (tx *catalogWriteTx) Commit() error {
	if tx.publication {
		if _, err := tx.ExecContext(tx.ctx, `UPDATE source_catalog_publication_writer SET operation_id=0 WHERE id=1`); err != nil {
			return err
		}
	}
	err := tx.BoundedTx.Commit()
	tx.recordDuration()
	return err
}

// A standalone source-owned statement must use the same writer context as a
// batch. Context values alone do not select SQLite's transactional projection.
func (s *Server) execCatalogWrite(ctx context.Context, query string, args ...any) (sql.Result, error) {
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return nil, err
	}
	defer release()
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return result, tx.Commit()
}

// Registration is live user state. Its source projection joins an existing
// publication only while this transaction writes that one presence key. The
// before-image describes the card the user actually read, and the working row
// uses the pending catalog's availability, so neither version loses the review.
func upsertCatalogReviewPresence(ctx context.Context, tx *sql.Tx, entry catalogEntry, presence workSourcePresence) (err error) {
	var operation int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM source_catalog_publication WHERE file_source_id=? AND state='preparing'`, entry.SourceID).Scan(&operation)
	if errors.Is(err, sql.ErrNoRows) {
		return upsertWorkSourcePresence(ctx, tx, presence)
	}
	if err != nil {
		return err
	}
	var previous int64
	if err = tx.QueryRowContext(ctx, `SELECT operation_id FROM source_catalog_publication_writer WHERE id=1`).Scan(&previous); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_publication_writer SET operation_id=? WHERE id=1`, operation); err != nil {
		return err
	}
	defer func() {
		_, restoreErr := tx.ExecContext(ctx, `UPDATE source_catalog_publication_writer SET operation_id=? WHERE id=1`, previous)
		err = errors.Join(err, restoreErr)
	}()
	publishedAvailability := presence.Availability
	if err = tx.QueryRowContext(ctx, `SELECT CASE WHEN EXISTS(SELECT 1 FROM source_catalog_entry WHERE work_id=? AND file_source_id=? AND relocated_to_id IS NULL AND availability_status='available') THEN 'available' ELSE availability_status END FROM source_catalog_entry WHERE id=?`, presence.WorkID, entry.SourceID, entry.ID).Scan(&presence.Availability); err != nil {
		return err
	}
	if err = upsertWorkSourcePresence(ctx, tx, presence); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE work_source_presence_publication_before SET existed=1,availability=? WHERE operation_id=? AND work_id=? AND file_source_id=? AND presence_type=? AND existed=0`, publishedAvailability, operation, presence.WorkID, entry.SourceID, presence.PresenceType)
	return err
}

func catalogProjectionWriterTx(ctx context.Context, tx *sql.Tx, sourceID int64) (int64, func() error, error) {
	var operation, previous int64
	err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT id FROM source_catalog_publication WHERE file_source_id=? AND state='preparing'),0),operation_id FROM source_catalog_publication_writer WHERE id=1`, sourceID).Scan(&operation, &previous)
	if err != nil || operation == 0 {
		return operation, func() error { return nil }, err
	}
	if previous != 0 && previous != operation {
		return 0, nil, errors.New("catalog projection belongs to another publication")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_publication_writer SET operation_id=? WHERE id=1`, operation); err != nil {
		return 0, nil, err
	}
	return operation, func() error {
		_, err := tx.ExecContext(ctx, `UPDATE source_catalog_publication_writer SET operation_id=? WHERE id=1`, previous)
		return err
	}, nil
}

func (s *Server) beginCatalogPublication(ctx context.Context, sourceID int64, owner string) (context.Context, int64, error) {
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return ctx, 0, err
	}
	defer release()
	var id int64
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT id,owner FROM source_catalog_publication WHERE file_source_id=? AND state='preparing'`, sourceID).Scan(&id, &existing)
	if err == nil && existing != owner {
		return ctx, 0, errors.New("catalog publication is owned by another operation")
	}
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, `INSERT INTO source_catalog_publication(file_source_id,owner) VALUES(?,?) RETURNING id`, sourceID, owner).Scan(&id)
	}
	if err != nil {
		return ctx, 0, err
	}
	if err = tx.Commit(); err != nil {
		return ctx, 0, err
	}
	return context.WithValue(ctx, catalogPublicationKey{}, id), id, nil
}

func (s *Server) commitCatalogPublication(ctx context.Context, id int64) error {
	for {
		if err := s.prepareCatalogSplitRetirements(ctx, id); err != nil {
			return err
		}
		if err := s.prepareCatalogPublicationRetirements(ctx, id); err != nil {
			return err
		}
		if err := s.prepareCatalogPublicationAdmissions(ctx, id); err != nil {
			return err
		}
		if err := s.prepareCatalogPublicationMedia(ctx, id); err != nil {
			return err
		}
		committed, err := s.commitCatalogPublicationTranslations(ctx, id)
		if err != nil {
			return err
		}
		if committed {
			return s.cleanupCatalogPublications(ctx)
		}
	}
}

func (s *Server) prepareCatalogPublicationAdmissions(ctx context.Context, id int64) error {
	// Media preparation follows admission. Completed media keys need no admission
	// pass on a translation-only retry; source mutations reset media_prepared.
	var after int64
	for {
		tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT entity_id FROM source_catalog_publication_entity dirty WHERE operation_id=? AND entity_kind='entry' AND media_prepared=0 AND entity_id>?
			AND EXISTS(SELECT 1 FROM source_catalog_entry e WHERE e.id=dirty.entity_id) ORDER BY entity_id LIMIT 64`, id, after)
		if err != nil {
			release()
			return err
		}
		ids := []int64{}
		for rows.Next() {
			var entry int64
			if err = rows.Scan(&entry); err != nil {
				break
			}
			ids = append(ids, entry)
		}
		if err == nil {
			err = rows.Err()
		}
		_ = rows.Close()
		if err != nil {
			release()
			return err
		}
		for _, entry := range ids {
			if err = refreshCatalogEntryAdmissionTx(ctx, tx.Tx, entry); err != nil {
				break
			}
		}
		if err == nil {
			err = tx.Commit()
		}
		release()
		if err != nil {
			return err
		}
		if len(ids) < 64 {
			break
		}
		after = ids[len(ids)-1]
	}
	return nil
}

// Keep entity ownership until cleanup so late translation writes can invalidate
// even an already prepared entity. The empty-queue check and marker share the
// write transaction: no external scope refresh can slip between them.
func (s *Server) commitCatalogPublicationTranslations(ctx context.Context, id int64) (bool, error) {
	for {
		tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
		if err != nil {
			return false, err
		}
		var kind string
		var entityID, cursor int64
		err = tx.QueryRowContext(ctx, `SELECT entity_kind,entity_id,after_translation_id FROM source_catalog_publication_entity WHERE operation_id=? AND translation_prepared=0 ORDER BY entity_kind,entity_id LIMIT 1`, id).Scan(&kind, &entityID, &cursor)
		if errors.Is(err, sql.ErrNoRows) {
			// Source writes can arrive while translation batches are preparing.
			// Retry admission/media preparation before publishing those keys.
			var pending bool
			err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM source_catalog_publication_entity WHERE operation_id=? AND entity_kind IN ('retired_aliases','retired_files'))
				OR EXISTS(SELECT 1 FROM source_catalog_publication_entity WHERE operation_id=? AND entity_kind='entry' AND media_prepared=0)
				OR EXISTS(SELECT 1 FROM source_catalog_split_retirement WHERE operation_id=?)`, id, id, id).Scan(&pending)
			if err == nil && !pending {
				_, err = tx.ExecContext(ctx, `UPDATE source_catalog_publication SET state='committed' WHERE id=? AND state='preparing'`, id)
				if err == nil {
					if committed, ok := ctx.Value(catalogPublicationCommitKey{}).(func(*sql.Tx) error); ok && committed != nil {
						err = committed(tx.Tx)
					}
				}
				if err == nil {
					err = tx.Commit()
				}
			}
			release()
			return !pending && err == nil, err
		}
		if err != nil {
			release()
			return false, err
		}
		rows, err := tx.QueryContext(ctx, `SELECT id FROM content_translation WHERE entity_kind=? AND entity_id=? AND id>? ORDER BY id LIMIT 64`, kind, entityID, cursor)
		if err != nil {
			release()
			return false, err
		}
		ids := []int64{}
		for rows.Next() {
			var row int64
			if err = rows.Scan(&row); err != nil {
				break
			}
			ids = append(ids, row)
		}
		if err == nil {
			err = rows.Err()
		}
		_ = rows.Close()
		if err != nil {
			release()
			return false, err
		}
		for _, row := range ids {
			if _, err = tx.ExecContext(ctx, `UPDATE content_translation SET (source_id,browsable)=(SELECT source_id,browsable FROM content_translation_scope_live WHERE translation_id=content_translation.id) WHERE id=?`, row); err != nil {
				break
			}
		}
		if err == nil {
			if len(ids) < 64 {
				_, err = tx.ExecContext(ctx, `UPDATE source_catalog_publication_entity SET translation_prepared=1 WHERE operation_id=? AND entity_kind=? AND entity_id=?`, id, kind, entityID)
			} else {
				_, err = tx.ExecContext(ctx, `UPDATE source_catalog_publication_entity SET after_translation_id=? WHERE operation_id=? AND entity_kind=? AND entity_id=?`, ids[len(ids)-1], id, kind, entityID)
			}
		}
		if err == nil {
			err = tx.Commit()
		}
		release()
		if err != nil {
			return false, err
		}
	}
}

// Public queries read the same source-owned projection; identity and user state
// remain live. The indexed before-image key has at most one row, independent of
// the number of earlier operations. A committed marker immediately selects live
// rows; later bounded cleanup only reclaims redundant before-images.
var catalogPublicationTables = regexp.MustCompile(`\b(source_catalog_entry_admission|source_catalog_episode_publication|source_catalog_episode_pending|source_catalog_search_doc|source_catalog_entry|source_catalog_file|catalog_series_member|catalog_series|content_translation|work_source_presence|media_file_location)\b`)
var catalogPublicationIndexHint = regexp.MustCompile(`\s+INDEXED BY (?:source_catalog_(?:entry|file)_[a-z_]+|idx_content_translation_[a-z_]+)`)

func publishedCatalogSQL(query string) string {
	query = catalogPublicationIndexHint.ReplaceAllString(query, "")
	return catalogPublicationTables.ReplaceAllStringFunc(query, func(table string) string { return "published_" + table })
}

type catalogReadTxKey struct{}

type catalogReadSnapshot struct {
	tx        *sql.Tx
	preparing bool
}

type publishedCatalogRows struct{ catalogEntryQueryer }

func (q publishedCatalogRows) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return q.catalogEntryQueryer.QueryRowContext(ctx, publishedCatalogSQL(query), args...)
}

func (s *Server) beginCatalogEntryRead(ctx context.Context, id int64) (context.Context, catalogEntry, remoteSourceForUse, func(), error) {
	entry, source, err := s.loadRetainedSourceCatalogEntry(ctx, id)
	if err != nil {
		return ctx, entry, source, nil, err
	}
	readCtx, done, err := s.beginCatalogRead(ctx)
	if err != nil {
		return ctx, entry, source, nil, err
	}
	published, err := resolveCatalogEntry(readCtx, s.catalogRead(readCtx), id)
	if err != nil {
		done()
		return ctx, entry, source, nil, err
	}
	if published.WorkID == nil && published.PrimaryCode == entry.PrimaryCode {
		published.WorkID = entry.WorkID
	}
	if published.SourceID > 0 {
		source, err = s.loadRemoteSourceForUse(readCtx, published.SourceID)
		if err != nil {
			done()
			return ctx, published, source, nil, err
		}
	}
	if published.SourceID == 0 {
		published.Status = "missing"
	} else if !source.Enabled {
		published.Status = "disabled"
	}
	return readCtx, published, source, done, nil
}

type catalogReadQueryer struct {
	db      *sql.DB
	tx      *sql.Tx
	working bool
}

func (q catalogReadQueryer) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if !q.working {
		query = publishedCatalogSQL(query)
	}
	if q.tx != nil {
		return q.tx.QueryContext(ctx, query, args...)
	}
	return q.db.QueryContext(ctx, query, args...)
}
func (q catalogReadQueryer) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if !q.working {
		query = publishedCatalogSQL(query)
	}
	if q.tx != nil {
		return q.tx.QueryRowContext(ctx, query, args...)
	}
	return q.db.QueryRowContext(ctx, query, args...)
}
func (s *Server) catalogRead(ctx context.Context) catalogReadQueryer {
	_, working := ctx.Value(catalogPublicationKey{}).(int64)
	if snapshot, _ := ctx.Value(catalogReadTxKey{}).(*catalogReadSnapshot); snapshot != nil {
		return catalogReadQueryer{db: s.db, tx: snapshot.tx, working: working || !snapshot.preparing}
	}
	return catalogReadQueryer{db: s.db, working: working}
}

func (s *Server) beginCatalogRead(ctx context.Context) (context.Context, func(), error) {
	if snapshot, _ := ctx.Value(catalogReadTxKey{}).(*catalogReadSnapshot); snapshot != nil {
		return ctx, func() {}, nil
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ctx, nil, err
	}
	// Establish the read snapshot while checking the partial pending index. With
	// no preparing operation every projection is identical to its live table.
	// This fact belongs only to this transaction: a later publication cannot
	// change its snapshot, and reads outside it still use the published views.
	var preparing bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM source_catalog_publication WHERE state='preparing')`).Scan(&preparing); err != nil {
		_ = tx.Rollback()
		return ctx, nil, err
	}
	snapshot := &catalogReadSnapshot{tx: tx, preparing: preparing}
	return context.WithValue(ctx, catalogReadTxKey{}, snapshot), func() { _ = tx.Rollback() }, nil
}

func catalogPublicationOwner(kind string, generation int64) string {
	// Full reconciliation and incremental recovery share the same observation
	// generation and resume the same unpublished operation after interruption.
	if kind == "sync" || kind == "full" {
		kind = "generation"
	}
	return fmt.Sprintf("%s:%d", strings.TrimSpace(kind), generation)
}

var catalogPublicationRelations = []string{"source_catalog_search_doc", "source_catalog_file", "source_catalog_entry_admission", "source_catalog_episode_pending", "source_catalog_episode_publication", "catalog_series_member", "catalog_series", "source_catalog_entry", "content_translation", "work_source_presence", "media_file_location"}

func (s *Server) cleanupCatalogPublications(ctx context.Context) error {
	for _, table := range catalogPublicationRelations {
		for {
			tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
			if err != nil {
				return err
			}
			result, err := tx.ExecContext(ctx, `DELETE FROM `+table+`_publication_before WHERE rowid IN (SELECT b.rowid FROM `+table+`_publication_before b JOIN source_catalog_publication op ON op.id=b.operation_id WHERE op.state='committed' LIMIT 64)`)
			var n int64
			if err == nil {
				n, err = result.RowsAffected()
			}
			if err == nil {
				err = tx.Commit()
			}
			release()
			if err != nil {
				return err
			}
			if n < 64 {
				break
			}
			if err = pauseCatalogWriter(ctx); err != nil {
				return err
			}
		}
	}
	for {
		tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM source_catalog_listing_item WHERE (listing_id,name) IN (SELECT i.listing_id,i.name FROM source_catalog_listing l JOIN source_catalog_listing_item i ON i.listing_id=l.id WHERE l.state IN ('applied','invalid') AND NOT EXISTS(SELECT 1 FROM source_catalog_publication pending WHERE pending.file_source_id=l.file_source_id AND pending.state='preparing') LIMIT 64)`)
		var n int64
		if err == nil {
			n, err = result.RowsAffected()
		}
		if err == nil {
			err = tx.Commit()
		}
		release()
		if err != nil {
			return err
		}
		if n < 64 {
			break
		}
		if err = pauseCatalogWriter(ctx); err != nil {
			return err
		}
	}
	for {
		tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM source_catalog_listing WHERE id IN (SELECT id FROM source_catalog_listing l WHERE state IN ('applied','invalid') AND NOT EXISTS(SELECT 1 FROM source_catalog_listing_item i WHERE i.listing_id=l.id) AND NOT EXISTS(SELECT 1 FROM source_catalog_publication pending WHERE pending.file_source_id=l.file_source_id AND pending.state='preparing') LIMIT 64)`)
		var n int64
		if err == nil {
			n, err = result.RowsAffected()
		}
		if err == nil {
			err = tx.Commit()
		}
		release()
		if err != nil {
			return err
		}
		if n < 64 {
			break
		}
		if err = pauseCatalogWriter(ctx); err != nil {
			return err
		}
	}
	for {
		tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM source_catalog_publication_entity WHERE (operation_id,entity_kind,entity_id) IN
			(SELECT e.operation_id,e.entity_kind,e.entity_id FROM source_catalog_publication_entity e JOIN source_catalog_publication op ON op.id=e.operation_id WHERE op.state='committed' LIMIT 64)`)
		var n int64
		if err == nil {
			n, err = result.RowsAffected()
		}
		if err == nil {
			err = tx.Commit()
		}
		release()
		if err != nil {
			return err
		}
		if n < 64 {
			break
		}
		if err = pauseCatalogWriter(ctx); err != nil {
			return err
		}
	}
	for {
		tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
		if err != nil {
			return err
		}
		query := `DELETE FROM source_catalog_publication WHERE id IN (SELECT op.id FROM source_catalog_publication op WHERE state='committed'`
		for _, table := range catalogPublicationRelations {
			query += ` AND NOT EXISTS(SELECT 1 FROM ` + table + `_publication_before b WHERE b.operation_id=op.id)`
		}
		query += ` AND NOT EXISTS(SELECT 1 FROM source_catalog_publication_entity e WHERE e.operation_id=op.id) LIMIT 64)`
		result, err := tx.ExecContext(ctx, query)
		var n int64
		if err == nil {
			n, err = result.RowsAffected()
		}
		if err == nil {
			err = tx.Commit()
		}
		release()
		if err != nil {
			return err
		}
		if n < 64 {
			break
		}
		if err = pauseCatalogWriter(ctx); err != nil {
			return err
		}
	}
	return nil
}
