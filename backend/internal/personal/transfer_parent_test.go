package personal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yexca/kikoto/backend/internal/storage"
	"github.com/yexca/kikoto/backend/internal/testfixture"
)

func emptyTransferStore(t *testing.T) Store {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "personal-parent.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	migrationDir, err := testfixture.MigrationDir(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	if err = storage.Migrate(db, migrationDir); err != nil {
		t.Fatal(err)
	}
	execFixture(t, db, `INSERT INTO user_account (id,username,display_name,role) VALUES
		(1,'synthetic-user','Example User','admin'),(2,'synthetic-other','Example Other','user')`)
	return Store{DB: db}
}

func refWith(prefix string, digit byte) string {
	return prefix + ":" + strings.Repeat(string(digit), 32)
}

func importBackup(t *testing.T, s Store, user int64, b Backup) ImportResult {
	t.Helper()
	result, err := s.ImportWithRegistration(context.Background(), user, b, false, true)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	return result
}

func exportBackup(t *testing.T, s Store, user int64) Backup {
	t.Helper()
	raw, err := s.Export(context.Background(), user)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	b, err := ParseImport(ImportRequest{Format: "kikoto", Data: raw, Conflict: "keep"})
	if err != nil {
		t.Fatalf("parse exported backup: %v", err)
	}
	return b
}

