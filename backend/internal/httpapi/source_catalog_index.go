package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/yexca/kikoto/backend/internal/openlist"
	"github.com/yexca/kikoto/backend/internal/workflow"
)

const sourceCatalogIndexWorkerType = "source_catalog_index"

const (
	// 每批行数与“批次已满”哨兵必须成对修改。行数小于哨兵会让 worker
	// 把未完成的队列判成空，且不报错。
	catalogPublishRows   = 50
	catalogLifecycleRows = 100
	catalogCleanupRows   = 100
	catalogStagingRows   = 100
	catalogRetireRoots   = 100
	catalogExpandRows    = 100

	// catalogPublishBatchesPerPass bounds publication work in one workflow
	// invocation. Each batch holds the write lock for at most
	// catalogPublishRows rows, and the remaining backlog returns to the
	// durable queue. Eight batches keep the per-pass total at 400 rows while
	// halving the longest single hold.
	catalogPublishBatchesPerPass = 8
	// Cleanup keeps its own pass count so the smaller publish batch does not
	// double the rows one pass scans for retirement.
	catalogCleanupBatchesPerPass = 4
)

type catalogIndexOptions struct {
	PageSize             int   `json:"pageSize"`
	PagesPerBatch        int   `json:"pagesPerBatch"`
	Concurrency          int   `json:"concurrency"`
	IntervalMS           int   `json:"intervalMs"`
	MaxResponseBytes     int64 `json:"maxResponseBytes"`
	MinFreeBytes         int64 `json:"minFreeBytes"`
	MaxPagesPerDirectory int   `json:"maxPagesPerDirectory"`
}

func defaultCatalogIndexOptions() catalogIndexOptions {
	return catalogIndexOptions{100, 8, 1, 1000, openlist.MaxResponseBytes, 2 << 30, 10000}
}

func (o catalogIndexOptions) validate() error {
	if o.PageSize < 1 || o.PageSize > 500 || o.PagesPerBatch < 1 || o.PagesPerBatch > 32 || o.Concurrency < 1 || o.Concurrency > 4 || o.IntervalMS < 100 || o.IntervalMS > 60000 || o.MaxResponseBytes < 1024 || o.MaxResponseBytes > 8<<20 || o.MinFreeBytes < 0 || o.MinFreeBytes > 1<<40 || o.MaxPagesPerDirectory < 1 || o.MaxPagesPerDirectory > 100000 {
		return errors.New("invalid catalog scan limits")
	}
	return nil
}

type catalogIndexInput struct {
	SourceID int64 `json:"sourceId"`
}
type catalogIndexQueued struct {
	RunID    int64 `json:"runId"`
	Existing bool  `json:"existing"`
}
type catalogDirectoryProgress struct {
	Path      string `json:"path"`
	Status    string `json:"status"`
	NextPage  int    `json:"nextPage"`
	ErrorCode string `json:"errorCode"`
}
type catalogPartitionProgress struct {
	Path        string `json:"path"`
	Directories int    `json:"directories"`
	Completed   int    `json:"completed"`
	Failed      int    `json:"failed"`
	Restricted  int    `json:"restricted"`
	Empty       int    `json:"unconfirmedEmpty"`
}

// catalogSidecarFailureCounts is the operator's view of cards that left the
// automatic credit-file queue. It is a projection only: the retry route owns
// the bookkeeping and this query never writes.
type catalogSidecarFailureCounts struct {
	Stopped                int `json:"stopped"`
	TemporarilyUnavailable int `json:"temporarilyUnavailable"`
	Unreadable             int `json:"unreadable"`
	RequestBudget          int `json:"requestBudget"`
}

type catalogIndexProgress struct {
	RunID               int64                       `json:"runId"`
	Status              string                      `json:"status"`
	PauseReason         string                      `json:"pauseReason"`
	Options             catalogIndexOptions         `json:"options"`
	Requests            int                         `json:"requests"`
	Directories         int                         `json:"directories"`
	Completed           int                         `json:"completedDirectories"`
	Pending             int                         `json:"pendingDirectories"`
	Failed              int                         `json:"failedDirectories"`
	Restricted          int                         `json:"restrictedDirectories"`
	Empty               int                         `json:"unconfirmedEmptyDirectories"`
	Observed            int                         `json:"observedEntries"`
	Indexed             int                         `json:"indexedEntries"`
	StagingDirectories  int                         `json:"stagingDirectories"`
	StagingObservations int                         `json:"stagingObservations"`
	StartedAt           string                      `json:"startedAt"`
	UpdatedAt           string                      `json:"updatedAt"`
	Partitions          []catalogPartitionProgress  `json:"partitions"`
	PartitionTotal      int                         `json:"partitionTotal"`
	Details             []catalogDirectoryProgress  `json:"details"`
	DetailsTotal        int                         `json:"detailsTotal"`
	SidecarFailures     catalogSidecarFailureCounts `json:"sidecarFailures"`
}

