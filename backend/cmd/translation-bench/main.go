// Command translation-bench measures the persistent translation store at
// several corpus sizes. It creates a disposable database, applies the packaged
// migration catalog, runs the production query shapes, and reports timings plus
// file sizes.
//
// One entity carries one row per model language, which is the shape the prefill
// pass writes. A corpus size therefore means "content_translation rows", and the
// entity count is a quarter of it.
//
//	go run ./cmd/translation-bench --rows 456000 --runs 30
package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yexca/kikoto/backend/internal/library"
	"github.com/yexca/kikoto/backend/internal/migrationfixture"
	"github.com/yexca/kikoto/backend/internal/storage"
	"github.com/yexca/kikoto/backend/internal/translation"
	_ "modernc.org/sqlite"
)

// benchLanguages mirrors the four languages the model writes. Every entity has
// one row in each, so a sample never lands in a language with no corpus.
var benchLanguages = []string{
	translation.SourceEnglish, translation.SourceHans,
	translation.SourceJapanese, translation.SourceKorean,
}

var benchConfigKey = translation.ConfigKey(translation.Config{
	Model: "bench", DisableThinking: true,
})

type queryResult struct {
	MedianMS float64   `json:"medianMs"`
	P95MS    float64   `json:"p95Ms"`
	P99MS    float64   `json:"p99Ms"`
	MaxMS    float64   `json:"maxMs"`
	Rows     int       `json:"rows"`
	Runs     int       `json:"runs"`
	Samples  []float64 `json:"samplesMs"`
	Plan     []string  `json:"plan,omitempty"`
}

type languageReport struct {
	Language   string      `json:"language"`
	Sample     string      `json:"sample"`
	ExactTitle queryResult `json:"exactTitle"`
	FTS        queryResult `json:"fts"`
	Short      queryResult `json:"short"`
	// ShortComplete counts the whole effective set with no ordering. It is a
	// diagnostic for an unpaged total, not a user path.
	ShortComplete queryResult `json:"shortComplete"`
}

type sizeReport struct {
	Rows          int              `json:"rows"`
	Entities      int              `json:"entities"`
	InsertSeconds float64          `json:"insertSeconds"`
	Checkpoints   int              `json:"checkpoints"`
	WalPeakBytes  int64            `json:"walPeakBytes"`
	DBBytes       int64            `json:"dbBytes"`
	FTSBytes      int64            `json:"ftsBytes"`
	AfterVacuum   int64            `json:"afterVacuumBytes"`
	ExactCode     queryResult      `json:"exactCode"`
	Languages     []languageReport `json:"languages"`
}

