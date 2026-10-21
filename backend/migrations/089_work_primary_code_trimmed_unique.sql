-- A work without a product code is a normal state (manual registration,
-- unnumbered releases), so empty codes must not be unique. A non-empty code is
-- a shared identity and must identify exactly one work. The earlier index used
-- primary_code <> ''; it already rejects raw duplicates, but a whitespace-only
-- value slipped through that predicate. This index treats a blank-after-trim
-- code as absent.
--
-- Fail before the index with an actionable message when a legacy catalog still
-- holds duplicates. A bare UNIQUE constraint error names the column but not the
-- migration or the required repair.
DROP TRIGGER IF EXISTS work_primary_code_089_preflight;
CREATE TEMP TRIGGER work_primary_code_089_preflight
BEFORE UPDATE ON work
WHEN EXISTS (
  SELECT 1 FROM work
  WHERE trim(primary_code) <> ''
  GROUP BY primary_code
  HAVING count(*) > 1
)
BEGIN
  SELECT RAISE(ABORT, '089: duplicate non-empty work.primary_code; merge or clear the duplicates before upgrading');
END;

UPDATE work SET id=id WHERE id = (SELECT min(id) FROM work);

DROP TRIGGER work_primary_code_089_preflight;

CREATE UNIQUE INDEX idx_work_primary_code_trimmed
  ON work(primary_code) WHERE trim(primary_code) <> '';