func (s *Server) catalogIndexSource(ctx context.Context, id int64) (remoteSourceForUse, error) {
	source, err := s.loadRemoteSourceForUse(ctx, id)
	if err == nil && (!source.Enabled || source.SourceType != sourceTypeOpenList) {
		err = errRemoteSourceNotUsable
	}
	return source, err
}

func (s *Server) startSourceCatalogIndex(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePermission(w, r, "sources:write"); !ok {
		return
	}
	id, err := parseInt64PathValue(r, "id")
	if err != nil {
		writeAPIError(w, 400, "invalid_request", "invalid source id", false)
		return
	}
	if _, err = s.catalogIndexSource(r.Context(), id); err != nil {
		writeUpstreamError(w, err)
		return
	}
	options := defaultCatalogIndexOptions()
	var saved string
	err = s.db.QueryRowContext(r.Context(), "SELECT options_json FROM source_catalog_scan WHERE file_source_id=?", id).Scan(&saved)
	if err == nil {
		err = json.Unmarshal([]byte(saved), &options)
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeError(w, err)
		return
	}
	request := struct {
		catalogIndexOptions
		// Force starts a new observation generation after a failed, partial or
		// paused run. Joining an active run still wins, so one source never
		// runs two full crawls at once.
		Force bool `json:"force"`
	}{catalogIndexOptions: options}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&request); err != nil {
		writeAPIError(w, 400, "invalid_request", "invalid catalog scan options", false)
		return
	}
	if err = decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		writeAPIError(w, 400, "invalid_request", "request must contain one options object", false)
		return
	}
	if err = request.validate(); err != nil {
		writeAPIError(w, 400, "invalid_request", err.Error(), false)
		return
	}
	result, err := s.enqueueSourceCatalogIndex(r.Context(), id, request.catalogIndexOptions, request.Force)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}

