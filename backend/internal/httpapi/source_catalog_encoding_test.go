package httpapi

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path"
	"testing"
	"unicode/utf8"

	"github.com/yexca/kikoto/backend/internal/config"
	"github.com/yexca/kikoto/backend/internal/openlist"
)

const (
	encodingSourceID   = int64(77)
	encodingGeneration = int64(501)
	encodingParent     = "/asmr/橙澄子汉化组"
	replacementName    = "[橙澄子汉化组]\ufffdRJ313527 MP3+lrc"
	cleanSiblingName   = "[橙澄子汉化组]RJ313527 MP3+Lrc"
	unresolvedName     = "[橙澄子汉化组]\ufffdRJ305379 MP3"
)

func seedEncodingScan(t *testing.T, db *sql.DB) {
	t.Helper()
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO file_source(id,code,display_name,source_type,enabled,config_json) VALUES(?,?,?,?,1,'{}')`,
			[]any{encodingSourceID, "encoding-source", "Encoding source", "openlist"}},
		{`INSERT INTO file_source_endpoint(file_source_id,api_url,base_url) VALUES(?,?,?)`,
			[]any{encodingSourceID, "https://source.example.invalid", "https://source.example.invalid"}},
		{`INSERT INTO workflow_run(id,workflow_code,display_name,status,trigger_type) VALUES(?,'source_catalog_index','Index a source','running','manual')`,
			[]any{encodingGeneration}},
		{`INSERT INTO source_catalog_scan(file_source_id,workflow_run_id,options_json) VALUES(?,?,?)`,
			[]any{encodingSourceID, encodingGeneration, mustJSON(defaultCatalogIndexOptions())}},
		{`INSERT INTO source_catalog_directory(file_source_id,path,generation,revision,partition_path,catalog_root,status)
			VALUES(?,?,?,1,?,?,'listing_complete')`,
			[]any{encodingSourceID, encodingParent, encodingGeneration, encodingParent, encodingParent}},
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("seed encoding scan: %v\n%s", err, statement.query)
		}
	}
}

func addEncodingObservation(t *testing.T, db *sql.DB, name, kind string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes)
		VALUES(?,?,?,1,?,10)`, encodingSourceID, encodingParent, name, kind); err != nil {
		t.Fatalf("add observation %q: %v", name, err)
	}
}

func expandEncodingChild(t *testing.T, server *Server, db *sql.DB, name string) bool {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	added, err := server.expandCatalogChildTx(context.Background(), tx, encodingSourceID, encodingGeneration, encodingParent, encodingParent, encodingParent, name)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return added
}

func TestCatalogEncodingNameDamage(t *testing.T) {
	damaged := []string{
		replacementName,
		"[橙澄子汉化组]RJ324820 MP3+Lrc\ufffd",
		"broken\xffname",
	}
	for _, name := range damaged {
		if !catalogNameEncodingDamaged(name) {
			t.Fatalf("%q was not flagged as damaged", name)
		}
	}
	for _, name := range []string{cleanSiblingName, "01 opening.mp3", ""} {
		if catalogNameEncodingDamaged(name) {
			t.Fatalf("%q was flagged as damaged", name)
		}
	}
	if code := catalogEncodingCandidateCode(replacementName); code != "RJ313527" {
		t.Fatalf("candidate code = %q, want RJ313527", code)
	}
	// A replacement character inside the digits leaves fewer than five of them.
	// The code must stay empty: a truncated number is a different work.
	if code := catalogEncodingCandidateCode("RJ3135\ufffd7"); code != "" {
		t.Fatalf("corrupted digit produced the guessed code %q", code)
	}
	// A clean name is never a recovery subject, even when its code is known.
	if code := catalogEncodingCandidateCode(cleanSiblingName); code != "" {
		t.Fatalf("clean name produced a recovery code %q", code)
	}
}

