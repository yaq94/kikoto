package httpapi

import (
	"testing"
)

func TestCatalogSeriesAdmissionMatchesReviewAndTranslation(t *testing.T) {
	for _, tc := range []struct {
		name, mutate string
		status       int
	}{
		{"empty", "", 404},
		{"no_members", `DELETE FROM catalog_series_member`, 404},
		{"source_less_member", `UPDATE source_catalog_entry SET file_source_id=NULL,availability_status='missing'`, 404},
		{"old_audio", `INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes,observed_generation) VALUES(1,'a.mp3','Audio','audio',1,-1)`, 404},
		{"admitted", `INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES(1,'a.mp3','Audio','audio',1)`, 200},
		{"missing", `UPDATE source_catalog_entry SET availability_status='missing' WHERE id=1`, 200},
		{"relocated", `INSERT INTO source_catalog_entry(id,file_source_id,root_path,title) VALUES(2,7,'/Moved','Moved'); UPDATE source_catalog_entry SET file_source_id=NULL,relocated_to_id=2 WHERE id=1; INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES(2,'a.mp3','Audio','audio',1)`, 200},
		{"disabled_admitted", `UPDATE file_source SET enabled=0 WHERE id=7; INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES(1,'a.mp3','Audio','audio',1)`, 200},
		{"restored", `INSERT INTO work(id,title) VALUES(1,'Example series'); INSERT INTO work(id,title,parent_work_id) VALUES(2,'Restored episode',1); UPDATE catalog_series SET work_id=1 WHERE id=1; DELETE FROM catalog_series_member`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, host, admin, listener := translationHTTPFixture(t)
			metadataReviewExec(t, s.db, `INSERT INTO file_source(id,code,display_name,source_type) VALUES(7,'example_remote_a','Example Remote A','openlist');
				INSERT INTO file_source_endpoint(file_source_id,base_url,api_url) VALUES(7,'https://source.example.invalid','https://source.example.invalid');
				INSERT INTO source_catalog_entry(id,file_source_id,root_path,title) VALUES(1,7,'/Episode','Episode');
				INSERT INTO catalog_series(id,title) VALUES(1,'Example series');
				INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence) VALUES(1,1,1,'manual');`)
			if tc.mutate != "" {
				metadataReviewExec(t, s.db, tc.mutate)
			}
			translationRequest(t, host, listener, "GET", "/api/catalog-series/1", "", tc.status)
			t.Run("review", func(t *testing.T) {
				translationRequest(t, host, admin, "PATCH", "/api/catalog-series/1/user-state", `{"rating":5,"note":"Series review"}`, tc.status)
			})
			t.Run("translation", func(t *testing.T) {
				translationRequest(t, host, listener, "POST", "/api/content-translations", `{"seriesId":1,"field":"title","text":"Example series","language":"en-us"}`, tc.status)
			})
			result := translationRequest(t, host, listener, "POST", "/api/content-translations/batch", `{"language":"en-us","items":[{"key":"series","seriesId":1,"field":"title","text":"Example series"}]}`, 200)
			item := result["items"].([]any)[0].(map[string]any)
			if tc.status == 404 && (item["status"] != "failed" || item["code"] != "not_found") {
				t.Fatalf("unreadable series translated in batch: %#v", item)
			}
			if tc.status == 200 && item["status"] != "native" {
				t.Fatalf("readable series translation: %#v", item)
			}
		})
	}
}

func TestContentTranslationCatalogAdmissionMatchesReadableFiles(t *testing.T) {
	for _, tc := range []struct {
		name, mutate string
		status       int
	}{
		{"empty", "", 404},
		{"old_audio", `INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes,observed_generation) VALUES(1,'a.mp3','Audio','audio',1,-1)`, 404},
		{"admitted", `INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES(1,'a.mp3','Audio','audio',1)`, 200},
		{"missing", `UPDATE source_catalog_entry SET availability_status='missing' WHERE id=1`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, host, _, listener := translationHTTPFixture(t)
			metadataReviewExec(t, s.db, `INSERT INTO file_source(id,code,display_name,source_type) VALUES(7,'example_remote_a','Example Remote A','openlist');
			 INSERT INTO file_source_endpoint(file_source_id,base_url,api_url) VALUES(7,'https://source.example.invalid','https://source.example.invalid');
			 INSERT INTO source_catalog_entry(id,file_source_id,root_path,title) VALUES(1,7,'/Example title','Example title');`)
			if tc.mutate != "" {
				metadataReviewExec(t, s.db, tc.mutate)
			}
			translationRequest(t, host, listener, "GET", "/api/source-catalog/1", "", tc.status)
			t.Run("single", func(t *testing.T) {
				translationRequest(t, host, listener, "POST", "/api/content-translations", `{"catalogEntryId":1,"field":"title","text":"Example title","language":"en-us"}`, tc.status)
			})
			result := translationRequest(t, host, listener, "POST", "/api/content-translations/batch", `{"language":"en-us","items":[{"key":"card","catalogEntryId":1,"field":"title","text":"Example title"}]}`, 200)
			item := result["items"].([]any)[0].(map[string]any)
			if tc.status == 404 && item["code"] != "not_found" {
				t.Fatalf("unreadable card translated: %#v", item)
			}
			if tc.status == 200 && item["status"] != "native" {
				t.Fatalf("readable card refused: %#v", item)
			}
		})
	}
}
