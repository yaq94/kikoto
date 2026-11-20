package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yexca/kikoto/backend/internal/storage"
)

func TestCatalogPublicationResumesBacklogWithinWriteBudget(t *testing.T) {
	checkCatalogPublicationBacklog(t, 4200, 60, 400)
}

func BenchmarkCatalogPublicationBacklog(b *testing.B) {
	for b.Loop() {
		checkCatalogPublicationBacklog(b, 42000, 600, 4000)
	}
}

func checkCatalogPublicationBacklog(t testing.TB, entries, series, translatedEntries int) {
	t.Helper()
	const owner = "synthetic:large-publication"
	ctx := context.WithValue(context.Background(), catalogIndexNoPacingKey{}, true)
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
	defer func() { _ = db.Close() }()
	execute := func(statement string, args ...any) {
		t.Helper()
		if _, err := db.Exec(statement, args...); err != nil {
			t.Fatal(err)
		}
	}
	execute(fmt.Sprintf(`
		INSERT INTO file_source(id,code,display_name,source_type) VALUES(7,'example_remote_a','Example Remote A','openlist');
		WITH RECURSIVE n(i) AS(SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<%d)
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,availability_status)
		SELECT i,7,'/Example Work '||i,'Example Work '||i,'available' FROM n;
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes)
		SELECT id,'track.mp3','Example Track','audio',128 FROM source_catalog_entry;
		WITH RECURSIVE n(i) AS(SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<%d)
		INSERT INTO catalog_series(id,title,source_id,source_key) SELECT i,'Example Series '||i,7,'example-series-'||i FROM n;
		INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence)
		SELECT id,id,1,'manual' FROM catalog_series
		UNION ALL SELECT id,id+%d,2,'manual' FROM catalog_series;
		INSERT INTO work(id,primary_code,title) VALUES(1,'RJ00000000','Example Work 1');
		UPDATE source_catalog_entry SET work_id=1 WHERE id=1;
		INSERT INTO media_item(id,work_id,kind,title,fingerprint)
		VALUES(1,1,'audio','Example Track','catalog:1:removed.mp3'),(2,1,'audio','Example Kept Track','catalog:1:track.mp3');
		INSERT INTO media_file_location(id,media_item_id,file_source_id,location_type,path,availability)
		VALUES(1,1,7,'catalog_stream','removed.mp3','available'),(2,2,7,'catalog_stream','track.mp3','available');`, entries, series, series))
	execute(`INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes)
		VALUES(1,'removed.mp3','Example Removed Track','audio',128);
		INSERT INTO source_catalog_entry_admission(catalog_entry_id,generation,files,audio,non_audio)
		SELECT id,last_seen_generation,1+(id=1),1+(id=1),0 FROM source_catalog_entry;
		INSERT INTO content_translation(cache_key,entity_kind,entity_id,field,language,source_text,translated_text,origin,source_id,browsable)
		VALUES('example-entry-old','entry',1,'title','zh-Hans','Example Work 1','示例旧标题','native',7,1),
			('example-entry-new','entry',1,'title','zh-Hans','Example Updated Work','示例新标题','native',7,0),
			('example-series-old','series',2,'title','zh-Hans','Example Series 2','示例旧系列','native',7,1),
			('example-series-new','series',2,'title','zh-Hans','Example Updated Series','示例新系列','native',7,0);`)
	execute(`INSERT INTO content_translation(cache_key,entity_kind,entity_id,field,language,
		source_text,translated_text,origin,source_id,browsable)
		SELECT 'example-stored:'||id,'entry',id,'title','zh-Hans',title,'示例标题 '||id,'native',7,1
		FROM source_catalog_entry WHERE id BETWEEN 2 AND ?`, translatedEntries+1)
	s := &Server{db: db}
	publicationCtx, op, err := s.beginCatalogPublication(ctx, 7, owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.execCatalogWrite(publicationCtx, `UPDATE source_catalog_entry
		SET availability_status=CASE WHEN id=1 THEN availability_status ELSE 'unavailable' END,
			title=CASE WHEN id=1 THEN 'Example Updated Work' ELSE title END
		WHERE file_source_id=7`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.execCatalogWrite(publicationCtx, `DELETE FROM source_catalog_file WHERE catalog_entry_id=1 AND relative_path='removed.mp3'`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.execCatalogWrite(publicationCtx, `UPDATE catalog_series SET title='Example Updated Series' WHERE id=2`); err != nil {
		t.Fatal(err)
	}
	var enqueued int
	if err = db.QueryRow(`SELECT count(*) FROM source_catalog_publication_entity WHERE operation_id=? AND media_prepared=0 AND translation_prepared=0`, op).Scan(&enqueued); err != nil || enqueued != entries+series {
		t.Fatalf("pending before-images=%d want=%d err=%v", enqueued, entries+series, err)
	}
	checkSnapshot := func(before bool) {
		t.Helper()
		wantTitle, wantMedia, wantTranslation := "Example Updated Work", "unavailable", "示例新标题"
		wantSeriesTranslation := "示例新系列"
		if before {
			wantTitle, wantMedia, wantTranslation = "Example Work 1", "available", "示例旧标题"
			wantSeriesTranslation = "示例旧系列"
		}
		var title, location, kept, translation string
		var identity int64
		if err := db.QueryRow(`SELECT title,work_id FROM published_source_catalog_entry WHERE id=1`).Scan(&title, &identity); err != nil || title != wantTitle || identity != 1 {
			t.Fatalf("published title=%q identity=%d err=%v", title, identity, err)
		}
		if err := db.QueryRow(`SELECT availability FROM published_media_file_location WHERE id=1`).Scan(&location); err != nil || location != wantMedia {
			t.Fatalf("published removed location=%q want=%q err=%v", location, wantMedia, err)
		}
		if err := db.QueryRow(`SELECT availability FROM published_media_file_location WHERE id=2`).Scan(&kept); err != nil || kept != "available" {
			t.Fatalf("published kept location=%q err=%v", kept, err)
		}
		if err := db.QueryRow(`SELECT translated_text FROM published_content_translation WHERE entity_kind='entry' AND entity_id=1 AND browsable=1`).Scan(&translation); err != nil || translation != wantTranslation {
			t.Fatalf("published translation=%q want=%q err=%v", translation, wantTranslation, err)
		}
		if err := db.QueryRow(`SELECT translated_text FROM published_content_translation WHERE entity_kind='series' AND entity_id=2 AND browsable=1`).Scan(&translation); err != nil || translation != wantSeriesTranslation {
			t.Fatalf("published series translation=%q want=%q err=%v", translation, wantSeriesTranslation, err)
		}
	}
	checkSnapshot(true)

	// Interrupt after preparation actually writes a media row, then reopen the
	// same database and verify that an invalid lease cannot publish the snapshot.
	interrupted := errors.New("synthetic interruption")
	midway := context.WithValue(publicationCtx, catalogPublicationResourceGuardKey{}, func() error {
		var status string
		if err := db.QueryRow(`SELECT availability FROM media_file_location WHERE id=1`).Scan(&status); err != nil {
			return err
		}
		if status == "unavailable" {
			return interrupted
		}
		return nil
	})
	if err = s.commitCatalogPublication(midway, op); !errors.Is(err, interrupted) {
		t.Fatalf("interrupted publication=%v", err)
	}
	checkSnapshot(true)
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s = &Server{db: db}
	publicationCtx, resumed, err := s.beginCatalogPublication(ctx, 7, owner)
	if err != nil || resumed != op {
		t.Fatalf("resumed publication=%d want=%d err=%v", resumed, op, err)
	}
	expired := errors.New("synthetic expired lease")
	invalidLease := context.WithValue(publicationCtx, catalogExclusionLeaseKey{}, func(*sql.Tx) error { return expired })
	if err = s.commitCatalogPublication(invalidLease, op); !errors.Is(err, expired) {
		t.Fatalf("expired lease=%v", err)
	}
	checkSnapshot(true)

	// Enforce a generous count of bounded writer passes rather than depending
	// on storage or scheduler timing. The published source has a mix of cached
	// translations and entries without any translations.
	var passes int
	maxPasses := (entries + series) / 64 * 7
	guarded := context.WithValue(publicationCtx, catalogPublicationResourceGuardKey{}, func() error {
		passes++
		if passes > maxPasses {
			var media, translated int
			if err := db.QueryRow(`SELECT sum(media_prepared),sum(translation_prepared)
				FROM source_catalog_publication_entity WHERE operation_id=?`, op).Scan(&media, &translated); err != nil {
				return err
			}
			return fmt.Errorf("write budget %d exceeded, prepared media=%d translation=%d", maxPasses, media, translated)
		}
		return nil
	})
	started := time.Now()
	if err = s.commitCatalogPublication(guarded, op); err != nil {
		t.Fatal(err)
	}
	t.Logf("resumed %d entries and %d series (%d with cached titles) in %d bounded writer passes (%s)", entries, series, translatedEntries, passes, time.Since(started))
	checkSnapshot(false)
	var pending int
	if err = db.QueryRow(`SELECT count(*) FROM source_catalog_publication WHERE id=? AND state='preparing'`, op).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("publication still preparing=%d err=%v", pending, err)
	}
	if _, _, err = s.beginCatalogPublication(ctx, 7, "synthetic:next-publication"); err != nil {
		t.Fatal(err)
	}
}