// enqueueSourceCatalogIndex is the single-source dispatch boundary. An active
// run is joined; interrupted/partial scans resume their completed directory set.
// A completed run starts a new observation generation without dropping cards.
func (s *Server) enqueueSourceCatalogIndex(ctx context.Context, sourceID int64, options catalogIndexOptions, force bool) (catalogIndexQueued, error) {
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return catalogIndexQueued{}, err
	}
	defer release()
	result, err := s.enqueueSourceCatalogIndexTx(ctx, tx.Tx, sourceID, options, "manual", force)
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func (s *Server) enqueueSourceCatalogIndexTx(ctx context.Context, tx *sql.Tx, sourceID int64, options catalogIndexOptions, trigger string, force bool) (catalogIndexQueued, error) {
	var runID int64
	var status string
	err := tx.QueryRowContext(ctx, `SELECT scan.workflow_run_id,run.status FROM source_catalog_scan scan JOIN workflow_run run ON run.id=scan.workflow_run_id WHERE scan.file_source_id=?`, sourceID).Scan(&runID, &status)
	if err == nil && (status == "queued" || status == "running") {
		return catalogIndexQueued{runID, true}, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return catalogIndexQueued{}, err
	}
	if runID == 0 {
		// Source ids may be reused after deletion. A replacement source must
		// earn its own first complete scan before inheriting a daily schedule.
		if _, err = tx.ExecContext(ctx, `DELETE FROM app_setting WHERE key=?`, catalogDailySettingPrefix+strconv.FormatInt(sourceID, 10)); err != nil {
			return catalogIndexQueued{}, err
		}
	}
	if runID > 0 && status != "succeeded" && !force {
		// Resume only unresolved directories. Already published identities and
		// personal state are independent of this staging/checkpoint data.
		for _, q := range []string{
			`UPDATE source_catalog_directory SET status='pending',next_page=1,expected_total=-1,children_cursor='',revision=revision+1,error_code='',attempts=0 WHERE file_source_id=? AND generation=(SELECT workflow_run_id FROM source_catalog_scan WHERE file_source_id=?) AND (status IN ('failed','restricted','empty') OR (status='pending' AND (SELECT json_extract(options_json,'$.pageSize') FROM source_catalog_scan WHERE file_source_id=source_catalog_directory.file_source_id)<>?))`,
		} {
			if _, err = tx.ExecContext(ctx, q, sourceID, sourceID, options.PageSize); err != nil {
				return catalogIndexQueued{}, err
			}
		}
		// A resume re-observes directories the previous attempt could not finish,
		// so it can publish entries the run had already passed. The lifecycle and
		// cleanup cursors record how far the post-processing walked this
		// generation; both must start over or those entries keep their previous
		// status and older file rows forever. The sweep is idempotent: it only
		// rewrites status and deletes rows an older generation left.
		if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_scan SET pause_reason='',options_json=?,lifecycle_stage='observe',lifecycle_cursor=0,publish_cleanup_cursor=0,updated_at=CURRENT_TIMESTAMP WHERE file_source_id=?`, mustJSON(options), sourceID); err != nil {
			return catalogIndexQueued{}, err
		}
		if err = resumeCatalogWorkflowTx(ctx, tx, runID); err != nil {
			return catalogIndexQueued{}, err
		}
		return catalogIndexQueued{runID, true}, nil
	}
	definitionID, err := workflow.EnsureDefinition(ctx, tx, sourceCatalogIndexWorkerType, "Index source catalog", "Read anonymous audio directories in recoverable batches.", map[string]any{"nodes": []map[string]string{{"id": "scan", "type": "source_catalog_index", "displayName": "Index source catalog"}}})
	if err != nil {
		return catalogIndexQueued{}, err
	}
	input := map[string]any{"sourceId": sourceID, "requested_by_user_id": optionalUserID(ctx)}
	runID, err = workflow.InsertRun(ctx, tx, definitionID, sourceCatalogIndexWorkerType, "Index source catalog", "queued", trigger, trigger, input, map[string]any{})
	if err != nil {
		return catalogIndexQueued{}, err
	}
	nodeID, err := workflow.InsertNodeRun(ctx, tx, runID, workflow.NodeRunSpec{NodeID: "scan", NodeType: sourceCatalogIndexWorkerType, DisplayName: "Index source catalog", Position: 1, Status: "queued", Input: input})
	if err != nil {
		return catalogIndexQueued{}, err
	}
	_, err = workflow.InsertJob(ctx, tx, runID, workflow.JobSpec{NodeRunID: nodeID, WorkerType: sourceCatalogIndexWorkerType, Status: "queued", Priority: workflow.JobPriorityBackground, ResourceKey: "remote:catalog", Payload: catalogIndexInput{sourceID}, Checkpoint: map[string]any{"phase": "queued"}, Recoverable: true, MaxRetries: 3})
	if err != nil {
		return catalogIndexQueued{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO source_catalog_scan(file_source_id,workflow_run_id,options_json) VALUES(?,?,?) ON CONFLICT(file_source_id) DO UPDATE SET workflow_run_id=excluded.workflow_run_id,options_json=excluded.options_json,pause_reason='',requests=0,lifecycle_stage='observe',lifecycle_cursor=0,publish_cleanup_cursor=0,started_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP`, sourceID, runID, mustJSON(options))
	if err != nil {
		return catalogIndexQueued{}, err
	}
	if err = enqueueCatalogDirectoryTx(ctx, tx, sourceID, runID, "/", "/", ""); err != nil {
		return catalogIndexQueued{}, err
	}
	return catalogIndexQueued{runID, false}, nil
}

func resumeCatalogWorkflowTx(ctx context.Context, tx *sql.Tx, runID int64) error {
	for _, q := range []string{
		`UPDATE workflow_job SET status='queued',error_message='',available_at=NULL,locked_by='',locked_at=NULL,heartbeat_at=NULL,retry_count=0,resume_count=0,updated_at=CURRENT_TIMESTAMP WHERE workflow_run_id=?`,
		`UPDATE workflow_node_run SET status='queued',error_message='',finished_at=NULL WHERE workflow_run_id=?`,
		`UPDATE workflow_run SET status='queued',finished_at=NULL WHERE id=?`,
	} {
		if _, err := tx.ExecContext(ctx, q, runID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) getSourceCatalogIndex(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePermission(w, r, "sources:write"); !ok {
		return
	}
	id, err := parseInt64PathValue(r, "id")
	if err != nil {
		writeAPIError(w, 400, "invalid_request", "invalid source id", false)
		return
	}
	sidecarFailures, err := s.sourceCatalogSidecarFailures(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	progress, err := s.sourceCatalogProgress(r.Context(), id, max(1, queryInt(r, "page", 1)))
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, 200, catalogIndexProgress{Status: "idle", Options: defaultCatalogIndexOptions(), Partitions: []catalogPartitionProgress{}, Details: []catalogDirectoryProgress{}, SidecarFailures: sidecarFailures})
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	progress.SidecarFailures = sidecarFailures
	writeJSON(w, 200, progress)
}
