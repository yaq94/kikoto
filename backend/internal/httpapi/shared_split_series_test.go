package httpapi

import (
	"context"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/yexca/kikoto/backend/internal/config"
)

func TestSharedSplitSeriesKeepsEpisodeReviewsAndMembership(t *testing.T) {
	db := openMigratedTestDB(t)
	s := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	defer func() { _ = s.Shutdown(context.Background()) }()
	createTestAdministrator(t, s, "synthetic-user", "synthetic-password")
	metadataReviewExec(t, db, `INSERT INTO work(id,primary_code,title) VALUES(11,'','Episode A'),(12,'','Episode B'),(21,'','Series A'),(22,'','Series B');
	INSERT INTO file_source(id,code,display_name,source_type) VALUES(7,'example_remote_a','Example Remote A','openlist');
	INSERT INTO file_source_endpoint(file_source_id,api_url,base_url) VALUES(7,'https://source.example.invalid','https://source.example.invalid');
	INSERT INTO catalog_series(id,title,work_id) VALUES(1,'Series A',21),(2,'Series B',22);
	INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,work_id) VALUES(1,7,'/A','Episode A',11),(2,7,'/B','Episode B',12);
	INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence) VALUES(1,1,1,'manual'),(2,2,1,'manual');`)
	app := httptest.NewServer(s.Routes())
	defer app.Close()
	admin := workReferenceLogin(t, app, "synthetic-user")
	workReferenceRequest(t, app, admin, "PATCH", "/api/catalog-series/1/user-state", `{"rating":1}`, 200, nil)
	workReferenceRequest(t, app, admin, "PATCH", "/api/catalog-series/2/user-state", `{"note":"Original series note"}`, 200, nil)
	for _, id := range []string{"1", "2"} {
		workReferenceRequest(t, app, admin, "PATCH", "/api/source-catalog/"+id+"/user-state", `{"rating":3,"note":"Episode `+id+`"}`, 200, nil)
	}
	var link, split map[string]any
	workReferenceRequest(t, app, admin, "POST", "/api/shared-associations/previews", `{"left":{"kind":"series","id":1},"right":{"kind":"series","id":2}}`, 201, &link)
	linkURL := "/api/shared-associations/" + strconv.Itoa(int(link["id"].(float64)))
	workReferenceRequest(t, app, admin, "POST", linkURL+"/start", mustJSON(map[string]any{"version": link["version"]}), 200, &link)
	workReferenceRequest(t, app, admin, "PATCH", "/api/catalog-series/2/user-state", `{"rating":null,"note":""}`, 200, nil)
	workReferenceRequest(t, app, admin, "POST", linkURL+"/split-preview", "{}", 201, &split)
	if split["myRecordChange"] != "modified" {
		t.Fatal("explicit review clear was not treated as a modification")
	}
	url := "/api/shared-splits/" + strconv.Itoa(int(split["id"].(float64)))
	workReferenceRequest(t, app, admin, "POST", url+"/start", mustJSON(map[string]any{"version": split["version"]}), 200, &split)
	if split["status"] != "pending" {
		t.Fatal("clear allocation was selected without the owner")
	}
	workReferenceRequest(t, app, admin, "POST", url+"/decision", mustJSON(map[string]any{"version": split["version"], "allocation": "both"}), 200, &split)
	for _, id := range []string{"1", "2"} {
		var series map[string]any
		workReferenceRequest(t, app, admin, "GET", "/api/catalog-series/"+id, "", 200, &series)
		assertReview(t, series, nil, "")
		if len(series["episodes"].([]any)) != 1 {
			t.Fatal("series split changed member identity")
		}
		var episode map[string]any
		workReferenceRequest(t, app, admin, "GET", "/api/source-catalog/"+id, "", 200, &episode)
		assertReview(t, episode, float64(3), "Episode "+id)
	}
	workReferenceRequest(t, app, admin, "PATCH", "/api/catalog-series/2/user-state", `{"note":"Independent series"}`, 200, nil)
	var original map[string]any
	workReferenceRequest(t, app, admin, "GET", "/api/catalog-series/1", "", 200, &original)
	assertReview(t, original, nil, "")
}

