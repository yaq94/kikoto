package httpapi

// Opt-in evidence for issue 20: browsing a 42,624-card OpenList catalog
// through the real HTTP route, plus the rejection path of a 5,001-file card.
//
// The measurement is reported, never asserted. A CI run must stay fast and
// deterministic, and the p95/p99 judgement belongs to the evidence report.
//
//	KIKOTO_CATALOG_42K_PERF=1 \
//	KIKOTO_CATALOG_42K_CARDS=42624 \
//	KIKOTO_CATALOG_42K_SAMPLES=30 \
//	KIKOTO_CATALOG_42K_TMP=/root/kikoto/bench42k \
//	KIKOTO_CATALOG_42K_OUT=/root/kikoto/bench42k/report.json \
//	go test ./internal/httpapi -run '^TestCatalog42KPerformance$' -count=1 -v -timeout 90m

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yexca/kikoto/backend/internal/catalogadmission"
	"github.com/yexca/kikoto/backend/internal/config"
	"github.com/yexca/kikoto/backend/internal/storage"
	"github.com/yexca/kikoto/backend/internal/testfixture"
)

const (
	catalog42KPerfEnv    = "KIKOTO_CATALOG_42K_PERF"
	catalog42KCardsEnv   = "KIKOTO_CATALOG_42K_CARDS"
	catalog42KSamplesEnv = "KIKOTO_CATALOG_42K_SAMPLES"
	catalog42KWarmupEnv  = "KIKOTO_CATALOG_42K_WARMUP"
	catalog42KTmpEnv     = "KIKOTO_CATALOG_42K_TMP"
	catalog42KOutEnv     = "KIKOTO_CATALOG_42K_OUT"

	catalog42KDefaultCards   = 42624
	catalog42KDefaultSamples = 30
	catalog42KDefaultWarmup  = 5
	catalog42KPageSize       = 24
	catalog42KGeneration     = 1
	catalog42KSessionToken   = "catalog-42k-synthetic-session"
	catalog42KOversizedFiles = 5001
	catalog42KOversizedAudio = 6
)

type catalog42KLatency struct {
	Samples int     `json:"samples"`
	MinMs   float64 `json:"minMs"`
	MeanMs  float64 `json:"meanMs"`
	P50Ms   float64 `json:"p50Ms"`
	P95Ms   float64 `json:"p95Ms"`
	P99Ms   float64 `json:"p99Ms"`
	MaxMs   float64 `json:"maxMs"`
}

type catalog42KMeasurement struct {
	Name          string            `json:"name"`
	Path          string            `json:"path"`
	StatusCodes   map[string]int    `json:"statusCodes"`
	Total         int               `json:"total"`
	Works         int               `json:"works"`
	ResponseBytes int64             `json:"responseBytesTotal"`
	Latency       catalog42KLatency `json:"latency"`
	DurationsMs   []float64         `json:"durationsMs"`
}

type catalog42KEnvironment struct {
	GOOS              string  `json:"goos"`
	GOARCH            string  `json:"goarch"`
	GoVersion         string  `json:"goVersion"`
	NumCPU            int     `json:"numCPU"`
	GOMAXPROCS        int     `json:"gomaxprocs"`
	MemTotalKiB       int64   `json:"memTotalKiB,omitempty"`
	TempRoot          string  `json:"tempRoot,omitempty"`
	DatabasePath      string  `json:"databasePath"`
	DatabaseBytes     int64   `json:"databaseBytes"`
	SeedDurationMs    float64 `json:"seedDurationMs"`
	AnalyzeDurationMs float64 `json:"analyzeDurationMs"`
}

type catalog42KFixture struct {
	Cards             int   `json:"cards"`
	Entries           int   `json:"entries"`
	Files             int   `json:"files"`
	OversizedID       int64 `json:"oversizedId"`
	OversizedFiles    int   `json:"oversizedFiles"`
	OversizedAudio    int   `json:"oversizedAudio"`
	OversizedNonAudio int   `json:"oversizedNonAudio"`
	Generation        int64 `json:"generation"`
	PageSize          int   `json:"pageSize"`
	Warmup            int   `json:"warmup"`
	Samples           int   `json:"samples"`
}

