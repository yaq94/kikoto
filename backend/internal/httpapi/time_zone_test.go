package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yexca/kikoto/backend/internal/account"
	"github.com/yexca/kikoto/backend/internal/config"
)

func TestTimeZoneFollowsAccountThenDeviceThenServer(t *testing.T) {
	t.Setenv("TZ", "")
	db := openMigratedTestDB(t)
	if _, err := db.Exec(`INSERT INTO user_account (id, username, role) VALUES (1, 'synthetic-listener', 'user')`); err != nil {
		t.Fatal(err)
	}
	server := NewServer(db, config.Config{})
	as := func(request *http.Request, permissions ...string) *http.Request {
		return request.WithContext(context.WithValue(request.Context(), currentUserKey, account.User{ID: 1, Role: "user", Permissions: permissions}))
	}
	patchPreferences := func(body string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		server.updateUserPreferences(response, as(httptest.NewRequest(http.MethodPatch, "/api/auth/me/preferences", strings.NewReader(body))))
		return response
	}
	ownZone := func() *string {
		t.Helper()
		result, err := server.loadUserPreferences(httptest.NewRequest(http.MethodGet, "/api/auth/me/preferences", nil), 1)
		if err != nil {
			t.Fatal(err)
		}
		return result.TimeZone
	}
	// calendar reads the zone a report request counts days in.
	calendar := func(query string) (string, bool) {
		t.Helper()
		response := httptest.NewRecorder()
		server.getListeningCalendar(response, as(httptest.NewRequest(http.MethodGet, "/api/listening-calendar"+query, nil), "library:read"))
		var result struct {
			TimeZone      string `json:"timeZone"`
			FollowsDevice bool   `json:"followsDevice"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != http.StatusOK {
			t.Fatalf("calendar%s = %d %s, %v", query, response.Code, response.Body.String(), err)
		}
		return result.TimeZone, result.FollowsDevice
	}
	// playerZone reads the zone a player is told to bucket listening by.
	playerZone := func() any {
		t.Helper()
		response := httptest.NewRecorder()
		server.getListeningGeneration(response, as(httptest.NewRequest(http.MethodGet, "/api/listening-sessions", nil), "playback:use"))
		var lookup map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &lookup); err != nil {
			t.Fatalf("generation lookup = %s, %v", response.Body.String(), err)
		}
		// The same lookup tells a player that reports may split days by hour.
		if lookup["hours"] != true {
			t.Fatalf("generation lookup hours = %v", lookup["hours"])
		}
		zone, present := lookup["timeZone"]
		if !present {
			t.Fatalf("generation lookup has no timeZone: %s", response.Body.String())
		}
		return zone
	}

	// An account without a zone follows the device a request names.
	if zone, follows := calendar("?timeZone=Asia%2FTokyo"); ownZone() != nil || zone != "Asia/Tokyo" || !follows {
		t.Fatalf("device zone = %q, %v, own %v", zone, follows, ownZone())
	}
	if zone := playerZone(); zone != nil {
		t.Fatalf("player zone = %v", zone)
	}
	// A request that names no usable zone counts days in the server's.
	for _, query := range []string{"", "?timeZone=Mars%2FOlympus", "?timeZone=Local"} {
		if zone, follows := calendar(query); zone != "UTC" || !follows {
			t.Fatalf("calendar%s zone = %q, %v", query, zone, follows)
		}
	}
	t.Setenv("TZ", "America/New_York")
	if zone, _ := calendar(""); zone != "America/New_York" {
		t.Fatalf("server zone = %q", zone)
	}

	// The process-local pseudo zone is not a zone a client can count days in.
	for _, body := range []string{`{"timeZone":"Local"}`, `{"timeZone":"Mars/Olympus"}`, `{"timeZone":7}`} {
		if response := patchPreferences(body); response.Code != http.StatusBadRequest {
			t.Fatalf("PATCH %s = %d", body, response.Code)
		}
	}
	if response := patchPreferences(`{"timeZone":"Europe/Berlin"}`); response.Code != http.StatusOK {
		t.Fatalf("own zone = %d %s", response.Code, response.Body.String())
	}
	// A save that omits the zone keeps it.
	if response := patchPreferences(`{"recommendationThreshold":70}`); response.Code != http.StatusOK {
		t.Fatalf("other preference = %d %s", response.Code, response.Body.String())
	}
	// A fixed zone wins over the device.
	if zone, follows := calendar("?timeZone=Asia%2FTokyo"); ownZone() == nil || *ownZone() != "Europe/Berlin" || zone != "Europe/Berlin" || follows {
		t.Fatalf("own zone = %q, %v, own %v", zone, follows, ownZone())
	}
	if zone := playerZone(); zone != "Europe/Berlin" {
		t.Fatalf("player zone = %v", zone)
	}

	if response := patchPreferences(`{"timeZone":null}`); response.Code != http.StatusOK || ownZone() != nil {
		t.Fatalf("cleared own zone = %d %v", response.Code, ownZone())
	}
	if zone, follows := calendar("?timeZone=Asia%2FTokyo"); zone != "Asia/Tokyo" || !follows {
		t.Fatalf("cleared zone = %q, %v", zone, follows)
	}
}
