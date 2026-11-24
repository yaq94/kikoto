package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"

	"github.com/yexca/kikoto/backend/internal/testfixture"
)

func TestCatalogSeriesManualTitleTranslationSearch(t *testing.T) {
	s, host, admin, listener := translationHTTPFixture(t)
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/fs/list" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{
			"content": []any{map[string]any{"name": "Example track.mp3", "size": 12}}, "total": 1,
		}})
	}))
	t.Cleanup(upstream.Close)
	sourceIDs := []int{}
	entryIDs := []int{}
	for index, suffix := range []string{"A", "B", "C"} {
		root := "/Creator/Example " + suffix
		if index == 0 {
			root = "/Creator/" + testfixture.WorkCode(testfixture.PrefixRJ, 1) + " Example A"
		}
		source := translationRequest(t, host, admin, "POST", "/api/file-sources", mustJSON(map[string]any{
			"displayName": "Example Remote " + suffix, "sourceType": "openlist", "enabled": true,
			"config":   map[string]any{"openListRoots": []string{root}, "openListIntervalMs": 100},
			"endpoint": map[string]any{"apiUrl": upstream.URL, "baseUrl": upstream.URL},
		}), http.StatusCreated)
		sourceID := int(source["id"].(float64))
		sourceIDs = append(sourceIDs, sourceID)
		base := fmt.Sprintf("/api/remote-sources/%d", sourceID)
		translationRequest(t, host, admin, "POST", base+"/catalog/scan", "{}", http.StatusOK)
		cards := translationRequest(t, host, listener, "GET", base+"/works", "", http.StatusOK)
		if cards["total"] != float64(1) {
			t.Fatalf("source %s cards = %#v, want one independent entry", suffix, cards)
		}
		entryIDs = append(entryIDs, int(cards["works"].([]any)[0].(map[string]any)["catalogEntryId"].(float64)))
	}

	const title = "Example manual collection"
	preview := translationRequest(t, host, admin, "POST", "/api/catalog-series/previews", mustJSON(map[string]any{
		"title": title, "catalogEntryIds": entryIDs[:2],
	}), http.StatusCreated)
	confirmed := translationRequest(t, host, admin, "POST", fmt.Sprintf("/api/catalog-series/previews/%d/confirm", int(preview["previewId"].(float64))), "{}", http.StatusOK)
	seriesID := confirmed["seriesId"]

	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"示例人工合集"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(model.Close)
	configurePrefillModel(t, host, admin, model.URL)
	translated := translationRequest(t, host, listener, "POST", "/api/content-translations", mustJSON(map[string]any{
		"seriesId": seriesID, "field": "title", "text": title, "language": "zh-cn",
	}), http.StatusOK)
	if translated["text"] != "示例人工合集" {
		t.Fatalf("manual series translation = %#v", translated)
	}

	for _, sourceID := range sourceIDs[:2] {
		for _, query := range []string{"示例人工合集", "人工", "工"} {
			t.Run(fmt.Sprintf("source_%d/%s", sourceID, query), func(t *testing.T) {
				response := translationRequest(t, host, listener, "GET", fmt.Sprintf("/api/remote-sources/%d/works?q=%s&lang=zh-cn", sourceID, url.QueryEscape(query)), "", http.StatusOK)
				cards := response["works"].([]any)
				if response["total"] != float64(1) || len(cards) != 1 {
					t.Fatalf("manual series title search = %#v, want one shared series card", response)
				}
				card := cards[0].(map[string]any)
				if card["seriesId"] != seriesID || card["primaryCode"] != "" || card["workId"] != nil {
					t.Fatalf("manual series search changed identity or registered a work: %#v", card)
				}
			})
		}
	}
	for _, query := range []string{"示例人工合集", "人工", "工"} {
		assertManualSeriesSearch(t, host, listener, int64(sourceIDs[2]), query, "zh-cn")
	}
	assertManualSeriesSearch(t, host, listener, int64(sourceIDs[0]), testfixture.WorkCode(testfixture.PrefixRJ, 1), "zh-cn", int64(seriesID.(float64)))
	if library := translationRequest(t, host, listener, "GET", "/api/works", "", http.StatusOK); library["total"] != float64(0) {
		t.Fatalf("manual grouping or translated search registered works: %#v", library)
	}
}

