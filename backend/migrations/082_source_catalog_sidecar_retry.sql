-- A missing credit file and a temporarily unreachable one are different
-- facts. A definitive answer (404/410 or a body above the read limit) spends
-- the permanent budget, while an outage, authentication refusal or timeout
-- waits on a bounded backoff without permanently exhausting the card.
ALTER TABLE source_catalog_entry ADD COLUMN sidecar_transient_attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE source_catalog_entry ADD COLUMN sidecar_retry_at TEXT NOT NULL DEFAULT '';
ALTER TABLE source_catalog_entry ADD COLUMN sidecar_failure TEXT NOT NULL DEFAULT '';

-- Only rows that are actually due are scanned by the worker.
CREATE INDEX source_catalog_entry_sidecar_due
    ON source_catalog_entry(file_source_id, sidecar_retry_at)
    WHERE sidecar_pending=1;
