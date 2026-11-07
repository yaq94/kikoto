package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCatalogPublicationHidesNewCardsAndKeepsPersonalWrites(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/", catalogSyncTestEntry{Name: "Example Work", IsDir: true})
	tree.set("/Example Work", catalogSyncTestEntry{Name: "old.mp3", Size: 1})
	f := newCatalogSyncFixture(t, tree)
	f.runFullScan(t, "")
	var oldID int64
	if err := f.server.db.QueryRow(`SELECT id FROM source_catalog_entry WHERE root_path='/Example Work'`).Scan(&oldID); err != nil {
		t.Fatal(err)
	}
	ctx, op, err := f.server.beginCatalogPublication(context.Background(), f.sourceID(t), "synthetic:personal")
	if err != nil {
		t.Fatal(err)
	}
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, f.server.db)
	if err != nil {
		t.Fatal(err)
	}
	newID, err := persistOpenListEntryTx(ctx, tx.Tx, f.sourceID(t), "/New Work", "", "New Work")
	if err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES(?,'new.mp3','Example Track','audio',1)`, newID)
	}
	if err == nil {
		err = refreshCatalogEntryAdmissionTx(ctx, tx.Tx, newID)
	}
	if err == nil {
		err = setCatalogEntryStatusTx(ctx, tx.Tx, oldID, "missing")
	}
	if err == nil {
		err = tx.Commit()
	}
	release()
	if err != nil {
		t.Fatal(err)
	}
	list := f.request(t, http.MethodGet, "/works", "", 200)
	if list["total"] != float64(1) {
		t.Fatalf("staged insert leaked: %#v", list)
	}
	reviewRequest(t, f.handler, f.cookie, http.MethodGet, "/api/source-catalog/"+strconv.FormatInt(newID, 10), "", 404)
	base := "/api/source-catalog/" + strconv.FormatInt(oldID, 10)
	reviewRequest(t, f.handler, f.cookie, http.MethodPatch, base+"/user-state", `{"rating":4,"note":"Example private review"}`, 200)
	entry, _, err := f.server.loadRetainedSourceCatalogEntry(context.Background(), oldID)
	if err != nil {
		t.Fatal(err)
	}
	targets, err := f.server.prepareCatalogPlaybackTargets(context.Background(), entry, []string{"old.mp3"})
	if err != nil || len(targets) != 1 {
		t.Fatalf("first playback during publication: %+v %v", targets, err)
	}
	locationID := targets[0]["locationId"].(int64)
	beforeTarget, _, err := f.server.loadMediaStreamTarget(context.Background(), locationID)
	if err != nil || beforeTarget.Availability != "available" {
		t.Fatalf("published playback: %+v %v", beforeTarget, err)
	}
	if err = f.server.commitCatalogPublication(ctx, op); err != nil {
		t.Fatal(err)
	}
	afterTarget, _, err := f.server.loadMediaStreamTarget(context.Background(), locationID)
	if err != nil || afterTarget.Availability != "unavailable" {
		t.Fatalf("committed playback: %+v %v", afterTarget, err)
	}
	detail := reviewRequest(t, f.handler, f.cookie, http.MethodGet, base, "", 200)
	assertReview(t, detail, float64(4), "Example private review")
}

func TestCatalogPublicationKeepsOldTracksUntilCommit(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/", catalogSyncTestEntry{Name: "Example Work", IsDir: true})
	tree.set("/Example Work", catalogSyncTestEntry{Name: "old.mp3", Size: 1})
	f := newCatalogSyncFixture(t, tree)
	f.runFullScan(t, "")
	var id int64
	if err := f.server.db.QueryRow(`SELECT id FROM source_catalog_entry WHERE root_path='/Example Work'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	ctx, op, err := f.server.beginCatalogPublication(context.Background(), f.sourceID(t), "synthetic:publication")
	if err != nil {
		t.Fatal(err)
	}
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, f.server.db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_file SET relative_path='new.mp3',title='New track',size_bytes=2 WHERE catalog_entry_id=?`, id); err != nil {
		release()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		release()
		t.Fatal(err)
	}
	release()
	files, err := f.server.loadSourceCatalogFiles(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Path != "old.mp3" || files[0].Size != 1 {
		t.Fatalf("uncommitted tracks visible: %+v", files)
	}
	if err = f.server.commitCatalogPublication(ctx, op); err != nil {
		t.Fatal(err)
	}
	files, err = f.server.loadSourceCatalogFiles(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Path != "new.mp3" || files[0].Size != 2 {
		t.Fatalf("new tracks not published atomically: %+v", files)
	}
}

func TestCatalogPublicationLongHistoryKeepsBoundedFilePlan(t *testing.T) {
	db := openMigratedTestDB(t)
	seedCatalogPlanSource(t, db)
	insertCatalogPlanEntry(t, db, 1, "Example Work", "")
	insertCatalogPlanAudio(t, db, 1, "track.mp3", "Example Track")
	metadataReviewExec(t, db, `WITH RECURSIVE n(i) AS(SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<10000)
		INSERT INTO source_catalog_publication(file_source_id,owner,state) SELECT 7,'synthetic:'||i,'committed' FROM n`)
	s := &Server{db: db}
	ctx, op, err := s.beginCatalogPublication(context.Background(), 7, "synthetic:latest")
	if err != nil {
		t.Fatal(err)
	}
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_file SET title='New track' WHERE catalog_entry_id=1`); err != nil {
		release()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		release()
		t.Fatal(err)
	}
	release()
	query := publishedCatalogSQL(`SELECT relative_path,title FROM source_catalog_file WHERE catalog_entry_id=? AND relative_path>? ORDER BY relative_path LIMIT ?`)
	plan := explainCatalogPlan(t, db, query, 1, "", 64)
	if strings.Contains(plan, "SCAN op") || strings.Contains(plan, "SCAN source_catalog_publication") {
		t.Fatalf("history table scan: %s", plan)
	}
	if !strings.Contains(plan, "catalog_entry_id=?") {
		t.Fatalf("no bounded file-key lookup: %s", plan)
	}
	started := time.Now()
	for i := 0; i < 100; i++ {
		files, err := s.loadSourceCatalogFiles(context.Background(), 1)
		if err != nil || len(files) != 1 || files[0].Title != "Example Track" {
			t.Fatalf("history read: %+v %v", files, err)
		}
	}
	t.Logf("10000 committed operations;100 reads=%s;plan=%s", time.Since(started), plan)
	if err = s.commitCatalogPublication(ctx, op); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogPublicationRejectsSharedKeyFromAnotherSource(t *testing.T) {
	db := openMigratedTestDB(t)
	seedCatalogPlanSource(t, db)
	metadataReviewExec(t, db, `INSERT INTO file_source(id,code,display_name,source_type) VALUES(8,'example_remote_b','Example Remote B','openlist'); INSERT INTO catalog_series(id,title) VALUES(1,'Example Series')`)
	s := &Server{db: db}
	first, _, err := s.beginCatalogPublication(context.Background(), 7, "synthetic:first")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := s.beginCatalogPublication(context.Background(), 8, "synthetic:second")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.execCatalogWrite(first, `UPDATE catalog_series SET title='First title' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.execCatalogWrite(second, `UPDATE catalog_series SET title='Second title' WHERE id=1`); err == nil {
		t.Fatal("two source operations modified the same shared series key")
	}
	var title string
	if err = s.catalogRead(context.Background()).QueryRowContext(context.Background(), `SELECT title FROM catalog_series WHERE id=1`).Scan(&title); err != nil || title != "Example Series" {
		t.Fatalf("shared key publication=%q %v", title, err)
	}
}

func TestCatalogPublicationSearchKeepsOldTextUntilCommit(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/", catalogSyncTestEntry{Name: "Original Example", IsDir: true})
	tree.set("/Original Example", catalogSyncTestEntry{Name: "track.mp3", Size: 1})
	f := newCatalogSyncFixture(t, tree)
	f.runFullScan(t, "")
	ctx, op, err := f.server.beginCatalogPublication(context.Background(), f.sourceID(t), "synthetic:search")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.server.execCatalogWrite(ctx, `UPDATE source_catalog_entry SET title='Replacement Example' WHERE root_path='/Original Example'`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		q     string
		total float64
	}{{"Original", 1}, {"Replacement", 0}} {
		view := f.request(t, http.MethodGet, "/works?q="+tc.q, "", 200)
		if view["total"] != tc.total {
			t.Fatalf("uncommitted search %s: %#v", tc.q, view)
		}
	}
	if err = f.server.commitCatalogPublication(ctx, op); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		q     string
		total float64
	}{{"Original", 0}, {"Replacement", 1}} {
		view := f.request(t, http.MethodGet, "/works?q="+tc.q, "", 200)
		if view["total"] != tc.total {
			t.Fatalf("committed search %s: %#v", tc.q, view)
		}
	}
}

func TestCatalogPublicationSearchDocumentIDsDoNotReusePendingTombstones(t *testing.T) {
	db := openMigratedTestDB(t)
	seedCatalogPlanSource(t, db)
	metadataReviewExec(t, db, `INSERT INTO file_source(id,code,display_name,source_type) VALUES(8,'example_remote_b','Example Remote B','openlist'); INSERT INTO source_catalog_entry(id,file_source_id,root_path,title) VALUES(1,7,'/First','First'),(2,8,'/Second','Second'); INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES(1,'old.mp3','Original audio','audio',1)`)
	s := &Server{db: db}
	first, firstID, err := s.beginCatalogPublication(context.Background(), 7, "synthetic:first")
	if err != nil {
		t.Fatal(err)
	}
	second, secondID, err := s.beginCatalogPublication(context.Background(), 8, "synthetic:second")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.execCatalogWrite(first, `DELETE FROM source_catalog_file WHERE catalog_entry_id=1`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.execCatalogWrite(second, `INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES(2,'new.mp3','Replacement audio','audio',1)`); err != nil {
		t.Fatal(err)
	}
	if err = s.commitCatalogPublication(first, firstID); err != nil {
		t.Fatal(err)
	}
	var visible int
	if err = db.QueryRow(`SELECT count(*) FROM published_source_catalog_search_doc WHERE file_source_id=8 AND body='Replacement audio'`).Scan(&visible); err != nil || visible != 0 {
		t.Fatalf("other operation search document leaked: %d %v", visible, err)
	}
	if err = s.commitCatalogPublication(second, secondID); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM published_source_catalog_search_doc WHERE file_source_id=8 AND body='Replacement audio'`).Scan(&visible); err != nil || visible != 1 {
		t.Fatalf("committed search document missing: %d %v", visible, err)
	}
}