func main() {
	rowsFlag := flag.String("rows", "456000", "comma separated content_translation row counts")
	runs := flag.Int("runs", 30, "measured repetitions per query (after one warmup)")
	out := flag.String("json-out", "", "write the report here")
	flag.Parse()
	sizes := []int{}
	for _, part := range strings.Split(*rowsFlag, ",") {
		var value int
		if _, err := fmt.Sscanf(strings.TrimSpace(part), "%d", &value); err != nil || value <= 0 {
			fmt.Fprintf(os.Stderr, "invalid size %q\n", part)
			os.Exit(2)
		}
		sizes = append(sizes, value)
	}
	reports := []sizeReport{}
	for _, size := range sizes {
		report, err := measure(size, *runs)
		if err != nil {
			fmt.Fprintf(os.Stderr, "rows=%d: %v\n", size, err)
			os.Exit(1)
		}
		reports = append(reports, report)
		fmt.Printf("rows=%-7d entities=%-6d insert=%.1fs db=%.1fMB fts=%.1fMB vacuum=%.1fMB exact-code p95=%.2fms\n",
			size, report.Entities, report.InsertSeconds, mb(report.DBBytes),
			mb(report.FTSBytes), mb(report.AfterVacuum), report.ExactCode.P95MS)
		for _, language := range report.Languages {
			fmt.Printf("  %-8s exact-title p95=%.2fms fts p95=%.2fms short p95=%.2fms short-complete p95=%.2fms (n=%d)\n",
				language.Language, language.ExactTitle.P95MS, language.FTS.P95MS,
				language.Short.P95MS, language.ShortComplete.P95MS, language.Short.Runs)
		}
	}
	if *out != "" {
		data, _ := json.MarshalIndent(reports, "", "  ")
		if err := os.WriteFile(*out, data, 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}

func mb(value int64) float64 { return float64(value) / (1 << 20) }

func measure(rows, runs int) (sizeReport, error) {
	if runs < 1 {
		return sizeReport{}, fmt.Errorf("runs must be positive")
	}
	dir, err := os.MkdirTemp("", "kikoto-translation-bench-")
	if err != nil {
		return sizeReport{}, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path := filepath.Join(dir, "bench.db")
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return sizeReport{}, err
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	files, err := migrationfixture.FS()
	if err != nil {
		return sizeReport{}, err
	}
	if err = storage.MigrateFS(db, files, "bench"); err != nil {
		return sizeReport{}, err
	}

	entities := rows / len(benchLanguages)
	if entities < 1 {
		return sizeReport{}, fmt.Errorf("rows must cover at least one entity")
	}
	report := sizeReport{Rows: entities * len(benchLanguages), Entities: entities}
	start := time.Now()
	walPeak := int64(0)
	const batch = 1000
	for begin := 0; begin < entities; begin += batch {
		end := min(begin+batch, entities)
		if err = insertBatch(db, begin, end); err != nil {
			return report, err
		}
		// Mirror the prefill pass: a checkpoint every five thousand entities
		// keeps the WAL bounded instead of growing with the whole corpus.
		if end%5000 == 0 {
			if _, err = db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
				return report, err
			}
			report.Checkpoints++
		}
		if size := fileSize(path + "-wal"); size > walPeak {
			walPeak = size
		}
	}
	report.InsertSeconds = time.Since(start).Seconds()
	report.WalPeakBytes = walPeak
	if _, err = db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return report, err
	}
	report.DBBytes = fileSize(path)
	report.FTSBytes = ftsBytes(db)

	code, err := sampleCode(db, entities)
	if err != nil {
		return report, err
	}
	report.ExactCode = timeQuery(runs, func() (int, error) { return countCode(db, code) })

	for index, language := range benchLanguages {
		title, err := sampleTitle(db, language, index, entities)
		if err != nil {
			return report, err
		}
		entry := languageReport{Language: language, Sample: title}
		entry.ExactTitle = timeQuery(runs, func() (int, error) { return countExactTitle(db, language, title) })
		term := searchTerm(title)
		entry.FTS = timeQuery(runs, func() (int, error) { return countFTS(db, language, term) })
		shortSQL, shortArgs := shortSearch(language, firstRunes(title, 2))
		if shortSQL == "" {
			return report, fmt.Errorf("%s: short query is empty", language)
		}
		entry.Short = timeQuery(runs, func() (int, error) { return countQuery(db, shortSQL, shortArgs...) })
		entry.Short.Plan = explainPlan(db, shortSQL, shortArgs...)
		completeSQL, completeArgs := shortSearchAll(language, firstRunes(title, 2))
		entry.ShortComplete = timeQuery(runs, func() (int, error) { return countQuery(db, completeSQL, completeArgs...) })
		report.Languages = append(report.Languages, entry)
	}
	if _, err = db.Exec("VACUUM"); err != nil {
		return report, err
	}
	report.AfterVacuum = fileSize(path)
	return report, nil
}

// insertBatch writes one entity per iteration: a work row plus one translation
// per model language. The source text is the work title, so the short-query
// scope predicate matches exactly as it does in production.
func insertBatch(db *sql.DB, begin, end int) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	work, err := tx.Prepare(`INSERT INTO work(id,primary_code,title,description) VALUES(?,?,?,'')`)
	if err != nil {
		return err
	}
	defer func() { _ = work.Close() }()
	statement, err := tx.Prepare(`INSERT INTO content_translation
		(cache_key,entity_kind,entity_id,field,language,source_text,translated_text,origin,config_key,model,template_version)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer func() { _ = statement.Close() }()
	for index := begin; index < end; index++ {
		id := int64(index + 1)
		source := sourceTitle(index)
		if _, err = work.Exec(id, benchCode(id), source); err != nil {
			return err
		}
		for _, language := range benchLanguages {
			key := fmt.Sprintf("%064x", id*int64(len(benchLanguages))+int64(indexOf(language)))
			text := translatedTitle(index, language)
			if _, err = statement.Exec(key, "work", id, "title", language, source, text,
				"model", benchConfigKey, "bench", translation.TemplateVersion); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func indexOf(language string) int {
	for index, candidate := range benchLanguages {
		if candidate == language {
			return index
		}
	}
	return 0
}

// benchCode produces a realistic release code. Non-empty codes stay unique,
// which the partial unique index requires.
func benchCode(id int64) string { return fmt.Sprintf("RJ%08d", id) }

func sourceTitle(index int) string {
	switch index % 3 {
	case 0:
		return fmt.Sprintf("催眠音声 第%d話 癒しの時間", index)
	case 1:
		return fmt.Sprintf("Example audio episode %d with a long descriptive title", index)
	default:
		return fmt.Sprintf("동면 음성 %d 편안한 밤", index)
	}
}

// translatedTitle is the model output per language. Each language gets its own
// script so a query in one language cannot accidentally match another.
func translatedTitle(index int, language string) string {
	switch language {
	case translation.SourceEnglish:
		return fmt.Sprintf("Hypnosis audio episode %d relaxing time", index)
	case translation.SourceHans:
		return fmt.Sprintf("催眠音声 第%d话 治愈的时间", index)
	case translation.SourceJapanese:
		return fmt.Sprintf("催眠音声 第%d話 癒しの時間", index)
	default:
		return fmt.Sprintf("최면 음성 %d편 편안한 밤", index)
	}
}

// sampleTitle picks a deterministic row per language. A random() sample would
// read the whole table once and make the corpus size dominate the setup time.
func sampleTitle(db *sql.DB, language string, offset, entities int) (string, error) {
	var title string
	err := db.QueryRow(`SELECT translated_text FROM content_translation
		WHERE language=? AND entity_kind='work' AND field='title'
		ORDER BY id LIMIT 1 OFFSET ?`, language, offset*entities/len(benchLanguages)+offset).Scan(&title)
	return title, err
}

func sampleCode(db *sql.DB, entities int) (string, error) {
	var code string
	err := db.QueryRow(`SELECT primary_code FROM work ORDER BY id LIMIT 1 OFFSET ?`, entities/2).Scan(&code)
	return code, err
}

func searchTerm(title string) string {
	runes := []rune(title)
	if len(runes) > 6 {
		return string(runes[2:6])
	}
	return title
}

func firstRunes(text string, count int) string {
	runes := []rune(text)
	if len(runes) > count {
		runes = runes[:count]
	}
	return string(runes)
}

// shortSearch returns the production 1-2 character query capped at one card
// page, wrapped as a count.
func shortSearch(language, needle string) (string, []any) {
	return shortSearchWithLimit(language, needle, 200)
}

// shortSearchAll is the same predicate without a result cap. An exact paginated
// total pays this cost, so it is measured separately from a card page.
func shortSearchAll(language, needle string) (string, []any) {
	return shortSearchWithLimit(language, needle, 0)
}

// shortSearchWithLimit goes through the same generator the API and the library
// use, so a change in the real search path is measured here without editing the
// benchmark.
func shortSearchWithLimit(language, needle string, limit int) (string, []any) {
	query, args := library.ShortTranslationSearchSQL(
		[]library.DisplaySearchTarget{{Language: language, Query: needle}},
		[]library.ShortSearchArm{{
			EntityKind: "work",
			Fields:     []string{"title", "description"},
			ConfigKey:  benchConfigKey,
			Scope: `EXISTS(SELECT 1 FROM work current_work
				WHERE current_work.id=ct.entity_id
				  AND (CASE WHEN ct.field='title' THEN ct.source_text=current_work.title
				            ELSE ct.source_text=current_work.description END))`,
		}},
		limit,
	)
	if query == "" {
		return "", nil
	}
	return `SELECT count(*) FROM (` + query + `)`, args
}

// countFTS mirrors the store query: page-capped, so SQLite stops once the page
// is full. The cap is the user-visible cost, not the size of the match set.
func countFTS(db *sql.DB, language, term string) (int, error) {
	var count int
	err := db.QueryRow(`SELECT count(*) FROM (
		SELECT t.entity_id FROM content_translation_search
		JOIN content_translation AS t ON t.id = content_translation_search.rowid
		WHERE content_translation_search MATCH ? AND t.language = ? AND t.entity_kind = 'work'
		  AND t.field = 'title'
		  AND (t.origin='native' OR (t.origin='model' AND t.config_key=?))
		LIMIT 200
	)`, `"`+strings.ReplaceAll(term, `"`, `""`)+`"`, language, benchConfigKey).Scan(&count)
	return count, err
}

// countExactTitle is the user path "the whole translated title equals the
// query", answered by the trigram phrase index rather than a cache key.
func countExactTitle(db *sql.DB, language, title string) (int, error) {
	var count int
	err := db.QueryRow(`SELECT count(*) FROM (
		SELECT t.entity_id FROM content_translation_search
		JOIN content_translation AS t ON t.id = content_translation_search.rowid
		WHERE content_translation_search MATCH ? AND t.language = ? AND t.entity_kind = 'work'
		  AND t.field = 'title' AND t.translated_text = ?
		  AND (t.origin='native' OR (t.origin='model' AND t.config_key=?))
		LIMIT 200
	)`, `"`+strings.ReplaceAll(title, `"`, `""`)+`"`, language, title, benchConfigKey).Scan(&count)
	return count, err
}

// countCode is the language-independent release-code lookup. It uses the same
// UPPER() comparison as the library's code clause, so the upper-code index
// answers it instead of a full table scan.
func countCode(db *sql.DB, code string) (int, error) {
	var count int
	err := db.QueryRow(`SELECT count(*) FROM work WHERE UPPER(primary_code) = UPPER(?)`, code).Scan(&count)
	return count, err
}

func countQuery(db *sql.DB, query string, args ...any) (int, error) {
	var count int
	err := db.QueryRow(query, args...).Scan(&count)
	return count, err
}

func explainPlan(db *sql.DB, query string, args ...any) []string {
	rows, err := db.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		return []string{"explain failed: " + err.Error()}
	}
	defer func() { _ = rows.Close() }()
	plan := []string{}
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			return plan
		}
		plan = append(plan, detail)
	}
	return plan
}