type catalog42KThresholds struct {
	BrowseP95Ms     float64 `json:"browseP95Ms"`
	BrowseP99Ms     float64 `json:"browseP99Ms"`
	OversizedP95Ms  float64 `json:"oversizedP95Ms"`
	OversizedP99Ms  float64 `json:"oversizedP99Ms"`
	BrowseWithin    bool    `json:"browseWithinThreshold"`
	OversizedWithin bool    `json:"oversizedWithinThreshold"`
}

type catalog42KReport struct {
	Label              string                `json:"label"`
	StartedAt          string                `json:"startedAt"`
	FinishedAt         string                `json:"finishedAt"`
	Environment        catalog42KEnvironment `json:"environment"`
	Fixture            catalog42KFixture     `json:"fixture"`
	Browse             catalog42KMeasurement `json:"browse"`
	OversizedRejection catalog42KMeasurement `json:"oversizedRejection"`
	SQLPageDiagnostic  catalog42KLatency     `json:"sqlPageDiagnostic"`
	SQLNoAdmission     catalog42KLatency     `json:"sqlPageWithoutAdmissionDiagnostic"`
	QueryPlan          []string              `json:"queryPlan"`
	Thresholds         catalog42KThresholds  `json:"thresholds"`
	Notes              []string              `json:"notes"`
}

