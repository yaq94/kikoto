package httpapi

import (
	"context"
	"database/sql"
	"testing"
)

func TestCatalogCodedChildWorkReadsBracketedDirectoryCodes(t *testing.T) {
	db := openMigratedTestDB(t)
	if _, err := db.Exec(`INSERT INTO file_source(id,code,display_name,source_type)
		VALUES(9,'coded-child-regression','Coded Child Regression','openlist')`); err != nil {
		t.Fatal(err)
	}
	directories := map[string]string{
		"/same-team":  "[团队][RJ00000001]Work",
		"/same-bonus": "[RJ00000001] Bonus",
		"/not-code":   "[MP3] Title",
		"/different":  "[团队][RJ00000002]Other",
	}
	for directory, child := range directories {
		if _, err := db.Exec(`INSERT INTO source_catalog_directory(file_source_id,path,generation,revision,partition_path,catalog_root,status)
			VALUES(9,?,1,1,?,?,'complete')`, directory, directory, directory); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes)
			VALUES(9,?,?,1,'folder',0)`, directory, child); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, testCase := range []struct {
		directory string
		parent    string
		want      int
	}{
		{directory: "/same-team", parent: "RJ00000001", want: 0},
		{directory: "/same-bonus", parent: "RJ00000001", want: 0},
		{directory: "/not-code", parent: "RJ00000001", want: 0},
		{directory: "/different", parent: "RJ00000001", want: 1},
		{directory: "/same-team", parent: "", want: 1},
	} {
		got, err := catalogCodedChildWorkTx(context.Background(), tx, 9, testCase.directory, testCase.parent)
		if err != nil {
			t.Fatal(err)
		}
		if got != testCase.want {
			t.Fatalf("coded child in %s with parent %q = %d, want %d", testCase.directory, testCase.parent, got, testCase.want)
		}
	}
	if code := catalogDirectoryCode("[MP3] Title"); code != "" {
		t.Fatalf("catalogDirectoryCode([MP3] Title) = %q, want no work code", code)
	}
}
