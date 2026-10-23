package httpapi

import (
	"net/http"
	"path"
	"strings"
	"testing"
)

// A zero-padded counter is still a track counter when the whole directory is
// one contiguous run. "T001/T002" stay separate works, but "Scene 01..06" is a
// single release split into six tracks and must not become six cards.
func TestCatalogSplitModeTreatsZeroPaddedRunAsOneWork(t *testing.T) {
	names := []string{"Secret 01.mp3", "Secret 02.mp3", "Secret 03.mp3", "Secret 04.mp3", "Secret 05.mp3", "Secret 06.mp3"}
	if mode := catalogWorkSplitModeNames(names); mode != catalogWorkSplitSingle {
		t.Fatalf("mode=%q, want one work", mode)
	}
}

// A format copy may carry a companion subtitle suffix or a circle/code prefix.
// These are still the release's audio folder, not a separate work title.
func TestCatalogFormatCopyRecognizesCompanionSuffixes(t *testing.T) {
	for _, name := range []string{
		"MP3+lrc",
		"MP3+LRC",
		"WAV+lrc",
		"[团队]RJ00000001 MP3+lrc",
	} {
		if !catalogFormatDirectory(name) {
			t.Errorf("%q is not recognized as a format copy", name)
		}
	}
	for _, name := range []string{"MP3 Collection", "Audio Drama", "Example Release 2", "My MP3"} {
		if catalogFormatDirectory(name) {
			t.Errorf("%q must stay a title", name)
		}
	}
	for _, tc := range []struct {
		name, want string
	}{
		{"Episode 1 (MP3+lrc)", "Episode 1"},
		{"第1集 (WAV+lrc)", "第1集"},
	} {
		got, ok := catalogEpisodeFormatCopy(tc.name)
		if !ok || got != tc.want {
			t.Errorf("catalogEpisodeFormatCopy(%q)=(%q,%v), want (%q,true)", tc.name, got, ok, tc.want)
		}
	}
}

// A format marker may be a bracketed tag among circle/code tags, and the
// release title may follow the tag. Only an audio format with a companion
// subtitle/lyrics marker is a format copy; arbitrary "+ Lyrics" text is not.
func TestCatalogFormatCopyRecognizesBracketFormatTag(t *testing.T) {
	for _, name := range []string{
		"[团队][RJ00000001][MP3+lrc]标题",
		"[MP3 320kbps+lrc]标题",
		"[WAV+lrc] 标题",
	} {
		if !catalogFormatDirectory(name) {
			t.Errorf("%q is not recognized as a format copy", name)
		}
	}
	for _, name := range []string{
		"[My MP3] Title",
		"[Audio Drama]",
		"Song + Lyrics",
		"[MP3 Collection] Title",
	} {
		if catalogFormatDirectory(name) {
			t.Errorf("%q must stay a title", name)
		}
	}
	for _, tc := range []struct {
		name, want string
	}{
		{"Episode 1 [MP3+lrc] Title", "Episode 1"},
		{"第1集 [WAV 24bit+lrc] 标题", "第1集"},
	} {
		got, ok := catalogEpisodeFormatCopy(tc.name)
		if !ok || got != tc.want {
			t.Errorf("catalogEpisodeFormatCopy(%q)=(%q,%v), want (%q,true)", tc.name, got, ok, tc.want)
		}
	}
}

// A wrapper that repeats the release code is part of the release even when the
// wrapper has no direct audio. The outer release must keep the card, not the
// inner wrapper.
func TestCatalogNestedSameCodeWrapperKeepsOuterRelease(t *testing.T) {
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/":        {looseDir("Creator")},
		"/Creator": {looseDir("[RJ00000001] Release")},
		"/Creator/[RJ00000001] Release": {
			looseDir("[RJ00000001] MP3+lrc"),
		},
		"/Creator/[RJ00000001] Release/[RJ00000001] MP3+lrc": {
			looseDir("[RJ00000001] Main"),
		},
		"/Creator/[RJ00000001] Release/[RJ00000001] MP3+lrc/[RJ00000001] Main": {
			looseAudio("track.mp3"),
		},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	cards := f.cards(t)
	if len(cards) != 1 {
		t.Fatalf("cards=%v, want one outer release card", cards)
	}
	card := cards["[RJ00000001] Release"]
	if card == nil {
		t.Fatalf("outer release card missing: %v", cards)
	}
	if got := countCatalogAudio(f.tracks(t, card)); got != 1 {
		t.Fatalf("tracks=%d, want 1", got)
	}
}

