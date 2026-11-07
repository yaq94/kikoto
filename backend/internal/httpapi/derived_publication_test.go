package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/yexca/kikoto/backend/internal/config"
	"github.com/yexca/kikoto/backend/internal/storage"
)

func newDerivedPublicationServer(t *testing.T) (*Server, http.Handler, *http.Cookie) {
	t.Helper()
	db := openMigratedTestDB(t)
	seedCatalogPlanSource(t, db)
	insertCatalogPlanEntry(t, db, 1, "Example Work", "RJ00000000")
	insertCatalogPlanAudio(t, db, 1, "track.mp3", "Example Track")
	metadataReviewExec(t, db, `INSERT INTO work(id,primary_code,title) VALUES(1,'RJ00000000','Example Work');
	 UPDATE source_catalog_entry SET work_id=1 WHERE id=1;
	 INSERT INTO person(id,display_name) VALUES(1,'Example Voice');
	 INSERT INTO work_credit(work_id,person_id,role,source) VALUES(1,1,'voice_actor','test');
	 INSERT INTO party(id,display_name) VALUES(1,'Example Circle');
	 INSERT INTO party_external_id(party_id,provider_id,id_type,external_id) SELECT 1,id,'maker_id','RG00000000' FROM metadata_provider WHERE code='dlsite';
	 INSERT INTO work_party(work_id,party_id,role,source) VALUES(1,1,'circle','manual');
	 INSERT INTO party_catalog_item(party_id,provider_id,primary_code,title) SELECT 1,id,'RJ00000000','Example Work' FROM metadata_provider WHERE code='dlsite';
	 INSERT INTO party_series(id,party_id,provider_id,title_id,name,url) SELECT 1,1,id,'synthetic-series','Example Series','' FROM metadata_provider WHERE code='dlsite';
	 INSERT INTO party_series_work(series_id,primary_code) VALUES(1,'RJ00000000');
	 INSERT INTO media_item(id,work_id,kind,title,fingerprint) VALUES(1,1,'audio','Example Track','catalog:1:track.mp3'),(2,1,'text','Example Lyrics','synthetic-lyrics');
	 INSERT INTO file_source(id,code,display_name,source_type,enabled) VALUES(8,'synthetic-local','Example Local','local',1);
	 INSERT INTO media_file_location(id,media_item_id,file_source_id,location_type,path,availability) VALUES(1,1,7,'catalog_stream','track.mp3','available'),(2,2,8,'local','track.lrc','available');
	 INSERT INTO media_lyrics_assignment(audio_media_item_id,lyrics_media_item_id) VALUES(1,2);
	 INSERT INTO work_source_presence(work_id,file_source_id,presence_type,availability,last_checked_at) VALUES(1,7,'source','available','2000-01-01 00:00:00');`)
	s := NewServer(db, config.Config{})
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	createTestAdministrator(t, s, "synthetic-user", "synthetic-password")
	h := s.Routes()
	cookie := loginTestSession(t, h, "synthetic-user", "synthetic-password")
	return s, h, cookie
}

func stageDerivedPublicationMissing(t *testing.T, s *Server) (context.Context, int64) {
	t.Helper()
	ctx, op, err := s.beginCatalogPublication(context.Background(), 7, "synthetic:derived-readers")
	if err != nil {
		t.Fatal(err)
	}
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		t.Fatal(err)
	}
	if err = setCatalogEntryStatusTx(ctx, tx.Tx, 1, "missing"); err == nil {
		err = tx.Commit()
	}
	release()
	if err != nil {
		t.Fatal(err)
	}
	if err = s.prepareCatalogPublicationMedia(ctx, op); err != nil {
		t.Fatal(err)
	}
	return ctx, op
}

