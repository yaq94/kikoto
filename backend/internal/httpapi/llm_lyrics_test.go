package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLLMLyricsPreviewAndPublicationPreserveOriginalAndTimeline(t *testing.T) {
	f := newLyricsFetchFixture(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("Example")) }, func(origin string) string { return origin + "/example.vtt" })
	if _, err := f.db.Exec("INSERT INTO user_account(username,role) VALUES('synthetic-admin','admin')"); err != nil {
		t.Fatal(err)
	}
	var userID, sourceID int64
	_ = f.db.QueryRow("SELECT id FROM user_account WHERE username='synthetic-admin'").Scan(&userID)
	_ = f.db.QueryRow("SELECT file_source_id FROM work_folder_location WHERE id=?", f.folderID).Scan(&sourceID)
	_, _ = f.db.Exec("UPDATE work SET age_rating='general' WHERE id=?", f.workID)
	original := "[00:01.00]Example original\n[00:02.00]Second line\n"
	relative := filepath.ToSlash(filepath.Join(f.root, "example.lrc"))
	absolute := filepath.Join(f.dataRoot, filepath.FromSlash(relative))
	if err := os.WriteFile(absolute, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	item, err := f.db.Exec("INSERT INTO media_item(work_id,kind,title,fingerprint) VALUES(?,'text','Example lyrics','synthetic-lyrics')", f.workID)
	if err != nil {
		t.Fatal(err)
	}
	itemID, _ := item.LastInsertId()
	location, err := f.db.Exec("INSERT INTO media_file_location(media_item_id,file_source_id,location_type,path,availability) VALUES(?,?,'local',?,'available')", itemID, sourceID, relative)
	if err != nil {
		t.Fatal(err)
	}
	locationID, _ := location.LastInsertId()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"lyrics\":[\"Example translation\",\"Translated line\"]}"}}]}`))
	}))
	defer upstream.Close()
	fixture := manualOverrideFixture{db: f.db, server: f.server, userID: userID, workID: f.workID}
	p := llmTestProvider(t, fixture, upstream.URL, "external", false)
	payload := llmTranslatePayload{ProviderID: p.ID, Target: "zh-cn", Fields: []string{"lyrics"}, LyricsLocationID: locationID, AudioMediaItemID: f.audioID, FolderID: f.folderID}
	body, _ := json.Marshal(payload)
	w := httptest.NewRecorder()
	f.server.translateWorkWithLLM(w, llmTestRequest(fixture, "POST", string(body)))
	if w.Code != 200 {
		t.Fatalf("translate %d %s", w.Code, w.Body.String())
	}
	var preview struct {
		ID string `json:"previewId"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	f.server.applyLLMTranslation(w, llmTestRequest(fixture, "PUT", `{"previewId":"`+preview.ID+`"}`))
	if w.Code != 200 {
		t.Fatalf("apply %d %s", w.Code, w.Body.String())
	}
	source, err := os.ReadFile(absolute)
	if err != nil || string(source) != original {
		t.Fatalf("original was replaced: %q %v", source, err)
	}
	var assetPath, status string
	var linkedAudio int64
	if err := f.db.QueryRow("SELECT relative_path,status,audio_media_item_id FROM llm_lyrics_asset WHERE id=?", preview.ID).Scan(&assetPath, &status, &linkedAudio); err != nil {
		t.Fatal(err)
	}
	translated, err := os.ReadFile(filepath.Join(f.dataRoot, filepath.FromSlash(assetPath)))
	if err != nil {
		t.Fatal(err)
	}
	if string(translated) != "[00:01.00]Example translation\n[00:02.00]Translated line\n" || linkedAudio != f.audioID || status != "accepted" {
		t.Fatalf("asset=%q status=%q audio=%d", translated, status, linkedAudio)
	}
	if err := f.server.publishLLMLyricsAsset(context.Background(), preview.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	_ = f.db.QueryRow("SELECT COUNT(*) FROM media_file_location WHERE path=?", assetPath).Scan(&count)
	if count != 1 {
		t.Fatalf("republication duplicated media: %d", count)
	}
	if err := f.server.indexLocalMediaForWork(context.Background(), f.workID, sourceID, f.root); err != nil {
		t.Fatal(err)
	}
	_ = f.db.QueryRow("SELECT COUNT(*) FROM media_file_location WHERE path=?", assetPath).Scan(&count)
	if count != 1 {
		t.Fatal("indexing duplicated the translated asset")
	}
	// Publication recovery uses the durable result, never the external endpoint.
	_, _ = f.db.Exec("UPDATE llm_lyrics_asset SET status='publishing' WHERE id=?", preview.ID)
	if err := os.WriteFile(filepath.Join(f.dataRoot, filepath.FromSlash(assetPath)), []byte("different"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.server.publishLLMLyricsAsset(context.Background(), preview.ID); err == nil {
		t.Fatal("publication accepted an unrelated existing file")
	}
	if strings.HasPrefix(assetPath, ".kikoto-staging") {
		t.Fatal("published asset left in staging")
	}
}
