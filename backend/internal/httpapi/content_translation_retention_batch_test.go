package httpapi

import (
	"context"
	"strings"
	"testing"

	"github.com/yexca/kikoto/backend/internal/testfixture"
	"github.com/yexca/kikoto/backend/internal/translation"
)

func seedTranslationRetentionBacklog(t *testing.T, s *Server, count int) {
	t.Helper()
	if _, err := s.db.Exec(`INSERT INTO file_source(id,code,display_name,source_type)
		VALUES(1,'retention-example','Example Source','openlist');`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`WITH RECURSIVE n(i) AS (
		SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<?
	)
	INSERT INTO source_field_translation(cache_key,source_id,work_code,field,path,language,
		source_text,translated_text,model,template_version,updated_at)
	SELECT printf('example-cache-%05d',i),1,?,'directory',printf('Example Track %05d',i),'en',
		'Example original','Example translation','example','v1','2026-01-01 00:00:00' FROM n`,
		count, testfixture.WorkCodeAt(1)); err != nil {
		t.Fatal(err)
	}
}

func TestTranslationRetentionContinuesBudgetedBacklogWithoutNewWrites(t *testing.T) {
	s := openContentTranslationTestServer(t)
	s.db.SetMaxOpenConns(1)
	seedTranslationRetentionBacklog(t, s, 10051)
	cfg := translation.Config{Limits: translation.Limits{MaxEntries: 1}}
	// A maintenance tick stops after its bounded batch count. The next tick
	// must still drain the old backlog even without new writes or a revision.
	for _, want := range []int{51, 1} {
		if err := s.pruneContentTranslations(context.Background(), cfg); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := s.db.QueryRow(`SELECT count(*) FROM source_field_translation`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Fatalf("remaining cache rows=%d, want %d", count, want)
		}
	}
	var retained string
	if err := s.db.QueryRow(`SELECT cache_key FROM source_field_translation`).Scan(&retained); err != nil || retained != "example-cache-10051" {
		t.Fatalf("newest cache row=%q err=%v", retained, err)
	}
}

func TestTranslationRetentionFailedBatchReleasesSingleConnection(t *testing.T) {
	s := openContentTranslationTestServer(t)
	s.db.SetMaxOpenConns(1)
	seedTranslationRetentionBacklog(t, s, 501)
	if _, err := s.db.Exec(`CREATE TRIGGER synthetic_retention_failure
		BEFORE DELETE ON source_field_translation WHEN old.cache_key='example-cache-00002'
		BEGIN SELECT RAISE(ABORT,'synthetic retention interruption'); END`); err != nil {
		t.Fatal(err)
	}
	cfg := translation.Config{Limits: translation.Limits{MaxEntries: 1}}
	err := s.pruneContentTranslations(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "synthetic retention interruption") {
		t.Fatalf("failed batch error=%v", err)
	}
	var count int
	if err = s.db.QueryRow(`SELECT count(*) FROM source_field_translation`).Scan(&count); err != nil || count != 501 {
		t.Fatalf("rollback rows=%d err=%v, want 501", count, err)
	}
	if _, err = s.db.Exec(`DROP TRIGGER synthetic_retention_failure`); err != nil {
		t.Fatal(err)
	}
	if err = s.pruneContentTranslations(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow(`SELECT count(*) FROM source_field_translation`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("resumed rows=%d err=%v, want 1", count, err)
	}
}
