package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yexca/kikoto/backend/internal/account"
	"github.com/yexca/kikoto/backend/internal/config"
	"github.com/yexca/kikoto/backend/internal/dlsite"
)

type previewDLsiteClient struct {
	product dlsite.Product
}

func (c previewDLsiteClient) FetchProduct(context.Context, string) (dlsite.Product, error) {
	return c.product, nil
}

func (previewDLsiteClient) DownloadCover(context.Context, dlsite.Product, string) (string, error) {
	return "", nil
}

func TestWorkMetadataPreviewDoesNotWriteProviderState(t *testing.T) {
	db := openMigratedTestDB(t)
	server := NewServer(db, config.Config{})
	result, err := db.Exec(`INSERT INTO work (primary_code, title, description) VALUES ('RJ00000000', 'Current title', 'Current description')`)
	if err != nil {
		t.Fatal(err)
	}
	workID, _ := result.LastInsertId()
	server.dlsiteClient = previewDLsiteClient{product: dlsite.Product{
		WorkNo: "RJ00000000", ProductName: "Fetched title", IntroShort: "Fetched description",
		RegistDate: "2026-10-11", MakerName: "Example Circle", MakerID: "RG00001",
		Genres: []dlsite.Genre{{Name: "Example genre"}}, Creators: dlsite.Creators{"voice_by": {{Name: "Example Voice"}}},
		LanguageEditions: []dlsite.LanguageEdition{{WorkNo: "RJ00000000", DisplayOrder: 1, Label: "日本語", Lang: "JPN"}},
	}}
	request := httptest.NewRequest(http.MethodPost, "/api/works/"+strconv.FormatInt(workID, 10)+"/metadata-preview", strings.NewReader(`{}`))
	request = request.WithContext(context.WithValue(request.Context(), currentUserKey, currentUser{ID: 1, Permissions: []string{"metadata:sync"}}))
	request.SetPathValue("id", strconv.FormatInt(workID, 10))
	response := httptest.NewRecorder()
	server.createWorkMetadataPreview(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("preview status = %d, body = %s", response.Code, response.Body.String())
	}
	var payload workMetadataPreviewResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Current.Title != "Current title" || payload.Fetched.Title != "Fetched title" || payload.Fetched.Circle != "Example Circle" {
		t.Fatalf("preview values = %+v", payload)
	}
	var snapshots int
	if err := db.QueryRow("SELECT COUNT(*) FROM metadata_snapshot WHERE work_id = ?", workID).Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	if snapshots != 0 {
		t.Fatalf("preview wrote %d snapshots", snapshots)
	}
}

func TestWorkMetadataPreviewApplyAcceptsProviderValueWithoutManualOverride(t *testing.T) {
	db := openMigratedTestDB(t)
	server := NewServer(db, config.Config{})
	result, err := db.Exec(`INSERT INTO work (primary_code, title) VALUES ('RJ00000001', 'Current title')`)
	if err != nil {
		t.Fatal(err)
	}
	workID, _ := result.LastInsertId()
	if _, err := db.Exec(`INSERT INTO user_account (id, username, display_name, role) VALUES (1, 'preview-user', 'Preview User', 'admin')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO work_manual_override(work_id, field_name, language, value_json) VALUES (?, 'title', '', '"Old manual title"')`, workID); err != nil {
		t.Fatal(err)
	}
	server.dlsiteClient = previewDLsiteClient{product: dlsite.Product{WorkNo: "RJ00000001", ProductName: "Fetched title"}}
	var baseUpdatedAt string
	if err := db.QueryRow("SELECT updated_at FROM work WHERE id = ?", workID).Scan(&baseUpdatedAt); err != nil {
		t.Fatal(err)
	}
	previewProduct := dlsite.Product{WorkNo: "RJ00000001", ProductName: "Fetched title"}
	previewID := server.storeMetadataPreview(metadataPreviewRecord{
		WorkID: workID, UserID: 1, PrimaryCode: "RJ00000001", BaseUpdatedAt: baseUpdatedAt,
		Product:   &previewProduct,
		CreatedAt: time.Now().UTC(),
	})
	body := `{"previewId":` + mustJSON(previewID) + `,"baseUpdatedAt":` + mustJSON(baseUpdatedAt) + `,"selections":{"title":"fetched","description":"manual","releaseDate":"manual","ageRating":"manual"},"values":{"title":"Fetched title","titleLanguage":"origin","description":"Authored description","releaseDate":"2026-10-11","ageRating":"general"}}`
	request := httptest.NewRequest(http.MethodPost, "/api/works/"+strconv.FormatInt(workID, 10)+"/metadata-preview/apply", strings.NewReader(body))
	request = request.WithContext(context.WithValue(request.Context(), currentUserKey, account.User{ID: 1, Permissions: []string{"library:write", "metadata:sync"}}))
	request.SetPathValue("id", strconv.FormatInt(workID, 10))
	response := httptest.NewRecorder()
	server.applyWorkMetadataPreview(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("apply status = %d, body = %s", response.Code, response.Body.String())
	}
	var title string
	if err := db.QueryRow("SELECT title FROM work WHERE id = ?", workID).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != "Fetched title" {
		t.Fatalf("stored title = %q", title)
	}
	var description, releaseDate, ageRating string
	if err := db.QueryRow(`SELECT
		(SELECT value_json FROM work_manual_override WHERE work_id = ? AND field_name = 'description'),
		(SELECT value_json FROM work_manual_override WHERE work_id = ? AND field_name = 'release_date'),
		(SELECT value_json FROM work_manual_override WHERE work_id = ? AND field_name = 'age_rating')`, workID, workID, workID).Scan(&description, &releaseDate, &ageRating); err != nil {
		t.Fatal(err)
	}
	if description != `"Authored description"` || releaseDate != `"2026-10-11"` || ageRating != `"general"` {
		t.Fatalf("scalar manual overrides = %q/%q/%q", description, releaseDate, ageRating)
	}
	var manualCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM work_manual_override WHERE work_id = ? AND field_name = 'title'", workID).Scan(&manualCount); err != nil {
		t.Fatal(err)
	}
	if manualCount != 0 {
		t.Fatalf("provider selection left %d manual title overrides", manualCount)
	}
}
