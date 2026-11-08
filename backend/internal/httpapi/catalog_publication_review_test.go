package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

func TestCatalogReviewDuringRetirementKeepsSuccessorIdentity(t *testing.T) {
	for _, registered := range []bool{false, true} {
		t.Run(fmt.Sprint(registered), func(t *testing.T) {
			db, file := openPublicationRetryDB(t)
			f := newLooseWorkFixtureWithDB(t, map[string][]map[string]any{"/Example": {looseAudio("Original.mp3")}}, db)
			f.request(t, http.MethodPost, "/catalog/scan", `{}`, 200)
			var oldID int64
			if err := db.QueryRow(`SELECT id FROM source_catalog_entry WHERE root_path='/Example'`).Scan(&oldID); err != nil {
				t.Fatal(err)
			}
			oldURL := fmt.Sprintf("/api/source-catalog/%d", oldID)
			if registered {
				reviewRequest(t, f.handler, f.cookie, http.MethodPatch, oldURL+"/user-state", `{"rating":2,"note":"Example first note"}`, 200)
			}
			ctx, op, err := f.server.beginCatalogPublication(context.Background(), f.sourceID(t), "synthetic:review-retirement")
			if err != nil {
				t.Fatal(err)
			}
			tx, release, err := beginTxWithDatabaseBusyRetry(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			newID, err := persistOpenListEntryTx(ctx, tx.Tx, f.sourceID(t), "/Successor", "", "Example successor")
			if err == nil {
				_, err = tx.ExecContext(ctx, `INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES(?,'Original.mp3','Example Track','audio',1)`, newID)
			}
			if err == nil {
				err = refreshCatalogEntryAdmissionTx(ctx, tx.Tx, newID)
			}
			var retired bool
			if err == nil {
				retired, err = retireCatalogCardTx(ctx, tx.Tx, oldID, newID)
			}
			if err == nil {
				err = tx.Commit()
			}
			release()
			if err != nil || !retired {
				t.Fatalf("retire=%v err=%v", retired, err)
			}
			reviewRequest(t, f.handler, f.cookie, http.MethodPatch, oldURL+"/user-state", `{"rating":4,"note":"Example saved during publication"}`, 200)
			assertReview(t, reviewRequest(t, f.handler, f.cookie, http.MethodGet, oldURL, "", 200), float64(4), "Example saved during publication")
			reopenCatalogFiniteFixture(t, f, file)
			if err = f.server.commitCatalogPublication(ctx, op); err != nil {
				t.Fatal(err)
			}
			for _, url := range []string{oldURL, fmt.Sprintf("/api/source-catalog/%d", newID)} {
				assertReview(t, reviewRequest(t, f.handler, f.cookie, http.MethodGet, url, "", 200), float64(4), "Example saved during publication")
			}
			var works, aliasLinks int
			if err = f.server.db.QueryRow(`SELECT count(*),(SELECT count(*) FROM source_catalog_entry WHERE id=? AND work_id IS NOT NULL) FROM work`, oldID).Scan(&works, &aliasLinks); err != nil || works != 1 || aliasLinks != 0 {
				t.Fatalf("works=%d alias links=%d err=%v", works, aliasLinks, err)
			}
			if len(f.gets) != 0 {
				t.Fatal("review requested media")
			}
		})
	}
}
