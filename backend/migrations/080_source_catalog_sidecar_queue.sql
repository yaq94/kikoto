-- Reading a release's credit file costs upstream requests, so publication
-- only queues the card. A bounded background pass reads the text and stores
-- the evidence; an unreadable file never blocks a playable card.
ALTER TABLE source_catalog_entry ADD COLUMN sidecar_pending INTEGER NOT NULL DEFAULT 0
    CHECK(sidecar_pending IN (0,1));
ALTER TABLE source_catalog_entry ADD COLUMN sidecar_attempts INTEGER NOT NULL DEFAULT 0;

CREATE INDEX source_catalog_entry_sidecar_pending
    ON source_catalog_entry(id)
    WHERE sidecar_pending=1;

-- OpenList publishes a modification timestamp for every object. It is the
-- cheap revision signal for a credit file: an edit can keep the same length.
ALTER TABLE source_catalog_observation ADD COLUMN modified_at TEXT NOT NULL DEFAULT '';
ALTER TABLE source_catalog_file ADD COLUMN modified_at TEXT NOT NULL DEFAULT '';