// A v2 episode review alone must recreate the series parent, the series work and
// the catalog-series row without inventing a source membership.
func TestTransferV2RestoresPersonalOnlyParentAndRoundTrips(t *testing.T) {
	s := emptyTransferStore(t)
	rating := 4
	episodeRef, parentRef := refWith("w", '1'), refWith("s", '2')
	backup := Backup{Format: "kikoto-user-data", Version: 2, Works: []BackupWork{{
		Identity: &BackupIdentity{
			Ref:   episodeRef,
			Kind:  "episode",
			Title: "Example Episode",
			Parent: &BackupIdentity{
				Ref:   parentRef,
				Kind:  "series",
				Title: "Example Series",
			},
		},
		ListeningStatus: "finished",
		Rating:          &rating,
		Note:            "Episode private note",
	}}}
	result := importBackup(t, s, 1, backup)
	if result.ImportedWorks != 1 || result.SkippedWorks != 0 {
		t.Fatalf("restore = %+v", result)
	}
	var parentWork, seriesID, episodeWork int64
	var sourceID, sourceKey sql.NullString
	if err := s.DB.QueryRow(`SELECT id FROM catalog_series WHERE title='Example Series'`).Scan(&seriesID); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT work_id FROM catalog_series WHERE id=?`, seriesID).Scan(&parentWork); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT id FROM work WHERE parent_work_id=?`, parentWork).Scan(&episodeWork); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT source_id,source_key FROM catalog_series WHERE id=?`, seriesID).Scan(&sourceID, &sourceKey); err != nil {
		t.Fatal(err)
	}
	if sourceID.Valid || sourceKey.Valid {
		t.Fatalf("personal-only series source = %v/%v, want NULL/NULL", sourceID, sourceKey)
	}
	var members int
	if err := s.DB.QueryRow(`SELECT count(*) FROM catalog_series_member`).Scan(&members); err != nil || members != 0 {
		t.Fatalf("personal-only series members = %d, %v", members, err)
	}
	var parentOfParent sql.NullInt64
	if err := s.DB.QueryRow(`SELECT parent_work_id FROM work WHERE id=?`, parentWork).Scan(&parentOfParent); err != nil || parentOfParent.Valid {
		t.Fatalf("series parent = %v, %v", parentOfParent, err)
	}
	var adopted int
	if err := s.DB.QueryRow(`SELECT count(*) FROM personal_transfer_ref WHERE ref=? AND series_id=?`, parentRef, seriesID).Scan(&adopted); err != nil || adopted != 1 {
		t.Fatalf("adopted parent ref = %d, %v", adopted, err)
	}

	exported := exportBackup(t, s, 1)
	if exported.Version != 2 || len(exported.Works) != 1 {
		t.Fatalf("export = version %d, %d works", exported.Version, len(exported.Works))
	}
	identity := exported.Works[0].Identity
	if identity.Parent == nil || identity.Parent.Ref != parentRef || identity.Parent.Kind != "series" || identity.Parent.Title != "Example Series" {
		t.Fatalf("exported parent = %#v", identity.Parent)
	}
	if identity.Parent.Parent != nil || len(identity.Parent.References) != 0 {
		t.Fatalf("parent carries nested state: %#v", identity.Parent)
	}

	// A repeated import reuses both works and the parent relation.
	if result = importBackup(t, s, 1, backup); result.ImportedWorks != 1 || result.SkippedWorks != 0 {
		t.Fatalf("duplicate restore = %+v", result)
	}
	var works, series int
	if err := s.DB.QueryRow(`SELECT (SELECT count(*) FROM work),(SELECT count(*) FROM catalog_series)`).Scan(&works, &series); err != nil {
		t.Fatal(err)
	}
	if works != 2 || series != 1 {
		t.Fatalf("duplicate restore works=%d series=%d", works, series)
	}
	var media, runs int
	if err := s.DB.QueryRow(`SELECT (SELECT count(*) FROM media_item),(SELECT count(*) FROM workflow_run)`).Scan(&media, &runs); err != nil {
		t.Fatal(err)
	}
	if media != 0 || runs != 0 {
		t.Fatalf("import created media=%d runs=%d", media, runs)
	}
	if other := exportBackup(t, s, 2); len(other.Works) != 0 {
		t.Fatalf("another user's export = %+v", other.Works)
	}
}

// A series that also appears as a top-level BackupWork is created once and keeps
// its own review; the episode's minimal parent never overwrites it.
func TestTransferV2TopLevelSeriesKeepsItsReview(t *testing.T) {
	s := emptyTransferStore(t)
	seriesRating, episodeRating := 5, 2
	parentRef, episodeRef, seriesRef := refWith("s", '3'), refWith("w", '4'), refWith("w", '5')
	backup := Backup{Format: "kikoto-user-data", Version: 2, Works: []BackupWork{
		{Identity: &BackupIdentity{Ref: episodeRef, Kind: "episode", Title: "Example Episode",
			Parent: &BackupIdentity{Ref: parentRef, Kind: "series", Title: "Example Series"}},
			Rating: &episodeRating, Note: "Episode note"},
		{Identity: &BackupIdentity{Ref: seriesRef, Kind: "series", Title: "Example Series", References: []string{parentRef}},
			ListeningStatus: "paused", Rating: &seriesRating, Note: "Series note"},
	}}
	if result := importBackup(t, s, 1, backup); result.ImportedWorks != 2 {
		t.Fatalf("restore = %+v", result)
	}
	var works, series int
	if err := s.DB.QueryRow(`SELECT (SELECT count(*) FROM work),(SELECT count(*) FROM catalog_series)`).Scan(&works, &series); err != nil {
		t.Fatal(err)
	}
	if works != 2 || series != 1 {
		t.Fatalf("works=%d series=%d, want one series and one episode", works, series)
	}
	exported := exportBackup(t, s, 1)
	if len(exported.Works) != 2 {
		t.Fatalf("exported works = %+v", exported.Works)
	}
	for _, w := range exported.Works {
		switch w.Identity.Kind {
		case "series":
			if w.Rating == nil || *w.Rating != 5 || w.Note != "Series note" || w.ListeningStatus != "paused" {
				t.Fatalf("series review = %+v", w)
			}
		case "episode":
			if w.Rating == nil || *w.Rating != 2 || w.Note != "Episode note" || w.Identity.Parent == nil {
				t.Fatalf("episode review = %+v", w)
			}
		}
	}
}

// A parent reference owned by a catalog entry or another series is
// contradictory and blocks only that episode.
func TestTransferV2ParentReferenceConflicts(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup string
		build func(t *testing.T, s Store, parentRef string) *BackupIdentity
	}{
		{
			name:  "catalog entry owner",
			setup: `INSERT INTO source_catalog_entry(id,root_path,title) VALUES(501,'/Example','Example Entry');`,
			build: func(t *testing.T, s Store, parentRef string) *BackupIdentity {
				var owned string
				if err := s.DB.QueryRow(`SELECT ref FROM personal_transfer_ref WHERE catalog_entry_id=501`).Scan(&owned); err != nil {
					t.Fatal(err)
				}
				return &BackupIdentity{Ref: owned, Kind: "series", Title: "Example Series"}
			},
		},
		{
			name:  "alias owned by another series",
			setup: `INSERT INTO catalog_series(id,title) VALUES(601,'First Series'),(602,'Second Series');`,
			build: func(t *testing.T, s Store, parentRef string) *BackupIdentity {
				var firstRef, secondRef string
				if err := s.DB.QueryRow(`SELECT ref FROM personal_transfer_ref WHERE series_id=601`).Scan(&firstRef); err != nil {
					t.Fatal(err)
				}
				if err := s.DB.QueryRow(`SELECT ref FROM personal_transfer_ref WHERE series_id=602`).Scan(&secondRef); err != nil {
					t.Fatal(err)
				}
				return &BackupIdentity{Ref: firstRef, Kind: "series", Title: "First Series", References: []string{secondRef}}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := emptyTransferStore(t)
			execFixture(t, s.DB, tc.setup)
			parentRef := refWith("s", '7')
			parent := tc.build(t, s, parentRef)
			backup := Backup{Format: "kikoto-user-data", Version: 2, Works: []BackupWork{{
				Identity: &BackupIdentity{Ref: refWith("w", '8'), Kind: "episode", Title: "Example Episode", Parent: parent},
				Note:     "Episode note",
			}}}
			result := importBackup(t, s, 1, backup)
			if result.ImportedWorks != 0 || result.SkippedWorks != 1 || len(result.Issues) != 1 || result.Issues[0].Reason != "parent_conflict" {
				t.Fatalf("restore = %+v", result)
			}
			var works int
			if err := s.DB.QueryRow(`SELECT count(*) FROM work`).Scan(&works); err != nil || works != 0 {
				t.Fatalf("conflicting parent wrote %d works, %v", works, err)
			}
		})
	}
}

// personal_transfer_series allows one reference per series, so an alias that
// names the same series cannot be stored. The episode still imports and the
// skipped alias is reported instead of failing the whole parent.
func TestTransferV2ParentAliasForTheSameSeriesIsReported(t *testing.T) {
	s := emptyTransferStore(t)
	primary, alias, episodeRef := refWith("s", 'c'), refWith("s", 'd'), refWith("w", 'e')
	backup := Backup{Format: "kikoto-user-data", Version: 2, Works: []BackupWork{{
		Identity: &BackupIdentity{
			Ref: episodeRef, Kind: "episode", Title: "Example Episode",
			Parent: &BackupIdentity{
				Ref: primary, Kind: "series", Title: "Example Series",
				References: []string{alias},
			},
		}, Note: "Episode note"},
	}}
	result := importBackup(t, s, 1, backup)
	if result.ImportedWorks != 1 || result.SkippedWorks != 0 {
		t.Fatalf("restore = %+v", result)
	}
	found := false
	for _, issue := range result.Issues {
		if issue.Reason == "parent_alias_not_imported" && issue.Ref == alias {
			found = true
		}
	}
	if !found {
		t.Fatalf("issues = %+v, want the skipped alias", result.Issues)
	}
	var seriesRefs int
	if err := s.DB.QueryRow(`SELECT count(*) FROM personal_transfer_ref WHERE series_id IS NOT NULL`).Scan(&seriesRefs); err != nil {
		t.Fatal(err)
	}
	if seriesRefs != 1 {
		t.Fatalf("series rows kept %d references, want one", seriesRefs)
	}
	var parent int64
	if err := s.DB.QueryRow(`SELECT parent_work_id FROM work WHERE id=(SELECT id FROM work WHERE title='Example Episode')`).Scan(&parent); err != nil || parent == 0 {
		t.Fatalf("episode parent = %d (%v)", parent, err)
	}
}

// A non-empty library keeps its existing parent when the backup names another.
func TestTransferV2KeepsExistingDifferentParent(t *testing.T) {
	s := emptyTransferStore(t)
	episodeRef, firstParent := refWith("w", '9'), refWith("s", 'a')
	first := Backup{Format: "kikoto-user-data", Version: 2, Works: []BackupWork{{
		Identity: &BackupIdentity{Ref: episodeRef, Kind: "episode", Title: "Example Episode",
			Parent: &BackupIdentity{Ref: firstParent, Kind: "series", Title: "First Series"}},
		Note: "Original note",
	}}}
	if result := importBackup(t, s, 1, first); result.ImportedWorks != 1 || result.SkippedWorks != 0 {
		t.Fatalf("first restore = %+v", result)
	}
	var firstWork, episodeWork int64
	if err := s.DB.QueryRow(`SELECT work_id FROM catalog_series WHERE title='First Series'`).Scan(&firstWork); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT id FROM work WHERE parent_work_id=?`, firstWork).Scan(&episodeWork); err != nil {
		t.Fatal(err)
	}
	second := first
	second.Works[0].Identity = &BackupIdentity{Ref: episodeRef, Kind: "episode", Title: "Example Episode",
		Parent: &BackupIdentity{Ref: refWith("s", 'b'), Kind: "series", Title: "Second Series"}}
	second.Works[0].Note = "Contradictory note"
	result := importBackup(t, s, 1, second)
	if result.ImportedWorks != 0 || result.SkippedWorks != 1 || len(result.Issues) != 1 || result.Issues[0].Reason != "parent_conflict" {
		t.Fatalf("restore = %+v", result)
	}
	var parent int64
	if err := s.DB.QueryRow(`SELECT parent_work_id FROM work WHERE id=?`, episodeWork).Scan(&parent); err != nil || parent != firstWork {
		t.Fatalf("episode parent = %d, want %d (%v)", parent, firstWork, err)
	}
	var series int
	if err := s.DB.QueryRow(`SELECT count(*) FROM catalog_series`).Scan(&series); err != nil || series != 1 {
		t.Fatalf("series rows = %d, %v", series, err)
	}
	var note string
	if err := s.DB.QueryRow(`SELECT note FROM user_work_state WHERE user_id=1 AND work_id=?`, episodeWork).Scan(&note); err != nil || note != "Original note" {
		t.Fatalf("episode note = %q, %v", note, err)
	}
}