func TestCatalogPublicationSwitchesNativeAndModelSearchScopeTogether(t *testing.T) {
	db := openMigratedTestDB(t)
	seedCatalogPlanSource(t, db)
	insertCatalogPlanEntry(t, db, 1, "Original Example", "")
	insertCatalogPlanEntry(t, db, 2, "旧文字示例", "")
	insertCatalogPlanAudio(t, db, 1, "track.mp3", "Example Track")
	insertCatalogPlanAudio(t, db, 2, "track.mp3", "Example Track")
	insertTranslatedContent(t, db, "entry", 1, "title", "zh-Hans", "Original Example", "旧文字示例", "model")
	insertTranslatedContent(t, db, "entry", 2, "title", "zh-Hans", "旧文字示例", "旧文字示例", "native")
	s := &Server{db: db}
	ctx, op, err := s.beginCatalogPublication(context.Background(), 7, "synthetic:translations")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.execCatalogWrite(ctx, `UPDATE source_catalog_entry SET title=CASE id WHEN 1 THEN 'Replacement Example' ELSE '新文字示例' END WHERE id IN (1,2)`); err != nil {
		t.Fatal(err)
	}
	// A translator can cache future text while preparation is in progress; its
	// scope still comes from the published title until the operation commits.
	metadataReviewExec(t, db, `INSERT INTO content_translation(cache_key,entity_kind,entity_id,field,language,source_text,translated_text,origin,config_key,model,template_version)
	 SELECT cache_key||':future',entity_kind,entity_id,field,language,CASE entity_id WHEN 1 THEN 'Replacement Example' ELSE '新文字示例' END,'新文字示例',origin,config_key,model,template_version FROM content_translation WHERE field='title'`)
	check := func(old, new int) {
		t.Helper()
		for _, tc := range []struct {
			q    string
			want int
		}{{"旧文", old}, {"旧文字示例", old}, {"新文", new}, {"新文字示例", new}} {
			view := requestRemoteWorks(t, db, "q="+url.QueryEscape(tc.q)+"&lang=zh-cn")
			if view.Total != tc.want {
				t.Fatalf("search %q total=%d want=%d ids=%v", tc.q, view.Total, tc.want, remoteWorkIDs(view))
			}
		}
	}
	check(2, 0)
	if err = s.commitCatalogPublication(ctx, op); err != nil {
		t.Fatal(err)
	}
	check(0, 2)
}

