package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/yexca/kikoto/backend/internal/library"
	"github.com/yexca/kikoto/backend/internal/storage"
	"github.com/yexca/kikoto/backend/internal/translation"
)

// A trigram index cannot answer fewer than three characters, so a 1-2
// character search walks its complete effective range. This benchmark measures
// that path through the API's own generator at the size of a large source. It is
// opt-in and reports latency without asserting it; the threshold judgement
// belongs to the release evidence.
const (
	shortQueryPerfEnv     = "KIKOTO_SHORT_QUERY_PERF"
	shortQueryEntitiesEnv = "KIKOTO_SHORT_QUERY_ENTITIES"
	shortQuerySamplesEnv  = "KIKOTO_SHORT_QUERY_SAMPLES"
	shortQueryWarmupEnv   = "KIKOTO_SHORT_QUERY_WARMUP"
	shortQueryOutEnv      = "KIKOTO_SHORT_QUERY_OUT"
	shortQueryEntities    = 114000
)

type shortQueryReport struct {
	Label       string             `json:"label"`
	StartedAt   string             `json:"startedAt"`
	FinishedAt  string             `json:"finishedAt"`
	Environment map[string]any     `json:"environment"`
	Fixture     map[string]int     `json:"fixture"`
	Cases       []shortQueryCase   `json:"cases"`
	Thresholds  map[string]float64 `json:"thresholds"`
	Notes       []string           `json:"notes"`
}

type shortQueryCase struct {
	Language string          `json:"language"`
	Needle   string          `json:"needle"`
	Matches  int             `json:"matches"`
	HitScan  shortQueryStats `json:"hitScan"`
	HitSet   shortQueryStats `json:"hitSet"`
	Complete shortQueryStats `json:"complete"`
	Page     shortQueryStats `json:"page"`
	HitPlan  []string        `json:"hitPlan"`
	Plan     []string        `json:"plan"`
}

type shortQueryStats struct {
	Samples   int       `json:"samples"`
	Rows      int       `json:"rows"`
	MinMs     float64   `json:"minMs"`
	MeanMs    float64   `json:"meanMs"`
	P50Ms     float64   `json:"p50Ms"`
	P95Ms     float64   `json:"p95Ms"`
	P99Ms     float64   `json:"p99Ms"`
	MaxMs     float64   `json:"maxMs"`
	Durations []float64 `json:"durationsMs"`
}

func TestShortQueryPerformance(t *testing.T) {
	if os.Getenv(shortQueryPerfEnv) != "1" {
		t.Skip("set " + shortQueryPerfEnv + "=1 to measure the complete short-query set")
	}
	entities := shortQueryEnvInt(t, shortQueryEntitiesEnv, shortQueryEntities)
	samples := shortQueryEnvInt(t, shortQuerySamplesEnv, 20)
	warmup := shortQueryEnvInt(t, shortQueryWarmupEnv, 3)
	startedAt := time.Now()
	db := openMigratedTestDBWithProductionPool(t)
	report := shortQueryReport{
		Label:      "short-query-complete-set",
		StartedAt:  startedAt.UTC().Format(time.RFC3339),
		Thresholds: map[string]float64{"completeP95Ms": 100, "pageP95Ms": 100},
		Notes: []string{
			"the SQL is the API's own generator, including the admission row",
			"samples are sequential (concurrency 1) over one pooled connection set",
			"complete is limit=0, the shape a paginated total needs; page is one card page",
			"latency is reported, not asserted: CI never fails on the 100 ms threshold",
			"percentiles use nearest-rank on the sorted sample: index ceil(q*n)-1",
		},
	}
	seedStarted := time.Now()
	sourceID := shortQuerySeed(t, db, entities)
	report.Environment = map[string]any{
		"goos": runtime.GOOS, "goarch": runtime.GOARCH, "goVersion": runtime.Version(),
		"numCPU": runtime.NumCPU(), "memTotalKiB": catalog42KMemTotalKiB(),
		"seedSeconds": time.Since(seedStarted).Seconds(),
	}
	if err := storage.OptimizeStatistics(context.Background(), db); err != nil {
		t.Fatalf("optimize statistics: %v", err)
	}
	report.Environment["databaseBytes"] = catalog42KDatabaseBytes(t, db)
	report.Fixture = map[string]int{
		"entities": entities, "files": entities, "translationRows": entities * 4,
	}

	configKey := translation.ConfigKey(translation.Config{Model: "short-query-bench"})
	targets := []library.DisplaySearchTarget{
		{Language: translation.SourceEnglish, Query: "Hy"},
		{Language: translation.SourceHans, Query: "催眠"},
		{Language: translation.SourceJapanese, Query: "催眠"},
		{Language: translation.SourceKorean, Query: "최면"},
		// A needle that matches nothing walks the whole range without an early
		// exit, which is the worst case for any capped-count design.
		{Language: translation.SourceHans, Query: "龘龘"},
	}
	for _, target := range targets {
		report.Cases = append(report.Cases, shortQueryMeasure(t, db, sourceID, target, configKey, warmup, samples))
	}
	report.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if out := os.Getenv(shortQueryOutEnv); out != "" {
		if err = os.WriteFile(out, append(encoded, '\n'), 0o644); err != nil {
			t.Fatalf("write report: %v", err)
		}
		t.Logf("report written to %s", out)
	}
	for _, item := range report.Cases {
		t.Logf("%-9s %-6q matches=%d hit p95=%.1f card p95=%.1f page p95=%.1f",
			item.Language, item.Needle, item.Matches, item.HitSet.P95Ms, item.Complete.P95Ms, item.Page.P95Ms)
	}
}