// A parent chain that loops is reported as a cycle instead of being followed
// forever or silently flattened.
func TestTransferV2ParentCycleIsReported(t *testing.T) {
	s := emptyTransferStore(t)
	execFixture(t, s.DB, `INSERT INTO work(id,primary_code,title) VALUES(701,'','Series A'),(702,'','Series B');
		INSERT INTO catalog_series(id,title,work_id) VALUES(801,'Series A',701),(802,'Series B',702);
		UPDATE work SET parent_work_id=702 WHERE id=701;
		UPDATE work SET parent_work_id=701 WHERE id=702;`)
	var parentRef string
	if err := s.DB.QueryRow(`SELECT ref FROM personal_transfer_ref WHERE series_id=801`).Scan(&parentRef); err != nil {
		t.Fatal(err)
	}
	backup := Backup{Format: "kikoto-user-data", Version: 2, Works: []BackupWork{{
		Identity: &BackupIdentity{Ref: refWith("w", 'c'), Kind: "episode", Title: "Example Episode",
			Parent: &BackupIdentity{Ref: parentRef, Kind: "series", Title: "Series A"}},
	}}}
	result := importBackup(t, s, 1, backup)
	if result.ImportedWorks != 0 || result.SkippedWorks != 1 || len(result.Issues) != 1 {
		t.Fatalf("restore = %+v", result)
	}
	if reason := result.Issues[0].Reason; reason != "parent_cycle" && reason != "parent_conflict" {
		t.Fatalf("cycle reason = %q", reason)
	}
}

