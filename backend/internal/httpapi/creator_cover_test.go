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

	"github.com/yexca/kikoto/backend/internal/config"
)

func writeCachedTestCover(t *testing.T, cacheRoot, code string) {
	t.Helper()
	path := filepath.Join(cacheRoot, "cover", filepath.FromSlash(coverAssetRelativePath(code, ".jpg")))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("cover"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func patchCreatorCover(t *testing.T, server *Server, path string, userID int64, code string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]string{"coverPrimaryCode": code})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPatch, path, strings.NewReader(string(body)))
	request = request.WithContext(context.WithValue(request.Context(), currentUserKey, currentUser{ID: userID, Permissions: []string{"library:read", "favorites:write"}}))
	response := httptest.NewRecorder()
	server.Routes().ServeHTTP(response, request)
	return response
}

func decodeCreatorCover(t *testing.T, response *httptest.ResponseRecorder) *creatorCover {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var payload struct {
		Cover *creatorCover `json:"cover"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	return payload.Cover
}

func TestCircleCoverFollowsTheChosenWorkUntilItIsRestored(t *testing.T) {
	db := openMigratedTestDB(t)
	cacheRoot := t.TempDir()
	for _, statement := range []string{
		"INSERT INTO user_account (id, username, role) VALUES (1, 'listener', 'user'), (2, 'other', 'user')",
		"INSERT INTO party (id, display_name) VALUES (1, 'Example Circle'), (2, 'Other Circle')",
		"INSERT INTO party_external_id (party_id, provider_id, id_type, external_id) SELECT 1, id, 'maker_id', 'RG00001' FROM metadata_provider WHERE code = 'dlsite'",
		"INSERT INTO party_external_id (party_id, provider_id, id_type, external_id) SELECT 2, id, 'maker_id', 'RG00002' FROM metadata_provider WHERE code = 'dlsite'",
		"INSERT INTO party_catalog_item (party_id, provider_id, primary_code, title, release_date) SELECT 1, id, 'RJ00000001', 'Older work', '2024-01-01' FROM metadata_provider WHERE code = 'dlsite'",
		"INSERT INTO party_catalog_item (party_id, provider_id, primary_code, title, release_date) SELECT 1, id, 'RJ00000002', 'Latest work', '2025-02-03' FROM metadata_provider WHERE code = 'dlsite'",
		"INSERT INTO party_catalog_item (party_id, provider_id, primary_code, title, release_date) SELECT 1, id, 'RJ00000003', 'Work without a cover', '2023-01-01' FROM metadata_provider WHERE code = 'dlsite'",
		"INSERT INTO party_catalog_item (party_id, provider_id, primary_code, title, release_date) SELECT 2, id, 'RJ00000004', 'Other work', '2025-03-01' FROM metadata_provider WHERE code = 'dlsite'",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	for _, code := range []string{"RJ00000001", "RJ00000002", "RJ00000004"} {
		writeCachedTestCover(t, cacheRoot, code)
	}
	server := NewServer(db, config.Config{CacheRoot: cacheRoot})
	const statePath = "/api/circles/RG00001/user-state"

	cover := decodeCreatorCover(t, patchCreatorCover(t, server, statePath, 1, "rj00000001"))
	if cover == nil || !cover.Custom || cover.PrimaryCode != "RJ00000001" || cover.CoverURL == "" {
		t.Fatalf("cover = %+v, want the chosen work", cover)
	}

	summaries, err := server.loadCircleSummaries(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	for index := range summaries {
		server.presentCircleCover(&summaries[index])
		if summaries[index].Cover != nil && summaries[index].Cover.Custom {
			t.Fatalf("another user's cover = %+v, want the latest work", summaries[index].Cover)
		}
	}

	for _, code := range []string{"RJ00000003", "RJ00000004", "RJ00000099", "not-a-code"} {
		if response := patchCreatorCover(t, server, statePath, 1, code); response.Code != http.StatusBadRequest {
			t.Fatalf("cover %s status = %d, want %d: %s", code, response.Code, http.StatusBadRequest, response.Body.String())
		}
	}

	if _, err := db.Exec("DELETE FROM party_catalog_item WHERE primary_code = 'RJ00000001'"); err != nil {
		t.Fatal(err)
	}
	summary, err := server.loadCircleSummary(context.Background(), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Cover == nil || summary.Cover.Custom || summary.Cover.PrimaryCode != "RJ00000002" {
		t.Fatalf("cover = %+v, want the latest work once the chosen work left the catalog", summary.Cover)
	}

	cover = decodeCreatorCover(t, patchCreatorCover(t, server, statePath, 1, "RJ00000002"))
	if cover == nil || !cover.Custom || cover.PrimaryCode != "RJ00000002" {
		t.Fatalf("cover = %+v, want the newly chosen work", cover)
	}
	cover = decodeCreatorCover(t, patchCreatorCover(t, server, statePath, 1, ""))
	if cover == nil || cover.Custom || cover.PrimaryCode != "RJ00000002" {
		t.Fatalf("cover = %+v, want the latest work after restoring the default", cover)
	}
}

func TestVoiceCoverFollowsTheChosenWorkUntilItIsRestored(t *testing.T) {
	db := openMigratedTestDB(t)
	for _, statement := range []string{
		"INSERT INTO user_account (id, username, role) VALUES (1, 'listener', 'user')",
		"INSERT INTO person (id, display_name) VALUES (1, 'Example Voice'), (2, 'Other Voice')",
		"INSERT INTO metadata_provider (id, code, display_name) VALUES (11, 'kikoeru_source_example_remote', 'Example Remote')",
		"INSERT INTO file_source (id, code, display_name, source_type, enabled) VALUES (11, 'example_remote', 'Example Remote', 'kikoeru_compatible', 1)",
		`INSERT INTO voice_catalog_item (id, person_id, primary_code, title, release_date, cover_url) VALUES
			(21, 1, 'RJ00000001', 'Older work', '2024-01-01', 'https://example.invalid/older.jpg'),
			(22, 1, 'RJ00000002', 'Latest work', '2025-02-03', 'https://example.invalid/latest.jpg'),
			(23, 1, 'RJ00000003', 'Work without a cover', '2023-01-01', ''),
			(24, 2, 'RJ00000004', 'Other work', '2025-03-01', 'https://example.invalid/other.jpg')`,
		"INSERT INTO voice_catalog_source (catalog_item_id, provider_id, remote_code, availability) VALUES (21, 11, 'RJ00000001', 'available'), (22, 11, 'RJ00000002', 'available'), (23, 11, 'RJ00000003', 'available'), (24, 11, 'RJ00000004', 'available')",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	server := NewServer(db, config.Config{CacheRoot: t.TempDir()})
	const statePath = "/api/voices/1/user-state"

	cover := decodeCreatorCover(t, patchCreatorCover(t, server, statePath, 1, "RJ00000001"))
	if cover == nil || !cover.Custom || cover.PrimaryCode != "RJ00000001" || cover.CoverURL == "" {
		t.Fatalf("cover = %+v, want the chosen work's catalog cover", cover)
	}

	for _, code := range []string{"RJ00000003", "RJ00000004", "RJ00000099"} {
		if response := patchCreatorCover(t, server, statePath, 1, code); response.Code != http.StatusBadRequest {
			t.Fatalf("cover %s status = %d, want %d: %s", code, response.Code, http.StatusBadRequest, response.Body.String())
		}
	}

	if _, err := db.Exec("DELETE FROM voice_catalog_item WHERE id = 21"); err != nil {
		t.Fatal(err)
	}
	summary, err := server.loadVoiceSummary(context.Background(), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Cover == nil || summary.Cover.Custom || summary.Cover.PrimaryCode != "RJ00000002" {
		t.Fatalf("cover = %+v, want the latest work once the chosen work left the catalog", summary.Cover)
	}

	cover = decodeCreatorCover(t, patchCreatorCover(t, server, statePath, 1, "RJ00000002"))
	if cover == nil || !cover.Custom || cover.PrimaryCode != "RJ00000002" {
		t.Fatalf("cover = %+v, want the newly chosen work", cover)
	}
	cover = decodeCreatorCover(t, patchCreatorCover(t, server, statePath, 1, ""))
	if cover == nil || cover.Custom || cover.PrimaryCode != "RJ00000002" {
		t.Fatalf("cover = %+v, want the latest work after restoring the default", cover)
	}
}
