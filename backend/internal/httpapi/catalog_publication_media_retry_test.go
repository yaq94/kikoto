package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"testing"

	"github.com/yexca/kikoto/backend/internal/storage"
)

func TestCatalogPublicationRemovedTrackKeepsIdentityAndProgressAcrossRetry(t *testing.T) {
	db, path := openPublicationRetryDB(t)
	tree := newCatalogSyncTestTree()
	tree.set("/", catalogSyncTestEntry{Name: "Example Work", IsDir: true})
	tree.set("/Example Work", catalogSyncTestEntry{Name: "old.mp3", Size: 1}, catalogSyncTestEntry{Name: "keep.mp3", Size: 1})
	f := newCatalogIndexFixtureWithDB(t, tree.handler(t), db)
	sourceID := f.sourceID(t)
	entryID := int64(1)
	metadataReviewExec(t, db, `INSERT INTO source_catalog_entry(id,file_source_id,root_path,title) VALUES(1,?,'/Example Work','Example Work')`, sourceID)
	insertCatalogPlanAudio(t, db, entryID, "old.mp3", "Example Old Track")
	insertCatalogPlanAudio(t, db, entryID, "keep.mp3", "Example Kept Track")
	base := "/api/source-catalog/" + strconv.FormatInt(entryID, 10)
	state := reviewRequest(t, f.handler, f.cookie, http.MethodPatch, base+"/user-state", `{"rating":4,"note":"Example retained note"}`, 200)
	workID := int64(state["workId"].(float64))
	old := reviewRequest(t, f.handler, f.cookie, http.MethodPost, base+"/playback", `{"path":"old.mp3"}`, 200)
	keep := reviewRequest(t, f.handler, f.cookie, http.MethodPost, base+"/playback", `{"path":"keep.mp3"}`, 200)
	oldLocation := int64(old["locationId"].(float64))
	keepLocation := int64(keep["locationId"].(float64))
	reviewRequest(t, f.handler, f.cookie, http.MethodPatch, fmt.Sprintf("/api/media-items/%.0f/progress", old["mediaItemId"]), fmt.Sprintf(`{"locationId":%d,"positionSeconds":42,"durationSeconds":180}`, oldLocation), 200)
	// Non-catalog storage of the same media item is independent of this source.
	metadataReviewExec(t, db, `INSERT INTO media_file_location(media_item_id,file_source_id,location_type,path,availability) VALUES(?,?,'cache','synthetic/old.mp3','available')`, int64(old["mediaItemId"].(float64)), sourceID)
	metadataReviewExec(t, db, `INSERT INTO file_source(id,code,display_name,source_type) VALUES(999,'synthetic_other','Example Other','openlist');
	 INSERT INTO media_file_location(media_item_id,file_source_id,location_type,path,availability) VALUES(?,999,'catalog_stream','other/old.mp3','available')`, int64(old["mediaItemId"].(float64)))
	ctx, op, err := f.server.beginCatalogPublication(context.Background(), sourceID, "synthetic:removed-track")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.server.execCatalogWrite(ctx, `DELETE FROM source_catalog_file WHERE catalog_entry_id=? AND relative_path='old.mp3'`, entryID); err != nil {
		t.Fatal(err)
	}
	check := func(phase, want string) {
		t.Helper()
		for id, expected := range map[int64]string{oldLocation: want, keepLocation: "available"} {
			target, _, err := f.server.loadMediaStreamTarget(context.Background(), id)
			if err != nil || target.Availability != expected {
				t.Errorf("%s location %d availability=%s want=%s err=%v", phase, id, target.Availability, expected, err)
			}
		}
		detail := reviewRequest(t, f.handler, f.cookie, http.MethodGet, base, "", 200)
		assertReview(t, detail, float64(4), "Example retained note")
		if detail["workId"] != float64(workID) {
			t.Fatal("publication replaced stable work identity")
		}
		cursor := reviewRequest(t, f.handler, f.cookie, http.MethodGet, fmt.Sprintf("/api/works/%d/playback-cursor", workID), "", 200)["cursor"].(map[string]any)
		if cursor["mediaItemId"] != old["mediaItemId"] || cursor["positionSeconds"] != float64(42) || cursor["locationId"] != old["locationId"] {
			t.Fatalf("%s changed personal playback: %#v", phase, cursor)
		}
		var cache string
		if err := db.QueryRow(`SELECT availability FROM media_file_location WHERE media_item_id=? AND location_type='cache'`, int64(old["mediaItemId"].(float64))).Scan(&cache); err != nil || cache != "available" {
			t.Fatalf("independent cache availability=%s err=%v", cache, err)
		}
		if err := db.QueryRow(`SELECT availability FROM media_file_location WHERE media_item_id=? AND file_source_id=999`, int64(old["mediaItemId"].(float64))).Scan(&cache); err != nil || cache != "available" {
			t.Fatalf("other source availability=%s err=%v", cache, err)
		}
	}
	check("preparing", "available")
	reviewRequest(t, f.handler, f.cookie, http.MethodPost, base+"/playback", `{"path":"old.mp3"}`, 200)
	metadataReviewExec(t, db, `CREATE TRIGGER synthetic_marker_failure BEFORE UPDATE OF state ON source_catalog_publication WHEN NEW.state='committed' BEGIN SELECT RAISE(ABORT,'synthetic marker failure'); END`)
	if err = f.server.commitCatalogPublication(ctx, op); err == nil {
		t.Fatal("injected marker failure ignored")
	}
	check("failed marker", "available")
	if err = f.server.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	f.server = NewServer(db, f.server.cfg)
	t.Cleanup(func() { _ = f.server.Shutdown(context.Background()) })
	f.handler = f.server.Routes()
	ctx, resumed, err := f.server.beginCatalogPublication(context.Background(), sourceID, "synthetic:removed-track")
	if err != nil || resumed != op {
		t.Fatalf("resume=%d want=%d err=%v", resumed, op, err)
	}
	check("restart", "available")
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err = f.server.commitCatalogPublication(canceled, op); err == nil {
		t.Fatal("canceled publication succeeded")
	}
	check("canceled", "available")
	metadataReviewExec(t, db, `DROP TRIGGER synthetic_marker_failure`)
	if err = f.server.commitCatalogPublication(ctx, op); err != nil {
		t.Fatal(err)
	}
	check("committed", "unavailable")
	reviewRequest(t, f.handler, f.cookie, http.MethodPost, base+"/playback", `{"path":"old.mp3"}`, 404)
	reviewRequest(t, f.handler, f.cookie, http.MethodPost, base+"/playback", `{"path":"keep.mp3"}`, 200)
	files, err := f.server.loadSourceCatalogFiles(context.Background(), entryID)
	if err != nil || len(files) != 1 || files[0].Path != "keep.mp3" {
		t.Fatalf("published files=%+v err=%v", files, err)
	}
	ctx, op, err = f.server.beginCatalogPublication(context.Background(), sourceID, "synthetic:restore-track")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.server.execCatalogWrite(ctx, `INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes,observed_generation) SELECT id,'old.mp3','Example Track','audio',1,last_seen_generation FROM source_catalog_entry WHERE id=?`, entryID); err != nil {
		t.Fatal(err)
	}
	check("restore preparing", "unavailable")
	if err = f.server.commitCatalogPublication(ctx, op); err != nil {
		t.Fatal(err)
	}
	check("restored", "available")
	again := reviewRequest(t, f.handler, f.cookie, http.MethodPost, base+"/playback", `{"path":"old.mp3"}`, 200)
	if again["mediaItemId"] != old["mediaItemId"] || again["locationId"] != old["locationId"] {
		t.Fatal("restoring a track replaced its media identity")
	}
}

