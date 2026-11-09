package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/yexca/kikoto/backend/internal/storage"
)

// A flat catalog misses the cost of published series membership. Keep the full
// 42,624-entry shape and the same 15-second client boundary as mixed-load.
// This opt-in budget measures ordinary HTTP requests without race instrumentation.
func TestCatalogSeriesBrowseRequestBudget(t *testing.T) {
	if os.Getenv("KIKOTO_CATALOG_RESOURCE_SCALE") != "1" {
		t.Skip("opt-in synthetic resource measurement")
	}
	s, host, admin, _ := translationHTTPFixture(t)
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, statement := range []string{
		`INSERT INTO file_source(id,code,display_name,source_type,enabled) VALUES
		 (70,'example_remote_a','Example Remote A','openlist',1),
		 (71,'example_remote_b','Example Remote B','openlist',1)`,
		`INSERT INTO file_source_endpoint(file_source_id,api_url,base_url) VALUES
		 (70,'https://source.example.invalid','https://source.example.invalid'),
		 (71,'https://other.example.invalid','https://other.example.invalid')`,
		`WITH RECURSIVE n(i) AS(SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<42624)
		 INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,availability_status,updated_at)
		 SELECT i,70,'/Example/'||i,'Example title '||i,'available','2026-01-01 00:00:00' FROM n`,
		`INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes)
		 SELECT id,'track.mp3','Example track','audio',128 FROM source_catalog_entry WHERE file_source_id=70`,
		`WITH RECURSIVE n(i) AS(SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<600)
		 INSERT INTO catalog_series(id,title,created_at)
		 SELECT i,'Example series '||i,'2026-01-02 00:00:00' FROM n`,
		`INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence)
		 SELECT (id-1)/10+1,id,(id-1)%10+1,'manual' FROM source_catalog_entry WHERE id<=6000`,
		// Missing members retain audio but cannot play; a failed observation keeps
		// the last published audio playable. Admission is per member.
		`UPDATE source_catalog_entry SET availability_status='missing' WHERE id BETWEEN 5981 AND 5990`,
		`UPDATE source_catalog_entry SET availability_status='unavailable' WHERE id BETWEEN 5971 AND 5980`,
		`DELETE FROM source_catalog_file WHERE catalog_entry_id BETWEEN 5961 AND 5970
		 OR catalog_entry_id BETWEEN 5952 AND 5960`,
		// A singleton does not fold entries, and other-source members do not
		// qualify a series for this source's page.
		`DELETE FROM catalog_series_member WHERE catalog_entry_id BETWEEN 5942 AND 5950`,
		`UPDATE source_catalog_entry SET file_source_id=71 WHERE id BETWEEN 5931 AND 5940 OR id=6004`,
		`INSERT INTO work(id,title) VALUES(1,'Example shared work')`,
		`UPDATE source_catalog_entry SET work_id=1 WHERE id IN (6001,6002)`,
		`UPDATE source_catalog_entry SET relocated_to_id=6001 WHERE id=6003`,
		`INSERT INTO source_catalog_entry_admission(catalog_entry_id,generation,files,audio,non_audio)
		 SELECT e.id,e.last_seen_generation,count(f.catalog_entry_id),count(f.catalog_entry_id),0
		 FROM source_catalog_entry e LEFT JOIN source_catalog_file f ON f.catalog_entry_id=e.id GROUP BY e.id`,
	} {
		if _, err := tx.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	host.Client().Timeout = 15 * time.Second
	readPage := func(label, query string, total int, ids []int64, title string, playable bool) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, host.URL+"/api/remote-sources/70/works?pageSize=24"+query, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.AddCookie(admin)
		started := time.Now()
		res, err := host.Client().Do(req)
		if err != nil {
			t.Fatalf("%s browse request: %v", label, err)
		}
		defer func() { _ = res.Body.Close() }()
		var view remoteWorksResponse
		if err := json.NewDecoder(res.Body).Decode(&view); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s_browse_ms=%.3f;entries=42624;series=600;total=%d", label, float64(time.Since(started).Microseconds())/1000, view.Total)
		if res.StatusCode != http.StatusOK || view.Status != "ok" || view.Total != total || view.Truncated || len(view.Works) != min(total, 24) {
			t.Fatalf("%s browse status=%d: %+v", label, res.StatusCode, view)
		}
		for i, id := range ids {
			card := view.Works[i]
			if card.SeriesID != id || card.CatalogEntryID != 0 || card.PrimaryCode != "" {
				t.Fatalf("%s card %d: %+v, want numberless series %d", label, i, card, id)
			}
			wantPlayable := id != 599
			if id == 600 {
				wantPlayable = playable
				if card.Title != title {
					t.Fatalf("%s series title=%q, want %q", label, card.Title, title)
				}
			}
			wantAvailability := "available"
			if !wantPlayable {
				wantAvailability = "missing"
			}
			if card.RemotePlayable != wantPlayable || card.Availability != wantAvailability {
				t.Fatalf("%s series %d availability=%q playable=%t, want %q/%t", label, id, card.Availability, card.RemotePlayable, wantAvailability, wantPlayable)
			}
		}
	}
	beforeIDs := []int64{600, 599, 598, 596, 593, 592, 591, 590, 589, 588, 587, 586, 585, 584, 583, 582, 581, 580, 579, 578, 577, 576, 575, 574}
	readPage("live", "", 37228, beforeIDs, "Example series 600", true)
	// Stable identities from prior source generations remain after their source
	// is retired. Planner statistics must include that reachable history shape.
	metadataReviewExec(t, s.db, `WITH RECURSIVE n(i) AS(SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<80000)
		INSERT INTO source_catalog_entry(id,root_path,title)
		SELECT 100000+i,'/Retired example/'||i,'Retired example '||i FROM n;`)
	if err := storage.OptimizeStatistics(context.Background(), s.db); err != nil {
		t.Fatal(err)
	}

	ctx, op, err := s.beginCatalogPublication(context.Background(), 70, "synthetic:browse")
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`UPDATE catalog_series_member SET position=position+10`,
		`UPDATE catalog_series SET title='Example replacement series' WHERE id=600`,
		`UPDATE source_catalog_entry SET availability_status='missing' WHERE id BETWEEN 5991 AND 6000`,
		`DELETE FROM catalog_series_member WHERE series_id=599 AND catalog_entry_id<>5981`,
	} {
		if _, err := s.execCatalogWrite(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	readPage("preparing", "", 37228, beforeIDs, "Example series 600", true)
	// Only file titles contain this needle. The original-language search must
	// keep those matches, including series members, while the new snapshot is
	// unpublished; filtering a page after admission would hide the query cost.
	readPage("preparing_origin_short", "&lang=origin&q=ck", 37228, beforeIDs, "Example series 600", true)
	readPage("preparing_origin_missing", "&lang=origin&q=zz", 0, nil, "", false)
	if err := s.commitCatalogPublication(ctx, op); err != nil {
		t.Fatal(err)
	}
	afterIDs := []int64{600, 598, 596, 593, 592, 591, 590, 589, 588, 587, 586, 585, 584, 583, 582, 581, 580, 579, 578, 577, 576, 575, 574, 573}
	readPage("committed", "", 37237, afterIDs, "Example replacement series", false)
}
