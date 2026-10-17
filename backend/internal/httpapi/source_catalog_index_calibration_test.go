package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yexca/kikoto/backend/internal/config"
	"github.com/yexca/kikoto/backend/internal/testfixture"
)

// Opt-in throughput calibration for the recoverable full source index. Each
// cell drives the production catalog-index worker one batch at a time against a
// synthetic anonymous tree served by a loopback stub, then reads the progress
// and database counters an operator would see. Nothing contacts a real source,
// and no cell asserts a throughput or latency budget: the experiment recommends
// an option combination, it does not gate one. See
// docs/development/browse-performance.md.
//
// Run with KIKOTO_SOURCE_INDEX_PERF=1.
const (
	indexCalibrationEnableEnv = "KIKOTO_SOURCE_INDEX_PERF"
	indexCalibrationCellsEnv  = "KIKOTO_SOURCE_INDEX_PERF_CELLS"
	indexCalibrationDirsEnv   = "KIKOTO_SOURCE_INDEX_PERF_DIRS"
	indexCalibrationRTTEnv    = "KIKOTO_SOURCE_INDEX_PERF_RTT_MS"
	indexCalibrationBudgetEnv = "KIKOTO_SOURCE_INDEX_PERF_BUDGET_MS"
	// indexCalibrationTestBudgetEnv bounds the whole experiment, so a long
	// matrix can be stopped after the cells that matter have measured.
	indexCalibrationTestBudgetEnv = "KIKOTO_SOURCE_INDEX_PERF_TOTAL_SECONDS"
	indexCalibrationPartitions    = 4
	indexCalibrationPageSize      = 500
	indexCalibrationWorks         = 5000
	indexCalibrationPoolSize      = 4
	indexCalibrationWorkerTick    = 2000
	indexCalibrationPaintMillis   = 25
	// indexCalibrationRefusalShare makes a refusing stub answer every eighth
	// directory request with 429 and Retry-After.
	indexCalibrationRefusalShare = 8
)

type indexCalibrationCell struct {
	// name identifies the cell in the logs and in the cell filter.
	name string
	// concurrency, intervalMS, pagesPerBatch and pageSize are the operator
	// options under calibration.
	concurrency, intervalMS, pagesPerBatch, pageSize int
	// workerTickMS emulates the coordinator's claim latency. The background
	// worker ticks every two seconds, so a real run pays this once per batch.
	workerTickMS int
	// rttMS simulates the upstream round-trip time; 0 selects the default.
	rttMS int
	// refuseEvery makes every nth stub request answer 429 with Retry-After.
	refuseEvery int
	// backoffSeconds writes the origin backoff settings when it is non-zero.
	backoffSeconds int
	// competition seeds a library and runs the recommendation and metadata tag
	// writers, so the index shares the single writer lock as it does when the
	// deployment host indexes a large source.
	competition bool
	// gomaxprocs bounds the process's parallelism for the cell when non-zero.
	gomaxprocs int
	// workDirectories is the number of work directories per partition.
	workDirectories int
}

// defaultIndexCalibrationCells is the matrix. The grid covers the accepted
// concurrency and interval range at the largest batch size; the remaining cells
// price the batch size, writer competition, an origin refusal and the process's
// parallelism.
func defaultIndexCalibrationCells(workDirectories int) []indexCalibrationCell {
	cell := func(name string, concurrency, intervalMS, pagesPerBatch int) indexCalibrationCell {
		return indexCalibrationCell{
			name: name, concurrency: concurrency, intervalMS: intervalMS,
			pagesPerBatch: pagesPerBatch, pageSize: indexCalibrationPageSize,
			workerTickMS: indexCalibrationWorkerTick, workDirectories: workDirectories,
		}
	}
	cells := []indexCalibrationCell{}
	for _, concurrency := range []int{1, 2, 4} {
		for _, intervalMS := range []int{100, 150, 300, 1000} {
			cells = append(cells, cell(fmt.Sprintf("grid/c%d-i%d", concurrency, intervalMS), concurrency, intervalMS, 32))
		}
	}
	cells = append(cells,
		cell("batch/c2-i150-p8", 2, 150, 8),
		cell("batch/c2-i150-p16", 2, 150, 16),
	)
	competition := cell("competition/c2-i150", 2, 150, 32)
	competition.competition = true
	cells = append(cells, competition)
	limited := cell("ratelimit/c2-i150", 2, 150, 32)
	limited.refuseEvery = indexCalibrationRefusalShare
	limited.backoffSeconds = 2
	cells = append(cells, limited)
	parallel := cell("gomaxprocs/c2-i150", 2, 150, 32)
	parallel.gomaxprocs = 2
	return append(cells, parallel)
}