func TestCatalogSeriesManualSearchTracksCurrentLanguageRows(t *testing.T) {
	s, host, admin, listener := translationHTTPFixture(t)
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	seedCatalogPlanSource(t, s.db)
	metadataReviewExec(t, s.db, `INSERT INTO file_source(id,code,display_name,source_type,enabled) VALUES
		(8,'example_remote_b','Example Remote B','openlist',1),(9,'example_remote_c','Example Remote C','openlist',1);
		INSERT INTO file_source_endpoint(file_source_id,base_url,api_url) VALUES
		(8,'https://source.example.invalid','https://source.example.invalid'),(9,'https://source.example.invalid','https://source.example.invalid');`)
	for id := int64(1); id <= 4; id++ {
		insertCatalogPlanEntry(t, s.db, id, fmt.Sprintf("Example episode %d", id), "")
		insertCatalogPlanAudio(t, s.db, id, "track.mp3", "Example track")
	}
	metadataReviewExec(t, s.db, `UPDATE source_catalog_entry SET file_source_id=8 WHERE id IN (2,4)`)
	confirm := func(title string, entries []int64) int64 {
		t.Helper()
		preview := translationRequest(t, host, admin, "POST", "/api/catalog-series/previews", mustJSON(map[string]any{
			"title": title, "catalogEntryIds": entries,
		}), http.StatusCreated)
		result := translationRequest(t, host, admin, "POST", fmt.Sprintf("/api/catalog-series/previews/%d/confirm", int(preview["previewId"].(float64))), "{}", http.StatusOK)
		return int64(result["seriesId"].(float64))
	}
	const original = "Example manual series"
	modelSeries := confirm(original, []int64{1, 2})
	const traditional = "示例龍語系列"
	nativeSeries := confirm(traditional, []int64{3, 4})
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"示例恋爱系列"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(model.Close)
	configurePrefillModel(t, host, admin, model.URL)
	translate := func(id int64, text, language, want string) {
		t.Helper()
		result := translationRequest(t, host, listener, "POST", "/api/content-translations", mustJSON(map[string]any{
			"seriesId": id, "field": "title", "text": text, "language": language,
		}), http.StatusOK)
		if result["text"] != want {
			t.Fatalf("series translation = %#v, want %q", result, want)
		}
	}
	translate(modelSeries, original, "zh-cn", "示例恋爱系列")
	translate(modelSeries, original, "en-us", original)
	translate(nativeSeries, traditional, "zh-tw", traditional)
	for _, sourceID := range []int64{7, 8} {
		for _, query := range []string{"恋爱系列", "恋爱", "恋"} {
			assertManualSeriesSearch(t, host, listener, sourceID, query, "zh-cn", modelSeries)
		}
		for _, query := range []string{"戀愛系列", "戀愛", "戀"} {
			assertManualSeriesSearch(t, host, listener, sourceID, query, "zh-tw", modelSeries)
		}
		for _, query := range []string{"龍語系列", "龍語", "龍"} {
			assertManualSeriesSearch(t, host, listener, sourceID, query, "zh-tw", nativeSeries)
		}
		for _, query := range []string{"manual series", "ma"} {
			assertManualSeriesSearch(t, host, listener, sourceID, query, "origin", modelSeries)
			assertManualSeriesSearch(t, host, listener, sourceID, query, "zh-cn")
		}
		assertManualSeriesSearch(t, host, listener, sourceID, "恋爱", "origin")
		assertManualSeriesSearch(t, host, listener, sourceID, "龙语", "zh-cn")
	}
	for _, query := range []string{"恋爱系列", "恋爱", "恋"} {
		assertManualSeriesSearch(t, host, listener, 9, query, "zh-cn")
	}

	translationRequest(t, host, admin, "PATCH", "/api/settings/translation", `{"model":"synthetic-next-model"}`, http.StatusOK)
	for _, query := range []string{"恋爱系列", "恋爱", "恋"} {
		assertManualSeriesSearch(t, host, listener, 7, query, "zh-cn")
	}
	for _, query := range []string{"manual series", "ma"} {
		assertManualSeriesSearch(t, host, listener, 7, query, "en-us", modelSeries)
	}
	for _, query := range []string{"龍語系列", "龍語", "龍"} {
		assertManualSeriesSearch(t, host, listener, 7, query, "zh-tw", nativeSeries)
	}
	translate(modelSeries, original, "zh-cn", "示例恋爱系列")
	assertManualSeriesSearch(t, host, listener, 7, "恋爱", "zh-cn", modelSeries)
	// A superseded source-text row has the current configuration but must not
	// match the current title. This is a persisted boundary fixture, not a mock
	// of the search or translation writer.
	if _, err := s.db.Exec(`INSERT INTO content_translation(cache_key,entity_kind,entity_id,field,language,source_text,translated_text,origin,config_key,model,template_version)
		SELECT 'synthetic-old-series-title',entity_kind,entity_id,field,language,'Example retired title','过往标题',origin,config_key,model,template_version
		FROM content_translation WHERE entity_kind='series' AND entity_id=? AND language='zh-Hans' ORDER BY id DESC LIMIT 1`, modelSeries); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"过往标题", "过往", "过"} {
		assertManualSeriesSearch(t, host, listener, 7, query, "zh-cn")
	}
}