func TestCatalogPublicationMediaPreservesExplicitOfflineState(t *testing.T) {
	db := openMigratedTestDBWithProductionPool(t)
	seedCatalogPlanSource(t, db)
	insertCatalogPlanEntry(t, db, 1, "Example Work", "")
	insertCatalogPlanAudio(t, db, 1, "old.mp3", "Example Track")
	metadataReviewExec(t, db, `INSERT INTO work(id,primary_code,title) VALUES(1,'RJ00000000','Example Work');
	 UPDATE source_catalog_entry SET work_id=1 WHERE id=1;
	 INSERT INTO media_item(id,work_id,kind,title,fingerprint) VALUES(1,1,'audio','Example Track','catalog:1:old.mp3');
	 INSERT INTO media_file_location(id,media_item_id,file_source_id,location_type,path,availability) VALUES(1,1,7,'catalog_stream','old.mp3','unavailable')`)
	s := &Server{db: db}
	ctx, op, err := s.beginCatalogPublication(context.Background(), 7, "synthetic:offline")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.execCatalogWrite(ctx, `UPDATE source_catalog_entry SET availability_status='unavailable' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if err = s.commitCatalogPublication(ctx, op); err != nil {
		t.Fatal(err)
	}
	target, _, err := s.loadMediaStreamTarget(context.Background(), 1)
	if err != nil || target.Availability != "unavailable" {
		t.Fatalf("offline location became available: %+v err=%v", target, err)
	}
}