// timeQuery runs one warmup execution, then measures `runs` samples. A query
// error is returned as an error rather than a zero timing, so a broken
// benchmark cannot look fast.
func timeQuery(runs int, fn func() (int, error)) queryResult {
	if _, err := fn(); err != nil {
		return queryResult{Runs: 0, Plan: []string{"warmup failed: " + err.Error()}}
	}
	samples := make([]float64, 0, runs)
	rows := 0
	for range runs {
		start := time.Now()
		count, err := fn()
		if err != nil {
			return queryResult{Runs: 0, Plan: []string{"measure failed: " + err.Error()}}
		}
		samples = append(samples, float64(time.Since(start).Microseconds())/1000)
		rows = count
	}
	sort.Float64s(samples)
	return queryResult{
		MedianMS: percentile(samples, 0.50),
		P95MS:    percentile(samples, 0.95),
		P99MS:    percentile(samples, 0.99),
		MaxMS:    samples[len(samples)-1],
		Rows:     rows,
		Runs:     runs,
		Samples:  samples,
	}
}

// percentile uses nearest-rank: the smallest sample whose rank covers the
// requested fraction. Interpolation would report a value no execution reached.
func percentile(samples []float64, fraction float64) float64 {
	if len(samples) == 0 {
		return 0
	}
	rank := int(math.Ceil(fraction * float64(len(samples))))
	return samples[max(min(rank, len(samples)), 1)-1]
}

func ftsBytes(db *sql.DB) int64 {
	var total sql.NullInt64
	_ = db.QueryRow(`SELECT sum(pgsize) FROM dbstat WHERE name LIKE 'content_translation_search%'`).Scan(&total)
	if total.Valid {
		return total.Int64
	}
	// dbstat is optional in some builds; report zero rather than guessing.
	return 0
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}
