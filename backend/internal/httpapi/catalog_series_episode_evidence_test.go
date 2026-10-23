package httpapi

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func scanCatalogTree(t *testing.T, tree map[string][]map[string]any) *looseWorkFixture {
	t.Helper()
	f := newLooseWorkFixture(t, tree)
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"pageSize":500}`, http.StatusAccepted)
	if progress := f.drain(t); progress["status"] != "complete" {
		t.Fatalf("scan = %#v", progress)
	}
	return f
}

func catalogSeriesCounts(t *testing.T, f *looseWorkFixture) (series, members int) {
	t.Helper()
	if err := f.server.db.QueryRow(`SELECT
		(SELECT count(*) FROM catalog_series),
		(SELECT count(*) FROM catalog_series_member)`).Scan(&series, &members); err != nil {
		t.Fatal(err)
	}
	return series, members
}

// A lone numbered directory is an independent work, not a one-member series.
func TestSourceCatalogSingleEpisodeDoesNotCreateSeries(t *testing.T) {
	f := scanCatalogTree(t, map[string][]map[string]any{
		"/":                       {looseDir("Creator")},
		"/Creator":                {looseDir("Show")},
		"/Creator/Show":           {looseDir("Episode 1")},
		"/Creator/Show/Episode 1": {looseAudio("track.mp3")},
	})
	series, members := catalogSeriesCounts(t, f)
	if series != 0 || members != 0 {
		t.Fatalf("series=%d members=%d, want a standalone card", series, members)
	}
	if got := len(f.cards(t)); got != 1 {
		t.Fatalf("cards=%d, want one standalone card", got)
	}
}

// Two formats of one episode share its card. Format count alone is not series
// evidence.
func TestSourceCatalogEpisodeFormatsShareOneCardWithoutSeries(t *testing.T) {
	f := scanCatalogTree(t, map[string][]map[string]any{
		"/":                              {looseDir("Creator")},
		"/Creator":                       {looseDir("Show")},
		"/Creator/Show":                  {looseDir("Episode 1 (MP3)"), looseDir("Episode 1 (FLAC)")},
		"/Creator/Show/Episode 1 (MP3)":  {looseAudio("track.mp3")},
		"/Creator/Show/Episode 1 (FLAC)": {looseAudio("track.flac")},
	})
	series, members := catalogSeriesCounts(t, f)
	if series != 0 || members != 0 {
		t.Fatalf("series=%d members=%d, want one card without a series", series, members)
	}
	if got := len(f.cards(t)); got != 1 {
		t.Fatalf("cards=%d, want one shared episode card", got)
	}
}

// A format copy and a bare sibling episode belong to the same two-member series.
func TestSourceCatalogMixedEpisodeFormatsFormOneSeries(t *testing.T) {
	f := scanCatalogTree(t, map[string][]map[string]any{
		"/":                             {looseDir("Creator")},
		"/Creator":                      {looseDir("Show")},
		"/Creator/Show":                 {looseDir("Episode 1 (MP3)"), looseDir("Episode 2")},
		"/Creator/Show/Episode 1 (MP3)": {looseAudio("track.mp3")},
		"/Creator/Show/Episode 2":       {looseAudio("track2.mp3")},
	})
	series, members := catalogSeriesCounts(t, f)
	if series != 1 || members != 2 {
		t.Fatalf("series=%d members=%d, want one two-member series", series, members)
	}
}

// Two format variants of one loose episode are one card, and a one-position
// group is not a series.
func TestSourceCatalogSingleEpisodeFormatsShareOneCard(t *testing.T) {
	f := scanCatalogTree(t, map[string][]map[string]any{
		"/":        {looseDir("Creator")},
		"/Creator": {looseDir("Show")},
		"/Creator/Show": {
			looseAudio("Episode 1 (MP3).mp3"),
			looseAudio("Episode 1 (FLAC).flac"),
		},
	})
	series, members := catalogSeriesCounts(t, f)
	if series != 0 || members != 0 {
		t.Fatalf("series=%d members=%d, want no series", series, members)
	}
	if got := len(f.cards(t)); got != 1 {
		t.Fatalf("cards=%d, want one episode card", got)
	}
}

// Bonus tracks belong to the series card's work-part tree, not to the episode
// sequence.
func TestSourceCatalogBonusDirectoryIsNotASeriesEpisode(t *testing.T) {
	f := scanCatalogTree(t, map[string][]map[string]any{
		"/":                               {looseDir("Creator")},
		"/Creator":                        {looseDir("Series: Show")},
		"/Creator/Series: Show":           {looseDir("Episode 1"), looseDir("Episode 2"), looseDir("Bonus")},
		"/Creator/Series: Show/Episode 1": {looseAudio("one.mp3")},
		"/Creator/Series: Show/Episode 2": {looseAudio("two.mp3")},
		"/Creator/Series: Show/Bonus":     {looseAudio("bonus.mp3")},
	})
	series, members := catalogSeriesCounts(t, f)
	if series != 1 || members != 2 {
		t.Fatalf("series=%d members=%d, want two episodes without Bonus", series, members)
	}
	for title := range f.cards(t) {
		if title == "Bonus" {
			t.Fatalf("Bonus published as a standalone card: %v", f.cards(t))
		}
	}
}

// Automatic discovery must stop at the shared safety bound with a visible
// error instead of silently dropping the next episode.
func TestCatalogSeriesSafetyLimitIsExplicit(t *testing.T) {
	db := openMigratedTestDB(t)
	metadataReviewExec(t, db, `
		INSERT INTO file_source(id,code,display_name,source_type)
			VALUES(7,'series_limit','Series limit','openlist');
		INSERT INTO catalog_series(id,title,source_id,source_key)
			VALUES(1,'Series',7,'/Series
series');
		WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i+1 FROM n WHERE i<9999)
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title)
			SELECT 100000+i,7,printf('/Series/Episode %d',i+1),printf('Episode %d',i+1) FROM n;
		INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence)
			SELECT 1,id,id-99999,'directory' FROM source_catalog_entry
			WHERE id BETWEEN 100000 AND 109999;
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,title)
			VALUES(200000,7,'/Series/Episode 10001','Episode 10001');
	`)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	err = attachCatalogSeriesEvidenceTx(context.Background(), tx, 200000, 7, catalogSeriesEvidence{
		Key: "/Series\nseries", Title: "Series", Position: 10001, Explicit: true,
	})
	if !errors.Is(err, errCatalogSeriesTooLarge) {
		t.Fatalf("series limit error=%v, want errCatalogSeriesTooLarge", err)
	}
}
