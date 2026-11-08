package httpapi

import (
	"strings"
	"testing"
)

// A bounded result alone does not bound a scan: an all-audio directory must
// not visit every audio observation just to establish that it has no folders.
func TestCatalogGroupingReadsKindOrderedRanges(t *testing.T) {
	db := openMigratedTestDB(t)
	for _, kind := range []string{"audio", "folder"} {
		t.Run("observation/"+kind, func(t *testing.T) {
			plan := explainCatalogPlan(t, db, `SELECT name FROM source_catalog_observation
				WHERE file_source_id=? AND directory_path=? AND revision=? AND kind=? AND name>?
				ORDER BY name LIMIT 64`, 7, "/Example", 1, kind, "Example Track")
			if !strings.Contains(plan, "kind=? AND name>?") || strings.Contains(plan, "USE TEMP B-TREE") {
				t.Fatalf("kind/name range is not indexed in order: %s", plan)
			}
		})
		t.Run("listing/"+kind, func(t *testing.T) {
			plan := explainCatalogPlan(t, db, `SELECT name FROM source_catalog_listing_item
				WHERE listing_id=? AND directory_path=? AND kind=? AND name>? AND instr(name,'/')=0
				ORDER BY name LIMIT 64`, 1, "/Example", kind, "Example Track")
			if !strings.Contains(plan, "kind=? AND name>?") || strings.Contains(plan, "USE TEMP B-TREE") {
				t.Fatalf("kind/name range is not indexed in order: %s", plan)
			}
		})
	}
}