// An auxiliary folder belongs to the release even when it is the only place
// with playable audio. The release keeps the card title.
func TestCatalogAuxiliaryOnlyReleaseKeepsReleaseTitle(t *testing.T) {
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/":           {looseDir("Release")},
		"/Release":    {looseDir("特典")},
		"/Release/特典": {looseAudio("track.mp3")},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	cards := f.cards(t)
	if len(cards) != 1 {
		t.Fatalf("cards=%v, want one release card", cards)
	}
	card := cards["Release"]
	if card == nil {
		t.Fatalf("release card missing: %v", cards)
	}
	if got := countCatalogAudio(f.tracks(t, card)); got != 1 {
		t.Fatalf("tracks=%d, want 1", got)
	}
}

// A coded bonus directory below an auxiliary wrapper still belongs to the
// release; it must not become a second card with the same code.
func TestCatalogNestedCodedBonusStaysInRelease(t *testing.T) {
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/":        {looseDir("Creator")},
		"/Creator": {looseDir("RJ00000001 Release")},
		"/Creator/RJ00000001 Release": {
			looseAudio("track.mp3"),
			looseDir("特典"),
		},
		"/Creator/RJ00000001 Release/特典": {
			looseDir("RJ00000001 Bonus"),
		},
		"/Creator/RJ00000001 Release/特典/RJ00000001 Bonus": {
			looseAudio("bonus.mp3"),
		},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	cards := f.cards(t)
	if len(cards) != 1 {
		t.Fatalf("cards=%v, want one release card", cards)
	}
	card := cards["RJ00000001 Release"]
	if card == nil {
		t.Fatalf("release card missing: %v", cards)
	}
	if got := countCatalogAudio(f.tracks(t, card)); got != 2 {
		t.Fatalf("tracks=%d, want 2", got)
	}
}

// Chapter, Part, Track, Disc and Scene number the pieces of one release. An
// explicit series label must not turn those pieces into episodes.
func TestCatalogEpisodeEvidenceIgnoresStructuralChildrenUnderASeriesLabel(t *testing.T) {
	for _, name := range []string{"Chapter 1", "Part 2", "Track 3", "Disc 1", "Scene 4"} {
		if _, ok := catalogEpisodeEvidence("/Series: Example/" + name); ok {
			t.Fatalf("%q was taken for an episode", name)
		}
	}
	if _, ok := catalogEpisodeEvidence("/Series: Example/Episode 1"); !ok {
		t.Fatal("a real episode marker stopped being an episode")
	}
}

// A prefix-split creator folder may hold unrelated uncoded recordings next to
// the coded works. They are separate works, not one card that mixes their
// evaluation.
func TestCatalogPrefixSplitKeepsUncodedRecordingsSeparate(t *testing.T) {
	names := []string{"RJ00000011 First.mp3", "RJ00000011 First (2).mp3", "Second Work.mp3", "Third Work.mp3"}
	plan := newCatalogSplitPlan("/Creator", names, catalogWorkSplitPrefix)
	second, ok := plan.targetFor("Second Work.mp3")
	if !ok {
		t.Fatal("uncoded recording stayed on the parent card")
	}
	third, ok := plan.targetFor("Third Work.mp3")
	if !ok {
		t.Fatal("uncoded recording stayed on the parent card")
	}
	if second.Root == third.Root {
		t.Fatalf("two uncoded works share a card: %q", second.Root)
	}
}