// Version 2 has no parent field: the episode keeps the previous independent
// restoration and reports that relationships were not imported.
func TestTransferV2EpisodeWithoutParentKeepsLegacyBehavior(t *testing.T) {
	s := emptyTransferStore(t)
	backup := Backup{Format: "kikoto-user-data", Version: 2, Works: []BackupWork{{
		Identity: &BackupIdentity{Ref: refWith("w", 'd'), Kind: "episode", Title: "Example Episode"},
		Note:     "Legacy note",
	}}}
	result := importBackup(t, s, 1, backup)
	if result.ImportedWorks != 1 || result.SkippedWorks != 0 {
		t.Fatalf("restore = %+v", result)
	}
	found := false
	for _, issue := range result.Issues {
		if issue.Reason == "relationships_not_imported" {
			found = true
		}
	}
	if !found {
		t.Fatalf("v2 restore issues = %+v", result.Issues)
	}
	var parent sql.NullInt64
	if err := s.DB.QueryRow(`SELECT parent_work_id FROM work`).Scan(&parent); err != nil || parent.Valid {
		t.Fatalf("v2 parent = %v, %v", parent, err)
	}
}

// Two series works with empty codes are normal numberless identities and must
// not collide on the partial code index.
func TestTransferV2TwoEmptyCodeSeriesDoNotCollide(t *testing.T) {
	s := emptyTransferStore(t)
	backup := Backup{Format: "kikoto-user-data", Version: 2, Works: []BackupWork{
		{Identity: &BackupIdentity{Ref: refWith("w", 'e'), Kind: "series", Title: "First Series"}},
		{Identity: &BackupIdentity{Ref: refWith("w", 'f'), Kind: "series", Title: "Second Series"}},
	}}
	if result := importBackup(t, s, 1, backup); result.ImportedWorks != 2 {
		t.Fatalf("restore = %+v", result)
	}
	var works, series int
	if err := s.DB.QueryRow(`SELECT (SELECT count(*) FROM work),(SELECT count(*) FROM catalog_series)`).Scan(&works, &series); err != nil {
		t.Fatal(err)
	}
	if works != 2 || series != 2 {
		t.Fatalf("works=%d series=%d", works, series)
	}
}

