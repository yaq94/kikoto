package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yexca/kikoto/backend/internal/config"
)

func TestWorkReferencePersonalStateUsesCodeCompatibility(t *testing.T) {
	db := openMigratedTestDB(t)
	metadataReviewExec(t, db, `INSERT INTO work(id,primary_code,title) VALUES
		(1,'RJ00000000','Example Work'),(2,'RJ00000001','Example English edition');
		INSERT INTO logical_work(id,canonical_work_id,canonical_code) VALUES(1,1,'RJ00000000');
		INSERT INTO work_edition(work_id,logical_work_id,provider_id,primary_code,base_code,metadata_language,is_canonical) VALUES
		(1,1,2,'RJ00000000','','JPN',1),(2,1,2,'RJ00000001','RJ00000000','ENG',0);
		INSERT INTO work_code_alias(logical_work_id,provider_id,primary_code) VALUES(1,2,'RJ00000004')`)
	server := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user"})
	createTestAdministrator(t, server, "synthetic-user", "synthetic-password")
	httpServer := httptest.NewServer(server.Routes())
	t.Cleanup(httpServer.Close)
	cookie := workReferenceLogin(t, httpServer, "synthetic-user")
	for _, ref := range []string{"1", "rj00000000", "RJ00000004", "RJ00000001"} {
		var updated workUserStateResponse
		workReferenceRequest(t, httpServer, cookie, http.MethodPatch, "/api/works/"+ref+"/user-state", `{"listeningStatus":"finished"}`, http.StatusOK, &updated)
		if updated.WorkID != 1 || updated.ListeningStatus != "finished" {
			t.Fatalf("write %s changed identity or personal state: %+v", ref, updated)
		}
		var detail workDetail
		workReferenceRequest(t, httpServer, cookie, http.MethodGet, "/api/works/"+ref+"?includeMedia=false", "", http.StatusOK, &detail)
		if detail.ID != 1 || detail.ListeningStatus != "finished" {
			t.Fatalf("read %s changed identity or personal state: %+v", ref, detail)
		}
	}
	var updated workUserStateResponse
	workReferenceRequest(t, httpServer, cookie, http.MethodPatch, "/api/works/2/user-state", `{"listeningStatus":"listening"}`, http.StatusOK, &updated)
	if updated.WorkID != 2 {
		t.Fatalf("numeric edition write resolved to %d, want 2", updated.WorkID)
	}
	for _, item := range []struct {
		ref, status string
		id          int64
	}{{"2", "listening", 2}, {"RJ00000001", "finished", 1}} {
		var detail workDetail
		workReferenceRequest(t, httpServer, cookie, http.MethodGet, "/api/works/"+item.ref+"?includeMedia=false", "", http.StatusOK, &detail)
		if detail.ID != item.id || detail.ListeningStatus != item.status {
			t.Fatalf("edition read %s: id=%d status=%s", item.ref, detail.ID, detail.ListeningStatus)
		}
	}
	for _, item := range []struct {
		ref  string
		code int
	}{{"not-a-code", 400}, {"RJ00000099", 404}, {"999", 404}} {
		workReferenceRequest(t, httpServer, cookie, http.MethodPatch, "/api/works/"+item.ref+"/user-state", `{"listeningStatus":"finished"}`, item.code, nil)
	}
}

func workReferenceLogin(t *testing.T, server *httptest.Server, username string) *http.Cookie {
	t.Helper()
	response, err := server.Client().Post(server.URL+"/api/auth/login", "application/json", strings.NewReader(`{"username":"`+username+`","password":"synthetic-password"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d", response.StatusCode)
	}
	for _, cookie := range response.Cookies() {
		if cookie.Name == sessionCookieName {
			return cookie
		}
	}
	t.Fatal("login did not return a session")
	return nil
}

func workReferenceRequest(t *testing.T, server *httptest.Server, cookie *http.Cookie, method, path, body string, status int, target any) {
	t.Helper()
	request, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != status {
		t.Fatalf("%s %s: status=%d want=%d body=%s", method, path, response.StatusCode, status, data)
	}
	if target != nil {
		if err := json.Unmarshal(data, target); err != nil {
			t.Fatal(err)
		}
	}
}