// competitionIndexCalibrationCells price the competing writers at each interval
// the grid covers. They stay behind their own prefix so a default run stays
// affordable.
func competitionIndexCalibrationCells(workDirectories int) []indexCalibrationCell {
	cells := []indexCalibrationCell{}
	for _, intervalMS := range []int{150, 300, 1000} {
		cells = append(cells, indexCalibrationCell{
			name: fmt.Sprintf("competition/c2-i%d", intervalMS), concurrency: 2,
			intervalMS: intervalMS, pagesPerBatch: 32, pageSize: indexCalibrationPageSize,
			workerTickMS: indexCalibrationWorkerTick, workDirectories: workDirectories,
			competition: true,
		})
	}
	return cells
}

func allIndexCalibrationCells(workDirectories int) []indexCalibrationCell {
	cells := defaultIndexCalibrationCells(workDirectories)
	// The default matrix already carries the interval-150 competition cell.
	for _, cell := range competitionIndexCalibrationCells(workDirectories) {
		if cell.name != "competition/c2-i150" {
			cells = append(cells, cell)
		}
	}
	return cells
}

func selectIndexCalibrationCells(cells []indexCalibrationCell, filter string) []indexCalibrationCell {
	prefixes := []string{}
	for _, prefix := range strings.Split(filter, ",") {
		if trimmed := strings.TrimSpace(prefix); trimmed != "" {
			prefixes = append(prefixes, trimmed)
		}
	}
	if len(prefixes) == 0 {
		return cells
	}
	selected := []indexCalibrationCell{}
	for _, cell := range cells {
		for _, prefix := range prefixes {
			// A prefix ends at a cell-name boundary, so "grid/c1-i100" does not
			// also select "grid/c1-i1000".
			if cell.name == prefix || strings.HasPrefix(cell.name, prefix+"/") ||
				strings.HasPrefix(cell.name, prefix+"-") {
				selected = append(selected, cell)
				break
			}
		}
	}
	return selected
}

// indexCalibrationTree is a synthetic anonymous audio tree: four partitions,
// each holding work directories with one format subdirectory. Every work
// directory qualifies as one published catalog entry. It returns the tree and
// its total directory count, which is also the number of directory pages the
// index needs at this page size.
func indexCalibrationTree(workDirectories int) (map[string][]map[string]any, int) {
	dir := func(name string) map[string]any { return map[string]any{"name": name, "is_dir": true} }
	audio := func(name string) map[string]any { return map[string]any{"name": name, "size": 1024} }
	tree := map[string][]map[string]any{}
	directories := 1
	root := []map[string]any{}
	for partition := range indexCalibrationPartitions {
		partitionName := fmt.Sprintf("Partition %c", 'A'+partition)
		partitionPath := "/" + partitionName
		root = append(root, dir(partitionName))
		directories++
		entries := []map[string]any{}
		for work := range workDirectories {
			workName := fmt.Sprintf("Example Work %c%02d", 'A'+partition, work)
			workPath := partitionPath + "/" + workName
			entries = append(entries, dir(workName))
			directories++
			tree[workPath] = []map[string]any{audio("01.mp3"), audio("02.flac"), dir("MP3")}
			tree[workPath+"/MP3"] = []map[string]any{audio("01.mp3"), audio("02.mp3")}
			directories++
		}
		tree[partitionPath] = entries
	}
	tree["/"] = root
	return tree, directories
}

