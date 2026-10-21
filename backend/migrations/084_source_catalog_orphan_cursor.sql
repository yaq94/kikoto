-- Orphan split retirement walks roots in entry id order. A completed run that
-- contains a failed or unconfirmed directory can skip a root without retiring
-- it; without a cursor the next batch selects that root again and the pass
-- never finishes. The cursor is reset when a new generation starts.
ALTER TABLE source_catalog_scan ADD COLUMN orphan_cleanup_cursor INTEGER NOT NULL DEFAULT 0;

-- A retired card keeps its row so old links and personal references resolve,
-- but it has no file source and no longer appears in any card list. The
-- matching queue must not spend its per-subject budget on those identities.
DROP VIEW work_matching_subject;
CREATE VIEW work_matching_subject AS
 SELECT 'work' AS kind,id,title,primary_code,id AS work_id FROM work
 UNION ALL SELECT 'catalog',id,title,primary_code,NULL FROM source_catalog_entry
   WHERE work_id IS NULL AND relocated_to_id IS NULL AND file_source_id IS NOT NULL
 UNION ALL SELECT 'series',id,title,'',NULL FROM catalog_series WHERE work_id IS NULL;
