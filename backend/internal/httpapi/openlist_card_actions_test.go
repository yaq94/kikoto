package httpapi

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/yexca/kikoto/backend/internal/buildinfo"
	"github.com/yexca/kikoto/backend/internal/config"
	"github.com/yexca/kikoto/backend/internal/storage"
	"github.com/yexca/kikoto/backend/internal/translation"
)

// A catalog card can join a list or take a listening mark only once the entry
// has a work. Registering that work must be idempotent and must not write a
// personal review; a review is still written only by the rating/note route.
func TestCatalogCardRegistrationIsIdempotentAndWritesNoReview(t *testing.T) {
	root := t.TempDir()
	filename := filepath.Join(root, "library.db")
	cfg := config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", DataRoot: filepath.Join(root, "data"), CacheRoot: filepath.Join(root, "cache")}
	if err := os.Mkdir(cfg.DataRoot, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err = storage.MigrateFS(db, completeMigrationFS(t), buildinfo.Version); err != nil {
		t.Fatal(err)
	}
	s := NewServer(db, cfg)
	app := httptest.NewServer(s.Routes())
	t.Cleanup(func() { app.Close(); _ = s.Shutdown(context.Background()) })
	createTestAdministrator(t, s, "synthetic-user", "synthetic-password")
	createAccountManagedTestUser(t, s, "synthetic-listener", "synthetic-password")
	metadataReviewExec(t, db, `INSERT INTO file_source(id,code,display_name,source_type) VALUES(7,'example_openlist','Example OpenList','openlist');
INSERT INTO file_source_endpoint(file_source_id,api_url,base_url) VALUES(7,'https://openlist.example.invalid','https://openlist.example.invalid');
INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,availability_status) VALUES(1,7,'/Numberless work','Numberless work','available');
INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES(1,'track.mp3','Track A','audio',128);`)
	admin := workReferenceLogin(t, app, "synthetic-user")
	listener := workReferenceLogin(t, app, "synthetic-listener")

	// A listener without sources:write still keeps the card's own record.
	workReferenceRequest(t, app, listener, "POST", "/api/source-catalog/1/work", "", 403, nil)

	var first map[string]float64
	workReferenceRequest(t, app, admin, "POST", "/api/source-catalog/1/work", "", 200, &first)
	if first["workId"] <= 0 {
		t.Fatalf("registration returned no work id: %#v", first)
	}
	var second map[string]float64
	workReferenceRequest(t, app, admin, "POST", "/api/source-catalog/1/work", "", 200, &second)
	if second["workId"] != first["workId"] {
		t.Fatalf("registration created a second work: %#v then %#v", first, second)
	}
	var reviews int
	if err = db.QueryRow(`SELECT COUNT(*) FROM user_work_state`).Scan(&reviews); err != nil {
		t.Fatal(err)
	}
	if reviews != 0 {
		t.Fatalf("registration wrote %d personal reviews", reviews)
	}
	var saved map[string]any
	workReferenceRequest(t, app, admin, "PATCH", "/api/source-catalog/1/user-state", `{"rating":4}`, 200, &saved)
	if saved["workId"] != first["workId"] {
		t.Fatalf("review used a different work: %#v", saved)
	}
}

func TestSharedTagTranslationReusesOneVocabularyEntry(t *testing.T) {
	db := openMigratedTestDB(t)
	s := NewServer(db, config.Config{Mode: config.ModeProduction})
	configKey := translation.ConfigKey(translation.Config{Model: translation.DefaultModel, DisableThinking: true})
	if _, err := db.Exec(`
		INSERT INTO content_translation(cache_key,entity_kind,entity_id,field,language,source_text,translated_text,origin,config_key,model,template_version)
		VALUES('tag-source','work',1,'tag','zh-Hans','耳かき','采耳','model',?,'m','t')`, configKey); err != nil {
		t.Fatal(err)
	}
	value, ok := s.sharedTagTranslation(context.Background(), "tag", "zh-Hans", "耳かき", configKey)
	if !ok || value != "采耳" {
		t.Fatalf("shared tag translation = %q, %v", value, ok)
	}
	// The memo answers the second call without a query; deleting the row proves
	// it, because a fresh read would now find nothing.
	if _, err := db.Exec(`DELETE FROM content_translation`); err != nil {
		t.Fatal(err)
	}
	if value, ok = s.sharedTagTranslation(context.Background(), "tag", "zh-Hans", "耳かき", configKey); !ok || value != "采耳" {
		t.Fatalf("memoized tag translation = %q, %v", value, ok)
	}
	if _, ok = s.sharedTagTranslation(context.Background(), "title", "zh-Hans", "耳かき", configKey); ok {
		t.Fatal("a title must not reuse a tag translation")
	}
}