// indexCalibrationStub serves the OpenList directory protocol from memory,
// counts what the worker asked for, and can refuse a request the way a
// rate-limited origin does.
type indexCalibrationStub struct {
	tree        map[string][]map[string]any
	server      *httptest.Server
	rtt         time.Duration
	refuseEvery int
	retryAfter  int
	mu          sync.Mutex
	requests    int
	refusals    int
}

func newIndexCalibrationStub(tree map[string][]map[string]any, rtt time.Duration, refuseEvery, retryAfter int) *indexCalibrationStub {
	stub := &indexCalibrationStub{tree: tree, rtt: rtt, refuseEvery: refuseEvery, retryAfter: retryAfter}
	stub.server = httptest.NewServer(http.HandlerFunc(stub.handle))
	return stub
}

func (s *indexCalibrationStub) URL() string { return s.server.URL }

func (s *indexCalibrationStub) Close() { s.server.Close() }

func (s *indexCalibrationStub) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests, s.refusals = 0, 0
}

func (s *indexCalibrationStub) counts() (requests, refusals int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests, s.refusals
}

func (s *indexCalibrationStub) handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/fs/list" {
		http.NotFound(w, r)
		return
	}
	var input struct {
		Path    string `json:"path"`
		Page    int    `json:"page"`
		PerPage int    `json:"per_page"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.requests++
	refused := s.refuseEvery > 0 && s.requests%s.refuseEvery == 0
	if refused {
		s.refusals++
	}
	s.mu.Unlock()
	if s.rtt > 0 {
		time.Sleep(s.rtt)
	}
	if refused {
		w.Header().Set("Retry-After", strconv.Itoa(max(s.retryAfter, 1)))
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}
	items, ok := s.tree[input.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	start := min(len(items), max((input.Page-1)*input.PerPage, 0))
	end := min(len(items), start+input.PerPage)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code": 200,
		"data": map[string]any{"content": items[start:end], "total": len(items)},
	})
}

// indexCalibrationLogMarkers lists the signals an operator sees when writers
// contend. The experiment counts them from the process log instead of scraping
// journal text.
var indexCalibrationLogMarkers = []string{
	"database is locked",
	"sqlite_busy",
	"context deadline exceeded",
	"recommendation catalog batch failed",
	"project queued metadata tags",
	"run queued workflow job",
}

type indexCalibrationLogCounter struct {
	mu     sync.Mutex
	counts map[string]int
}

func (c *indexCalibrationLogCounter) Write(p []byte) (int, error) {
	line := strings.ToLower(string(p))
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, marker := range indexCalibrationLogMarkers {
		if strings.Contains(line, marker) {
			c.counts[marker]++
		}
	}
	return len(p), nil
}

func (c *indexCalibrationLogCounter) snapshot() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	counts := map[string]int{}
	for marker, count := range c.counts {
		counts[marker] = count
	}
	return counts
}

// indexCalibrationPoolSampler samples the connection pool while a cell runs, so
// a saturated pool shows up as a sample instead of as a rounded cumulative wait.
type indexCalibrationPoolSampler struct {
	db          *sql.DB
	stop        chan struct{}
	done        chan struct{}
	mu          sync.Mutex
	maxInUse    int
	saturated   int
	longestWait time.Duration
	lastWait    time.Duration
}

func startIndexCalibrationPoolSampler(db *sql.DB) *indexCalibrationPoolSampler {
	sampler := &indexCalibrationPoolSampler{db: db, stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(sampler.done)
		ticker := time.NewTicker(indexCalibrationPaintMillis * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-sampler.stop:
				return
			case <-ticker.C:
			}
			stats := db.Stats()
			sampler.mu.Lock()
			if stats.InUse > sampler.maxInUse {
				sampler.maxInUse = stats.InUse
			}
			if stats.InUse >= stats.MaxOpenConnections {
				sampler.saturated++
			}
			if delta := stats.WaitDuration - sampler.lastWait; delta > sampler.longestWait {
				sampler.longestWait = delta
			}
			sampler.lastWait = stats.WaitDuration
			sampler.mu.Unlock()
		}
	}()
	return sampler
}

func (s *indexCalibrationPoolSampler) stopAndRead() (maxInUse, saturated int, longestWaitMillis float64) {
	close(s.stop)
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.maxInUse, s.saturated, float64(s.longestWait) / float64(time.Millisecond)
}

type indexCalibrationMetrics struct {
	cell              string
	directories       int
	entries           int
	requests          int
	refusals          int
	pages             int
	completed         int
	indexed           int
	failed            int
	restricted        int
	status            string
	elapsed           time.Duration
	ticks             int
	cpuSeconds        float64
	cores             float64
	drainFailures     int
	poolWaitCount     int64
	poolWaitMS        float64
	poolWaitMaxMS     float64
	poolMaxInUse      int
	poolSaturated     int
	budgetHit         bool
	logCounts         map[string]int
	incompleteDetails []catalogDirectoryProgress
}

func TestSourceCatalogIndexThroughputCalibration(t *testing.T) {
	if os.Getenv(indexCalibrationEnableEnv) != "1" {
		t.Skip("opt-in performance experiment")
	}
	workDirectories := indexCalibrationIntEnv(t, indexCalibrationDirsEnv, 24)
	rttMS := indexCalibrationIntEnv(t, indexCalibrationRTTEnv, 150)
	budgetMS := indexCalibrationIntEnv(t, indexCalibrationBudgetEnv, 900000)
	cells := selectIndexCalibrationCells(allIndexCalibrationCells(workDirectories), os.Getenv(indexCalibrationCellsEnv))
	if len(cells) == 0 {
		t.Fatalf("no calibration cell matched %q", os.Getenv(indexCalibrationCellsEnv))
	}
	t.Logf("calibration conditions runtime=%s logical_cpus=%d gomaxprocs=%d work_directories_per_partition=%d rtt_ms=%d budget_ms=%d pool=%d cells=%d",
		runtime.Version(), runtime.NumCPU(), runtime.GOMAXPROCS(0), workDirectories, rttMS, budgetMS,
		indexCalibrationPoolSize, len(cells))
	results := []indexCalibrationMetrics{}
	testBudget := time.Duration(indexCalibrationIntEnv(t, indexCalibrationTestBudgetEnv, 0)) * time.Second
	testStart := time.Now()
	for index, cell := range cells {
		if testBudget > 0 && time.Since(testStart) > testBudget {
			t.Logf("calibration stopped before cell=%s after the %s experiment budget", cell.name, testBudget)
			break
		}
		t.Logf("calibration starting cell=%d/%d name=%s concurrency=%d interval_ms=%d pages_per_batch=%d competition=%t refuse_every=%d gomaxprocs=%d",
			index+1, len(cells), cell.name, cell.concurrency, cell.intervalMS, cell.pagesPerBatch,
			cell.competition, cell.refuseEvery, cell.gomaxprocs)
		result := runIndexCalibrationCell(t, cell, rttMS, time.Duration(budgetMS)*time.Millisecond)
		results = append(results, result)
		logIndexCalibrationResult(t, result)
	}
	logIndexCalibrationRecommendation(t, results)
}

func runIndexCalibrationCell(t *testing.T, cell indexCalibrationCell, defaultRTTMS int, budget time.Duration) indexCalibrationMetrics {
	t.Helper()
	if cell.gomaxprocs > 0 {
		previous := runtime.GOMAXPROCS(cell.gomaxprocs)
		defer runtime.GOMAXPROCS(previous)
	}
	db := openMigratedTestDBWithProductionPool(t)
	if cell.competition {
		testfixture.SeedRecommendationScale(t, db, indexCalibrationWorks, 20)
		// Provider snapshots give the metadata tag projection real work, as a
		// synced library does in production.
		if _, err := db.Exec(`INSERT INTO metadata_snapshot(work_id,provider_id,external_id,snapshot_json) SELECT id,2,'example','{}' FROM work`); err != nil {
			t.Fatal(err)
		}
	}
	if cell.backoffSeconds > 0 {
		value := strconv.Itoa(cell.backoffSeconds)
		for _, key := range []string{"remote_rate_limit_backoff_seconds", "remote_max_backoff_seconds"} {
			if _, err := db.Exec(`INSERT INTO app_setting(key,value_json) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value_json=excluded.value_json`, key, value); err != nil {
				t.Fatal(err)
			}
		}
	}
	server := NewServer(db, config.Config{Mode: config.ModeProduction, RootUsername: "synthetic-user", DataRoot: t.TempDir(), CacheRoot: t.TempDir()})
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	if err := server.LoadAccessPolicy(context.Background()); err != nil {
		t.Fatal(err)
	}
	createTestAdministrator(t, server, "synthetic-user", "synthetic-password")
	handler := server.Routes()
	cookie := loginTestSession(t, handler, "synthetic-user", "synthetic-password")

	rttMS := cell.rttMS
	if rttMS <= 0 {
		rttMS = defaultRTTMS
	}
	tree, directories := indexCalibrationTree(cell.workDirectories)
	stub := newIndexCalibrationStub(tree, time.Duration(rttMS)*time.Millisecond, cell.refuseEvery, cell.backoffSeconds)
	defer stub.Close()
	input := mustJSON(map[string]any{
		"displayName": "Example Remote A", "sourceType": "openlist", "enabled": true,
		"config":   map[string]any{"openListRoots": []string{}},
		"endpoint": map[string]any{"apiUrl": stub.URL(), "baseUrl": stub.URL()},
	})
	source := reviewRequest(t, handler, cookie, http.MethodPost, "/api/file-sources", input, http.StatusCreated)
	sourceID := int64(source["id"].(float64))

	var workers sync.WaitGroup
	stopWorkers := func() {}
	if cell.competition {
		ctx, cancel := context.WithCancel(context.Background())
		stopWorkers = cancel
		workers.Add(2)
		go func() {
			defer workers.Done()
			server.RunRecommendationWorker(ctx)
		}()
		go func() {
			defer workers.Done()
			server.runMetadataTagQueueWorker(ctx)
		}()
	}
	result := measureIndexCalibration(t, server, handler, cookie, sourceID, cell, stub, directories, budget)
	stopWorkers()
	workers.Wait()
	if cell.competition {
		var pending int
		if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM recommendation_catalog_dirty)
			+(SELECT COUNT(*) FROM recommendation_name_dirty)+(SELECT COUNT(*) FROM work_metadata_tag_dirty)`).Scan(&pending); err != nil {
			t.Fatal(err)
		}
		t.Logf("calibration cell=%s competition_writers=2 competition_works=%d projector_backlog=%d", cell.name, indexCalibrationWorks, pending)
	}
	return result
}