// A work reachable from two different series has no single parent. The export
// keeps the episode, its exact catalog references and its review, leaves the
// relation empty and reports the ambiguity instead of failing the whole backup.
func TestTransferExportReportsMultipleSeriesParentInsteadOfFailing(t *testing.T) {
	s := emptyTransferStore(t)
	execFixture(t, s.DB, `INSERT INTO work(id,primary_code,title) VALUES
		(301,'','Series A'),(302,'','Episode One'),(303,'','Series B'),(304,'','Episode Two');
		INSERT INTO catalog_series(id,title,work_id) VALUES
		(201,'Series A',301),(202,'Series B',303);
		INSERT INTO source_catalog_entry(id,root_path,title,work_id) VALUES
		(101,'/A/Episode One','Episode One',302),
		(102,'/B/Episode One','Episode One',302),
		(103,'/A/Episode Two','Episode Two',304);
		INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence) VALUES
		(201,101,1,'directory'),(202,102,1,'directory'),(201,103,2,'directory');
		UPDATE work SET parent_work_id=301 WHERE id=304;
		INSERT INTO user_work_state(user_id,work_id,rating,note) VALUES
			(1,302,4,'Episode private note'),(1,304,5,'Second episode note');`)

	raw, err := s.Export(context.Background(), 1)
	if err != nil {
		t.Fatalf("export failed on an ambiguous parent: %v", err)
	}
	exported, err := ParseImport(ImportRequest{Format: "kikoto", Data: raw, Conflict: "keep"})
	if err != nil {
		t.Fatalf("parse exported backup: %v", err)
	}
	if len(exported.Works) != 2 {
		t.Fatalf("exported works = %+v, want both reviewed episodes", exported.Works)
	}
	byTitle := map[string]BackupWork{}
	for _, w := range exported.Works {
		if w.Identity != nil {
			byTitle[w.Identity.Title] = w
		}
	}
	ambiguous, ok := byTitle["Episode One"]
	if !ok {
		t.Fatalf("exported works = %+v, want Episode One", exported.Works)
	}
	if ambiguous.Identity.Kind != "episode" || ambiguous.Identity.Parent != nil {
		t.Fatalf("ambiguous identity = %#v, want an episode with no parent", ambiguous.Identity)
	}
	if ambiguous.Rating == nil || *ambiguous.Rating != 4 || ambiguous.Note != "Episode private note" {
		t.Fatalf("ambiguous review = %v/%q, want it preserved", ambiguous.Rating, ambiguous.Note)
	}
	catalogRefs := 0
	for _, ref := range ambiguous.Identity.References {
		if strings.HasPrefix(ref, "c:") {
			catalogRefs++
		}
	}
	if catalogRefs != 2 {
		t.Fatalf("references = %v, want both retained catalog references", ambiguous.Identity.References)
	}
	unique, ok := byTitle["Episode Two"]
	if !ok {
		t.Fatalf("exported works = %+v, want Episode Two", exported.Works)
	}
	if unique.Identity.Kind != "episode" || unique.Identity.Parent == nil || unique.Identity.Parent.Title != "Series A" {
		t.Fatalf("unique identity = %#v, want the unaffected episode with its parent", unique.Identity)
	}
	if unique.Rating == nil || *unique.Rating != 5 || unique.Note != "Second episode note" {
		t.Fatalf("unique review = %v/%q, want it preserved", unique.Rating, unique.Note)
	}
	found := false
	for _, issue := range exported.Issues {
		if issue.Reason == "parent_conflict" && issue.Ref == ambiguous.Identity.Ref {
			found = true
		}
	}
	if !found {
		t.Fatalf("issues = %+v, want parent_conflict for %s", exported.Issues, ambiguous.Identity.Ref)
	}
	if len(exported.Issues) != 1 {
		t.Fatalf("issues = %+v, want exactly one parent conflict", exported.Issues)
	}
}

// Exactly one series membership is a usable parent even when the entry's work
// is registered, and it must not be reported as a conflict.
func TestTransferExportWritesSingleMembershipParent(t *testing.T) {
	s := emptyTransferStore(t)
	execFixture(t, s.DB, `INSERT INTO work(id,primary_code,title) VALUES(301,'','Series A'),(302,'','Episode One');
		INSERT INTO catalog_series(id,title,work_id) VALUES(201,'Series A',301);
		INSERT INTO source_catalog_entry(id,root_path,title,work_id) VALUES(101,'/A/Episode One','Episode One',302);
		INSERT INTO catalog_series_member(series_id,catalog_entry_id,position,evidence) VALUES(201,101,1,'directory');
		INSERT INTO user_work_state(user_id,work_id,rating,note) VALUES(1,302,5,'Episode note');`)
	var seriesRef string
	if err := s.DB.QueryRow(`SELECT ref FROM personal_transfer_ref WHERE series_id=201`).Scan(&seriesRef); err != nil {
		t.Fatal(err)
	}
	exported := exportBackup(t, s, 1)
	if len(exported.Works) != 1 {
		t.Fatalf("exported works = %+v", exported.Works)
	}
	identity := exported.Works[0].Identity
	if identity == nil || identity.Kind != "episode" || identity.Parent == nil {
		t.Fatalf("identity = %#v, want an episode with its unique parent", identity)
	}
	if identity.Parent.Ref != seriesRef || identity.Parent.Kind != "series" || identity.Parent.Title != "Series A" {
		t.Fatalf("parent = %#v, want the single membership series", identity.Parent)
	}
	if len(exported.Issues) != 0 {
		t.Fatalf("issues = %+v, want no conflict for one parent", exported.Issues)
	}
}