func TestCatalogEncodingOpenListKeepsInvalidBytes(t *testing.T) {
	body := []byte(`{"code":200,"data":{"total":2,"content":[{"name":"broken`)
	body = append(body, 0xff)
	body = append(body, []byte(`name","size":10,"is_dir":true,"raw_url":"https://cdn.example.invalid/a.mp3"},{"name":"literal\ufffdname","size":10,"is_dir":true}]}}`)...)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer upstream.Close()
	page, err := openlist.NewClient(upstream.URL, upstream.Client()).ListPage(context.Background(), "/asmr", 1, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Content) != 2 {
		t.Fatalf("objects = %d, want 2", len(page.Content))
	}
	invalid := page.Content[0]
	if invalid.Name != "broken\xffname" {
		t.Fatalf("invalid name was rewritten: %q", invalid.Name)
	}
	if invalid.NameDamaged == false || utf8.ValidString(invalid.Name) {
		t.Fatal("invalid bytes were not reported as damage")
	}
	// Every ordinary field keeps the value the listing carried.
	if invalid.RawURL != "https://cdn.example.invalid/a.mp3" || invalid.Size != 10 || !invalid.IsDir {
		t.Fatalf("ordinary fields changed for a damaged name: %#v", invalid)
	}
	if !catalogNameEncodingDamaged(invalid.Name) {
		t.Fatal("the scanner did not treat the invalid name as damaged")
	}
	// U+FFFD arrived as valid UTF-8. The client cannot know whether the source
	// meant the character, so only the scanner marks it.
	literal := page.Content[1]
	if literal.NameDamaged {
		t.Fatal("a valid UTF-8 replacement character was reported as invalid bytes")
	}
	if !catalogNameEncodingDamaged(literal.Name) {
		t.Fatal("a replacement character reached the scanner unmarked")
	}
}

func TestCatalogEncodingDamagedDirectoryIsRecordedNotEnqueued(t *testing.T) {
	db := openMigratedTestDB(t)
	seedEncodingScan(t, db)
	addEncodingObservation(t, db, replacementName, "folder")
	addEncodingObservation(t, db, unresolvedName, "folder")
	addEncodingObservation(t, db, "unrelated clean folder", "folder")
	server := NewServer(db, configForEncodingTest(t))
	defer func() { _ = server.Shutdown(context.Background()) }()

	for _, name := range []string{replacementName, unresolvedName} {
		if expandEncodingChild(t, server, db, name) {
			t.Fatalf("a damaged directory name was enqueued for listing: %q", name)
		}
	}
	// The sibling that shares the work code is still only a guess: it must be
	// left to the normal listing pass rather than enqueued from here.
	var enqueued int
	if err := db.QueryRow(`SELECT count(*) FROM source_catalog_directory WHERE file_source_id=? AND path=?`,
		encodingSourceID, path.Join(encodingParent, replacementName)).Scan(&enqueued); err != nil {
		t.Fatal(err)
	}
	if enqueued != 0 {
		t.Fatal("the damaged path reached the directory queue and would be requested")
	}
	var reported, candidate, status string
	if err := db.QueryRow(`SELECT raw_name,candidate_code,status
		FROM source_catalog_encoding_issue WHERE file_source_id=? AND parent_path=? AND raw_name=?`,
		encodingSourceID, encodingParent, replacementName).Scan(&reported, &candidate, &status); err != nil {
		t.Fatal(err)
	}
	if reported != replacementName {
		t.Fatalf("recorded name = %q, want the listed bytes %q", reported, replacementName)
	}
	if candidate != "RJ313527" || status != catalogEncodingUnresolved {
		t.Fatalf("issue = code %q status %q", candidate, status)
	}
	// The sibling directory is untouched by the damaged name: a later listing
	// expansion still queues it under its real path.
	if !expandEncodingChild(t, server, db, cleanSiblingName) {
		t.Fatal("a clean sibling was not enqueued")
	}
	var siblingQueued int
	if err := db.QueryRow(`SELECT count(*) FROM source_catalog_directory WHERE file_source_id=? AND path=? AND generation=?`,
		encodingSourceID, path.Join(encodingParent, cleanSiblingName), encodingGeneration).Scan(&siblingQueued); err != nil {
		t.Fatal(err)
	}
	if siblingQueued != 1 {
		t.Fatal("a clean sibling was blocked by an unrelated damaged name")
	}
}