func TestCatalogPublicationDoesNotFreezeSourceEnabledConfiguration(t *testing.T) {
	db := openMigratedTestDB(t)
	seedCatalogPlanSource(t, db)
	insertCatalogPlanEntry(t, db, 1, "Original Example", "")
	insertCatalogPlanAudio(t, db, 1, "track.mp3", "Example Track")
	insertTranslatedContent(t, db, "entry", 1, "title", "zh-Hans", "Original Example", "旧文字示例", "model")
	s := &Server{db: db}
	ctx, op, err := s.beginCatalogPublication(context.Background(), 7, "synthetic:enabled")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.execCatalogWrite(ctx, `UPDATE content_translation SET browsable=0 WHERE entity_id=1`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE file_source SET enabled=0 WHERE id=7`); err != nil {
		t.Fatal(err)
	}
	var enabled int
	if err = db.QueryRow(`SELECT count(*) FROM published_content_translation WHERE browsable=1`).Scan(&enabled); err != nil || enabled != 0 {
		t.Fatalf("disabled source remained browsable: %d %v", enabled, err)
	}
	if _, err = db.Exec(`UPDATE file_source SET enabled=1 WHERE id=7`); err != nil {
		t.Fatal(err)
	}
	if err = s.commitCatalogPublication(ctx, op); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogPublicationPinsReadersAndRejectsConflictingWriters(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/", catalogSyncTestEntry{Name: "Example Work", IsDir: true})
	tree.set("/Example Work", catalogSyncTestEntry{Name: "old.mp3", Size: 1})
	f := newCatalogSyncFixture(t, tree)
	f.runFullScan(t, "")
	f.server.db.SetMaxOpenConns(4)
	var entryID int64
	if err := f.server.db.QueryRow(`SELECT id FROM source_catalog_entry WHERE root_path='/Example Work'`).Scan(&entryID); err != nil {
		t.Fatal(err)
	}
	readCtx, done, err := f.server.beginCatalogRead(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	ctx, op, err := f.server.beginCatalogPublication(context.Background(), f.sourceID(t), "synthetic:reader")
	if err != nil {
		t.Fatal(err)
	}
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, f.server.db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM source_catalog_file WHERE catalog_entry_id=?`, entryID); err != nil {
		release()
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes,observed_generation) SELECT id,'new.mp3','Example Track','audio',2,last_seen_generation FROM source_catalog_entry WHERE id=?`, entryID); err != nil {
		release()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		release()
		t.Fatal(err)
	}
	release()
	if _, err = f.server.db.Exec(`UPDATE source_catalog_entry SET title='Conflicting title' WHERE id=?`, entryID); err == nil || !strings.Contains(err.Error(), "another operation") {
		t.Fatalf("conflicting writer not refused: %v", err)
	}
	if _, _, err = f.server.beginCatalogPublication(context.Background(), f.sourceID(t), "synthetic:other"); err == nil {
		t.Fatal("conflicting operation acquired same source")
	}
	metadataReviewExec(t, f.server.db, `CREATE TRIGGER synthetic_publication_fail BEFORE UPDATE OF state ON source_catalog_publication WHEN NEW.state='committed' BEGIN SELECT RAISE(ABORT,'synthetic marker failure'); END`)
	if err = f.server.commitCatalogPublication(ctx, op); err == nil {
		t.Fatal("faulted marker succeeded")
	}
	files, err := f.server.loadSourceCatalogFiles(context.Background(), entryID)
	if err != nil || len(files) != 1 || files[0].Path != "old.mp3" {
		t.Fatalf("failed marker exposed new files: %+v %v", files, err)
	}
	metadataReviewExec(t, f.server.db, `DROP TRIGGER synthetic_publication_fail`)
	if err = f.server.commitCatalogPublication(ctx, op); err != nil {
		t.Fatal(err)
	}
	files, err = f.server.loadSourceCatalogFiles(readCtx, entryID)
	if err != nil || len(files) != 1 || files[0].Path != "old.mp3" {
		t.Fatalf("pinned read changed: %+v %v", files, err)
	}
	done()
	files, err = f.server.loadSourceCatalogFiles(context.Background(), entryID)
	if err != nil || len(files) != 1 || files[0].Path != "new.mp3" {
		t.Fatalf("fresh read did not switch: %+v %v", files, err)
	}
}

func TestCatalogPublicationKeepsPlaybackAndLibraryPresenceUntilCommit(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/", catalogSyncTestEntry{Name: "Example Work", IsDir: true})
	tree.set("/Example Work", catalogSyncTestEntry{Name: "old.mp3", Size: 1})
	f := newCatalogSyncFixture(t, tree)
	f.runFullScan(t, "")
	var entryID int64
	if err := f.server.db.QueryRow(`SELECT id FROM source_catalog_entry WHERE root_path='/Example Work'`).Scan(&entryID); err != nil {
		t.Fatal(err)
	}
	sourceID := f.sourceID(t)
	metadataReviewExec(t, f.server.db, `INSERT INTO work(id,primary_code,title) VALUES(900,'RJ00000000','Example Work')`)
	metadataReviewExec(t, f.server.db, `UPDATE source_catalog_entry SET work_id=900 WHERE id=?`, entryID)
	metadataReviewExec(t, f.server.db, `INSERT INTO media_item(id,work_id,kind,title,fingerprint) VALUES(900,900,'audio','Example Track',?)`, fmt.Sprintf("catalog:%d:old.mp3", entryID))
	metadataReviewExec(t, f.server.db, `INSERT INTO media_file_location(id,media_item_id,file_source_id,location_type,path,availability) VALUES(900,900,?,'catalog_stream','old.mp3','available')`, sourceID)
	metadataReviewExec(t, f.server.db, `INSERT INTO work_source_presence(work_id,file_source_id,presence_type,availability) VALUES(900,?,'source','available')`, sourceID)
	ctx, op, err := f.server.beginCatalogPublication(context.Background(), sourceID, "synthetic:lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, f.server.db)
	if err != nil {
		t.Fatal(err)
	}
	if err = setCatalogEntryStatusTx(ctx, tx.Tx, entryID, "missing"); err != nil {
		release()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		release()
		t.Fatal(err)
	}
	release()
	assertState := func(want string) {
		t.Helper()
		wantPlayback := want
		if want == "missing" {
			wantPlayback = "unavailable"
		}
		target, _, err := f.server.loadMediaStreamTarget(context.Background(), 900)
		if err != nil || target.Availability != wantPlayback {
			var actual string
			_ = f.server.db.QueryRow(`SELECT availability FROM media_file_location WHERE id=900`).Scan(&actual)
			t.Logf("live media availability=%s", actual)
			t.Fatalf("playback availability=%s err=%v, want %s", target.Availability, err, want)
		}
		presence := f.server.sourcePresenceForWork(context.Background(), 900, "")
		if len(presence) != 1 || presence[0].Availability != want {
			t.Fatalf("library presence=%+v, want %s", presence, want)
		}
	}
	assertViews := func(available bool) {
		t.Helper()
		tags, _, err := f.server.workSourceStateByCode(context.Background(), "RJ00000000")
		found := false
		for _, tag := range tags {
			if tag.SourceID != nil && *tag.SourceID == sourceID {
				found = (tag.Status == "available") == available
			}
		}
		if err != nil || !found {
			t.Errorf("source tags=%+v err=%v, available=%v", tags, err, available)
		}
		if got := f.server.workHasSourcePresence(context.Background(), 900, sourceID, "source", "available"); got != available {
			t.Errorf("source availability flag=%v, want %v", got, available)
		}
		editions, err := f.server.loadMaterializedWorkTranslationsByCodes(context.Background(), []string{"RJ00000000"})
		if err != nil || len(editions) != 1 || editions[0].HasMedia != available {
			t.Errorf("edition availability=%+v err=%v, available=%v", editions, err, available)
		}
	}
	if err = f.server.prepareCatalogPublicationMedia(ctx, op); err != nil {
		t.Fatal(err)
	}
	assertState("available")
	assertViews(true)
	if err = f.server.commitCatalogPublication(ctx, op); err != nil {
		t.Fatal(err)
	}
	assertState("missing")
	assertViews(false)
}

func TestCatalogPublicationBoundsPlaybackProjectionUpdates(t *testing.T) {
	db := openMigratedTestDB(t)
	seedCatalogPlanSource(t, db)
	insertCatalogPlanEntry(t, db, 1, "Example Work", "")
	metadataReviewExec(t, db, `INSERT INTO work(id,primary_code,title) VALUES(1,'RJ00000000','Example Work'); UPDATE source_catalog_entry SET work_id=1 WHERE id=1;
	 WITH RECURSIVE n(i) AS(SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<150) INSERT INTO media_item(id,work_id,kind,title,fingerprint) SELECT i,1,'audio','Example Track','catalog:1:'||i||'.mp3' FROM n;
	 INSERT INTO media_file_location(media_item_id,file_source_id,location_type,path,availability) SELECT id,7,'catalog_stream',id||'.mp3','available' FROM media_item;
	 CREATE TABLE synthetic_playback_budget(n INTEGER NOT NULL); INSERT INTO synthetic_playback_budget VALUES(0);
	 CREATE TRIGGER synthetic_reset_playback_budget AFTER UPDATE OF operation_id ON source_catalog_publication_writer WHEN NEW.operation_id>0 BEGIN UPDATE synthetic_playback_budget SET n=0; END;
	 CREATE TRIGGER synthetic_playback_row_budget AFTER UPDATE ON media_file_location BEGIN UPDATE synthetic_playback_budget SET n=n+1; SELECT CASE WHEN (SELECT n FROM synthetic_playback_budget)>64 THEN RAISE(ABORT,'playback batch exceeded 64 rows') END; END;`)
	s := &Server{db: db}
	ctx, op, err := s.beginCatalogPublication(context.Background(), 7, "synthetic:many-playback")
	if err != nil {
		t.Fatal(err)
	}
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	err = setCatalogEntryStatusTx(ctx, tx.Tx, 1, "missing")
	if err == nil {
		err = tx.Commit()
	}
	release()
	if err != nil {
		t.Fatal(err)
	}
	if err = s.commitCatalogPublication(ctx, op); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = db.QueryRow(`SELECT count(*) FROM published_media_file_location WHERE availability='unavailable'`).Scan(&n); err != nil || n != 150 {
		t.Fatalf("playback projection=%d %v", n, err)
	}
}

func TestCatalogPublicationBoundsStaleFileDeletion(t *testing.T) {
	for _, phase := range []string{"lifecycle", "cleanup"} {
		t.Run(phase, func(t *testing.T) {
			audit := &catalogWriteAudit{}
			db := openCatalogWriteAuditedDB(t, audit)
			run, job := seedCursorCatalog(t, db, 7)
			metadataReviewExec(t, db, `INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,last_seen_generation) VALUES(1,7,'/Example/Part 0','Example Work',?)`, run)
			seedAdmissionRows(t, db, 1, "old", ".mp3", "audio", 150, 0)
			seedAdmissionRows(t, db, 1, "new", ".mp3", "audio", 1, run)
			s := &Server{db: db}
			ctx, op, err := s.beginCatalogPublication(context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true), 7, "synthetic:cleanup")
			if err != nil {
				t.Fatal(err)
			}
			audit.start()
			for i := 0; i < 20; i++ {
				if phase == "cleanup" {
					n, _, e := s.cleanupCatalogPublishBatch(ctx, catalogJobRecord(db, run, job), 7)
					err = e
					if err != nil || n == 0 {
						break
					}
				} else {
					pending, e := s.settleCatalogLifecycleBatch(ctx, catalogJobRecord(db, run, job), 7)
					err = e
					if err != nil || !pending {
						break
					}
				}
			}
			audit.stop()
			if err != nil {
				t.Fatal(err)
			}
			_, _, peakFiles, _ := audit.details()
			if peakFiles > 64 {
				t.Fatalf("%s deleted %d files in one transaction", phase, peakFiles)
			}
			if err = s.commitCatalogPublication(ctx, op); err != nil {
				t.Fatal(err)
			}
			files, err := s.loadSourceCatalogFiles(context.Background(), 1)
			if err != nil || len(files) != 1 {
				t.Fatalf("final set=%d %v", len(files), err)
			}
		})
	}
}

func TestCatalogPublicationRetirementKeepsOldPersonalReferenceReadable(t *testing.T) {
	tree := newCatalogSyncTestTree()
	tree.set("/", catalogSyncTestEntry{Name: "Example Work", IsDir: true})
	tree.set("/Example Work", catalogSyncTestEntry{Name: "old.mp3", Size: 1})
	f := newCatalogSyncFixture(t, tree)
	f.runFullScan(t, "")
	var id int64
	if err := f.server.db.QueryRow(`SELECT id FROM source_catalog_entry WHERE root_path='/Example Work'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	base := "/api/source-catalog/" + strconv.FormatInt(id, 10)
	reviewRequest(t, f.handler, f.cookie, http.MethodPatch, base+"/user-state", `{"rating":4,"note":"Example retained review"}`, 200)
	ctx, op, err := f.server.beginCatalogPublication(context.Background(), f.sourceID(t), "synthetic:retire")
	if err != nil {
		t.Fatal(err)
	}
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, f.server.db)
	if err != nil {
		t.Fatal(err)
	}
	target, err := persistOpenListEntryTx(ctx, tx.Tx, f.sourceID(t), "/Renamed Work", "", "Renamed Work")
	if err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES(?,'new.mp3','Example Track','audio',1)`, target)
	}
	if err == nil {
		_, err = retireCatalogCardTx(ctx, tx.Tx, id, target)
	}
	if err == nil {
		err = tx.Commit()
	}
	release()
	if err != nil {
		t.Fatal(err)
	}
	detail := reviewRequest(t, f.handler, f.cookie, http.MethodGet, base, "", 200)
	assertReview(t, detail, float64(4), "Example retained review")
	if detail["title"] != "Example Work" {
		t.Fatalf("pending retirement title=%v", detail["title"])
	}
	if err = f.server.commitCatalogPublication(ctx, op); err != nil {
		t.Fatal(err)
	}
	detail = reviewRequest(t, f.handler, f.cookie, http.MethodGet, base, "", 200)
	assertReview(t, detail, float64(4), "Example retained review")
}
