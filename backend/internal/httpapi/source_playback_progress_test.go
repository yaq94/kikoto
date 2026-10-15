package httpapi

import (
	"net/http"
	"testing"
)

func TestSourcePlaybackCursorKeepsExactTrackAndUser(t *testing.T) {
	fixture := newRoleBoundaryFixture(t)
	if _, err := fixture.server.db.Exec(`
		INSERT INTO work (id,primary_code,title) VALUES (1,'RJ00000000','Example Work');
		INSERT INTO file_source (id,code,display_name,source_type) VALUES
		(1,'example_local','Local','local'), (2,'example_remote','Example Remote','kikoeru_compatible');
		INSERT INTO media_item (id,work_id,kind,title,fingerprint) VALUES
		(1,1,'audio','track.wav','synthetic-a'), (2,1,'audio','track.wav','synthetic-b');
		INSERT INTO media_file_location (id,media_item_id,file_source_id,location_type,path,availability) VALUES
		(1,1,1,'local','RJ00000000/track.wav','available'),
		(2,2,2,'remote_stream','track.wav','available');
	`); err != nil {
		t.Fatal(err)
	}
	cookie := fixture.cookies["user"]
	read := func() map[string]any {
		t.Helper()
		return reviewRequest(t, fixture.handler, cookie, http.MethodGet, "/api/works/1/playback-cursor", "", http.StatusOK)["cursor"].(map[string]any)
	}
	reviewRequest(t, fixture.handler, cookie, http.MethodPatch, "/api/media-items/1/progress", `{"locationId":1,"positionSeconds":37,"durationSeconds":180}`, http.StatusOK)
	if cursor := read(); cursor["mediaItemId"] != float64(1) || cursor["positionSeconds"] != float64(37) || cursor["fileSourceId"] != float64(1) {
		t.Fatalf("first track cursor = %#v", cursor)
	}
	// Reading another source does not move the cursor or assume its same-named track is equivalent.
	reviewRequest(t, fixture.handler, cookie, http.MethodGet, "/api/works/RJ00000000/source-availability", "", http.StatusOK)
	if cursor := read(); cursor["mediaItemId"] != float64(1) || cursor["positionSeconds"] != float64(37) {
		t.Fatalf("source browse changed cursor = %#v", cursor)
	}
	reviewRequest(t, fixture.handler, cookie, http.MethodPatch, "/api/media-items/2/progress", `{"locationId":2,"positionSeconds":0,"durationSeconds":240}`, http.StatusOK)
	if cursor := read(); cursor["mediaItemId"] != float64(2) || cursor["positionSeconds"] != float64(0) || cursor["fileSourceId"] != float64(2) {
		t.Fatalf("explicit second track cursor = %#v", cursor)
	}
	reviewRequest(t, fixture.handler, cookie, http.MethodPatch, "/api/media-items/2/progress", `{"locationId":2,"positionSeconds":48,"durationSeconds":240}`, http.StatusOK)
	reviewRequest(t, fixture.handler, cookie, http.MethodPatch, "/api/media-items/2/progress", `{"locationId":1,"positionSeconds":5}`, http.StatusBadRequest)
	if cursor := read(); cursor["locationId"] != float64(2) || cursor["positionSeconds"] != float64(48) {
		t.Fatalf("saved cursor = %#v", cursor)
	}
	if other := reviewRequest(t, fixture.handler, fixture.cookies["contributor"], http.MethodGet, "/api/works/1/playback-cursor", "", http.StatusOK); other["cursor"] != nil {
		t.Fatalf("other user sees cursor = %#v", other)
	}
}