func TestCatalogEncodingCleanNameWritesNoIssue(t *testing.T) {
	db := openMigratedTestDB(t)
	seedEncodingScan(t, db)
	addEncodingObservation(t, db, cleanSiblingName, "folder")
	server := NewServer(db, configForEncodingTest(t))
	defer func() { _ = server.Shutdown(context.Background()) }()

	if !expandEncodingChild(t, server, db, cleanSiblingName) {
		t.Fatal("a clean name was not enqueued")
	}
	var issues int
	if err := db.QueryRow(`SELECT count(*) FROM source_catalog_encoding_issue WHERE file_source_id=?`, encodingSourceID).Scan(&issues); err != nil {
		t.Fatal(err)
	}
	if issues != 0 {
		t.Fatalf("a normal UTF-8 name wrote %d issue rows", issues)
	}
	var queued int
	if err := db.QueryRow(`SELECT count(*) FROM source_catalog_directory WHERE file_source_id=? AND path=?`,
		encodingSourceID, path.Join(encodingParent, cleanSiblingName)).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Fatal("the clean directory did not enter the queue")
	}
}

func TestCatalogEncodingRescanKeepsOneUnresolvedRow(t *testing.T) {
	db := openMigratedTestDB(t)
	seedEncodingScan(t, db)
	addEncodingObservation(t, db, replacementName, "folder")
	server := NewServer(db, configForEncodingTest(t))
	defer func() { _ = server.Shutdown(context.Background()) }()

	for pass := 0; pass < 3; pass++ {
		if expandEncodingChild(t, server, db, replacementName) {
			t.Fatalf("pass %d enqueued the damaged name", pass)
		}
	}
	var rows int
	var firstID, lastID int64
	if err := db.QueryRow(`SELECT count(*),min(id),max(id) FROM source_catalog_encoding_issue
		WHERE file_source_id=? AND parent_path=? AND raw_name=?`,
		encodingSourceID, encodingParent, replacementName).Scan(&rows, &firstID, &lastID); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || firstID != lastID {
		t.Fatalf("rescans produced %d rows (ids %d..%d), want one", rows, firstID, lastID)
	}
	// A duplicate observation row is overwritten by the unique key, not added.
	unresolved, err := server.catalogEncodingUnresolvedCount(context.Background(), encodingSourceID)
	if err != nil {
		t.Fatal(err)
	}
	if unresolved != 1 {
		t.Fatalf("unresolved count = %d, want 1", unresolved)
	}
}