func TestCatalogSeriesManualSearchUsesAdmittedSourceMembers(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		audio, nonAudio, peerNonAudio int
		oldAudio, admitted            bool
	}{
		{"no_audio", 0, 0, 0, false, false},
		{"old_audio", 0, 0, 0, true, false},
		{"files_5000", 5, 4995, 0, false, true},
		{"files_5001", 5, 4996, 0, false, false},
		{"non_audio_2000", 4, 2000, 0, false, true},
		{"non_audio_2001", 4, 2001, 0, false, false},
		{"audio_5", 5, 2001, 0, false, true},
		{"members_are_not_aggregated", 5, 2995, 2995, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, host, _, listener, seriesID := manualTitleSearchFixture(t)
			metadataReviewExec(t, s.db, `DELETE FROM source_catalog_file WHERE catalog_entry_id=1`)
			seedAdmissionRows(t, s.db, 1, "a", ".mp3", "audio", tc.audio, 0)
			seedAdmissionRows(t, s.db, 1, "n", ".jpg", "other", tc.nonAudio, 0)
			if tc.oldAudio {
				seedAdmissionRows(t, s.db, 1, "old", ".mp3", "audio", 1, -1)
			}
			if tc.peerNonAudio > 0 {
				seedAdmissionRows(t, s.db, 2, "a", ".mp3", "audio", 4, 0)
				seedAdmissionRows(t, s.db, 2, "n", ".jpg", "other", tc.peerNonAudio, 0)
			}
			for _, query := range []string{"violet", "vi", "v"} {
				if tc.admitted {
					assertManualSeriesSearch(t, host, listener, 7, query, "en-us", seriesID)
				} else {
					assertManualSeriesSearch(t, host, listener, 7, query, "en-us")
				}
				// A valid member of another source cannot admit this source's card.
				assertManualSeriesSearch(t, host, listener, 8, query, "en-us", seriesID)
			}
		})
	}
}

