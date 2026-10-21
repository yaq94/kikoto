package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// catalogMaintenanceSetting holds the deployment hold. A hold asks the running
// index to stop at the next directory boundary so a rebuild can replace the
// binary without killing a job mid-page. The hold never mutates scan state:
// the worker checkpoints as usual and resumes from that checkpoint.
const catalogMaintenanceSetting = "catalog_maintenance"

const (
	catalogMaintenanceMinHold = 30 * time.Second
	catalogMaintenanceMaxHold = 15 * time.Minute
)

type catalogMaintenanceState struct {
	// Until is the wall clock at which the hold expires. Zero means no hold.
	Until  time.Time `json:"until"`
	Reason string    `json:"reason"`
}

func (s *Server) catalogMaintenance(ctx context.Context) (catalogMaintenanceState, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT value_json FROM app_setting WHERE key=?`, catalogMaintenanceSetting).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return catalogMaintenanceState{}, nil
	}
	if err != nil {
		return catalogMaintenanceState{}, err
	}
	var state catalogMaintenanceState
	if err = json.Unmarshal([]byte(raw), &state); err != nil {
		// A malformed value must not block indexing forever: treat it as absent
		// and let the next write replace it.
		return catalogMaintenanceState{}, nil
	}
	return state, nil
}

// catalogMaintenanceActive reports the hold end when a hold is in force. The
// caller uses the deadline as the job's next available time so a held scan does
// not spin on the queue.
func (s *Server) catalogMaintenanceActive(ctx context.Context) (bool, time.Time, error) {
	state, err := s.catalogMaintenance(ctx)
	if err != nil {
		return false, time.Time{}, err
	}
	if state.Until.IsZero() || !state.Until.After(time.Now()) {
		return false, time.Time{}, nil
	}
	return true, state.Until, nil
}

func (s *Server) writeCatalogMaintenance(ctx context.Context, state catalogMaintenanceState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO app_setting (key, value_json) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value_json=excluded.value_json`, catalogMaintenanceSetting, string(raw))
	return err
}

type catalogMaintenanceRequest struct {
	HoldSeconds int    `json:"holdSeconds"`
	Reason      string `json:"reason"`
}

type catalogMaintenanceResponse struct {
	Active          bool   `json:"active"`
	Until           string `json:"until,omitempty"`
	RemainingMillis int64  `json:"remainingMillis"`
	Reason          string `json:"reason,omitempty"`
}

func catalogMaintenanceView(state catalogMaintenanceState) catalogMaintenanceResponse {
	view := catalogMaintenanceResponse{Reason: state.Reason}
	if state.Until.After(time.Now()) {
		view.Active = true
		view.Until = state.Until.UTC().Format(time.RFC3339)
		view.RemainingMillis = int64(time.Until(state.Until) / time.Millisecond)
	}
	return view
}

// getCatalogMaintenance returns the current hold. It is readable by anyone who
// may read sources, because the UI shows "index paused for maintenance".
func (s *Server) getCatalogMaintenance(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePermission(w, r, "sources:read"); !ok {
		return
	}
	state, err := s.catalogMaintenance(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, catalogMaintenanceView(state))
}

func (s *Server) setCatalogMaintenance(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePermission(w, r, "sources:write"); !ok {
		return
	}
	var input catalogMaintenanceRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error(), false)
		return
	}
	hold := time.Duration(input.HoldSeconds) * time.Second
	if hold < catalogMaintenanceMinHold {
		hold = catalogMaintenanceMinHold
	}
	if hold > catalogMaintenanceMaxHold {
		hold = catalogMaintenanceMaxHold
	}
	state := catalogMaintenanceState{
		Until:  time.Now().Add(hold),
		Reason: strings.TrimSpace(input.Reason),
	}
	if state.Reason == "" {
		state.Reason = "maintenance"
	}
	if len(state.Reason) > 120 {
		state.Reason = state.Reason[:120]
	}
	if err := s.writeCatalogMaintenance(r.Context(), state); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, catalogMaintenanceView(state))
}

func (s *Server) clearCatalogMaintenance(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePermission(w, r, "sources:write"); !ok {
		return
	}
	if _, err := s.db.ExecContext(r.Context(), `DELETE FROM app_setting WHERE key=?`, catalogMaintenanceSetting); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, catalogMaintenanceResponse{})
}

// recordCatalogPhase stores one phase duration. Failures are non-fatal for the
// caller: timing is diagnostic evidence, never a correctness dependency, so a
// busy database must not fail the scan.
func (s *Server) recordCatalogPhase(ctx context.Context, runID int64, phase string, started time.Time) {
	finished := time.Now()
	elapsed := finished.Sub(started)
	if elapsed < 0 {
		elapsed = 0
	}
	_, _ = s.db.ExecContext(ctx, `INSERT INTO source_catalog_phase_timing(workflow_run_id,phase,started_at,finished_at,duration_ms)
		VALUES(?,?,?,?,?)
		ON CONFLICT(workflow_run_id,phase) DO UPDATE SET
			started_at=excluded.started_at, finished_at=excluded.finished_at,
			duration_ms=duration_ms+excluded.duration_ms`,
		runID, phase, started.UTC().Format("2006-01-02 15:04:05"), finished.UTC().Format("2006-01-02 15:04:05"), elapsed.Milliseconds())
}

// catalogPhaseTimings serves the per-phase table for one run so an operator can
// explain a long wall clock without reading container logs.
func (s *Server) catalogPhaseTimings(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePermission(w, r, "sources:read"); !ok {
		return
	}
	runID, err := parseInt64Query(r, "runId")
	if err != nil || runID <= 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "runId is required", false)
		return
	}
	rows, err := s.db.QueryContext(r.Context(), `SELECT phase,started_at,finished_at,duration_ms FROM source_catalog_phase_timing
		WHERE workflow_run_id=? ORDER BY duration_ms DESC`, runID)
	if err != nil {
		writeError(w, err)
		return
	}
	defer rows.Close()
	type row struct {
		Phase      string `json:"phase"`
		StartedAt  string `json:"startedAt"`
		FinishedAt string `json:"finishedAt"`
		DurationMs int64  `json:"durationMs"`
	}
	phases := []row{}
	for rows.Next() {
		var value row
		if err := rows.Scan(&value.Phase, &value.StartedAt, &value.FinishedAt, &value.DurationMs); err != nil {
			writeError(w, err)
			return
		}
		phases = append(phases, value)
	}
	if err := rows.Err(); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runId": runID, "phases": phases})
}

func parseInt64Query(r *http.Request, key string) (int64, error) {
	return strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get(key)), 10, 64)
}