// A format copy beside its episode joins the episode card. Its files live in a
// different directory, so they must stay addressable: a relative path is never
// the absolute source path.
func TestCatalogFormatCopyKeepsAddressableRelativePaths(t *testing.T) {
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/":                                {looseDir("Series: Example")},
		"/Series: Example":                 {looseDir("Episode 1"), looseDir("Episode 1 (MP3)")},
		"/Series: Example/Episode 1":       {looseAudioSized("lossless.flac", 500)},
		"/Series: Example/Episode 1 (MP3)": {looseAudioSized("copy.mp3", 200)},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	var entryID int64
	if err := f.server.db.QueryRow(`SELECT id FROM source_catalog_entry WHERE root_path='/Series: Example/Episode 1' AND relocated_to_id IS NULL`).Scan(&entryID); err != nil {
		t.Fatal(err)
	}
	var cards int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_entry WHERE root_path LIKE '/Series: Example/Episode 1%' AND relocated_to_id IS NULL`).Scan(&cards); err != nil || cards != 1 {
		t.Fatalf("cards=%d err=%v, want one episode card", cards, err)
	}
	rows, err := f.server.db.Query(`SELECT relative_path FROM source_catalog_file WHERE catalog_entry_id=? AND kind='audio' ORDER BY relative_path`, entryID)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for rows.Next() {
		var rel string
		if err = rows.Scan(&rel); err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(rel, "/") {
			t.Fatalf("relative path is absolute: %q", rel)
		}
		found[rel] = true
	}
	_ = rows.Close()
	if len(found) != 2 {
		t.Fatalf("files=%v, want both copies on the episode", found)
	}
	var root, sourceRoot string
	if err = f.server.db.QueryRow(`SELECT root_path,source_root FROM source_catalog_entry WHERE id=?`, entryID).Scan(&root, &sourceRoot); err != nil {
		t.Fatal(err)
	}
	if sourceRoot == "" {
		sourceRoot = root
	}
	for rel := range found {
		resolved := path.Join(sourceRoot, rel)
		if !strings.HasPrefix(resolved, "/Series: Example/") {
			t.Fatalf("file %q resolves to %q", rel, resolved)
		}
	}
}

// A format or disc directory never owns a work by itself. When it is reached
// before its release has an identity it still has to publish into that release
// instead of becoming one card per format.
func TestCatalogFormatDirectoryPublishesIntoItsRelease(t *testing.T) {
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/":                        {looseDir("Release")},
		"/Release":                 {looseDir("RJ00000011")},
		"/Release/RJ00000011":      {looseDir("MP3"), looseDir("FLAC")},
		"/Release/RJ00000011/MP3":  {looseAudioSized("a.mp3", 100)},
		"/Release/RJ00000011/FLAC": {looseAudioSized("b.flac", 200)},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	var cards int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_entry WHERE relocated_to_id IS NULL AND file_source_id IS NOT NULL`).Scan(&cards); err != nil || cards != 1 {
		var rows *int
		_ = rows
		t.Fatalf("cards=%d err=%v, want one release card", cards, err)
	}
	var root string
	if err := f.server.db.QueryRow(`SELECT root_path FROM source_catalog_entry WHERE relocated_to_id IS NULL AND file_source_id IS NOT NULL`).Scan(&root); err != nil {
		t.Fatal(err)
	}
	if root != "/Release/RJ00000011" {
		t.Fatalf("card root=%q, want the release", root)
	}
}

// A format directory inside a coded release keeps publishing into that
// release. A nested bonus folder carrying the same code is a copy, not a
// second work, so it must not disown the release's own format directories.
func TestCatalogFormatDirectoryStaysInItsCodedRelease(t *testing.T) {
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/":        {looseDir("Creator")},
		"/Creator": {looseDir("RJ00000001 Release")},
		"/Creator/RJ00000001 Release": {
			looseAudioSized("track.mp3", 100),
			looseDir("MP3"),
			looseDir("RJ00000001 Bonus"),
		},
		"/Creator/RJ00000001 Release/MP3":              {looseAudioSized("01 first.mp3", 200)},
		"/Creator/RJ00000001 Release/RJ00000001 Bonus": {looseAudioSized("bonus.mp3", 300)},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	var cards int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_entry WHERE relocated_to_id IS NULL AND file_source_id IS NOT NULL`).Scan(&cards); err != nil || cards != 1 {
		t.Fatalf("cards=%d err=%v, want one release card", cards, err)
	}
	var tracks int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_file f JOIN source_catalog_entry e ON e.id=f.catalog_entry_id WHERE e.root_path='/Creator/RJ00000001 Release' AND f.kind='audio'`).Scan(&tracks); err != nil || tracks != 3 {
		t.Fatalf("tracks=%d err=%v, want 3", tracks, err)
	}
}

