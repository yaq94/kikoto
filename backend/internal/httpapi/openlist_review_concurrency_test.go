package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/yexca/kikoto/backend/internal/config"
)

// A catalog rescan can rename an entry or move it to another source before the
// review transaction obtains its write lock. The review must resolve the entry
// after the lock, not reuse the pre-transaction snapshot.
func TestSourceCatalogReviewUsesCurrentEntryAfterConcurrentRename(t *testing.T) {
	db := openMigratedTestDB(t)
	metadataReviewExec(t, db, `INSERT INTO file_source(id,code,display_name,source_type) VALUES
		(7,'review-source-old','Review Source Old','openlist'),
		(8,'review-source-new','Review Source New','openlist');
		INSERT INTO file_source_endpoint(file_source_id,api_url,base_url) VALUES
		(7,'https://old.example.invalid','https://old.example.invalid'),
		(8,'https://new.example.invalid','https://new.example.invalid');
		INSERT INTO work(id,primary_code,title) VALUES
		(11,'RJ00000011','Old work'),
		(42,'RJ00000042','Current work');
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,primary_code,title)
		VALUES(1,7,'/Example','RJ00000011','Example');
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes)
		VALUES(1,'track.mp3','track.mp3','audio',100);`)
	server := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", CacheRoot: t.TempDir(), DataRoot: t.TempDir()})
	defer func() { _ = server.Shutdown(context.Background()) }()
	createTestAdministrator(t, server, "synthetic-user", "synthetic-password")
	handler := server.Routes()
	cookie := loginTestSession(t, handler, "synthetic-user", "synthetic-password")

	// The lock statement is a no-op UPDATE of id. A BEFORE trigger gives the
	// test a deterministic point between the retained load and the transaction,
	// exactly where a concurrent rescan can change identity and source.
	metadataReviewExec(t, db, `CREATE TRIGGER source_catalog_review_rename
		BEFORE UPDATE OF id ON source_catalog_entry
		WHEN OLD.id=1
		BEGIN
			UPDATE source_catalog_entry
			SET primary_code='RJ00000042',file_source_id=8
			WHERE id=OLD.id;
		END`)

	result := reviewRequest(t, handler, cookie, http.MethodPatch, "/api/source-catalog/1/user-state", `{"rating":4}`, 200)
	if int64(result["workId"].(float64)) != 42 {
		t.Fatalf("review work=%v, want the current code's work 42", result["workId"])
	}
	var code string
	var sourceID, workID int64
	if err := db.QueryRow(`SELECT primary_code,file_source_id,work_id FROM source_catalog_entry WHERE id=1`).Scan(&code, &sourceID, &workID); err != nil {
		t.Fatal(err)
	}
	if code != "RJ00000042" || sourceID != 8 || workID != 42 {
		t.Fatalf("entry code=%q source=%d work=%d, want the current identity", code, sourceID, workID)
	}
	var presenceSource, presenceWork int64
	var remoteID string
	if err := db.QueryRow(`SELECT file_source_id,work_id,remote_id FROM work_source_presence
		WHERE presence_type='source' AND work_id=42`).Scan(&presenceSource, &presenceWork, &remoteID); err != nil {
		t.Fatal(err)
	}
	if presenceSource != 8 || presenceWork != 42 || remoteID != "1" {
		t.Fatalf("presence source=%d work=%d remote=%q, want source 8 work 42", presenceSource, presenceWork, remoteID)
	}
}
