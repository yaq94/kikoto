-- An episode keeps one explicit series parent. The evaluation level is not a
-- work column: kind is inferred from this relation, catalog series membership
-- and personal_transfer_ref.object_kind, so number parsing and user-visible
-- release codes never learn an internal kind.
ALTER TABLE work ADD COLUMN parent_work_id INTEGER REFERENCES work(id) ON DELETE RESTRICT;
CREATE INDEX idx_work_parent_work ON work(parent_work_id);

-- Backfill only unambiguous evidence: the series is registered, the member's
-- entry is registered as a different work, and that entry belongs to exactly
-- one series. Missing, multi-series or contradictory rows stay NULL instead of
-- guessing a parent.
UPDATE work
SET parent_work_id = (
    SELECT series.work_id
    FROM source_catalog_entry entry
    JOIN catalog_series_member member ON member.catalog_entry_id = entry.id
    JOIN catalog_series series ON series.id = member.series_id
    WHERE entry.work_id = work.id
      AND series.work_id IS NOT NULL
      AND series.work_id <> work.id
      AND NOT EXISTS (SELECT 1 FROM catalog_series own WHERE own.work_id = work.id)
      AND (SELECT COUNT(*) FROM catalog_series_member sibling WHERE sibling.catalog_entry_id = entry.id) = 1
      AND (SELECT COUNT(DISTINCT other_series.work_id)
           FROM source_catalog_entry other_entry
           JOIN catalog_series_member other_member ON other_member.catalog_entry_id = other_entry.id
           JOIN catalog_series other_series ON other_series.id = other_member.series_id
           WHERE other_entry.work_id = work.id
             AND other_series.work_id IS NOT NULL
             AND other_series.work_id <> work.id) = 1
    ORDER BY series.id
    LIMIT 1
)
WHERE EXISTS (
    SELECT 1
    FROM source_catalog_entry entry
    JOIN catalog_series_member member ON member.catalog_entry_id = entry.id
    JOIN catalog_series series ON series.id = member.series_id
    WHERE entry.work_id = work.id
      AND series.work_id IS NOT NULL
      AND series.work_id <> work.id
      AND NOT EXISTS (SELECT 1 FROM catalog_series own WHERE own.work_id = work.id)
      AND (SELECT COUNT(*) FROM catalog_series_member sibling WHERE sibling.catalog_entry_id = entry.id) = 1
      AND (SELECT COUNT(DISTINCT other_series.work_id)
           FROM source_catalog_entry other_entry
           JOIN catalog_series_member other_member ON other_member.catalog_entry_id = other_entry.id
           JOIN catalog_series other_series ON other_series.id = other_member.series_id
           WHERE other_entry.work_id = work.id
             AND other_series.work_id IS NOT NULL
             AND other_series.work_id <> work.id) = 1
);