// A series that keeps two episodes must retain both after a shared split, each
// with its own work, edition and media location. The split changes identity
// links, not the episode tree or the playback target.
func TestSharedSplitSeriesKeepsTwoEpisodesAndTheirPlaybackEditions(t *testing.T) {
	db := openMigratedTestDB(t)
	s := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	defer func() { _ = s.Shutdown(context.Background()) }()
	createTestAdministrator(t, s, "synthetic-user", "synthetic-password")
	metadataReviewExec(t, db, `INSERT INTO work(id,primary_code,title) VALUES(11,'','Episode A'),(12,'','Episode B'),(13,'','Episode C'),(21,'','Series A'),(22,'','Series B');
	INSERT INTO logical_work(id,canonical_work_id,canonical_code) VALUES(101,11,'EXAMPLE-A'),(102,12,'EXAMPLE-B'),(103,13,'EXAMPLE-C'),(121,21,'SERIES-A'),(122,22,'SERIES-B');
	INSERT INTO work_edition(work_id,logical_work_id,primary_code,edition_label,is_canonical) VALUES
		(11,101,'','A edition',1),(12,102,'','B edition',1),(13,103,'','C edition',1),
		(21,121,'','Series A edition',1),(22,122,'','Series B edition',1);
	INSERT INTO file_source(id,code,display_name,source_type) VALUES(7,'example_remote_a','Example Remote A','openlist');
	INSERT INTO file_source_endpoint(file_source_id,api_url,base_url) VALUES(7,'https://source.example.invalid','https://source.example.invalid');
	INSERT INTO catalog_series(id,title,work_id) VALUES(1,'Series A',21),(2,'Series B',22);
	INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,work_id) VALUES
		(1,7,'/A','Episode A',11),(2,7,'/B','Episode B',12),(3,7,'/C','Episode C',13);
	INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence) VALUES
		(1,1,1,'manual'),(1,2,2,'manual'),(2,3,1,'manual');
	INSERT INTO media_item(id,work_id,kind,title) VALUES(1,11,'audio','A track'),(2,12,'audio','B track'),(3,13,'audio','C track');
	INSERT INTO media_file_location(id,media_item_id,file_source_id,location_type,path,stream_url,availability) VALUES
		(1,1,7,'catalog_stream','/A/a.mp3','/stream/a','available'),
		(2,2,7,'catalog_stream','/B/b.mp3','/stream/b','available'),
		(3,3,7,'catalog_stream','/C/c.mp3','/stream/c','available');`)
	app := httptest.NewServer(s.Routes())
	defer app.Close()
	admin := workReferenceLogin(t, app, "synthetic-user")
	var link, split map[string]any
	workReferenceRequest(t, app, admin, "POST", "/api/shared-associations/previews", `{"left":{"kind":"series","id":1},"right":{"kind":"series","id":2}}`, 201, &link)
	linkURL := "/api/shared-associations/" + strconv.Itoa(int(link["id"].(float64)))
	workReferenceRequest(t, app, admin, "POST", linkURL+"/start", mustJSON(map[string]any{"version": link["version"]}), 200, &link)
	workReferenceRequest(t, app, admin, "POST", linkURL+"/split-preview", "{}", 201, &split)
	url := "/api/shared-splits/" + strconv.Itoa(int(split["id"].(float64)))
	workReferenceRequest(t, app, admin, "POST", url+"/start", mustJSON(map[string]any{"version": split["version"]}), 200, &split)
	if split["status"] != "completed" {
		workReferenceRequest(t, app, admin, "POST", url+"/decision", mustJSON(map[string]any{"version": split["version"], "allocation": "both"}), 200, &split)
	}

	var series map[string]any
	workReferenceRequest(t, app, admin, "GET", "/api/catalog-series/1", "", 200, &series)
	episodes := series["episodes"].([]any)
	if len(episodes) != 2 {
		t.Fatalf("split dropped an episode: %#v", episodes)
	}
	if episodes[0].(map[string]any)["workId"] != float64(11) || episodes[1].(map[string]any)["workId"] != float64(12) {
		t.Fatalf("episode works changed: %#v", episodes)
	}
	var labels string
	if err := db.QueryRow(`SELECT group_concat(edition_label, '|') FROM (SELECT edition_label FROM work_edition WHERE work_id IN (11,12,13,21,22) ORDER BY work_id)`).Scan(&labels); err != nil {
		t.Fatal(err)
	}
	if labels != "A edition|B edition|C edition|Series A edition|Series B edition" {
		t.Fatalf("split rewrote editions: %s", labels)
	}
	var locations int
	if err := db.QueryRow(`SELECT count(*) FROM media_file_location WHERE id IN (1,2,3) AND location_type='catalog_stream' AND availability='available'`).Scan(&locations); err != nil || locations != 3 {
		t.Fatalf("split changed media locations: %d %v", locations, err)
	}
}
