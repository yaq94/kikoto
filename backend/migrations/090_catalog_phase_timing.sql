-- A run could spend hours between "the last directory finished" and "the job
-- ended" without any evidence of which phase owned the time. Each phase now
-- records its own wall clock, so a later run can explain the gap instead of
-- inferring it from log timestamps.
CREATE TABLE source_catalog_phase_timing (
  workflow_run_id INTEGER NOT NULL REFERENCES workflow_run(id) ON DELETE CASCADE,
  phase TEXT NOT NULL CHECK(phase IN ('scan','publish','cleanup','lifecycle','settle')),
  started_at TEXT NOT NULL,
  finished_at TEXT NOT NULL,
  duration_ms INTEGER NOT NULL CHECK(duration_ms >= 0),
  included INTEGER NOT NULL DEFAULT 1 CHECK(included IN (0,1)),
  PRIMARY KEY(workflow_run_id, phase)
);
CREATE INDEX idx_source_catalog_phase_timing_run ON source_catalog_phase_timing(workflow_run_id, duration_ms DESC);