func TestCatalogEncodingFixListOnlyUnresolved(t *testing.T) {
	db := openMigratedTestDB(t)
	seedEncodingScan(t, db)
	addEncodingObservation(t, db, unresolvedName, "folder")
	addEncodingObservation(t, db, replacementName, "folder")
	originalBytes := "broken\xffname"
	addEncodingObservation(t, db, originalBytes, "folder")
	server := NewServer(db, configForEncodingTest(t))
	defer func() { _ = server.Shutdown(context.Background()) }()

	for _, name := range []string{unresolvedName, replacementName, originalBytes} {
		if expandEncodingChild(t, server, db, name) {
			t.Fatalf("damaged name was enqueued: %q", name)
		}
	}
	// An operator decision is not a scanner write, but the fix list respects it.
	if _, err := db.Exec(`UPDATE source_catalog_encoding_issue SET status=?,resolved_path='/asmr/fixed'
		WHERE file_source_id=? AND raw_name=?`, catalogEncodingIgnored, encodingSourceID, replacementName); err != nil {
		t.Fatal(err)
	}
	issues, err := server.catalogEncodingFixList(context.Background(), encodingSourceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 2 {
		t.Fatalf("fix list has %d rows, want the two unresolved names: %#v", len(issues), issues)
	}
	byName := map[string]catalogEncodingIssueRecord{}
	for _, issue := range issues {
		byName[issue.RawName] = issue
	}
	if _, ignored := byName[replacementName]; ignored {
		t.Fatal("an ignored name stayed on the fix list")
	}
	invalid, ok := byName[originalBytes]
	if !ok {
		t.Fatalf("the invalid-byte name is missing: %#v", issues)
	}
	// raw_name stores the listed bytes unchanged; the invalid byte is not
	// replaced by a guess and not stripped.
	if invalid.RawName != originalBytes {
		t.Fatalf("raw_name was rewritten: %q", invalid.RawName)
	}
	if _, ok := byName[unresolvedName]; !ok {
		t.Fatalf("the unresolved replacement name is missing: %#v", issues)
	}
	unresolved, err := server.catalogEncodingUnresolvedCount(context.Background(), encodingSourceID)
	if err != nil {
		t.Fatal(err)
	}
	if unresolved != 2 {
		t.Fatalf("unresolved count = %d, want 2", unresolved)
	}
	recovered, err := server.catalogEncodingRecoveredCount(context.Background(), encodingSourceID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered != 0 {
		t.Fatalf("recovered count = %d, want 0", recovered)
	}
}

func TestCatalogEncodingUnresolvedDoesNotMakeRunPartial(t *testing.T) {
	db := openMigratedTestDB(t)
	seedEncodingScan(t, db)
	addEncodingObservation(t, db, unresolvedName, "folder")
	server := NewServer(db, configForEncodingTest(t))
	defer func() { _ = server.Shutdown(context.Background()) }()

	if expandEncodingChild(t, server, db, unresolvedName) {
		t.Fatal("a damaged name without a sibling was enqueued")
	}
	// The parent listing itself is complete, and its root already holds audio,
	// which is the case where a leftover pending folder row would keep the
	// whole range incomplete.
	if _, err := db.Exec(`UPDATE source_catalog_directory SET status='complete' WHERE file_source_id=? AND path=?`, encodingSourceID, encodingParent); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO source_catalog_audio_root(file_source_id,generation,catalog_root) VALUES(?,?,?)`,
		encodingSourceID, encodingGeneration, encodingParent); err != nil {
		t.Fatal(err)
	}
	// The playable row is already published, so a true result below could only
	// come from the damaged folder row that must not stay pending.
	if _, err := db.Exec(`INSERT INTO source_catalog_observation(file_source_id,directory_path,name,revision,kind,size_bytes,publish_pending,published_generation)
		VALUES(?,?,?,1,'audio',10,0,?)`, encodingSourceID, encodingParent, "01 track.mp3", encodingGeneration); err != nil {
		t.Fatal(err)
	}
	progress, err := server.sourceCatalogProgress(context.Background(), encodingSourceID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if progress.Failed != 0 || progress.Restricted != 0 || progress.Empty != 0 {
		t.Fatalf("an encoding issue leaked into the run counters: failed=%d restricted=%d empty=%d",
			progress.Failed, progress.Restricted, progress.Empty)
	}
	if progress.EncodingUnresolved != 1 {
		t.Fatalf("progress encoding count = %d, want the independent count of 1", progress.EncodingUnresolved)
	}
	incompleteTx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	incomplete, err := catalogRootIncomplete(context.Background(), incompleteTx, encodingSourceID, encodingGeneration, encodingParent)
	// The test database holds a single connection; a lingering read transaction
	// would block every later query.
	if rollbackErr := incompleteTx.Rollback(); rollbackErr != nil {
		t.Fatal(rollbackErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	if incomplete {
		t.Fatal("an unresolved encoding issue kept the range incomplete")
	}
	var rootEmpty int
	if err = db.QueryRow(`SELECT EXISTS(SELECT 1 FROM source_catalog_directory
		WHERE file_source_id=? AND generation=? AND status='empty' AND error_code<>'not_found' AND path='/')`,
		encodingSourceID, encodingGeneration).Scan(&rootEmpty); err != nil {
		t.Fatal(err)
	}
	// These are the three inputs the worker's settle decision reads. With all
	// three clear, the run settles as succeeded, not partial.
	if progress.Failed+progress.Restricted != 0 || rootEmpty != 0 {
		t.Fatal("the run would settle as partial")
	}
}

func configForEncodingTest(t *testing.T) config.Config {
	t.Helper()
	return config.Config{
		Mode:         config.ModeProduction,
		RootUsername: "synthetic-user",
		DataRoot:     t.TempDir(),
		CacheRoot:    t.TempDir(),
	}
}
