package httpapi

import (
	"net/http"
	"testing"
)

// A work can have an OpenList catalog directory and a Kikoeru directory. The
// detail response must list both so the viewer can switch between them, and it
// must keep them distinct: the Kikoeru row carries no catalog entry id.
func TestCatalogDirectoriesListBothOpenListAndKikoeru(t *testing.T) {
	f := newRoleBoundaryFixture(t)
	if _, err := f.server.db.Exec(`
		INSERT INTO work(id,primary_code,title) VALUES(41,'RJ00000061','Example Work');
		INSERT INTO file_source(id,code,display_name,source_type,enabled) VALUES
			(41,'example_catalog','Example Catalog','openlist',1),
			(42,'example_kikoeru','Example Kikoeru','kikoeru_compatible',1);
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,primary_code,title,work_id)
			VALUES(410,41,'/Example/RJ00000061','RJ00000061','Example Work',41);
		INSERT INTO work_source_presence(work_id,file_source_id,presence_type,remote_id,remote_code,availability,raw_json)
			VALUES(41,42,'source','7701','RJ00000061','available','{"title":"Example Work"}');
	`); err != nil {
		t.Fatal(err)
	}
	got := reviewRequest(t, f.handler, f.cookies["user"], http.MethodGet,
		"/api/works/41/catalog-directories", "", http.StatusOK)["directories"].([]any)
	if len(got) != 2 {
		t.Fatalf("directories = %#v, want the catalog and the Kikoeru directory", got)
	}
	types := map[string]float64{}
	for _, raw := range got {
		item := raw.(map[string]any)
		types[item["directoryType"].(string)] = item["sourceId"].(float64)
	}
	if types["catalog"] != 41 || types["remote"] != 42 {
		t.Fatalf("directory sources = %#v, want catalog 41 and remote 42", types)
	}
}

// A shared association groups two works into one identity. Each side's catalog
// directory must still be listed, otherwise switching sources loses one side.
func TestCatalogDirectoriesFollowSharedIdentity(t *testing.T) {
	f := newRoleBoundaryFixture(t)
	if _, err := f.server.db.Exec(`
		INSERT INTO work(id,primary_code,title) VALUES
			(51,'RJ00000062','Example Left'),(52,'RJ00000063','Example Right');
		INSERT INTO file_source(id,code,display_name,source_type,enabled) VALUES
			(51,'example_catalog','Example Catalog','openlist',1);
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,primary_code,title,work_id) VALUES
			(510,51,'/Example/RJ00000062','RJ00000062','Example Left',51),
			(511,51,'/Example/RJ00000063','RJ00000063','Example Right',52);
		INSERT INTO shared_association(id,operation,status,version,fingerprint,snapshot_json)
			VALUES(51,'link','completed','v1','example','{}');
		INSERT INTO shared_identity_member(work_id,identity_work_id,operation_id) VALUES(51,51,51),(52,51,51);
	`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"51", "52"} {
		got := reviewRequest(t, f.handler, f.cookies["user"], http.MethodGet,
			"/api/works/"+id+"/catalog-directories", "", http.StatusOK)["directories"].([]any)
		if len(got) != 2 {
			t.Fatalf("work %s directories = %#v, want both shared sides", id, got)
		}
	}
}