// A restored episode and its series keep distinct evaluation levels, and each
// reference stays with the object class that owns it. No media is invented.
func TestTransferEmptyLibraryRoundTripKeepsKindsAndRefOwners(t *testing.T) {
	s := emptyTransferStore(t)
	episodeRef, parentRef := refWith("w", '1'), refWith("s", '2')
	rating := 3
	backup := Backup{Format: "kikoto-user-data", Version: 2, Works: []BackupWork{{
		Identity: &BackupIdentity{
			Ref: episodeRef, Kind: "episode", Title: "Example Episode",
			Parent: &BackupIdentity{Ref: parentRef, Kind: "series", Title: "Example Series"},
		},
		Rating: &rating,
		Note:   "Episode note",
	}}}
	if result := importBackup(t, s, 1, backup); result.ImportedWorks != 1 || result.SkippedWorks != 0 {
		t.Fatalf("restore = %+v", result)
	}
	// The parent keeps its own review too, so both levels appear in the export.
	execFixture(t, s.DB, `INSERT INTO user_work_state(user_id,work_id,rating,note)
		SELECT 1,work_id,5,'Series note' FROM catalog_series WHERE title='Example Series'`)
	exported := exportBackup(t, s, 1)
	if len(exported.Works) != 2 {
		t.Fatalf("exported works = %+v", exported.Works)
	}
	var episode, series *BackupWork
	for index := range exported.Works {
		switch {
		case exported.Works[index].Identity != nil && exported.Works[index].Identity.Kind == "episode":
			episode = &exported.Works[index]
		case exported.Works[index].Identity != nil && exported.Works[index].Identity.Kind == "series":
			series = &exported.Works[index]
		}
	}
	if episode == nil || series == nil {
		t.Fatalf("exported kinds = %+v, want one episode and one series", exported.Works)
	}
	identity := episode.Identity
	if identity.Parent == nil || identity.Parent.Kind != "series" || identity.Parent.Ref != parentRef {
		t.Fatalf("parent identity = %#v, want kind=series with the retained s: ref", identity.Parent)
	}
	if series.Identity.Parent != nil {
		t.Fatalf("series identity carries parent %#v", series.Identity.Parent)
	}
	var workID, seriesID sql.NullInt64
	if err := s.DB.QueryRow(`SELECT work_id,series_id FROM personal_transfer_ref WHERE ref=?`, identity.Ref).Scan(&workID, &seriesID); err != nil {
		t.Fatal(err)
	}
	if !workID.Valid || seriesID.Valid {
		t.Fatalf("episode ref owner = work %v, series %v; want a work reference", workID, seriesID)
	}
	if err := s.DB.QueryRow(`SELECT work_id,series_id FROM personal_transfer_ref WHERE ref=?`, series.Identity.Ref).Scan(&workID, &seriesID); err != nil {
		t.Fatal(err)
	}
	if !workID.Valid || seriesID.Valid {
		t.Fatalf("series w: ref owner = work %v, series %v; want a work reference", workID, seriesID)
	}
	if err := s.DB.QueryRow(`SELECT work_id,series_id FROM personal_transfer_ref WHERE ref=?`, parentRef).Scan(&workID, &seriesID); err != nil {
		t.Fatal(err)
	}
	if workID.Valid || !seriesID.Valid {
		t.Fatalf("parent ref owner = work %v, series %v; want a catalog_series reference", workID, seriesID)
	}
	var media, runs int
	if err := s.DB.QueryRow(`SELECT (SELECT count(*) FROM media_item),(SELECT count(*) FROM workflow_run)`).Scan(&media, &runs); err != nil {
		t.Fatal(err)
	}
	if media != 0 || runs != 0 {
		t.Fatalf("restore created media=%d runs=%d", media, runs)
	}
}

// Two series works in one backup are numberless identities. Neither the import
// nor the follow-up export may collide on the partial primary/canonical code
// indexes or leak an internal code into the user-visible identity.
func TestTransferTwoSeriesWorksWithEmptyCodesRoundTrip(t *testing.T) {
	s := emptyTransferStore(t)
	firstRating, secondRating := 4, 2
	backup := Backup{Format: "kikoto-user-data", Version: 2, Works: []BackupWork{
		{Identity: &BackupIdentity{Ref: refWith("w", '1'), Kind: "series", Title: "First Series"},
			Rating: &firstRating, Note: "First note"},
		{Identity: &BackupIdentity{Ref: refWith("w", '2'), Kind: "series", Title: "Second Series"},
			Rating: &secondRating, Note: "Second note"},
	}}
	if result := importBackup(t, s, 1, backup); result.ImportedWorks != 2 || result.SkippedWorks != 0 {
		t.Fatalf("restore = %+v", result)
	}
	var codes int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM work WHERE primary_code<>''`).Scan(&codes); err != nil || codes != 0 {
		t.Fatalf("restored series gained %d visible codes, %v", codes, err)
	}
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM logical_work WHERE canonical_code<>''`).Scan(&codes); err != nil || codes != 0 {
		t.Fatalf("restored series gained %d canonical codes, %v", codes, err)
	}
	exported := exportBackup(t, s, 1)
	if len(exported.Works) != 2 {
		t.Fatalf("exported works = %+v", exported.Works)
	}
	for _, w := range exported.Works {
		if w.Identity == nil || w.Identity.Kind != "series" || w.PrimaryCode != "" {
			t.Fatalf("series identity = %#v code=%q", w.Identity, w.PrimaryCode)
		}
	}
}

