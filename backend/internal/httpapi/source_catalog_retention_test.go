package httpapi

import (
	"net/http"
	"testing"
)

func TestSourceCatalogCleanupRetainsEveryPersonalReference(t *testing.T) {
	for _, tc := range []struct{ name, insert string }{
		{"review", `INSERT INTO user_work_state(user_id,work_id,rating,note) VALUES(2,101,4,'Example protected note')`},
		{"favorite", `INSERT INTO favorite_list(id,user_id,name) VALUES(91,2,'Example list'); INSERT INTO favorite_list_item(list_id,work_id) VALUES(91,101)`},
		{"tag", `INSERT INTO user_tag(id,user_id,name) VALUES(91,2,'Example tag'); INSERT INTO user_work_tag(user_id,work_id,user_tag_id) VALUES(2,101,91)`},
		{"listening", `INSERT INTO user_listening_session(user_id,session_id,work_id,listened_seconds) VALUES(2,'example-session',101,10)`},
		{"media_progress", `INSERT INTO user_media_progress(user_id,media_item_id,position_seconds) VALUES(2,201,10)`},
		{"cursor", `INSERT INTO user_work_playback_cursor(user_id,work_id,media_item_id,position_seconds) VALUES(2,101,201,10)`},
		{"lyrics", `INSERT INTO user_media_lyrics_preference(user_id,audio_media_item_id,lyrics_media_item_id) VALUES(2,201,202)`},
		{"recommendation_event", `INSERT INTO recommendation_event(user_id,work_id,event_type) VALUES(2,101,'play')`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCatalogIndexFixture(t, http.NotFoundHandler())
			createAccountManagedTestUser(t, f.server, "synthetic-listener", "synthetic-password")
			other := loginTestSession(t, f.handler, "synthetic-listener", "synthetic-password")
			metadataReviewExec(t, f.server.db, `INSERT INTO work(id,primary_code,title) VALUES(101,'RJ00000001','Example protected work'),(102,'RJ00000002','Example disposable work');
			INSERT INTO media_item(id,work_id,kind,title) VALUES(201,101,'audio','Example audio'),(202,101,'text','Example lyrics');`+tc.insert)
			body := `{"workIds":[101,102],"confirm":true}`
			reviewRequest(t, f.handler, other, http.MethodPost, "/api/maintenance/unlinked-works/delete", body, 403)
			result := reviewRequest(t, f.handler, f.cookie, http.MethodPost, "/api/maintenance/unlinked-works/delete", body, 200)
			if result["deletedWorkCount"] != float64(1) {
				t.Fatalf("cleanup did not protect reference: %#v", result)
			}
			skipped := result["skipped"].([]any)
			if len(skipped) != 1 || skipped[0].(map[string]any)["reason"] != "personal_records" {
				t.Fatalf("missing safe cleanup explanation: %#v", result)
			}
			reviewRequest(t, f.handler, f.cookie, http.MethodGet, "/api/works/101?includeMedia=false", "", 200)
			if tc.name == "review" {
				assertReview(t, reviewRequest(t, f.handler, other, http.MethodGet, "/api/works/101?includeMedia=false", "", 200), float64(4), "Example protected note")
			}
		})
	}
}
