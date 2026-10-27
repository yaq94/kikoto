package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/yexca/kikoto/backend/internal/sqlutil"
)

// errCatalogEntryUnavailable marks an entry whose published files never
// reached the admission threshold. The caller turns it into the same
// "not available" answer the detail, tracks and browse routes give.
type catalogEntryUnavailableError struct{ admission catalogEntryAdmission }

func (e catalogEntryUnavailableError) Error() string {
	return "catalog entry is not available: " + e.admission.Reason
}

// errCatalogWorkRegistrationForbidden means the entry has no work yet and the
// caller may not create one. It is a permission answer, not a source failure.
var errCatalogWorkRegistrationForbidden = errors.New("catalog work registration requires remote:track")

// lockCatalogEntry takes the writer lock before reading entry identity. Every
// field read afterwards is the row a concurrent rename or source move committed
// before the lock, never a pre-transaction snapshot.
func lockCatalogEntry(ctx context.Context, tx *sql.Tx, retainedID int64) (catalogEntry, error) {
	if _, err := tx.ExecContext(ctx, "UPDATE source_catalog_entry SET id=id WHERE id=?", retainedID); err != nil {
		return catalogEntry{}, err
	}
	entry, err := resolveCatalogEntry(ctx, tx, retainedID)
	if err != nil {
		return catalogEntry{}, err
	}
	if entry.SourceID == 0 {
		return entry, nil
	}
	admission, err := catalogPublishedEntryAdmission(ctx, tx, entry.ID)
	if err != nil {
		return catalogEntry{}, err
	}
	// A retained missing record has no current published file set to judge;
	// every other source-backed card must pass the same admission rule as the
	// detail, tracks and browse routes.
	if catalogEntryAdmissionRequired(entry) && !admission.Admitted() {
		return catalogEntry{}, catalogEntryUnavailableError{admission: admission}
	}
	return entry, nil
}

// ensureCatalogEntryWork returns the work a catalog entry stands for, reusing
// a work that already carries the same code and creating one when this entry is
// its first publication. The caller holds the writer lock and has already
// accepted the entry.
func ensureCatalogEntryWork(ctx context.Context, tx *sql.Tx, entry catalogEntry, mayRegister bool) (int64, error) {
	var resolvedID int64
	var err error
	if entry.PrimaryCode != "" {
		resolvedID, err = resolveCatalogReviewWorkID(ctx, tx, entry.PrimaryCode)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
		if errors.Is(err, sql.ErrNoRows) {
			resolvedID = 0
		}
	}
	var workID sql.NullInt64
	if err = tx.QueryRowContext(ctx, "SELECT work_id FROM source_catalog_entry WHERE id=?", entry.ID).Scan(&workID); err != nil {
		return 0, err
	}
	if !workID.Valid && resolvedID > 0 {
		return resolvedID, nil
	}
	if !workID.Valid {
		if !mayRegister {
			return 0, errCatalogWorkRegistrationForbidden
		}
		if workID.Int64, err = insertWorkWithEdition(ctx, tx, entry.PrimaryCode, entry.Title); err != nil {
			return 0, err
		}
	}
	return workID.Int64, nil
}

// linkCatalogEntryWork records the resolved identity on the entry and keeps the
// source presence row in step, so a later list or mark reads the same work.
func linkCatalogEntryWork(ctx context.Context, tx *sql.Tx, entry catalogEntry, workID int64, availability string) error {
	if _, err := tx.ExecContext(ctx, "UPDATE source_catalog_entry SET work_id=? WHERE id=?", workID, entry.ID); err != nil {
		return err
	}
	if entry.SourceID == 0 {
		return nil
	}
	return upsertWorkSourcePresence(ctx, tx, workSourcePresence{
		WorkID: workID, FileSourceID: entry.SourceID, PresenceType: sourcePresenceTypeRemoteSource,
		RemoteID: fmt.Sprint(entry.ID), RemoteCode: entry.PrimaryCode, Availability: availability,
		RawJSON: mustJSON(map[string]any{"catalogEntryId": entry.ID}),
	})
}

// insertWorkWithEdition creates a work and the single-edition identity every
// card and series needs. A non-empty code reuses a work that already carries
// it, so two catalog rows for one release share one identity.
func insertWorkWithEdition(ctx context.Context, tx *sql.Tx, code, title string) (int64, error) {
	var workID int64
	var err error
	if code != "" {
		_, err = tx.ExecContext(ctx, `INSERT INTO work(primary_code,title) VALUES(?,?) ON CONFLICT(primary_code) WHERE primary_code<>'' DO NOTHING`, code, title)
		if err == nil {
			err = tx.QueryRowContext(ctx, "SELECT id FROM work WHERE primary_code=?", code).Scan(&workID)
		}
	} else {
		workID, err = sqlutil.InsertID(ctx, tx, "INSERT INTO work(primary_code,title) VALUES('',?)", title)
	}
	if err != nil {
		return 0, err
	}
	var logicalID int64
	err = tx.QueryRowContext(ctx, "SELECT logical_work_id FROM work_edition WHERE work_id=?", workID).Scan(&logicalID)
	if errors.Is(err, sql.ErrNoRows) {
		logicalID, err = sqlutil.InsertID(ctx, tx, "INSERT INTO logical_work(canonical_work_id,canonical_code) VALUES(?,?)", workID, code)
		if err == nil {
			_, err = tx.ExecContext(ctx, "INSERT INTO work_edition(work_id,logical_work_id,primary_code,is_canonical) VALUES(?,?,?,1)", workID, logicalID, code)
		}
	}
	if err != nil {
		return 0, err
	}
	return workID, nil
}
