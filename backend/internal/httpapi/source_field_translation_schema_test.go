package httpapi

import "testing"

// Source-field translations are rebuildable cache rows owned by one source.
// Deleting the source must not leave unreachable rows behind.
func TestSourceFieldTranslationRowsCascadeWithSource(t *testing.T) {
	db := openMigratedTestDB(t)
	if _, err := db.Exec(`INSERT INTO file_source(id,code,display_name,source_type)
		VALUES(95,'cache-owner','Cache owner','openlist');
		INSERT INTO source_field_translation(
			cache_key,source_id,work_code,field,path,language,source_text,translated_text)
		VALUES('synthetic-cache-key',95,'','title','/Example','zh-Hans','Example title','示例标题')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM file_source WHERE id=95`); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := db.QueryRow(`SELECT count(*) FROM source_field_translation WHERE source_id=95`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("orphan source translation rows=%d, want 0", rows)
	}
}