func TestCatalog42KPerformance(t *testing.T) {
	if os.Getenv(catalog42KPerfEnv) != "1" {
		t.Skip("set " + catalog42KPerfEnv + "=1 to measure the 42k catalog browse path")
	}
	tempRoot := catalog42KConfigureTempDir(t)
	cards := catalog42KEnvPositiveInt(t, catalog42KCardsEnv, catalog42KDefaultCards)
	samples := catalog42KEnvPositiveInt(t, catalog42KSamplesEnv, catalog42KDefaultSamples)
	warmup := catalog42KEnvPositiveInt(t, catalog42KWarmupEnv, catalog42KDefaultWarmup)
	if cards > 399999 {
		t.Fatalf("cards=%d exceeds the synthetic work-code range", cards)
	}

	startedAt := time.Now()
	report := catalog42KReport{
		Label:     "catalog-42k-browse",
		StartedAt: startedAt.UTC().Format(time.RFC3339),
		Environment: catalog42KEnvironment{
			GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, GoVersion: runtime.Version(),
			NumCPU: runtime.NumCPU(), GOMAXPROCS: runtime.GOMAXPROCS(0),
			MemTotalKiB: catalog42KMemTotalKiB(), TempRoot: tempRoot,
		},
		Fixture: catalog42KFixture{
			Cards: cards, PageSize: catalog42KPageSize, Warmup: warmup,
			Samples: samples, Generation: catalog42KGeneration,
			OversizedFiles: catalog42KOversizedFiles, OversizedAudio: catalog42KOversizedAudio,
			OversizedNonAudio: catalog42KOversizedFiles - catalog42KOversizedAudio,
		},
		Thresholds: catalog42KThresholds{BrowseP95Ms: 300, BrowseP99Ms: 800, OversizedP95Ms: 300, OversizedP99Ms: 800},
		Notes: []string{
			"warm: warmup requests over one keep-alive connection before sampling; samples are sequential (concurrency 1)",
			"production middleware: auth session cookie, gzip, first-response deadline, 8-connection SQLite pool, WAL, synchronous=NORMAL",
			"no background workers, workflow jobs or statistics maintainer are started",
			"storage.OptimizeStatistics (PRAGMA optimize) runs after seeding, like a settled production instance",
			"latency is reported, not asserted: CI never fails on the 300/800 ms thresholds",
			"percentiles use nearest-rank on the sorted sample: index ceil(q*n)-1",
			"with the default 30 samples nearest-rank p99 is the slowest sample; p95 is the 29th of 30",
			"sqlPageDiagnostic runs the same page SQL directly on the pool to separate SQL cost from HTTP overhead",
			"sqlPageWithoutAdmissionDiagnostic replaces only the admission predicates with 1; it attributes cost and is not a proposed query change",
		},
	}

	db := openMigratedTestDBWithProductionPool(t)
	report.Environment.DatabasePath = catalog42KDatabasePath(t, db)

	seedStarted := time.Now()
	sourceID, oversizedID, fileCount := catalog42KSeed(t, db, cards)
	report.Environment.SeedDurationMs = catalog42KMillis(time.Since(seedStarted))
	report.Fixture.Entries = cards + 1
	report.Fixture.Files = fileCount
	report.Fixture.OversizedID = oversizedID
	t.Logf("seeded %d cards + 1 oversized card, %d file rows in %.1fs", cards, fileCount, report.Environment.SeedDurationMs/1000)

	analyzeStarted := time.Now()
	if err := storage.OptimizeStatistics(context.Background(), db); err != nil {
		t.Fatalf("optimize statistics: %v", err)
	}
	report.Environment.AnalyzeDurationMs = catalog42KMillis(time.Since(analyzeStarted))
	report.Environment.DatabaseBytes = catalog42KDatabaseBytes(t, db)
	t.Logf("database %s: %.1f MiB, analyze %.1fs", report.Environment.DatabasePath,
		float64(report.Environment.DatabaseBytes)/(1<<20), report.Environment.AnalyzeDurationMs/1000)

	server := NewServer(db, config.Config{Mode: config.ModeProduction, DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	if err := server.LoadAccessPolicy(context.Background()); err != nil {
		t.Fatalf("load access policy: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()
	httpServer := httptest.NewServer(server.Routes())
	defer httpServer.Close()
	client := &http.Client{
		Timeout: 120 * time.Second,
		Transport: &http.Transport{
			Proxy:               nil,
			MaxIdleConns:        4,
			MaxIdleConnsPerHost: 4,
			IdleConnTimeout:     90 * time.Second,
		},
	}

	browsePath := fmt.Sprintf("/api/remote-sources/%d/works?page=1&pageSize=%d", sourceID, catalog42KPageSize)
	report.Browse = catalog42KMeasureHTTP(t, client, httpServer.URL, "browse/42k-cards", browsePath, warmup, samples,
		func(status int, body []byte) (int, int, error) {
			if status != http.StatusOK {
				return 0, 0, fmt.Errorf("status %d", status)
			}
			var page struct {
				Status string `json:"status"`
				Total  int    `json:"total"`
				Works  []struct {
					CatalogEntryID int64 `json:"catalogEntryId"`
				} `json:"works"`
			}
			if err := json.Unmarshal(body, &page); err != nil {
				return 0, 0, fmt.Errorf("decode page: %w", err)
			}
			if page.Status != "ok" {
				return 0, 0, fmt.Errorf("status field %q", page.Status)
			}
			if page.Total != cards {
				return 0, 0, fmt.Errorf("total %d, want %d", page.Total, cards)
			}
			if len(page.Works) != catalog42KPageSize {
				return 0, 0, fmt.Errorf("works %d, want %d", len(page.Works), catalog42KPageSize)
			}
			for _, work := range page.Works {
				if work.CatalogEntryID == oversizedID {
					return 0, 0, fmt.Errorf("oversized card %d leaked into the browse page", oversizedID)
				}
			}
			return page.Total, len(page.Works), nil
		})
	t.Logf("browse %d cards: n=%d status=%v total=%d p50=%.2fms p95=%.2fms p99=%.2fms max=%.2fms",
		cards, report.Browse.Latency.Samples, report.Browse.StatusCodes, report.Browse.Total,
		report.Browse.Latency.P50Ms, report.Browse.Latency.P95Ms, report.Browse.Latency.P99Ms, report.Browse.Latency.MaxMs)

	oversizedPath := fmt.Sprintf("/api/source-catalog/%d/tracks", oversizedID)
	report.OversizedRejection = catalog42KMeasureHTTP(t, client, httpServer.URL, "rejection/5001-files", oversizedPath, warmup, samples,
		func(status int, body []byte) (int, int, error) {
			if status != http.StatusNotFound {
				return 0, 0, fmt.Errorf("status %d, want 404", status)
			}
			var payload struct {
				Code    string `json:"code"`
				Details struct {
					Reason   string `json:"reason"`
					Files    int    `json:"files"`
					Audio    int    `json:"audio"`
					NonAudio int    `json:"nonAudio"`
				} `json:"details"`
			}
			if err := json.Unmarshal(body, &payload); err != nil {
				return 0, 0, fmt.Errorf("decode refusal: %w", err)
			}
			if payload.Code != "not_found" {
				return 0, 0, fmt.Errorf("code %q, want not_found", payload.Code)
			}
			if payload.Details.Reason != "too_many_files" {
				return 0, 0, fmt.Errorf("reason %q, want too_many_files", payload.Details.Reason)
			}
			// The probe stops one row past each threshold, so the reported counts
			// are the bounds, not the 5,001 physical rows.
			if payload.Details.Files != catalogadmission.ProbeFiles+1 {
				return 0, 0, fmt.Errorf("counted files %d, want %d", payload.Details.Files, catalogadmission.ProbeFiles+1)
			}
			if payload.Details.Audio != catalogadmission.ProbeAudio {
				return 0, 0, fmt.Errorf("counted audio %d, want %d", payload.Details.Audio, catalogadmission.ProbeAudio)
			}
			if payload.Details.NonAudio != catalogadmission.ProbeNonAudio+1 {
				return 0, 0, fmt.Errorf("counted non-audio %d, want %d", payload.Details.NonAudio, catalogadmission.ProbeNonAudio+1)
			}
			return 0, 0, nil
		})
	t.Logf("oversized rejection: n=%d status=%v p50=%.2fms p95=%.2fms p99=%.2fms max=%.2fms",
		report.OversizedRejection.Latency.Samples, report.OversizedRejection.StatusCodes,
		report.OversizedRejection.Latency.P50Ms, report.OversizedRejection.Latency.P95Ms,
		report.OversizedRejection.Latency.P99Ms, report.OversizedRejection.Latency.MaxMs)

	// The handler builds this exact statement for an empty browse; mirroring it
	// keeps the plan attached to the query that was measured above.
	prefix, from, args := openListCatalogCardsQuery(sourceID, "")
	pageSQL := prefix + `SELECT id,series_id,primary_code,title,work_id,updated_at,availability_status,has_audio,COUNT(*) OVER () AS total ` +
		from + ` ORDER BY updated_at DESC,series_id DESC,id DESC LIMIT ? OFFSET ?`
	pageArgs := append(append([]any{}, args...), catalog42KPageSize, 0)
	report.QueryPlan = catalog42KExplain(t, db, pageSQL, pageArgs...)
	for _, line := range report.QueryPlan {
		t.Logf("plan: %s", line)
	}
	report.SQLPageDiagnostic = catalog42KLatencyOf(catalog42KTimeSQL(t, db, pageSQL, pageArgs, warmup, samples))
	t.Logf("sql page only: p50=%.2fms p95=%.2fms p99=%.2fms max=%.2fms",
		report.SQLPageDiagnostic.P50Ms, report.SQLPageDiagnostic.P95Ms,
		report.SQLPageDiagnostic.P99Ms, report.SQLPageDiagnostic.MaxMs)
	// Diagnostic only: the admission predicate is the per-candidate set of file
	// probes. Dropping it isolates that share of the page cost; the shipped
	// query and handler are untouched.
	noAdmissionSQL := strings.ReplaceAll(pageSQL, catalogEntryAdmittedSQL("e"), "1")
	noAdmissionSQL = strings.ReplaceAll(noAdmissionSQL, catalogEntryAdmittedSQL("ee"), "1")
	if noAdmissionSQL == pageSQL {
		t.Fatal("admission predicate diagnostic did not rewrite the page query")
	}
	report.SQLNoAdmission = catalog42KLatencyOf(catalog42KTimeSQL(t, db, noAdmissionSQL, pageArgs, warmup, samples))
	t.Logf("sql page without admission predicates: p50=%.2fms p95=%.2fms p99=%.2fms max=%.2fms",
		report.SQLNoAdmission.P50Ms, report.SQLNoAdmission.P95Ms,
		report.SQLNoAdmission.P99Ms, report.SQLNoAdmission.MaxMs)

	report.Thresholds.BrowseWithin = report.Browse.Latency.P95Ms <= report.Thresholds.BrowseP95Ms &&
		report.Browse.Latency.P99Ms <= report.Thresholds.BrowseP99Ms
	report.Thresholds.OversizedWithin = report.OversizedRejection.Latency.P95Ms <= report.Thresholds.OversizedP95Ms &&
		report.OversizedRejection.Latency.P99Ms <= report.Thresholds.OversizedP99Ms
	t.Logf("thresholds: browse within=%t, oversized rejection within=%t (reported only, not asserted)",
		report.Thresholds.BrowseWithin, report.Thresholds.OversizedWithin)

	report.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	catalog42KWriteReport(t, report)
}

// catalog42KConfigureTempDir moves both the migrated schema image and the test
// database onto the requested disk before any t.TempDir call resolves.
func catalog42KConfigureTempDir(t *testing.T) string {
	t.Helper()
	dir := strings.TrimSpace(os.Getenv(catalog42KTmpEnv))
	if dir == "" {
		return ""
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("resolve %s: %v", catalog42KTmpEnv, err)
	}
	if err := os.MkdirAll(absolute, 0o755); err != nil {
		t.Fatalf("create %s: %v", catalog42KTmpEnv, err)
	}
	if runtime.GOOS == "windows" {
		t.Setenv("TMP", absolute)
		t.Setenv("TEMP", absolute)
	} else {
		t.Setenv("TMPDIR", absolute)
	}
	return absolute
}

func catalog42KEnvPositiveInt(t *testing.T, name string, fallback int) int {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		t.Fatalf("%s=%q is not a positive integer", name, raw)
	}
	return value
}

// catalog42KSeed writes one OpenList source. Every card carries 1-3 audio
// files, every fifth card also carries one non-audio file, and the last card
// is over the 5,000-file admission limit. observed_generation matches
// last_seen_generation so the fixture stays valid when the admission counts
// are restricted to the current generation.
func catalog42KSeed(t *testing.T, db *sql.DB, cards int) (int64, int64, int) {
	t.Helper()
	const sourceID int64 = 1
	oversizedID := int64(cards) + 1
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin catalog seed: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	mustExec := func(query string, args ...any) {
		if _, err := tx.Exec(query, args...); err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
	}
	mustExec(`INSERT INTO user_account(id,username,role) VALUES(1,'catalog-42k-user','user')`)
	mustExec(`INSERT INTO file_source(id,code,display_name,source_type,enabled)
		VALUES(?,'catalog-42k','Catalog 42K','openlist',1)`, sourceID)
	mustExec(`INSERT INTO file_source_endpoint(file_source_id,api_url,base_url)
		VALUES(?,'https://catalog-42k.example.invalid','https://catalog-42k.example.invalid')`, sourceID)
	session := sha256.Sum256([]byte(catalog42KSessionToken))
	mustExec(`INSERT INTO user_session(id,user_id,expires_at) VALUES(?,1,'2999-01-01 00:00:00')`, hex.EncodeToString(session[:]))

	entryStatement, err := tx.Prepare(`INSERT INTO source_catalog_entry(
		id,file_source_id,root_path,primary_code,title,availability_status,
		last_seen_generation,discovered_generation,updated_at)
		VALUES(?,?,?,?,?,'available',?,?,?)`)
	if err != nil {
		t.Fatalf("prepare entry insert: %v", err)
	}
	fileStatement, err := tx.Prepare(`INSERT INTO source_catalog_file(
		catalog_entry_id,relative_path,title,kind,size_bytes,observed_generation)
		VALUES(?,?,?,?,?,?)`)
	if err != nil {
		_ = entryStatement.Close()
		t.Fatalf("prepare file insert: %v", err)
	}

	files := 0
	insertFile := func(entryID int64, relativePath, title, kind string, size int64) {
		if _, err := fileStatement.Exec(entryID, relativePath, title, kind, size, catalog42KGeneration); err != nil {
			t.Fatalf("seed file %d %q: %v", entryID, relativePath, err)
		}
		files++
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for index := 0; index < cards; index++ {
		id := int64(index) + 1
		updated := base.Add(-time.Duration(index) * time.Minute).Format("2006-01-02 15:04:05")
		if _, err := entryStatement.Exec(id, sourceID,
			fmt.Sprintf("/catalog/card-%05d", index+1),
			testfixture.HighCardinalityWorkCodeAt(index),
			fmt.Sprintf("Card %05d", index+1),
			catalog42KGeneration, catalog42KGeneration, updated); err != nil {
			t.Fatalf("seed entry %d: %v", id, err)
		}
		for track := 1; track <= 1+index%3; track++ {
			insertFile(id, fmt.Sprintf("track-%02d.mp3", track), fmt.Sprintf("Track %02d", track), "audio", 4<<20)
		}
		if index%5 == 0 {
			insertFile(id, "cover.jpg", "Cover", "other", 128<<10)
		}
	}
	// The newest card in the fixture is also the refused one, so a regression in
	// the admission filter would surface on page 1 instead of hiding at the end.
	if _, err := entryStatement.Exec(oversizedID, sourceID, "/catalog/oversized", "", "Oversized Card",
		catalog42KGeneration, catalog42KGeneration, base.Add(time.Hour).Format("2006-01-02 15:04:05")); err != nil {
		t.Fatalf("seed oversized entry: %v", err)
	}
	for track := 1; track <= catalog42KOversizedAudio; track++ {
		insertFile(oversizedID, fmt.Sprintf("track-%02d.mp3", track), fmt.Sprintf("Track %02d", track), "audio", 4<<20)
	}
	for extra := 0; extra < catalog42KOversizedFiles-catalog42KOversizedAudio; extra++ {
		insertFile(oversizedID, fmt.Sprintf("extra-%04d.jpg", extra), "Extra", "other", 1<<20)
	}
	if err := entryStatement.Close(); err != nil {
		t.Fatalf("close entry insert: %v", err)
	}
	if err := fileStatement.Close(); err != nil {
		t.Fatalf("close file insert: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit catalog seed: %v", err)
	}
	return sourceID, oversizedID, files
}

func catalog42KMeasureHTTP(t *testing.T, client *http.Client, baseURL, name, path string, warmup, samples int,
	validate func(status int, body []byte) (int, int, error)) catalog42KMeasurement {
	t.Helper()
	fetch := func() (time.Duration, int, []byte, error) {
		request, err := http.NewRequest(http.MethodGet, baseURL+path, nil)
		if err != nil {
			return 0, 0, nil, err
		}
		request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: catalog42KSessionToken})
		start := time.Now()
		response, err := client.Do(request)
		if err != nil {
			return time.Since(start), 0, nil, err
		}
		defer func() { _ = response.Body.Close() }()
		body, err := io.ReadAll(response.Body)
		return time.Since(start), response.StatusCode, body, err
	}
	for index := 0; index < warmup; index++ {
		elapsed, status, body, err := fetch()
		if err != nil {
			t.Fatalf("%s warmup %d: %v", name, index+1, err)
		}
		if _, _, err := validate(status, body); err != nil {
			t.Fatalf("%s warmup %d: %v (%.2fms)", name, index+1, err, catalog42KMillis(elapsed))
		}
	}
	measurement := catalog42KMeasurement{
		Name: name, Path: path, StatusCodes: map[string]int{}, DurationsMs: []float64{},
	}
	times := make([]time.Duration, 0, samples)
	var responseBytes int64
	for index := 0; index < samples; index++ {
		elapsed, status, body, err := fetch()
		if err != nil {
			t.Fatalf("%s sample %d: %v", name, index+1, err)
		}
		total, works, err := validate(status, body)
		if err != nil {
			t.Fatalf("%s sample %d: %v (%.2fms)", name, index+1, err, catalog42KMillis(elapsed))
		}
		if index == 0 {
			measurement.Total, measurement.Works = total, works
		} else if total != measurement.Total {
			t.Fatalf("%s total changed across samples: %d -> %d", name, measurement.Total, total)
		}
		measurement.StatusCodes[strconv.Itoa(status)]++
		responseBytes += int64(len(body))
		times = append(times, elapsed)
		measurement.DurationsMs = append(measurement.DurationsMs, catalog42KMillis(elapsed))
	}
	measurement.ResponseBytes = responseBytes
	measurement.Latency = catalog42KLatencyOf(times)
	return measurement
}

func catalog42KTimeSQL(t *testing.T, db *sql.DB, query string, args []any, warmup, samples int) []time.Duration {
	t.Helper()
	run := func() time.Duration {
		start := time.Now()
		rows, err := db.Query(query, args...)
		if err != nil {
			t.Fatalf("sql page query: %v", err)
		}
		for rows.Next() {
			var id, seriesID int64
			var code, title, updated, availability string
			var workID sql.NullInt64
			var hasAudio bool
			var total int
			if err := rows.Scan(&id, &seriesID, &code, &title, &workID, &updated, &availability, &hasAudio, &total); err != nil {
				_ = rows.Close()
				t.Fatalf("scan sql page: %v", err)
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			t.Fatalf("iterate sql page: %v", err)
		}
		_ = rows.Close()
		return time.Since(start)
	}
	for index := 0; index < warmup; index++ {
		run()
	}
	times := make([]time.Duration, 0, samples)
	for index := 0; index < samples; index++ {
		times = append(times, run())
	}
	return times
}

func catalog42KExplain(t *testing.T, db *sql.DB, query string, args ...any) []string {
	t.Helper()
	rows, err := db.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatalf("explain catalog page: %v", err)
	}
	defer func() { _ = rows.Close() }()
	plan := []string{}
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatalf("scan query plan: %v", err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate query plan: %v", err)
	}
	return plan
}

func catalog42KLatencyOf(times []time.Duration) catalog42KLatency {
	if len(times) == 0 {
		return catalog42KLatency{}
	}
	sorted := append([]time.Duration(nil), times...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	var sum time.Duration
	for _, value := range sorted {
		sum += value
	}
	return catalog42KLatency{
		Samples: len(sorted),
		MinMs:   catalog42KMillis(sorted[0]),
		MeanMs:  catalog42KMillis(sum / time.Duration(len(sorted))),
		P50Ms:   catalog42KMillis(catalog42KPercentile(sorted, 0.50)),
		P95Ms:   catalog42KMillis(catalog42KPercentile(sorted, 0.95)),
		P99Ms:   catalog42KMillis(catalog42KPercentile(sorted, 0.99)),
		MaxMs:   catalog42KMillis(sorted[len(sorted)-1]),
	}
}

// catalog42KPercentile is nearest-rank: the smallest sample whose rank covers
// the quantile. For 30 samples p95 is the 29th value, not an interpolation.
func catalog42KPercentile(sorted []time.Duration, quantile float64) time.Duration {
	index := int(math.Ceil(quantile*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}

func catalog42KMillis(value time.Duration) float64 {
	return math.Round(float64(value)/float64(time.Millisecond)*1000) / 1000
}

func catalog42KDatabasePath(t *testing.T, db *sql.DB) string {
	t.Helper()
	var sequence int
	var name, path string
	if err := db.QueryRow(`PRAGMA database_list`).Scan(&sequence, &name, &path); err != nil {
		t.Fatalf("database_list: %v", err)
	}
	return path
}

func catalog42KDatabaseBytes(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	path := catalog42KDatabasePath(t, db)
	var total int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if info, err := os.Stat(path + suffix); err == nil {
			total += info.Size()
		}
	}
	return total
}

// catalog42KMemTotalKiB reads the host figure that the report is judged
// against. It is empty on platforms without /proc.
func catalog42KMemTotalKiB() int64 {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		value, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0
		}
		return value
	}
	return 0
}

func catalog42KWriteReport(t *testing.T, report catalog42KReport) {
	t.Helper()
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatalf("encode report: %v", err)
	}
	out := strings.TrimSpace(os.Getenv(catalog42KOutEnv))
	if out == "" {
		t.Logf("report (set %s to write it to a file):\n%s", catalog42KOutEnv, encoded)
		return
	}
	absolute, err := filepath.Abs(out)
	if err != nil {
		t.Fatalf("resolve %s: %v", catalog42KOutEnv, err)
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		t.Fatalf("create report directory: %v", err)
	}
	if err := os.WriteFile(absolute, append(encoded, '\n'), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	t.Logf("report written to %s", absolute)
}