func measureIndexCalibration(t *testing.T, server *Server, handler http.Handler, cookie *http.Cookie, sourceID int64, cell indexCalibrationCell, stub *indexCalibrationStub, directories int, budget time.Duration) indexCalibrationMetrics {
	t.Helper()
	ctx := context.Background()
	result := indexCalibrationMetrics{
		cell: cell.name, directories: directories,
		entries: indexCalibrationPartitions * cell.workDirectories,
	}
	// Creating a source queues its own availability check. Settle every
	// available job first, so the measured window contains only the index run.
	result.drainFailures = drainIndexCalibrationJobs(t, server)
	options := mustJSON(map[string]any{
		"pageSize": cell.pageSize, "pagesPerBatch": cell.pagesPerBatch,
		"concurrency": cell.concurrency, "intervalMs": cell.intervalMS,
	})
	reviewRequest(t, handler, cookie, http.MethodPost,
		fmt.Sprintf("/api/remote-sources/%d/catalog/index", sourceID), options, http.StatusAccepted)
	stub.reset()

	logCapture := &indexCalibrationLogCounter{counts: map[string]int{}}
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logCapture, nil)))
	defer slog.SetDefault(previousLogger)
	sampler := startIndexCalibrationPoolSampler(server.db)
	poolBefore := server.db.Stats()
	cpuBefore := calibrationProcessCPUSeconds()
	start := time.Now()
	for {
		if time.Since(start) > budget {
			result.budgetHit = true
			break
		}
		progress, err := server.sourceCatalogProgress(ctx, sourceID, 1)
		if err != nil {
			t.Fatal(err)
		}
		if indexCalibrationTerminal(progress.Status) {
			break
		}
		if cell.workerTickMS > 0 {
			time.Sleep(time.Duration(cell.workerTickMS) * time.Millisecond)
			result.ticks++
		}
		if err := server.runNextQueuedWorkflowJob(ctx); err != nil && !errors.Is(err, context.Canceled) {
			t.Logf("calibration cell=%s job error: %v", cell.name, err)
		}
	}
	result.elapsed = time.Since(start)
	result.cpuSeconds = calibrationProcessCPUSeconds() - cpuBefore
	if result.elapsed > 0 {
		result.cores = result.cpuSeconds / result.elapsed.Seconds()
	}
	result.poolMaxInUse, result.poolSaturated, result.poolWaitMaxMS = sampler.stopAndRead()
	poolAfter := server.db.Stats()
	result.poolWaitCount = poolAfter.WaitCount - poolBefore.WaitCount
	result.poolWaitMS = float64(poolAfter.WaitDuration-poolBefore.WaitDuration) / float64(time.Millisecond)
	result.requests, result.refusals = stub.counts()
	result.logCounts = logCapture.snapshot()

	final, err := server.sourceCatalogProgress(ctx, sourceID, 1)
	if err != nil {
		t.Fatal(err)
	}
	result.status = final.Status
	result.pages = final.Requests
	result.completed = final.Completed
	result.indexed = final.Indexed
	result.failed = final.Failed
	result.restricted = final.Restricted
	result.incompleteDetails = final.Details
	if result.budgetHit {
		return result
	}
	// The functional contract of a cell whose stub answered every directory
	// page: complete coverage with exactly one published entry per work
	// directory, and every directory accounted for under some status.
	if cell.refuseEvery == 0 {
		if final.Status != "complete" {
			t.Errorf("calibration cell=%s ended %s, want complete (failed=%d restricted=%d details=%v)",
				cell.name, final.Status, final.Failed, final.Restricted, final.Details)
		}
		if final.Indexed != result.entries {
			t.Errorf("calibration cell=%s published %d entries, want %d", cell.name, final.Indexed, result.entries)
		}
		if accounted := final.Completed + final.Failed + final.Restricted + final.Empty; accounted != result.directories {
			t.Errorf("calibration cell=%s accounted for %d directories, want %d",
				cell.name, accounted, result.directories)
		}
	} else {
		// A refusing origin must never produce a claim of complete coverage.
		// The production worker marks the refused page failed and ends partial;
		// the next start resumes it from the saved cursors. The experiment only
		// records that shape, so its numbers stay comparable with the other
		// cells.
		if result.refusals == 0 {
			t.Errorf("calibration cell=%s never received the injected refusal", cell.name)
		}
		if final.Status == "complete" {
			t.Errorf("calibration cell=%s claimed complete coverage after %d refusals", cell.name, result.refusals)
		}
	}
	return result
}

