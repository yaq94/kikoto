-- The attempt budget must survive a rescan without hiding a changed credit
-- file. Store the revision observed with the evidence so publication can tell
-- "same unreadable file" from "the release published a new readme".
ALTER TABLE source_catalog_entry ADD COLUMN sidecar_revision TEXT NOT NULL DEFAULT '';