func TestCatalogSeriesManualSearchBudgetCountsEligibleCards(t *testing.T) {
	s, host, admin, listener := translationHTTPFixture(t)
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	seedCatalogPlanSource(t, s.db)
	metadataReviewExec(t, s.db, `INSERT INTO file_source(id,code,display_name,source_type,enabled)
		VALUES(8,'example_remote_b','Example Remote B','openlist',1);
		INSERT INTO file_source_endpoint(file_source_id,base_url,api_url)
		VALUES(8,'https://source.example.invalid','https://source.example.invalid');`)
	// Numberless persisted fixtures put 1100 matching but ineligible series
	// before 1000 eligible series in translation-index order. They have either
	// no members, one member, or members only in another source.
	for _, statement := range []string{
		`WITH RECURSIVE n(i) AS(SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<2100)
		 INSERT INTO catalog_series(id,title,created_at)
		 SELECT i,'Example violet collection '||i,'2026-01-01 00:00:00' FROM n`,
		`WITH RECURSIVE n(i) AS(SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<4200)
		 INSERT INTO source_catalog_entry(id,file_source_id,root_path,title,updated_at)
		 SELECT i,CASE WHEN (i+1)/2<=1100 AND ((i+1)/2)%3=2 THEN 8 ELSE 7 END,
		 '/Example/'||i,'Example episode '||i,'2026-01-01 00:00:00' FROM n`,
		`INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes)
		 SELECT id,'track.mp3','Example track','audio',128 FROM source_catalog_entry`,
		`INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence)
		 SELECT id,id*2-1,1,'manual' FROM catalog_series WHERE id>1100 OR id%3<>0
		 UNION ALL SELECT id,id*2,2,'manual' FROM catalog_series WHERE id>1100 OR id%3=2`,
		`INSERT INTO source_catalog_entry_admission(catalog_entry_id,generation,files,audio,non_audio)
		 SELECT id,last_seen_generation,1,1,0 FROM source_catalog_entry`,
		`INSERT INTO content_translation(cache_key,entity_kind,entity_id,field,language,source_text,translated_text,origin)
		 SELECT 'synthetic:manual-budget:'||id,'series',id,'title','en',title,title,'native' FROM catalog_series`,
	} {
		metadataReviewExec(t, s.db, statement)
	}
	checkPage := func(query string, page, total int, truncated bool, count int, first, last int64) {
		t.Helper()
		var response remoteWorksResponse
		workReferenceRequest(t, host, listener, http.MethodGet,
			fmt.Sprintf("/api/remote-sources/7/works?q=%s&lang=en-us&pageSize=100&page=%d", url.QueryEscape(query), page),
			"", http.StatusOK, &response)
		ids := remoteWorkIDs(response)
		if response.Total != total || response.Truncated != truncated || len(ids) != count ||
			(count > 0 && (ids[0] != first || ids[count-1] != last)) {
			t.Fatalf("%q page %d: total=%d truncated=%t cards=%v; want total=%d truncated=%t count=%d first=%d last=%d",
				query, page, response.Total, response.Truncated, ids, total, truncated, count, first, last)
		}
	}
	for _, query := range []string{"violet", "vi", "v"} {
		checkPage(query, 1, 1000, false, 100, -2100, -2001)
		checkPage(query, 10, 1000, false, 100, -1200, -1101)
		checkPage(query, 11, 1000, false, 0, 0, 0)
	}
	checkPage("absent", 11, 0, false, 0, 0, 0)

	for id := int64(4201); id <= 4202; id++ {
		insertCatalogPlanEntry(t, s.db, id, fmt.Sprintf("Example episode %d", id), "")
		insertCatalogPlanAudio(t, s.db, id, "track.mp3", "Example track")
	}
	const overflowTitle = "Example violet overflow collection"
	preview := translationRequest(t, host, admin, "POST", "/api/catalog-series/previews", mustJSON(map[string]any{
		"title": overflowTitle, "catalogEntryIds": []int64{4201, 4202},
	}), http.StatusCreated)
	confirmed := translationRequest(t, host, admin, "POST", fmt.Sprintf("/api/catalog-series/previews/%d/confirm", int(preview["previewId"].(float64))), "{}", http.StatusOK)
	translationRequest(t, host, listener, "POST", "/api/content-translations", mustJSON(map[string]any{
		"seriesId": confirmed["seriesId"], "field": "title", "text": overflowTitle, "language": "en-us",
	}), http.StatusOK)
	checkPage("violet", 11, 1001, false, 1, -1101, -1101)
	for _, query := range []string{"vi", "v"} {
		checkPage(query, 1, 1000, true, 100, -2101, -2002)
		checkPage(query, 10, 1000, true, 100, -1201, -1102)
		checkPage(query, 11, 1000, true, 0, 0, 0)
	}

	// Entry hits and manual-series hits share the response budget.
	metadataReviewExec(t, s.db, `DELETE FROM catalog_series_member WHERE series_id=2101`)
	const entryTitle = "Example violet standalone"
	insertCatalogPlanEntry(t, s.db, 4203, entryTitle, "")
	insertCatalogPlanAudio(t, s.db, 4203, "track.mp3", "Example track")
	translationRequest(t, host, listener, "POST", "/api/content-translations", mustJSON(map[string]any{
		"catalogEntryId": 4203, "field": "title", "text": entryTitle, "language": "en-us",
	}), http.StatusOK)
	checkPage("violet", 11, 1001, false, 1, -1101, -1101)
	for _, query := range []string{"vi", "v"} {
		checkPage(query, 1, 1000, true, 100, 4203, -2002)
		checkPage(query, 11, 1000, true, 0, 0, 0)
	}
}