// shortQueryMeasure times the two shapes the API asks for: the complete hit set
// behind a paginated total, and one page of cards.
func shortQueryMeasure(t *testing.T, db *sql.DB, sourceID int64, target library.DisplaySearchTarget, configKey string, warmup, samples int) shortQueryCase {
	t.Helper()
	prefix, from, args := openListCatalogCardsQueryLocalized(sourceID, target.Query, []library.DisplaySearchTarget{target}, configKey)
	result := shortQueryCase{Language: target.Language, Needle: target.Query}

	// The hit set is the short query itself: the complete effective range with
	// the current-row and admission predicates, capped only at the return.
	hitPrefix, hitArgs := localizedCatalogSearchSQL([]library.DisplaySearchTarget{target}, sourceID, true, configKey)
	// The raw scan is the same generator without the catalog id mapping, so the
	// difference between it and the hit set is the cost of turning a translation
	// row into a card id.
	scanSQL, scanArgs := library.ShortTranslationSearchSQL(
		[]library.DisplaySearchTarget{target},
		[]library.ShortSearchArm{
			{EntityKind: "entry", Fields: []string{"title"}, SourceID: sourceID, ConfigKey: configKey},
			{EntityKind: "series", Fields: []string{"title"}, SourceID: sourceID, ConfigKey: configKey},
		}, 0)
	scanCountSQL := `SELECT count(*) FROM (` + scanSQL + `)`
	result.HitScan = shortQuerySample(samples, func() error {
		var total int
		return db.QueryRowContext(context.Background(), scanCountSQL, scanArgs...).Scan(&total)
	})
	hitSQL := hitPrefix + `SELECT count(*) FROM search_hits`
	for range warmup {
		if err := db.QueryRowContext(context.Background(), hitSQL, hitArgs...).Scan(&result.Matches); err != nil {
			t.Fatalf("%s warm hit set: %v", target.Language, err)
		}
	}
	result.HitSet = shortQuerySample(samples, func() error {
		var total int
		return db.QueryRowContext(context.Background(), hitSQL, hitArgs...).Scan(&total)
	})
	result.HitSet.Rows = result.Matches
	result.HitPlan = shortQueryPlan(t, db, hitSQL, hitArgs)

	completeSQL := catalogStatementPrefix(prefix) + `SELECT count(*) ` + from
	for range warmup {
		if err := db.QueryRowContext(context.Background(), completeSQL, args...).Scan(&result.Matches); err != nil {
			t.Fatalf("%s warm complete: %v", target.Language, err)
		}
	}
	result.Complete = shortQuerySample(samples, func() error {
		var total int
		return db.QueryRowContext(context.Background(), completeSQL, args...).Scan(&total)
	})
	result.Complete.Rows = result.Matches

	pageSQL := catalogStatementPrefix(prefix) + `SELECT id ` + from +
		` ORDER BY exact DESC,updated_at DESC,series_id DESC,id DESC LIMIT ? OFFSET ?`
	pageArgs := append(append([]any{}, args...), 200, 0)
	result.Page = shortQuerySample(samples, func() error {
		rows, err := db.QueryContext(context.Background(), pageSQL, pageArgs...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		count := 0
		for rows.Next() {
			count++
		}
		result.Page.Rows = count
		return rows.Err()
	})
	result.Plan = shortQueryPlan(t, db, completeSQL, args)
	return result
}

func shortQueryPlan(t *testing.T, db *sql.DB, sqlText string, args []any) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+sqlText, args...)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	defer func() { _ = rows.Close() }()
	plan := []string{}
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err = rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatalf("plan row: %v", err)
		}
		plan = append(plan, detail)
	}
	return plan
}

