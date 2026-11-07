package httpapi

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/yexca/kikoto/backend/internal/config"
	"github.com/yexca/kikoto/backend/internal/storage"
	"github.com/yexca/kikoto/backend/internal/translation"
)

func openPublicationRetryDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	image, err := migratedTestDatabaseImage()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "publication.db")
	if err = os.WriteFile(path, image, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, path
}

func TestCatalogPublicationRetryRefreshesLateTranslationsAfterRestart(t *testing.T) {
	db, path := openPublicationRetryDB(t)
	seedCatalogPlanSource(t, db)
	insertCatalogPlanEntry(t, db, 1, "Old source title", "")
	insertCatalogPlanEntry(t, db, 2, "旧文字示例", "")
	for _, id := range []int64{1, 2} {
		insertCatalogPlanAudio(t, db, id, "audio.mp3", "Example Track")
	}
	s := NewServer(db, config.Config{})
	createTestAdministrator(t, s, "synthetic-user", "synthetic-password")
	handler := s.Routes()
	cookie := loginTestSession(t, handler, "synthetic-user", "synthetic-password")
	cfg := mustTranslationConfig(t, s)
	ctx, op, err := s.beginCatalogPublication(context.Background(), 7, "synthetic:late-translation")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.execCatalogWrite(ctx, `UPDATE source_catalog_entry SET title=CASE id WHEN 1 THEN 'New source title' ELSE '新文字示例' END WHERE id IN (1,2)`); err != nil {
		t.Fatal(err)
	}
	metadataReviewExec(t, db, `CREATE TRIGGER synthetic_marker_failure BEFORE UPDATE OF state ON source_catalog_publication WHEN NEW.state='committed' BEGIN SELECT RAISE(ABORT,'synthetic marker failure'); END`)
	if err = s.commitCatalogPublication(ctx, op); err == nil {
		t.Fatal("injected marker failure ignored")
	}
	rows := []contentTranslationRow{
		{CacheKey: "late-old-model", EntityKind: "entry", EntityID: 1, Field: "title", Language: "zh-Hans", SourceText: "Old source title", TranslatedText: "旧文字示例", Origin: "model", ConfigKey: translation.ConfigKey(cfg)},
		{CacheKey: "late-new-model", EntityKind: "entry", EntityID: 1, Field: "title", Language: "zh-Hans", SourceText: "New source title", TranslatedText: "新文字示例", Origin: "model", ConfigKey: translation.ConfigKey(cfg)},
		{CacheKey: "late-old-native", EntityKind: "entry", EntityID: 2, Field: "title", Language: "zh-Hans", SourceText: "旧文字示例", TranslatedText: "旧文字示例", Origin: "native"},
		{CacheKey: "late-new-native", EntityKind: "entry", EntityID: 2, Field: "title", Language: "zh-Hans", SourceText: "新文字示例", TranslatedText: "新文字示例", Origin: "native"},
	}
	if _, err = s.saveContentTranslations(context.Background(), rows); err != nil {
		t.Fatal(err)
	}
	check := func(phase string, old, new int) {
		t.Helper()
		for _, tc := range []struct {
			q    string
			want int
		}{{"旧文", old}, {"旧文字示例", old}, {"新文", new}, {"新文字示例", new}} {
			view := reviewRequest(t, handler, cookie, http.MethodGet, "/api/remote-sources/7/works?q="+url.QueryEscape(tc.q)+"&lang=zh-cn", "", 200)
			if view["total"] != float64(tc.want) {
				t.Errorf("%s search %q total=%v want=%d", phase, tc.q, view["total"], tc.want)
			}
		}
	}
	check("failed marker", 2, 0)
	// Repeated failures can consume preparation without publishing. A later
	// upsert of a prepared row must invalidate its working scope again.
	if err = s.commitCatalogPublication(ctx, op); err == nil {
		t.Fatal("second injected marker failure ignored")
	}
	if _, err = s.saveContentTranslations(context.Background(), rows); err != nil {
		t.Fatal(err)
	}
	check("late upsert", 2, 0)
	metadataReviewExec(t, db, `UPDATE file_source SET enabled=0 WHERE id=7`)
	if _, _, err = s.loadSourceCatalogEntry(context.Background(), 1); err == nil {
		t.Fatal("publication restored disabled source")
	}
	metadataReviewExec(t, db, `UPDATE file_source SET enabled=1 WHERE id=7`)
	model := "synthetic-new-model"
	if _, err = translation.NewStore(db).Update(context.Background(), translation.Patch{Model: &model}); err != nil {
		t.Fatal(err)
	}
	check("configuration changed", 1, 0)
	newConfig := mustTranslationConfig(t, s)
	for i := range 2 {
		rows[i].CacheKey += ":new-config"
		rows[i].ConfigKey = translation.ConfigKey(newConfig)
	}
	if _, err = s.saveContentTranslations(context.Background(), rows[:2]); err != nil {
		t.Fatal(err)
	}
	check("current configuration", 2, 0)
	if err = s.Shutdown(context.Background()); err != nil {
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
	s = NewServer(db, config.Config{})
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	handler = s.Routes()
	ctx, resumed, err := s.beginCatalogPublication(context.Background(), 7, "synthetic:late-translation")
	if err != nil || resumed != op {
		t.Fatalf("resume=%d want=%d err=%v", resumed, op, err)
	}
	check("restart", 2, 0)
	metadataReviewExec(t, db, `DROP TRIGGER synthetic_marker_failure`)
	if err = s.commitCatalogPublication(ctx, op); err != nil {
		t.Fatal(err)
	}
	check("committed", 0, 2)
	if err = s.cleanupCatalogPublications(context.Background()); err != nil {
		t.Fatal(err)
	}
	check("repeated cleanup", 0, 2)
	if got := mustTranslationConfig(t, s); got.Model != model {
		t.Fatalf("publication rewound config to %q", got.Model)
	}
}

func TestCatalogPublicationRetryInvalidatesEarlierTranslationBatches(t *testing.T) {
	db := openMigratedTestDBWithProductionPool(t)
	seedCatalogPlanSource(t, db)
	insertCatalogPlanEntry(t, db, 1, "Original Example", "")
	insertCatalogPlanAudio(t, db, 1, "audio.mp3", "Example Track")
	s := NewServer(db, config.Config{})
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	cfg := mustTranslationConfig(t, s)
	rows := make([]contentTranslationRow, 130)
	for i := range rows {
		rows[i] = contentTranslationRow{CacheKey: fmt.Sprintf("synthetic-batch-%d", i), EntityKind: "entry", EntityID: 1, Field: "title", Language: "zh-Hans", SourceText: "Final Example", TranslatedText: "最终文字示例", Origin: "model", ConfigKey: translation.ConfigKey(cfg)}
	}
	if _, err := s.saveContentTranslations(context.Background(), rows); err != nil {
		t.Fatal(err)
	}
	ctx, op, err := s.beginCatalogPublication(context.Background(), 7, "synthetic:translation-batches")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.execCatalogWrite(ctx, `UPDATE source_catalog_entry SET title='Intermediate Example' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	// Fault injection at a real write-transaction boundary, after the first
	// keyset batch. Earlier rows must be revisited after an upsert/title edit.
	injected, lateInserted := false, false
	guarded := context.WithValue(ctx, catalogPublicationResourceGuardKey{}, func() error {
		var cursor int64
		var prepared bool
		if lateInserted {
			return nil
		}
		if injected {
			if err := db.QueryRow(`SELECT translation_prepared FROM source_catalog_publication_entity WHERE operation_id=? AND entity_kind='entry' AND entity_id=1`, op).Scan(&prepared); err != nil || !prepared {
				return err
			}
			// The previous transaction drained the queue. Arrive just before
			// the transaction which would otherwise publish the marker.
			lateInserted = true
			late := rows[0]
			late.CacheKey = "synthetic-marker-boundary"
			late.TranslatedText = "末刻文字示例"
			_, err := s.saveContentTranslations(context.Background(), []contentTranslationRow{late})
			return err
		}
		if err := db.QueryRow(`SELECT after_translation_id FROM source_catalog_publication_entity WHERE operation_id=? AND entity_kind='entry' AND entity_id=1`, op).Scan(&cursor); err != nil || cursor == 0 {
			return err
		}
		injected = true
		if _, err := s.saveContentTranslations(context.Background(), rows[:1]); err != nil {
			return err
		}
		_, err := s.execCatalogWrite(ctx, `UPDATE source_catalog_entry SET title='Final Example' WHERE id=1`)
		return err
	})
	// A database budget fails the test if preparation updates over 64 rows in
	// one transaction or the marker performs a corpus rewrite.
	metadataReviewExec(t, db, `CREATE TABLE synthetic_translation_budget(n INTEGER NOT NULL); INSERT INTO synthetic_translation_budget VALUES(0);
	 CREATE TRIGGER synthetic_reset_translation_budget AFTER UPDATE OF operation_id ON source_catalog_publication_writer BEGIN UPDATE synthetic_translation_budget SET n=0; END;
	 CREATE TRIGGER synthetic_translation_budget_row AFTER UPDATE OF source_id,browsable ON content_translation BEGIN UPDATE synthetic_translation_budget SET n=n+1; SELECT CASE WHEN (SELECT n FROM synthetic_translation_budget)>64 THEN RAISE(ABORT,'translation batch exceeded 64 rows') END; END;
	 CREATE TRIGGER synthetic_marker_budget BEFORE UPDATE OF state ON source_catalog_publication WHEN NEW.state='committed' BEGIN SELECT CASE WHEN (SELECT n FROM synthetic_translation_budget)<>0 THEN RAISE(ABORT,'marker rewrote translations') END; END;`)
	if err = s.commitCatalogPublication(guarded, op); err != nil {
		t.Fatal(err)
	}
	if !injected || !lateInserted {
		t.Fatal("did not interleave between batches and before marker")
	}
	for _, q := range []string{"最终", "最终文字示例", "末刻", "末刻文字示例"} {
		view := requestRemoteWorks(t, db, "q="+url.QueryEscape(q)+"&lang=zh-cn")
		if view.Total != 1 {
			t.Fatalf("search %q total=%d want=1", q, view.Total)
		}
	}
	var stale int
	if err = db.QueryRow(`SELECT count(*) FROM content_translation WHERE source_text='Final Example' AND browsable<>1`).Scan(&stale); err != nil || stale != 0 {
		t.Fatalf("earlier batch remained stale: %d err=%v", stale, err)
	}
}

func TestCatalogPublicationRetryRechecksAdmissionAfterConcurrentSourceWrite(t *testing.T) {
	db := openMigratedTestDBWithProductionPool(t)
	seedCatalogPlanSource(t, db)
	insertCatalogPlanEntry(t, db, 1, "Original Example", "")
	insertCatalogPlanAudio(t, db, 1, "audio.mp3", "Example Track")
	insertTranslatedContent(t, db, "entry", 1, "title", "zh-Hans", "Final Example", "最终文字示例", "model")
	metadataReviewExec(t, db, `UPDATE source_catalog_entry_admission SET updated_at='2000-01-01 00:00:00' WHERE catalog_entry_id=1`)
	s := &Server{db: db}
	ctx, op, err := s.beginCatalogPublication(context.Background(), 7, "synthetic:concurrent-admission")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.execCatalogWrite(ctx, `UPDATE source_catalog_entry SET title='Final Example' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	injected := false
	guarded := context.WithValue(ctx, catalogPublicationResourceGuardKey{}, func() error {
		if injected {
			return nil
		}
		var prepared bool
		if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM source_catalog_entry_admission_publication_before WHERE operation_id=?)`, op).Scan(&prepared); err != nil || !prepared {
			return err
		}
		injected = true
		_, err := s.execCatalogWrite(ctx, `DELETE FROM source_catalog_file WHERE catalog_entry_id=1`)
		return err
	})
	if err = s.commitCatalogPublication(guarded, op); err != nil {
		t.Fatal(err)
	}
	if !injected {
		t.Fatal("did not interleave after admission preparation")
	}
	for _, q := range []string{"最终", "最终文字示例"} {
		view := requestRemoteWorks(t, db, "q="+url.QueryEscape(q)+"&lang=zh-cn")
		if view.Total != 0 {
			t.Errorf("source write left stale admission: %q total=%d", q, view.Total)
		}
	}
}