func TestDerivedPublicationVoiceSummaryUsesCommittedPresence(t *testing.T) {
	s, h, cookie := newDerivedPublicationServer(t)
	check := func(phase string, want float64) {
		t.Helper()
		page := reviewRequest(t, h, cookie, http.MethodGet, "/api/voices", "", 200)
		voice := page["voices"].([]any)[0].(map[string]any)
		if voice["remoteWorks"] != want {
			t.Errorf("%s voice remoteWorks=%v want=%v", phase, voice["remoteWorks"], want)
		}
		detail := reviewRequest(t, h, cookie, http.MethodGet, "/api/voices/1?includeWorks=false", "", 200)
		if detail["remoteWorks"] != want {
			t.Errorf("%s voice detail remoteWorks=%v want=%v", phase, detail["remoteWorks"], want)
		}
	}
	check("initial", 1)
	ctx, op := stageDerivedPublicationMissing(t, s)
	check("preparing", 1)
	if err := s.commitCatalogPublication(ctx, op); err != nil {
		t.Fatal(err)
	}
	check("committed", 0)
}

func TestDerivedPublicationCircleAndLyricsUseCommittedLocations(t *testing.T) {
	s, h, cookie := newDerivedPublicationServer(t)
	check := func(phase string, want float64) {
		t.Helper()
		page := reviewRequest(t, h, cookie, http.MethodGet, "/api/circles", "", 200)
		circle := page["circles"].([]any)[0].(map[string]any)
		if circle["remoteWorks"] != want {
			t.Errorf("%s circle remoteWorks=%v want=%v", phase, circle["remoteWorks"], want)
		}
		detail := reviewRequest(t, h, cookie, http.MethodGet, "/api/circles/RG00000000?pageSize=10&availability=remote", "", 200)
		if detail["worksMeta"].(map[string]any)["total"] != want {
			t.Errorf("%s filtered works=%v want=%v", phase, detail["worksMeta"], want)
		}
		series := detail["series"].([]any)[0].(map[string]any)
		if series["remoteWorks"] != want {
			t.Errorf("%s series remoteWorks=%v want=%v", phase, series["remoteWorks"], want)
		}
		lyrics, err := s.loadWorksWithLyrics(context.Background(), []int64{1})
		if err != nil || lyrics[1] != (want == 1) {
			t.Errorf("%s lyrics=%v err=%v", phase, lyrics, err)
		}
	}
	check("initial", 1)
	ctx, op := stageDerivedPublicationMissing(t, s)
	check("preparing", 1)
	if err := s.commitCatalogPublication(ctx, op); err != nil {
		t.Fatal(err)
	}
	check("committed", 0)
}

func TestDerivedPublicationUnlinkedDeletionKeepsPublishedAndPendingWork(t *testing.T) {
	for _, initial := range []string{"available", "missing"} {
		t.Run(initial, func(t *testing.T) {
			s, h, cookie := newDerivedPublicationServer(t)
			// Remove unrelated local lyrics so only the source projection
			// determines deletion eligibility. No personal state is seeded.
			metadataReviewExec(t, s.db, `DELETE FROM media_lyrics_assignment; DELETE FROM media_item WHERE id=2`)
			if initial == "missing" {
				metadataReviewExec(t, s.db, `UPDATE work_source_presence SET availability='missing'; UPDATE media_file_location SET availability='unavailable'`)
			}
			ctx, op := stageDerivedPublicationMissing(t, s)
			result := reviewRequest(t, h, cookie, http.MethodPost, "/api/maintenance/unlinked-works/delete", `{"workIds":[1],"confirm":true}`, 200)
			if result["deletedWorkCount"] != float64(0) {
				t.Fatalf("preparing publication lost work: %#v", result)
			}
			if err := s.commitCatalogPublication(ctx, op); err != nil {
				t.Fatal(err)
			}
			result = reviewRequest(t, h, cookie, http.MethodPost, "/api/maintenance/unlinked-works/delete", `{"workIds":[1],"confirm":true}`, 200)
			if result["deletedWorkCount"] != float64(1) {
				t.Fatalf("committed missing work not eligible: %#v", result)
			}
		})
	}
}

