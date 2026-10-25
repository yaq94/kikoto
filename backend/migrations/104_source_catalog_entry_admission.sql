-- Admission is decided from a card's current published file set. Reading that
-- set through three bounded counts and an audio probe costs five index lookups
-- per card, which dominates a 42k-card page. This table stores the same counts
-- once per card, tagged with the generation they describe, so a browse or
-- search page answers the question with one primary-key lookup.
--
-- `generation` is the entry's last_seen_generation at the time the counts were
-- taken. A reader accepts the row only while it matches the entry, so a card
-- whose files changed in a newer generation is judged from its live rows
-- instead of a stale count.
CREATE TABLE source_catalog_entry_admission (
  catalog_entry_id INTEGER PRIMARY KEY REFERENCES source_catalog_entry(id) ON DELETE CASCADE,
  generation INTEGER NOT NULL,
  files INTEGER NOT NULL CHECK(files >= 0),
  audio INTEGER NOT NULL CHECK(audio >= 0),
  non_audio INTEGER NOT NULL CHECK(non_audio >= 0),
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- The counts are capped one row past each threshold, so the stored value is
-- only ever compared against that bound and never reported as an exact size.
-- Backfill every card that already has a published generation; cards published
-- later are written by the publish transaction.
INSERT INTO source_catalog_entry_admission(catalog_entry_id,generation,files,audio,non_audio)
SELECT e.id,e.last_seen_generation,
  min((SELECT count(*) FROM source_catalog_file f
        WHERE f.catalog_entry_id=e.id AND f.kind<>'folder'
          AND f.observed_generation=e.last_seen_generation),5001),
  min((SELECT count(*) FROM source_catalog_file f
        WHERE f.catalog_entry_id=e.id AND f.kind='audio'
          AND f.observed_generation=e.last_seen_generation),5),
  min((SELECT count(*) FROM source_catalog_file f
        WHERE f.catalog_entry_id=e.id AND f.kind<>'audio' AND f.kind<>'folder'
          AND f.observed_generation=e.last_seen_generation),2001)
FROM source_catalog_entry e
WHERE e.file_source_id IS NOT NULL;

-- A file write makes the stored counts unverifiable, so the row is marked with
-- an impossible generation and the reader falls back to counting the live rows.
-- The publish batch refreshes the row once the card's file set is complete.
-- Invalidation is what keeps a missed refresh a performance question rather
-- than a correctness one.
CREATE TRIGGER source_catalog_entry_admission_file_insert
AFTER INSERT ON source_catalog_file
BEGIN
  UPDATE source_catalog_entry_admission SET generation=-1
   WHERE catalog_entry_id=NEW.catalog_entry_id;
END;

CREATE TRIGGER source_catalog_entry_admission_file_delete
AFTER DELETE ON source_catalog_file
BEGIN
  UPDATE source_catalog_entry_admission SET generation=-1
   WHERE catalog_entry_id=OLD.catalog_entry_id;
END;

CREATE TRIGGER source_catalog_entry_admission_file_update
AFTER UPDATE OF catalog_entry_id,kind,observed_generation ON source_catalog_file
BEGIN
  UPDATE source_catalog_entry_admission SET generation=-1
   WHERE catalog_entry_id=OLD.catalog_entry_id;
  UPDATE source_catalog_entry_admission SET generation=-1
   WHERE catalog_entry_id=NEW.catalog_entry_id;
END;
