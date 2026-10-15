ALTER TABLE source_catalog_entry ADD COLUMN availability_status TEXT NOT NULL DEFAULT 'available'
    CHECK(availability_status IN ('available','unavailable','maintenance','restricted','unconfirmed_empty','missing'));
ALTER TABLE source_catalog_entry ADD COLUMN status_checked_at TEXT;
ALTER TABLE source_catalog_entry ADD COLUMN last_seen_generation INTEGER NOT NULL DEFAULT 0;
ALTER TABLE source_catalog_entry ADD COLUMN discovered_generation INTEGER NOT NULL DEFAULT 0;
ALTER TABLE source_catalog_entry ADD COLUMN content_fingerprint TEXT NOT NULL DEFAULT '';
ALTER TABLE source_catalog_entry ADD COLUMN layout_fingerprint TEXT NOT NULL DEFAULT '';
ALTER TABLE source_catalog_entry ADD COLUMN relocated_to_id INTEGER REFERENCES source_catalog_entry(id) ON DELETE RESTRICT;
CREATE INDEX source_catalog_entry_lifecycle ON source_catalog_entry(file_source_id,last_seen_generation,id);
CREATE INDEX source_catalog_entry_content ON source_catalog_entry(file_source_id,content_fingerprint);
CREATE INDEX source_catalog_entry_layout ON source_catalog_entry(file_source_id,layout_fingerprint);
CREATE INDEX source_catalog_entry_relocated ON source_catalog_entry(relocated_to_id);

ALTER TABLE source_catalog_file ADD COLUMN content_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE source_catalog_file ADD COLUMN observed_generation INTEGER NOT NULL DEFAULT 0;
ALTER TABLE source_catalog_observation ADD COLUMN content_hash TEXT NOT NULL DEFAULT '';

ALTER TABLE source_catalog_scan ADD COLUMN lifecycle_stage TEXT NOT NULL DEFAULT 'observe'
    CHECK(lifecycle_stage IN ('observe','reconcile','done'));
ALTER TABLE source_catalog_scan ADD COLUMN lifecycle_cursor INTEGER NOT NULL DEFAULT 0;

-- Candidates carry source evidence only. They do not authorize a shared work
-- merge or move any user's private state.
CREATE TABLE source_catalog_relocation_candidate (
    catalog_entry_id INTEGER NOT NULL REFERENCES source_catalog_entry(id) ON DELETE CASCADE,
    candidate_entry_id INTEGER NOT NULL REFERENCES source_catalog_entry(id) ON DELETE CASCADE,
    reason TEXT NOT NULL CHECK(reason IN ('insufficient_evidence','ambiguous_evidence','conflicting_identity')),
    generation INTEGER NOT NULL,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY(catalog_entry_id,candidate_entry_id),
    CHECK(catalog_entry_id<>candidate_entry_id)
);
CREATE INDEX source_catalog_relocation_target ON source_catalog_relocation_candidate(candidate_entry_id);
