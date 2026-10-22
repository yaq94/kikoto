-- A card normally owns one directory, so relative_path is relative to the
-- card's base (source_root or root_path). A format copy beside its release is
-- published into the release card while its files still live in the copy's
-- directory; one base per card cannot address both. Record the directory per
-- file where it differs from the card base.
ALTER TABLE source_catalog_file ADD COLUMN source_root TEXT NOT NULL DEFAULT '';