func shortQuerySample(samples int, run func() error) shortQueryStats {
	stats := shortQueryStats{Samples: samples, Durations: make([]float64, 0, samples)}
	for range samples {
		started := time.Now()
		if err := run(); err != nil {
			stats.Durations = append(stats.Durations, -1)
			continue
		}
		stats.Durations = append(stats.Durations, float64(time.Since(started).Microseconds())/1000)
	}
	sorted := append([]float64{}, stats.Durations...)
	sort.Float64s(sorted)
	// A failed sample sorts first; reporting it as a fast query would hide a
	// broken shape behind a good number.
	if sorted[0] < 0 {
		stats.Samples = 0
		return stats
	}
	stats.MinMs = sorted[0]
	stats.MaxMs = sorted[len(sorted)-1]
	sum := 0.0
	for _, value := range sorted {
		sum += value
	}
	stats.MeanMs = sum / float64(len(sorted))
	stats.P50Ms = sorted[shortQueryRank(len(sorted), 0.50)]
	stats.P95Ms = sorted[shortQueryRank(len(sorted), 0.95)]
	stats.P99Ms = sorted[shortQueryRank(len(sorted), 0.99)]
	return stats
}

func shortQueryRank(n int, quantile float64) int {
	index := int(float64(n)*quantile+0.999999) - 1
	if index < 0 {
		return 0
	}
	if index >= n {
		return n - 1
	}
	return index
}

// shortQuerySeed writes a source shaped like the production corpus: one audio
// file per card, one translated title per display language, and a shared needle
// that matches every title so the measured total is the worst case.
func shortQuerySeed(t *testing.T, db *sql.DB, entities int) int64 {
	t.Helper()
	const sourceID int64 = 71
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin seed: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	mustExec := func(query string, args ...any) {
		if _, err := tx.Exec(query, args...); err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
	}
	mustExec(`INSERT INTO file_source(id,code,display_name,source_type,enabled)
		VALUES(?,'short-query','Short query','openlist',1)`, sourceID)
	mustExec(`INSERT INTO file_source_endpoint(file_source_id,api_url,base_url)
		VALUES(?,'https://short-query.example.invalid','https://short-query.example.invalid')`, sourceID)
	entryStatement, err := tx.Prepare(`INSERT INTO source_catalog_entry(
		id,file_source_id,root_path,primary_code,title,availability_status,last_seen_generation)
		VALUES(?,?,?,?,?,'available',1)`)
	if err != nil {
		t.Fatalf("prepare entry: %v", err)
	}
	fileStatement, err := tx.Prepare(`INSERT INTO source_catalog_file(
		catalog_entry_id,relative_path,title,kind,size_bytes,observed_generation)
		VALUES(?,'01.mp3','01.mp3','audio',1024,1)`)
	if err != nil {
		t.Fatalf("prepare file: %v", err)
	}
	translationStatement, err := tx.Prepare(`INSERT INTO content_translation(
		cache_key,entity_kind,entity_id,entity_key,field,language,source_text,translated_text,origin,config_key,model,template_version)
		VALUES(?, 'entry', ?, '', 'title', ?, ?, ?, 'model', ?, 'short-query-bench', 1)`)
	if err != nil {
		t.Fatalf("prepare translation: %v", err)
	}
	configKey := translation.ConfigKey(translation.Config{Model: "short-query-bench"})
	languages := []string{translation.SourceEnglish, translation.SourceHans, translation.SourceJapanese, translation.SourceKorean}
	for index := 0; index < entities; index++ {
		id := int64(index) + 1
		title := fmt.Sprintf("Card %06d", index+1)
		if _, err = entryStatement.Exec(id, sourceID, fmt.Sprintf("/card-%06d", index+1), fmt.Sprintf("RJ%08d", index+1), title); err != nil {
			t.Fatalf("seed entry %d: %v", id, err)
		}
		if _, err = fileStatement.Exec(id); err != nil {
			t.Fatalf("seed file %d: %v", id, err)
		}
		for _, language := range languages {
			translated := fmt.Sprintf("催眠 Card %06d", index+1)
			if language == translation.SourceEnglish {
				translated = fmt.Sprintf("Hypnosis Card %06d", index+1)
			}
			if language == translation.SourceKorean {
				translated = fmt.Sprintf("최면 Card %06d", index+1)
			}
			if _, err = translationStatement.Exec(fmt.Sprintf("%d-%s", id, language), id, language, title, translated, configKey); err != nil {
				t.Fatalf("seed translation %d %s: %v", id, language, err)
			}
		}
	}
	for _, statement := range []*sql.Stmt{entryStatement, fileStatement, translationStatement} {
		if err = statement.Close(); err != nil {
			t.Fatalf("close statement: %v", err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
	if err = catalog42KRefreshAdmissions(db, sourceID); err != nil {
		t.Fatalf("refresh admission counts: %v", err)
	}
	return sourceID
}

func shortQueryEnvInt(t *testing.T, name string, fallback int) int {
	t.Helper()
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	var value int
	if _, err := fmt.Sscanf(raw, "%d", &value); err != nil || value <= 0 {
		t.Fatalf("%s=%q is not a positive integer", name, raw)
	}
	return value
}