// A release whose only audio sits in a format directory still has a card. A
// sibling coded folder must not push its format directory out and leave the
// release empty.
func TestCatalogReleaseKeepsCardWhenAudioIsInAFormatDirectory(t *testing.T) {
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/":        {looseDir("Creator")},
		"/Creator": {looseDir("RJ00000001 Release")},
		"/Creator/RJ00000001 Release": {
			looseDir("MP3"),
			looseDir("RJ00000002 Extra"),
		},
		"/Creator/RJ00000001 Release/MP3":              {looseAudioSized("01 first.mp3", 200)},
		"/Creator/RJ00000001 Release/RJ00000002 Extra": {map[string]any{"name": "cover.jpg", "size": 10}},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	var root, status string
	if err := f.server.db.QueryRow(`SELECT root_path,availability_status FROM source_catalog_entry WHERE root_path='/Creator/RJ00000001 Release' AND relocated_to_id IS NULL`).Scan(&root, &status); err != nil {
		t.Fatalf("release card is missing: %v", err)
	}
	if status != "available" {
		t.Fatalf("release status=%q", status)
	}
	var tracks int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_file f JOIN source_catalog_entry e ON e.id=f.catalog_entry_id WHERE e.root_path=? AND f.kind='audio'`, root).Scan(&tracks); err != nil || tracks != 1 {
		t.Fatalf("tracks=%d err=%v", tracks, err)
	}
}

// Deleting the episode must not hand its identity to the remaining format
// copy. The copy becomes its own card; the episode card turns missing.
func TestCatalogDeletedEpisodeDoesNotReviveThroughItsFormatCopy(t *testing.T) {
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/":                     {looseDir("Show")},
		"/Show":                 {looseDir("Episode 1"), looseDir("Episode 1 (MP3)")},
		"/Show/Episode 1":       {looseAudioSized("audio.flac", 900)},
		"/Show/Episode 1 (MP3)": {looseAudioSized("audio.mp3", 300)},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("first scan = %#v", p)
	}
	var episodeID int64
	if err := f.server.db.QueryRow(`SELECT id FROM source_catalog_entry WHERE root_path='/Show/Episode 1' AND relocated_to_id IS NULL`).Scan(&episodeID); err != nil {
		t.Fatal(err)
	}
	f.updates(map[string][]map[string]any{
		"/":                     {looseDir("Show")},
		"/Show":                 {looseDir("Episode 1 (MP3)")},
		"/Show/Episode 1 (MP3)": {looseAudioSized("audio.mp3", 300)},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100,"force":true}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("rescan = %#v", p)
	}
	var status string
	if err := f.server.db.QueryRow(`SELECT availability_status FROM source_catalog_entry WHERE id=?`, episodeID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "missing" {
		t.Fatalf("deleted episode status=%q, want missing", status)
	}
	// A missing card keeps its metadata for provenance, but the copy must own
	// its own files: the deleted episode must not advertise a path that now
	// belongs to another card.
	var copyFiles int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_file WHERE catalog_entry_id=(SELECT id FROM source_catalog_entry WHERE root_path='/Show/Episode 1 (MP3)' AND relocated_to_id IS NULL) AND kind='audio'`).Scan(&copyFiles); err != nil || copyFiles != 1 {
		t.Fatalf("copy card files=%d: %v", copyFiles, err)
	}
	var copyID int64
	if err := f.server.db.QueryRow(`SELECT id FROM source_catalog_entry WHERE root_path='/Show/Episode 1 (MP3)' AND relocated_to_id IS NULL`).Scan(&copyID); err != nil {
		t.Fatalf("format copy has no card: %v", err)
	}
	if copyID == episodeID {
		t.Fatal("format copy reused the deleted episode identity")
	}
}

// A card an older generation published for an episode's format copy retires
// once the episode card owns that audio.
func TestCatalogStaleEpisodeFormatCardRetires(t *testing.T) {
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/":                     {looseDir("Show")},
		"/Show":                 {looseDir("Episode 1"), looseDir("Episode 1 (MP3)")},
		"/Show/Episode 1":       {looseAudioSized("audio.flac", 900)},
		"/Show/Episode 1 (MP3)": {looseAudioSized("audio.mp3", 300)},
	})
	if _, err := f.server.db.Exec(`INSERT INTO source_catalog_entry(file_source_id,root_path,title,last_seen_generation,discovered_generation)
		SELECT id,'/Show/Episode 1 (MP3)','Episode 1 (MP3)',0,0 FROM file_source`); err != nil {
		t.Fatal(err)
	}
	var staleID int64
	if err := f.server.db.QueryRow(`SELECT id FROM source_catalog_entry WHERE root_path='/Show/Episode 1 (MP3)'`).Scan(&staleID); err != nil {
		t.Fatal(err)
	}
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("scan = %#v", p)
	}
	var live int
	if err := f.server.db.QueryRow(`SELECT count(*) FROM source_catalog_entry WHERE root_path='/Show/Episode 1 (MP3)' AND relocated_to_id IS NULL AND file_source_id IS NOT NULL`).Scan(&live); err != nil || live != 0 {
		t.Fatalf("stale format card still live: %d %v", live, err)
	}
	var target int64
	if err := f.server.db.QueryRow(`SELECT COALESCE(relocated_to_id,0) FROM source_catalog_entry WHERE id=?`, staleID).Scan(&target); err != nil || target == 0 {
		t.Fatalf("stale format card has no alias target: %v %v", target, err)
	}
}

// A CJK chapter directory below an explicitly labelled series is a chapter of
// one work, not an episode.
func TestCatalogCJKChapterUnderSeriesLabelIsNotAnEpisode(t *testing.T) {
	for _, name := range []string{"第1章", "第2節", "第3节", "第4幕"} {
		if _, ok := catalogEpisodeEvidence("/Series: Example/" + name); ok {
			t.Fatalf("%q was taken for an episode", name)
		}
	}
	for _, name := range []string{"第1話", "第2集", "第3回"} {
		if _, ok := catalogEpisodeEvidence("/Series: Example/" + name); !ok {
			t.Fatalf("%q stopped being an episode", name)
		}
	}
}
