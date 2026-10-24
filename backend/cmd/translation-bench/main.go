// Command translation-bench measures the persistent translation store at
// several corpus sizes. It creates a disposable database, applies the real
// migration chain and the real queries, and reports timings plus file sizes.
//
//	go run ./cmd/translation-bench --rows 8000,32000 --runs 20
package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yexca/kikoto/backend/internal/storage"
	"github.com/yexca/kikoto/backend/migrations"
	_ "modernc.org/sqlite"
)

// benchLanguages mirrors the four languages the model writes. One row per
// language per entity is what production will hold.
var benchLanguages = []string{"en", "zh-Hans", "ja", "ko"}

type queryResult struct {
	MedianMS float64 `json:"medianMs"`
	P95MS    float64 `json:"p95Ms"`
	MaxMS    float64 `json:"maxMs"`
	Rows     int     `json:"rows"`
}

type sizeReport struct {
	Rows          int         `json:"rows"`
	InsertSeconds float64     `json:"insertSeconds"`
	Checkpoints   int         `json:"checkpoints"`
	WalPeakBytes  int64       `json:"walPeakBytes"`
	DBBytes       int64       `json:"dbBytes"`
	FTSBytes      int64       `json:"ftsBytes"`
	AfterVacuum   int64       `json:"afterVacuumBytes"`
	Exact         queryResult `json:"exact"`
	FTS           queryResult `json:"fts"`
	Short         queryResult `json:"short"`
	ShortPlan     []string    `json:"shortPlan"`
}

func main() {
	rowsFlag := flag.String("rows", "8000,32000,65000", "comma separated corpus sizes")
	runs := flag.Int("runs", 20, "measured repetitions per query")
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
		fmt.Printf("rows=%-7d insert=%.1fs db=%.1fMB fts=%.1fMB vacuum=%.1fMB exact p95=%.2fms fts p95=%.2fms short p95=%.2fms\n",
			size, report.InsertSeconds, mb(report.DBBytes), mb(report.FTSBytes), mb(report.AfterVacuum),
			report.Exact.P95MS, report.FTS.P95MS, report.Short.P95MS)
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
	if err = storage.MigrateFS(db, migrations.Files, "bench"); err != nil {
		return sizeReport{}, err
	}
	report := sizeReport{Rows: rows}
	start := time.Now()
	walPeak := int64(0)
	const batch = 1000
	for begin := 0; begin < rows; begin += batch {
		end := begin + batch
		if end > rows {
			end = rows
		}
		if err = insertBatch(db, begin, end); err != nil {
			return report, err
		}
		// Mirror the prefill pass: a checkpoint every five thousand rows keeps
		// the WAL bounded instead of growing with the whole corpus.
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
	sampleKey, sampleTitle, err := sampleRow(db)
	if err != nil {
		return report, err
	}
	report.Exact = timeQuery(runs, func() (int, error) {
		var count int
		err := db.QueryRow(`SELECT count(*) FROM content_translation WHERE cache_key=?`, sampleKey).Scan(&count)
		return count, err
	})
	term := searchTerm(sampleTitle)
	report.FTS = timeQuery(runs, func() (int, error) {
		return countFTS(db, term)
	})
	report.Short = timeQuery(runs, func() (int, error) {
		return countShort(db, firstRunes(sampleTitle, 2))
	})
	report.ShortPlan = explainPlan(db, shortSearchSQL, "zh-Hans", "entry", firstRunes(sampleTitle, 2))
	if _, err = db.Exec("VACUUM"); err != nil {
		return report, err
	}
	report.AfterVacuum = fileSize(path)
	return report, nil
}

// insertBatch writes synthetic titles in three scripts so the trigram index and
// the language filter see a realistic mix.
func insertBatch(db *sql.DB, begin, end int) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	statement, err := tx.Prepare(`INSERT INTO content_translation
		(cache_key,entity_kind,entity_id,field,language,source_text,translated_text,origin,model,template_version)
		VALUES(?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer func() { _ = statement.Close() }()
	for index := begin; index < end; index++ {
		title := syntheticTitle(index)
		key := fmt.Sprintf("%064x", index+1)
		language := benchLanguages[(index/3)%len(benchLanguages)]
		if _, err = statement.Exec(key, "entry", index/3+1, "title", language, title, title, "model", "bench", "index-user-v1"); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func syntheticTitle(index int) string {
	switch index % 3 {
	case 0:
		return fmt.Sprintf("催眠音声 第%d話 癒しの時間", index)
	case 1:
		return fmt.Sprintf("Example audio episode %d with a long descriptive title", index)
	default:
		return fmt.Sprintf("동면 음성 %d 편안한 밤", index)
	}
}

func sampleRow(db *sql.DB) (string, string, error) {
	var key, title string
	err := db.QueryRow(`SELECT cache_key,translated_text FROM content_translation ORDER BY random() LIMIT 1`).Scan(&key, &title)
	return key, title, err
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

// countFTS mirrors the store query: no explicit ordering, capped at the page
// budget, so SQLite can stop once the cap is reached.
func countFTS(db *sql.DB, term string) (int, error) {
	var count int
	err := db.QueryRow(`SELECT count(*) FROM (
		SELECT t.entity_id FROM content_translation_search
		JOIN content_translation AS t ON t.id = content_translation_search.rowid
		WHERE content_translation_search MATCH ? AND t.language = ? AND t.entity_kind = ?
		LIMIT 5000
	)`, `"`+strings.ReplaceAll(term, `"`, `""`)+`"`, "zh-Hans", "entry").Scan(&count)
	return count, err
}

const shortSearchSQL = `SELECT count(*) FROM (
	WITH RECURSIVE segments(start_id,max_id) AS (
		SELECT 0,COALESCE(MAX(id),0) FROM content_translation
		WHERE language=? AND entity_kind=?
		UNION ALL
		SELECT start_id+4096,max_id FROM segments WHERE start_id+4096<=max_id
	)
	SELECT ct.entity_id FROM segments seg
	CROSS JOIN content_translation ct
	  ON ct.id>seg.start_id AND ct.id<=seg.start_id+4096 AND ct.id<=seg.max_id
	WHERE ct.language=? AND ct.entity_kind=? AND ct.field='title'
	  AND instr(lower(ct.translated_text),lower(?))>0
)`

func countShort(db *sql.DB, term string) (int, error) {
	var count int
	err := db.QueryRow(shortSearchSQL, "zh-Hans", "entry", "zh-Hans", "entry", term).Scan(&count)
	return count, err
}

func explainPlan(db *sql.DB, query string, args ...any) []string {
	rows, err := db.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		return nil
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

func timeQuery(runs int, fn func() (int, error)) queryResult {
	samples := make([]float64, 0, runs)
	rows := 0
	for range runs {
		start := time.Now()
		count, err := fn()
		if err != nil {
			return queryResult{}
		}
		samples = append(samples, float64(time.Since(start).Microseconds())/1000)
		rows = count
	}
	sort.Float64s(samples)
	return queryResult{
		MedianMS: samples[len(samples)/2],
		P95MS:    samples[min(len(samples)-1, int(float64(len(samples))*0.95))],
		MaxMS:    samples[len(samples)-1],
		Rows:     rows,
	}
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