func TestCatalogSeriesManualSearchUsesPublishedSnapshot(t *testing.T) {
	s, host, _, listener, seriesID := manualTitleSearchFixture(t)
	check := func(sourceID int64, queries []string, visible bool) {
		t.Helper()
		for _, query := range queries {
			if visible {
				assertManualSeriesSearch(t, host, listener, sourceID, query, "en-us", seriesID)
			} else {
				assertManualSeriesSearch(t, host, listener, sourceID, query, "en-us")
			}
		}
	}
	ctx, op, err := s.beginCatalogPublication(context.Background(), 7, "synthetic:manual-title-search")
	if err != nil {
		t.Fatal(err)
	}
	const title = "Example indigo collection"
	if _, err = s.execCatalogWrite(ctx, `UPDATE catalog_series SET title=? WHERE id=?`, title, seriesID); err != nil {
		t.Fatal(err)
	}
	for _, sourceID := range []int64{7, 8} {
		check(sourceID, []string{"violet", "vi", "v"}, true)
		check(sourceID, []string{"indigo", "in"}, false)
	}
	if err = s.commitCatalogPublication(ctx, op); err != nil {
		t.Fatal(err)
	}
	for _, sourceID := range []int64{7, 8} {
		check(sourceID, []string{"violet", "vi", "v"}, false)
	}
	translationRequest(t, host, listener, "POST", "/api/content-translations", mustJSON(map[string]any{
		"seriesId": seriesID, "field": "title", "text": title, "language": "en-us",
	}), http.StatusOK)
	check(7, []string{"indigo", "in", "g"}, true)
	metadataReviewExec(t, s.db, `INSERT INTO source_catalog_episode_pending(source_id,parent_root,catalog_entry_root,catalog_entry_id)
		VALUES(7,'/Example','/work/1',1);
		INSERT INTO source_catalog_episode_publication(source_id,parent_root,state) VALUES(7,'/Example','pending')`)
	check(7, []string{"indigo", "in", "g"}, false)
	check(8, []string{"indigo", "in", "g"}, true)
	metadataReviewExec(t, s.db, `UPDATE source_catalog_episode_publication SET state='published' WHERE source_id=7`)
	check(7, []string{"indigo", "in", "g"}, true)
	metadataReviewExec(t, s.db, `DELETE FROM catalog_series_member WHERE catalog_entry_id=2`)
	for _, sourceID := range []int64{7, 8} {
		check(sourceID, []string{"indigo", "in", "g"}, false)
	}
}

func manualTitleSearchFixture(t *testing.T) (*Server, *httptest.Server, *http.Cookie, *http.Cookie, int64) {
	t.Helper()
	s, host, admin, listener := translationHTTPFixture(t)
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	seedCatalogPlanSource(t, s.db)
	metadataReviewExec(t, s.db, `INSERT INTO file_source(id,code,display_name,source_type,enabled)
		VALUES(8,'example_remote_b','Example Remote B','openlist',1);
		INSERT INTO file_source_endpoint(file_source_id,base_url,api_url)
		VALUES(8,'https://source.example.invalid','https://source.example.invalid');`)
	for id := int64(1); id <= 2; id++ {
		insertCatalogPlanEntry(t, s.db, id, fmt.Sprintf("Example episode %d", id), "")
		insertCatalogPlanAudio(t, s.db, id, "track.mp3", "Example track")
	}
	metadataReviewExec(t, s.db, `UPDATE source_catalog_entry SET file_source_id=8 WHERE id=2`)
	const title = "Example violet collection"
	preview := translationRequest(t, host, admin, "POST", "/api/catalog-series/previews", mustJSON(map[string]any{
		"title": title, "catalogEntryIds": []int64{1, 2},
	}), http.StatusCreated)
	result := translationRequest(t, host, admin, "POST", fmt.Sprintf("/api/catalog-series/previews/%d/confirm", int(preview["previewId"].(float64))), "{}", http.StatusOK)
	seriesID := int64(result["seriesId"].(float64))
	translationRequest(t, host, listener, "POST", "/api/content-translations", mustJSON(map[string]any{
		"seriesId": seriesID, "field": "title", "text": title, "language": "en-us",
	}), http.StatusOK)
	return s, host, admin, listener, seriesID
}

func assertManualSeriesSearch(t *testing.T, host *httptest.Server, cookie *http.Cookie, sourceID int64, query, language string, seriesIDs ...int64) {
	t.Helper()
	var response remoteWorksResponse
	workReferenceRequest(t, host, cookie, http.MethodGet,
		fmt.Sprintf("/api/remote-sources/%d/works?q=%s&lang=%s", sourceID, url.QueryEscape(query), url.QueryEscape(language)), "", http.StatusOK, &response)
	want := make([]int64, 0, len(seriesIDs))
	for _, id := range seriesIDs {
		want = append(want, -id)
	}
	if response.Total != len(want) || response.Truncated || !slices.Equal(remoteWorkIDs(response), want) {
		t.Fatalf("source %d %s %q: total=%d truncated=%t cards=%v, want %v", sourceID, language, query, response.Total, response.Truncated, remoteWorkIDs(response), want)
	}
}