// A restored series and its episode can be reviewed independently after the
// empty-library restore; neither review overwrites the other.
func TestTransferRestoredSeriesAndEpisodeHaveIndependentReviews(t *testing.T) {
	s := emptyTransferStore(t)
	episodeRating := 2
	backup := Backup{Format: "kikoto-user-data", Version: 2, Works: []BackupWork{{
		Identity: &BackupIdentity{Ref: refWith("w", '1'), Kind: "episode", Title: "Example Episode",
			Parent: &BackupIdentity{Ref: refWith("s", '2'), Kind: "series", Title: "Example Series"}},
		Rating: &episodeRating, Note: "Episode note",
	}}}
	if result := importBackup(t, s, 1, backup); result.ImportedWorks != 1 {
		t.Fatalf("restore = %+v", result)
	}
	var seriesWork, episodeWork int64
	if err := s.DB.QueryRow(`SELECT work_id FROM catalog_series WHERE title='Example Series'`).Scan(&seriesWork); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT id FROM work WHERE parent_work_id=?`, seriesWork).Scan(&episodeWork); err != nil {
		t.Fatal(err)
	}
	execFixture(t, s.DB, `INSERT INTO user_work_state(user_id,work_id,rating,note) VALUES(1,?,5,'Series note')
		ON CONFLICT(user_id,work_id) DO UPDATE SET rating=excluded.rating,note=excluded.note`, seriesWork)
	var episodeRatingAfter, seriesRating int
	var episodeNote, seriesNote string
	if err := s.DB.QueryRow(`SELECT COALESCE(rating,0),COALESCE(note,'') FROM user_work_state WHERE user_id=1 AND work_id=?`, episodeWork).Scan(&episodeRatingAfter, &episodeNote); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT COALESCE(rating,0),COALESCE(note,'') FROM user_work_state WHERE user_id=1 AND work_id=?`, seriesWork).Scan(&seriesRating, &seriesNote); err != nil {
		t.Fatal(err)
	}
	if episodeRatingAfter != 2 || episodeNote != "Episode note" {
		t.Fatalf("episode review = %d/%q, want 2/Episode note", episodeRatingAfter, episodeNote)
	}
	if seriesRating != 5 || seriesNote != "Series note" {
		t.Fatalf("series review = %d/%q, want 5/Series note", seriesRating, seriesNote)
	}
}

// A known catalog series without a work is registered and bound; the restore
// never invents a catalog entry or membership for the empty library.
func TestTransferV2BindsKnownCatalogSeriesWithoutWork(t *testing.T) {
	s := emptyTransferStore(t)
	execFixture(t, s.DB, `INSERT INTO catalog_series(id,title) VALUES(901,'Known Series');`)
	var parentRef string
	if err := s.DB.QueryRow(`SELECT ref FROM personal_transfer_ref WHERE series_id=901`).Scan(&parentRef); err != nil {
		t.Fatal(err)
	}
	backup := Backup{Format: "kikoto-user-data", Version: 2, Works: []BackupWork{{
		Identity: &BackupIdentity{Ref: refWith("w", '2'), Kind: "episode", Title: "Example Episode",
			Parent: &BackupIdentity{Ref: parentRef, Kind: "series", Title: "Known Series"}},
		Note: "Episode note",
	}}}
	if result := importBackup(t, s, 1, backup); result.ImportedWorks != 1 {
		t.Fatalf("restore = %+v", result)
	}
	var seriesWork int64
	if err := s.DB.QueryRow(`SELECT work_id FROM catalog_series WHERE id=901`).Scan(&seriesWork); err != nil {
		t.Fatal(err)
	}
	var parent int64
	if err := s.DB.QueryRow(`SELECT parent_work_id FROM work WHERE title='Example Episode'`).Scan(&parent); err != nil {
		t.Fatal(err)
	}
	if parent != seriesWork {
		t.Fatalf("episode parent = %d, want known series work %d", parent, seriesWork)
	}
	var members, entries int
	if err := s.DB.QueryRow(`SELECT (SELECT count(*) FROM catalog_series_member),(SELECT count(*) FROM source_catalog_entry)`).Scan(&members, &entries); err != nil {
		t.Fatal(err)
	}
	if members != 0 || entries != 0 {
		t.Fatalf("known series restore members=%d entries=%d", members, entries)
	}
}

