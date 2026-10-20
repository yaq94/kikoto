package httpapi

import (
	"fmt"
	"strings"
	"testing"
)

// The publish batch must be driven by the current generation's directories.
// If SQLite starts from the pending index instead, a large backlog left by an
// earlier generation turns each batch into a sort of every queued row. The
// plan therefore has to open with the directory scan and must not build a
// temporary B-tree for the ordering.
func TestCatalogPublishQueryPlanStaysDirectoryDriven(t *testing.T) {
	db := openMigratedTestDB(t)
	if _, err := db.Exec(`INSERT INTO file_source(id,code,display_name,source_type) VALUES(1,'plan-source','Plan source','openlist')`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 400; i++ {
		dir := fmt.Sprintf("/stale/Work %04d", i)
		if _, err := db.Exec(`INSERT INTO source_catalog_directory(file_source_id,path,generation,partition_path,catalog_root,status)
			VALUES(1,?,9,'/stale',?,'complete')`, dir, dir); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO source_catalog_audio_root(file_source_id,generation,catalog_root) VALUES(1,9,?)`, dir); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes,published_generation,publish_pending)
			VALUES(1,?,'track.mp3',1,'audio',100,9,1)`, dir); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 20; i++ {
		dir := fmt.Sprintf("/current/Work %04d", i)
		if _, err := db.Exec(`INSERT INTO source_catalog_directory(file_source_id,path,generation,partition_path,catalog_root,status)
			VALUES(1,?,10,'/current',?,'complete')`, dir, dir); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO source_catalog_audio_root(file_source_id,generation,catalog_root) VALUES(1,10,?)`, dir); err != nil {
			t.Fatal(err)
		}
		for n := 0; n < 5; n++ {
			if _, err := db.Exec(`INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes,published_generation,publish_pending)
				VALUES(1,?,? ,1,'audio',100,0,1)`, dir, fmt.Sprintf("track%d.mp3", n)); err != nil {
				t.Fatal(err)
			}
		}
	}
	plan := strings.Join(queryPlan(t, db, catalogPublishQuery, int64(1), int64(10), catalogPublishRows), "\n")
	if strings.Contains(plan, "TEMP B-TREE") {
		t.Fatalf("publish query sorts instead of walking directories:\n%s", plan)
	}
	if first, second := planLine(plan, "SEARCH d"), planLine(plan, "SEARCH o"); first < 0 || second < 0 || first > second {
		t.Fatalf("publish query is not driven by directories:\n%s", plan)
	}
}

func planLine(plan, needle string) int {
	for i, line := range strings.Split(plan, "\n") {
		if strings.Contains(line, needle) {
			return i
		}
	}
	return -1
}
