package httpapi

import (
	"net/http"
	"testing"
)

// Excluding a card is an operator decision, not an availability answer. A later
// lifecycle pass must leave the verdict alone even though the card's directory
// no longer has a row in the current generation.
func TestCatalogLifecycleKeepsExcludedCardsOutOfMissing(t *testing.T) {
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/":             {looseDir("Creator")},
		"/Creator":      {looseDir("Keep"), looseDir("Drop")},
		"/Creator/Keep": {looseAudio("keep.mp3")},
		"/Creator/Drop": {looseAudio("drop.mp3")},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	sourceID := f.sourceID(t)
	tx, release, err := beginTxWithDatabaseBusyRetry(t.Context(), f.server.db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(t.Context(), `INSERT INTO source_catalog_exclusion(file_source_id,path,note) VALUES(?,?,?)`,
		sourceID, "/Creator/Drop", "test"); err != nil {
		release()
		t.Fatal(err)
	}
	if err = applyCatalogExclusionTx(t.Context(), tx.Tx, sourceID, "/Creator/Drop"); err != nil {
		release()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		release()
		t.Fatal(err)
	}
	release()
	// A second generation makes the excluded card look "not published", which is
	// exactly the state that used to be rewritten as missing.
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"force":true}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("rescan = %#v", p)
	}
	var status string
	if err := f.server.db.QueryRow(`SELECT availability_status FROM source_catalog_entry
		WHERE file_source_id=? AND root_path='/Creator/Drop'`, sourceID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "excluded" {
		t.Fatalf("excluded card became %q after a lifecycle pass", status)
	}
	var keep string
	if err := f.server.db.QueryRow(`SELECT availability_status FROM source_catalog_entry
		WHERE file_source_id=? AND root_path='/Creator/Keep'`, sourceID).Scan(&keep); err != nil {
		t.Fatal(err)
	}
	if keep != "available" {
		t.Fatalf("kept card = %q, want available", keep)
	}
}
