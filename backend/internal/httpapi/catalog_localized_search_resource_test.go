package httpapi

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/yexca/kikoto/backend/internal/config"
	"github.com/yexca/kikoto/backend/internal/storage"
	"modernc.org/sqlite"
)

func TestCatalogRareLocalizedSearchBoundsPublishedReads(t *testing.T) {
	entries, retired := 6000, 12000
	if os.Getenv("KIKOTO_CATALOG_RESOURCE_SCALE") == "1" {
		entries, retired = 42624, 80000
	}
	t.Logf("entries=%d series=599 retired_entries=%d", entries, retired)
	db := openMigratedTestDB(t)
	seedCatalogPlanSource(t, db)
	for _, statement := range []string{
		fmt.Sprintf(`WITH RECURSIVE n(i) AS(SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<%d)
		 INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,availability_status,updated_at)
		 SELECT i,7,'/Example/'||i,CASE WHEN i IN (1,6000) THEN 'Example violet specimen' ELSE 'Example catalog title '||i END,
		 'available','2026-01-01 00:00:00' FROM n`, entries),
		`INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes)
		 SELECT id,'track.mp3','Example track','audio',128 FROM source_catalog_entry`,
		`WITH RECURSIVE n(i) AS(SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<599)
		 INSERT INTO catalog_series(id,title,source_id,created_at)
		 SELECT i,CASE WHEN i=2 THEN 'Example violet collection' ELSE 'Example series '||i END,7,'2026-01-02 00:00:00' FROM n`,
		`INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence)
		 SELECT (id-1)/10+1,id,(id-1)%10+1,'manual' FROM source_catalog_entry WHERE id<=5990`,
		`INSERT INTO source_catalog_entry_admission(catalog_entry_id,generation,files,audio,non_audio)
		 SELECT id,last_seen_generation,1,1,0 FROM source_catalog_entry`,
		`INSERT INTO content_translation(cache_key,entity_kind,entity_id,field,language,source_text,translated_text,origin)
		 SELECT 'entry:'||id,'entry',id,'title','en',title,title,'native' FROM source_catalog_entry
		 UNION ALL SELECT 'series:'||id,'series',id,'title','en',title,title,'native' FROM catalog_series`,
		fmt.Sprintf(`WITH RECURSIVE n(i) AS(SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<%d)
		 INSERT INTO source_catalog_entry(id,root_path,title) SELECT 100000+i,'/Retired example/'||i,'Retired example '||i FROM n`, retired),
	} {
		metadataReviewExec(t, db, statement)
	}
	if err := storage.OptimizeStatistics(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	checkPage := func(t *testing.T, term string, page, total int, wantIDs []int64) int {
		t.Helper()
		catalogSearchPageAccesses(t, db)
		started := time.Now()
		response := requestRemoteWorks(t, db, fmt.Sprintf("q=%s&lang=en&pageSize=24&page=%d", url.QueryEscape(term), page))
		cost := catalogSearchPageAccesses(t, db)
		t.Logf("%q page=%d sqlite_page_accesses=%d elapsed=%s", term, page, cost, time.Since(started))
		if response.Total != total || response.Truncated || !slices.Equal(remoteWorkIDs(response), wantIDs) {
			t.Errorf("%q page=%d: total=%d truncated=%t cards=%v, want %d/false/%v", term, page, response.Total, response.Truncated, remoteWorkIDs(response), total, wantIDs)
		}
		// The fixed overhead includes transaction setup and SQLite planning. A
		// three-hit query or an empty FTS hit set should not read a catalog-sized
		// number of data and b-tree pages, even when the page has no rows.
		if cost > 5000 {
			t.Errorf("%q page=%d read %d SQLite pages, want at most 5000", term, page, cost)
		}
		return cost
	}
	checkSearch := func(t *testing.T) {
		t.Helper()
		firstPage := checkPage(t, "violet", 1, 3, []int64{-2, -1, 6000})
		pastEnd := checkPage(t, "violet", 9, 3, nil)
		if pastEnd > firstPage+24 {
			t.Errorf("out-of-range page read %d SQLite pages after first page read %d; the total should not re-run the search", pastEnd, firstPage)
		}
		checkPage(t, "unpublished", 9, 0, nil)
	}
	t.Run("stable", checkSearch)

	s := NewServer(db, config.Config{})
	ctx, op, err := s.beginCatalogPublication(context.Background(), 7, "synthetic:localized-search")
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`UPDATE source_catalog_entry SET availability_status='missing' WHERE id BETWEEN 3 AND 64`,
		`UPDATE source_catalog_entry SET title='Example unpublished specimen' WHERE id=6000`,
		`UPDATE catalog_series SET title='Example unpublished collection' WHERE id=2`,
		`DELETE FROM catalog_series_member WHERE series_id=1 AND catalog_entry_id<>1`,
		`INSERT INTO content_translation(cache_key,entity_kind,entity_id,field,language,source_text,translated_text,origin)
		 SELECT 'changed:'||id,'entry',id,'title','en',title,title,'native' FROM source_catalog_entry WHERE id=6000`,
	} {
		if _, err = s.execCatalogWrite(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("preparing", checkSearch)
	if err = s.commitCatalogPublication(ctx, op); err != nil {
		t.Fatal(err)
	}
	t.Run("committed", func(t *testing.T) {
		checkPage(t, "violet", 1, 1, []int64{1})
		checkPage(t, "unpublished", 1, 1, []int64{6000})
	})
}

// Count accesses to SQLite pages in a single pooled test connection, including
// cache hits, so this regression is independent of cache warmth and wall time.
func catalogSearchPageAccesses(t *testing.T, db *sql.DB) int {
	t.Helper()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	var pages int
	if err = conn.Raw(func(driver any) error {
		status := driver.(sqlite.DBStatus)
		for _, op := range []sqlite.DBStatusOp{sqlite.DBStatusCacheHit, sqlite.DBStatusCacheMiss} {
			count, _, err := status.Status(op, true)
			if err != nil {
				return err
			}
			pages += count
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return pages
}
