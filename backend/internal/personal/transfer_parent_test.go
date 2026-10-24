package personal

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// A trigger normally supplies a generated series reference. Restoring a parent
// must still create the exact backup reference when that row is absent, without
// attaching the reference to a work or catalog entry.
func TestClaimTransferRefCreatesExplicitSeriesReference(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `INSERT INTO catalog_series(id,title) VALUES(901,'Example Series')`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM personal_transfer_ref WHERE series_id=901`); err != nil {
		t.Fatal(err)
	}
	ref := "s:" + strings.Repeat("2", 32)
	if err = claimTransferRef(ctx, tx, ref, "series_id", 901); err != nil {
		t.Fatal(err)
	}
	var series, work, catalog sql.NullInt64
	if err = tx.QueryRowContext(ctx, `SELECT series_id,work_id,catalog_entry_id FROM personal_transfer_ref WHERE ref=?`, ref).
		Scan(&series, &work, &catalog); err != nil {
		t.Fatal(err)
	}
	if !series.Valid || series.Int64 != 901 || work.Valid || catalog.Valid {
		t.Fatalf("claimed ref = series %v work %v catalog %v, want only series 901", series, work, catalog)
	}
}

func TestClaimTransferRefRefusesReferenceOwnedByAnotherSeries(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `INSERT INTO catalog_series(id,title) VALUES(901,'First Series'),(902,'Second Series')`); err != nil {
		t.Fatal(err)
	}
	var ref string
	if err = tx.QueryRowContext(ctx, `SELECT ref FROM personal_transfer_ref WHERE series_id=902`).Scan(&ref); err != nil {
		t.Fatal(err)
	}
	if err = claimTransferRef(ctx, tx, ref, "series_id", 901); !errors.Is(err, ErrConflict) {
		t.Fatalf("claiming another series ref = %v, want conflict", err)
	}
	var owner int64
	if err = tx.QueryRowContext(ctx, `SELECT series_id FROM personal_transfer_ref WHERE ref=?`, ref).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner != 902 {
		t.Fatalf("ref owner = %d, want the original series 902", owner)
	}
}