func TestDerivedPublicationSourceDisabledAndLocalLyricsRemainLive(t *testing.T) {
	s, h, cookie := newDerivedPublicationServer(t)
	ctx, op := stageDerivedPublicationMissing(t, s)
	metadataReviewExec(t, s.db, `UPDATE file_source SET enabled=0 WHERE id=7`)
	page := reviewRequest(t, h, cookie, http.MethodGet, "/api/circles", "", 200)
	circle := page["circles"].([]any)[0].(map[string]any)
	if circle["remoteWorks"] != float64(0) {
		t.Errorf("disabled source remoteWorks=%v", circle["remoteWorks"])
	}
	detail := reviewRequest(t, h, cookie, http.MethodGet, "/api/circles/RG00000000?availability=remote", "", 200)
	if detail["worksMeta"].(map[string]any)["total"] != float64(0) {
		t.Errorf("disabled source page=%v", detail["worksMeta"])
	}
	if detail["series"].([]any)[0].(map[string]any)["remoteWorks"] != float64(0) {
		t.Error("series restored disabled source")
	}
	lyrics, err := s.loadWorksWithLyrics(context.Background(), []int64{1})
	if err != nil || lyrics[1] {
		t.Fatalf("disabled source lyrics=%v err=%v", lyrics, err)
	}
	metadataReviewExec(t, s.db, `UPDATE file_source SET enabled=1 WHERE id=7; UPDATE media_file_location SET availability='unavailable' WHERE id=2`)
	lyrics, err = s.loadWorksWithLyrics(context.Background(), []int64{1})
	if err != nil || lyrics[1] {
		t.Fatalf("publication restored unavailable local lyrics=%v err=%v", lyrics, err)
	}
	metadataReviewExec(t, s.db, `UPDATE media_file_location SET availability='available' WHERE id=2`)
	lyrics, err = s.loadWorksWithLyrics(context.Background(), []int64{1})
	if err != nil || !lyrics[1] {
		t.Fatalf("published audio with live lyrics=%v err=%v", lyrics, err)
	}
	if err = s.commitCatalogPublication(ctx, op); err != nil {
		t.Fatal(err)
	}
}

func TestDerivedPublicationAvailabilityCheckedAtUsesCommittedPresence(t *testing.T) {
	s, h, cookie := newDerivedPublicationServer(t)
	path := "/api/works/RJ00000000/source-availability"
	before := reviewRequest(t, h, cookie, http.MethodGet, path, "", 200)
	ctx, op := stageDerivedPublicationMissing(t, s)
	during := reviewRequest(t, h, cookie, http.MethodGet, path, "", 200)
	if before["checkedAt"] != during["checkedAt"] {
		t.Errorf("unpublished check time leaked: %v -> %v", before["checkedAt"], during["checkedAt"])
	}
	if err := s.commitCatalogPublication(ctx, op); err != nil {
		t.Fatal(err)
	}
	after := reviewRequest(t, h, cookie, http.MethodGet, path, "", 200)
	if before["checkedAt"] == after["checkedAt"] {
		t.Errorf("committed check time unchanged: %v", after["checkedAt"])
	}
}

func TestDerivedPublicationAggregatesRetainPinnedSnapshotAcrossMarker(t *testing.T) {
	s, _, _ := newDerivedPublicationServer(t)
	s.db.SetMaxOpenConns(storage.FilePoolSize)
	ctx, op := stageDerivedPublicationMissing(t, s)
	readCtx, done, err := s.beginCatalogRead(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	if err = s.commitCatalogPublication(ctx, op); err != nil {
		t.Fatal(err)
	}
	counts, err := s.loadCircleAvailableWorkCounts(readCtx, []int64{1})
	if err != nil || counts[1] != 1 {
		t.Fatalf("pinned counts=%v err=%v", counts, err)
	}
	tags, err := s.workSourceTags(readCtx, 1, "RJ00000000")
	if err != nil || !sourceStatsContain(tags, "remote") {
		t.Fatalf("pinned tags=%v err=%v", tags, err)
	}
	lyrics, err := s.loadWorksWithLyrics(readCtx, []int64{1})
	if err != nil || !lyrics[1] {
		t.Fatalf("pinned lyrics=%v err=%v", lyrics, err)
	}
	codes, total, err := s.circleCatalogLogicalPage(readCtx, 1, circleWorksQuery{Page: 1, PageSize: 10, Availability: "remote"})
	if err != nil || total != 1 || len(codes) != 1 {
		t.Fatalf("pinned page=%v total=%d err=%v", codes, total, err)
	}
	done()
	lyrics, err = s.loadWorksWithLyrics(context.Background(), []int64{1})
	if err != nil || lyrics[1] {
		t.Fatalf("fresh lyrics=%v err=%v", lyrics, err)
	}
}