// Several episodes naming the same parent create that parent once.
func TestTransferV2SharedParentIsCreatedOnce(t *testing.T) {
	s := emptyTransferStore(t)
	parentRef := refWith("s", '3')
	backup := Backup{Format: "kikoto-user-data", Version: 2, Works: []BackupWork{
		{Identity: &BackupIdentity{Ref: refWith("w", '4'), Kind: "episode", Title: "Episode One",
			Parent: &BackupIdentity{Ref: parentRef, Kind: "series", Title: "Example Series"}}, Note: "One"},
		{Identity: &BackupIdentity{Ref: refWith("w", '5'), Kind: "episode", Title: "Episode Two",
			Parent: &BackupIdentity{Ref: parentRef, Kind: "series", Title: "Example Series"}}, Note: "Two"},
	}}
	if result := importBackup(t, s, 1, backup); result.ImportedWorks != 2 || result.SkippedWorks != 0 {
		t.Fatalf("restore = %+v", result)
	}
	var works, series, children int
	if err := s.DB.QueryRow(`SELECT (SELECT count(*) FROM work),(SELECT count(*) FROM catalog_series),
		(SELECT count(*) FROM work WHERE parent_work_id IS NOT NULL)`).Scan(&works, &series, &children); err != nil {
		t.Fatal(err)
	}
	if works != 3 || series != 1 || children != 2 {
		t.Fatalf("shared parent works=%d series=%d children=%d", works, series, children)
	}
}

// Version 2 accepts an optional parent. A malformed parent is a per-entry
// conflict and must not reject other records. A version this build does not
// define rejects the document instead of being guessed.
func TestTransferParentFormatValidation(t *testing.T) {
	base := func(version int, parent *BackupIdentity) []byte {
		raw, err := json.Marshal(Backup{Format: "kikoto-user-data", Version: version, Works: []BackupWork{{
			Identity:        &BackupIdentity{Ref: refWith("w", '1'), Kind: "episode", Title: "Example Episode", Parent: parent},
			ListeningStatus: "none",
		}}})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	valid := &BackupIdentity{Ref: refWith("s", '2'), Kind: "series", Title: "Example Series"}
	if _, err := ParseImport(ImportRequest{Format: "kikoto", Data: base(2, valid)}); err != nil {
		t.Fatalf("valid v2 parent: %v", err)
	}
	const unsupportedVersion = 3
	if _, err := ParseImport(ImportRequest{Format: "kikoto", Data: base(unsupportedVersion, valid)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unsupported version error = %v, want ErrInvalid", err)
	}
	nested := *valid
	nested.Parent = valid
	if _, err := ParseImport(ImportRequest{Format: "kikoto", Data: base(2, &nested)}); err != nil {
		t.Fatalf("nested parent should be blocked per entry, not rejected: %v", err)
	}
	wrongKind := *valid
	wrongKind.Kind = "episode"
	if _, err := ParseImport(ImportRequest{Format: "kikoto", Data: base(2, &wrongKind)}); err != nil {
		t.Fatalf("wrong-kind parent should be blocked per entry, not rejected: %v", err)
	}
}

// Malformed parent shapes block their episode with parent_conflict and leave
// the rest of the document importable.
func TestTransferMalformedParentBlocksOnlyItsEntry(t *testing.T) {
	for _, tc := range []struct {
		name   string
		parent *BackupIdentity
	}{
		{name: "nested", parent: func() *BackupIdentity {
			p := &BackupIdentity{Ref: refWith("s", '1'), Kind: "series", Title: "Example Series"}
			p.Parent = &BackupIdentity{Ref: refWith("s", '2'), Kind: "series", Title: "Nested Series"}
			return p
		}()},
		{name: "episode kind", parent: &BackupIdentity{Ref: refWith("s", '3'), Kind: "episode", Title: "Example Episode"}},
		{name: "work ref", parent: &BackupIdentity{Ref: refWith("w", '4'), Kind: "series", Title: "Example Series"}},
		{name: "empty title", parent: &BackupIdentity{Ref: refWith("s", '5'), Kind: "series", Title: " "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := emptyTransferStore(t)
			backup := Backup{Format: "kikoto-user-data", Version: 2, Works: []BackupWork{
				{Identity: &BackupIdentity{Ref: refWith("w", '6'), Kind: "episode", Title: "Blocked Episode", Parent: tc.parent}},
				{Identity: &BackupIdentity{Ref: refWith("w", '7'), Kind: "work", Title: "Safe Work"}, Note: "Safe note"},
			}}
			result := importBackup(t, s, 1, backup)
			if result.ImportedWorks != 1 || result.SkippedWorks != 1 {
				t.Fatalf("restore = %+v", result)
			}
			blocked := false
			for _, issue := range result.Issues {
				if issue.Reason == "parent_conflict" {
					blocked = true
				}
			}
			if !blocked {
				t.Fatalf("issues = %+v, want a parent conflict", result.Issues)
			}
			var episodes int
			if err := s.DB.QueryRow(`SELECT count(*) FROM work WHERE parent_work_id IS NOT NULL`).Scan(&episodes); err != nil || episodes != 0 {
				t.Fatalf("blocked episode wrote %d relations, %v", episodes, err)
			}
		})
	}
}
