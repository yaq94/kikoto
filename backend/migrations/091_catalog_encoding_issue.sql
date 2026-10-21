-- A directory name that arrived as U+FFFD or as invalid UTF-8 cannot be
-- requested: the source answered "object not found" for every such path and
-- left the whole run partial. The bytes were lost before this layer could read
-- them, so the scanner records the name instead of guessing a replacement,
-- skips that child, and keeps the rest of the listing complete.
--
-- The record lives in its own table rather than as a
-- source_catalog_directory.status value: that table's projection treats every
-- status other than complete/empty as unobserved, which would keep
-- catalogRootIncomplete true for the range and freeze lifecycle. An encoding
-- issue does not hide audio coverage; it hides one subtree whose size is
-- unknown, and the fix list is what an operator needs.
--
-- Numbered 091: 085-089 are already taken by the integration branch and 090 is
-- the maintenance lock. This migration has no dependency on them; it only
-- needs file_source, which every earlier baseline carries.
CREATE TABLE source_catalog_encoding_issue (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  file_source_id INTEGER NOT NULL REFERENCES file_source(id) ON DELETE CASCADE,
  parent_path TEXT NOT NULL,
  raw_name TEXT NOT NULL,          -- 原始名字，含 U+FFFD 或非法字节，不做任何清理
  candidate_code TEXT NOT NULL DEFAULT '',
  suggested_name TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'unresolved' CHECK(status IN ('unresolved','recovered','ignored')),
  resolved_path TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(file_source_id, parent_path, raw_name)
);

CREATE INDEX idx_source_catalog_encoding_issue_status
  ON source_catalog_encoding_issue(file_source_id, status);
