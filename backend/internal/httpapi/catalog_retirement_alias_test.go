package httpapi

import (
	"context"
	"fmt"
	"testing"

	"github.com/yexca/kikoto/backend/internal/config"
	"github.com/yexca/kikoto/backend/internal/storage"
)

func TestCatalogPublicationRetiresAliasesInResumableBatches(t *testing.T) {
	audit := &catalogWriteAudit{}
	db := openCatalogWriteAuditedDB(t, audit)
	s := NewServer(db, config.Config{})
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	metadataReviewExec(t, db, `INSERT INTO file_source(id,code,display_name,source_type) VALUES(1,'example_remote','Example','openlist');
	 INSERT INTO source_catalog_entry(id,file_source_id,root_path,title) VALUES(1,1,'/Old','Old'),(2,1,'/New','New')`)
	for id := 10; id < 160; id++ {
		metadataReviewExec(t, db, `INSERT INTO source_catalog_entry(id,root_path,title,relocated_to_id) VALUES(?,?,?,1)`, id, fmt.Sprintf("/Alias%d", id), "Example alias")
	}
	ctx, op, err := s.beginCatalogPublication(context.Background(), 1, "synthetic:alias-retire")
	if err != nil {
		t.Fatal(err)
	}
	audit.start()
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	retired, err := retireCatalogCardTx(ctx, tx.Tx, 1, 2)
	if err == nil {
		err = tx.Commit()
	}
	release()
	if err != nil || !retired {
		t.Fatalf("retire=%v err=%v", retired, err)
	}
	_, peak, _, _ := audit.details()
	if peak > 64 {
		t.Fatalf("retirement updated %d entries in one transaction, want <=64", peak)
	}
	metadataReviewExec(t, db, `CREATE TRIGGER synthetic_alias_fail BEFORE UPDATE OF relocated_to_id ON source_catalog_entry
	 WHEN OLD.id=100 BEGIN SELECT RAISE(ABORT,'synthetic alias batch failure'); END`)
	if err := s.prepareCatalogPublicationRetirements(ctx, op); err == nil {
		t.Fatal("expected injected batch failure")
	}
	var prepared, published int
	if err := db.QueryRow(`SELECT count(*) FROM source_catalog_entry WHERE id>=10 AND relocated_to_id=2`).Scan(&prepared); err != nil {
		t.Fatal(err)
	}
	if err := s.catalogRead(context.Background()).QueryRowContext(context.Background(), `SELECT count(*) FROM source_catalog_entry WHERE id>=10 AND relocated_to_id=1`).Scan(&published); err != nil {
		t.Fatal(err)
	}
	if prepared != 64 || published != 150 {
		t.Fatalf("partial working=%d published old=%d, want 64/150", prepared, published)
	}
	metadataReviewExec(t, db, `DROP TRIGGER synthetic_alias_fail`)
	var seq int
	var name, databasePath string
	if err := db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &databasePath); err != nil {
		t.Fatal(err)
	}
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = storage.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s = NewServer(db, config.Config{})
	// Resuming the same operation must consume only aliases still pointing at the
	// retired card, leaving the published relation unchanged until the marker.
	if err := s.commitCatalogPublication(ctx, op); err != nil {
		t.Fatal(err)
	}
	if err := s.catalogRead(context.Background()).QueryRowContext(context.Background(), `SELECT count(*) FROM source_catalog_entry WHERE id>=10 AND relocated_to_id=2`).Scan(&published); err != nil {
		t.Fatal(err)
	}
	_, peak, _, _ = audit.details()
	if published != 150 || peak > 64 {
		t.Fatalf("published=%d peak entries=%d, want 150/<=64", published, peak)
	}
}
