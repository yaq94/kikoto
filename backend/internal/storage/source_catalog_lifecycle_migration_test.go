package storage

import (
	"testing"
)

func TestSourceCatalogLifecycleUpgradePreservesEntriesAndPersonalState(t *testing.T) {
	source := testMigrationDir(t)
	previous := copyNumberedMigrationsThrough(t, source, 71)
	db := openMigrationManagerDB(t)
	if err := Migrate(db, previous); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO user_account(id,username,role) VALUES(1,'synthetic-user','user');
		INSERT INTO file_source(id,code,display_name,source_type) VALUES(1,'example_remote_a','Example Remote A','openlist');
		INSERT INTO work(id,primary_code,title) VALUES(1,'','Example Work');
		INSERT INTO user_work_state(user_id,work_id,rating,note) VALUES(1,1,4,'Example retained note');
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,work_id) VALUES(1,1,'/Example Work','Example Work',1);
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES(1,'track.mp3','track.mp3','audio',100);
		INSERT INTO catalog_series(id,title) VALUES(1,'Example Series');
		INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence) VALUES(1,1,1,'manual');
	`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Migrate(db, completeMigrationDir(t)); err != nil {
			t.Fatal(err)
		}
		var status, hash, note string
		var generation, rating, workID, members int
		if err := db.QueryRow(`SELECT e.availability_status,e.last_seen_generation,f.content_hash,e.work_id,u.rating,u.note,
			(SELECT count(*) FROM catalog_series_member WHERE catalog_entry_id=e.id)
			FROM source_catalog_entry e JOIN source_catalog_file f ON f.catalog_entry_id=e.id
			JOIN user_work_state u ON u.work_id=e.work_id WHERE e.id=1`).Scan(&status, &generation, &hash, &workID, &rating, &note, &members); err != nil {
			t.Fatal(err)
		}
		if status != "available" || generation != 0 || hash != "" || workID != 1 || rating != 4 || note != "Example retained note" || members != 1 {
			t.Fatal("upgrade changed existing identity, series or personal state")
		}
	}
	if _, err := db.Exec(`INSERT INTO source_catalog_relocation_candidate(catalog_entry_id,candidate_entry_id,reason,generation) VALUES(1,999,'insufficient_evidence',1)`); err == nil {
		t.Fatal("candidate accepted a missing target")
	}
}
