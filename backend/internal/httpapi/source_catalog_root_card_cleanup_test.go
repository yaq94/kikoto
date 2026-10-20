package httpapi

import (
	"net/http"
	"strconv"
	"testing"
)

// A card for the configured root publishes its direct files. A child work has
// its own card, so the parent must not keep the child's folder row as one of
// its own tracks; the "/" spelling needs the same rule as any other parent.
func TestSourceCatalogRootCardDropsChildWorkFolderRows(t *testing.T) {
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/":             {looseAudio("root track.mp3"), looseDir("Example Work")},
		"/Example Work": {looseAudio("child track.mp3")},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	cards := f.cards(t)
	if len(cards) != 2 || cards["Example Remote A"] == nil || cards["Example Work"] == nil {
		t.Fatalf("cards=%v, want the root card and the child work", cards)
	}
	rootID := int64(cards["Example Remote A"]["catalogEntryId"].(float64))
	body := reviewRequest(t, f.handler, f.cookie, http.MethodGet, "/api/source-catalog/"+strconv.FormatInt(rootID, 10)+"/tracks", "", 200)
	for _, raw := range body["tracks"].([]any) {
		row := raw.(map[string]any)
		if row["type"] == "folder" && row["title"] == "Example Work" {
			t.Fatalf("root card still lists the child work as a track: %#v", body["tracks"])
		}
	}
	var folderRows int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_file WHERE catalog_entry_id=? AND kind='folder'`, rootID).Scan(&folderRows); err != nil {
		t.Fatal(err)
	}
	if folderRows != 0 {
		t.Fatalf("root card kept %d folder row(s)", folderRows)
	}
}
