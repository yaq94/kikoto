package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/yexca/kikoto/backend/internal/config"
)

// A metadata-only parent is catalog provenance, not a card. Both the detail and
// review routes must hide it, while a retired alias without a source remains
// readable for old links. A source-backed zero-file identity has no current
// published release either, so admission hides it like any other no-audio card.
func TestSourceCatalogHidesMetadataOnlyParentsButKeepsSourcelessAliases(t *testing.T) {
	db := openMigratedTestDB(t)
	if _, err := db.Exec(`INSERT INTO file_source(id,code,display_name,source_type)
		VALUES(7,'hidden-parent-source','Hidden Parent Source','openlist');
		INSERT INTO file_source_endpoint(file_source_id,api_url,base_url)
		VALUES(7,'https://hidden.example.invalid','https://hidden.example.invalid');
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title) VALUES
			(1,7,'/Metadata only','Metadata only'),
			(2,7,'/Playable work','Playable work');
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES
			(1,'cover.jpg','cover.jpg','other',100),
			(2,'track.mp3','track.mp3','audio',100);
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,relocated_to_id)
		VALUES(3,NULL,'/Old playable link','Old playable link',2);
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title)
			VALUES(4,NULL,'/Retired link','Retired link'),
				(5,7,'/Synthetic identity','Synthetic identity');`); err != nil {
		t.Fatal(err)
	}
	server := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", CacheRoot: t.TempDir(), DataRoot: t.TempDir()})
	defer func() { _ = server.Shutdown(context.Background()) }()
	createTestAdministrator(t, server, "synthetic-user", "synthetic-password")
	handler := server.Routes()
	admin := loginTestSession(t, handler, "synthetic-user", "synthetic-password")

	reviewRequest(t, handler, admin, http.MethodGet, "/api/source-catalog/1", "", http.StatusNotFound)
	reviewRequest(t, handler, admin, http.MethodPatch, "/api/source-catalog/1/user-state", `{"rating":4}`, http.StatusNotFound)
	var works int
	if err := db.QueryRow(`SELECT count(*) FROM work`).Scan(&works); err != nil {
		t.Fatal(err)
	}
	if works != 0 {
		t.Fatalf("hidden parent created %d works", works)
	}
	reviewRequest(t, handler, admin, http.MethodGet, "/api/source-catalog/5", "", http.StatusNotFound)
	reviewRequest(t, handler, admin, http.MethodPatch, "/api/source-catalog/5/user-state", `{"rating":3}`, http.StatusNotFound)
	reviewRequest(t, handler, admin, http.MethodGet, "/api/source-catalog/5/tracks", "", http.StatusNotFound)
	reviewRequest(t, handler, admin, http.MethodGet, "/api/source-catalog/2", "", http.StatusOK)
	alias := reviewRequest(t, handler, admin, http.MethodGet, "/api/source-catalog/3", "", http.StatusOK)
	if int64(alias["catalogEntryId"].(float64)) != 2 {
		t.Fatalf("old alias resolved to %#v, want the playable entry", alias)
	}
	reviewRequest(t, handler, admin, http.MethodGet, "/api/source-catalog/4", "", http.StatusOK)
}