// drainIndexCalibrationJobs settles every available queued job before the index
// starts. Failures are expected: the synthetic source only answers the
// directory protocol.
func drainIndexCalibrationJobs(t *testing.T, server *Server) (failed int) {
	t.Helper()
	ctx := context.Background()
	for range 200 {
		var queued int
		if err := server.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_job AS job
			JOIN workflow_run AS run ON run.id=job.workflow_run_id
			WHERE job.status='queued' AND run.status='queued'
				AND (job.available_at IS NULL OR job.available_at <= CURRENT_TIMESTAMP)`).Scan(&queued); err != nil {
			t.Fatal(err)
		}
		if queued == 0 {
			return failed
		}
		if runErr := server.runNextQueuedWorkflowJob(ctx); runErr != nil && !errors.Is(runErr, context.Canceled) {
			failed++
		}
	}
	return failed
}

func indexCalibrationTerminal(status string) bool {
	return status != "queued" && status != "running"
}

func logIndexCalibrationResult(t *testing.T, result indexCalibrationMetrics) {
	t.Helper()
	minutes := result.elapsed.Minutes()
	directoriesPerMinute, requestsPerMinute := 0.0, 0.0
	if minutes > 0 {
		directoriesPerMinute = float64(result.completed) / minutes
		requestsPerMinute = float64(result.requests) / minutes
	}
	t.Logf("calibration cell=%s directories=%d entries=%d requests=%d pages=%d status=%s indexed=%d completed=%d failed=%d restricted=%d "+
		"elapsed_ms=%d ticks=%d directories_per_min=%.1f requests_per_min=%.1f refusals=%d drain_failures=%d "+
		"pool_wait_count=%d pool_wait_ms=%.3f pool_wait_max_ms=%.3f pool_max_in_use=%d pool_saturated_samples=%d "+
		"cpu_seconds=%.3f cores=%.2f logs=%v",
		result.cell, result.directories, result.entries, result.requests, result.pages, result.status, result.indexed,
		result.completed, result.failed, result.restricted, result.elapsed.Milliseconds(), result.ticks,
		directoriesPerMinute, requestsPerMinute, result.refusals, result.drainFailures,
		result.poolWaitCount, result.poolWaitMS, result.poolWaitMaxMS, result.poolMaxInUse, result.poolSaturated,
		result.cpuSeconds, result.cores, result.logCounts)
	if len(result.incompleteDetails) > 0 {
		t.Logf("calibration cell=%s incomplete=%v", result.cell, result.incompleteDetails)
	}
}

// logIndexCalibrationRecommendation ranks the cells that measured a complete
// run and prints the fastest ones. It compares cells on the same host within
// one run, which is the only comparison the experiment supports.
func logIndexCalibrationRecommendation(t *testing.T, results []indexCalibrationMetrics) {
	t.Helper()
	complete := []indexCalibrationMetrics{}
	for _, result := range results {
		if result.status == "complete" && !result.budgetHit {
			complete = append(complete, result)
		}
	}
	if len(complete) == 0 {
		t.Log("calibration recommendation: no cell reached complete coverage")
		return
	}
	for i := 1; i < len(complete); i++ {
		for j := i; j > 0 && complete[j].elapsed < complete[j-1].elapsed; j-- {
			complete[j], complete[j-1] = complete[j-1], complete[j]
		}
	}
	for rank, result := range complete {
		t.Logf("calibration rank=%d cell=%s elapsed_ms=%d directories_per_min=%.1f pool_wait_ms=%.3f pool_wait_max_ms=%.3f cores=%.2f",
			rank+1, result.cell, result.elapsed.Milliseconds(),
			float64(result.completed)/max(result.elapsed.Minutes(), 1e-9),
			result.poolWaitMS, result.poolWaitMaxMS, result.cores)
	}
}

func indexCalibrationIntEnv(t *testing.T, name string, fallback int) int {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		t.Fatalf("%s must be an integer: %v", name, err)
	}
	return parsed
}
