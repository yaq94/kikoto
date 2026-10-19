-- Release folders publish readmes and credit lists beside their audio. They
-- are matching evidence only: no product code is derived from them and no
-- work is registered from a sidecar.
ALTER TABLE source_catalog_entry ADD COLUMN sidecar_json TEXT NOT NULL DEFAULT '';
