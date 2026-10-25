-- The parent relation is derived from catalog membership. When the evidence is
-- ambiguous the relation stays NULL, so the decision must be queryable instead
-- of living only in a log line. One row per unresolved work.
CREATE TABLE work_parent_backfill_report (
  work_id INTEGER PRIMARY KEY REFERENCES work(id) ON DELETE CASCADE,
  reason TEXT NOT NULL CHECK(reason IN ('missing','conflict','multi_parent')),
  detail TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Re-run the unambiguous backfill. Migration 100 already did this once; doing it
-- again keeps the report and the column consistent for rows registered after
-- that migration, and the predicate is idempotent.
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
WHERE parent_work_id IS NULL
  AND EXISTS (
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

-- Rebuild the report from the current evidence so a repeated run reports the
-- same rows. A work that now has a parent is no longer unresolved.
DELETE FROM work_parent_backfill_report;

INSERT INTO work_parent_backfill_report(work_id, reason, detail)
SELECT
  evidence.work_id,
  CASE
    WHEN evidence.series_works > 1 THEN 'multi_parent'
    WHEN evidence.registered_series = 0 THEN 'missing'
    ELSE 'conflict'
  END,
  CASE
    WHEN evidence.series_works > 1 THEN 'member entries resolve to ' || evidence.series_works || ' series works'
    WHEN evidence.registered_series = 0 THEN 'no member series has a registered work'
    ELSE 'parent equals the work itself or the work already owns a series'
  END
FROM (
  SELECT
    w.id AS work_id,
    COUNT(DISTINCT s.work_id) AS series_works,
    COUNT(DISTINCT CASE WHEN s.work_id IS NOT NULL THEN s.id END) AS registered_series
  FROM work w
  JOIN source_catalog_entry e ON e.work_id = w.id
  JOIN catalog_series_member m ON m.catalog_entry_id = e.id
  JOIN catalog_series s ON s.id = m.series_id
  WHERE w.parent_work_id IS NULL
  GROUP BY w.id
  HAVING COUNT(DISTINCT s.id) > 0
) AS evidence;