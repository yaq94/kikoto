# Release Rollback

This page gives the operator procedure for rolling back a Kikoto deployment
whose schema reached `095`. It states which image can read which schema, how to
take and restore a database snapshot, and what to check afterwards. It records
no deployment result; run the commands on the target host and keep their output
with the release record.

## Compatibility Matrix

Numbered migrations cannot be reverted by the application. A rollback restores
a database snapshot and starts a binary whose packaged migration catalog ends
at that snapshot's schema. The image names are the ones used by the current
next-release deployment; prefer a digest when the deployment recorded one.

| Image | Highest packaged migration | Schema `092` | Schema `094` | Schema `095` |
| --- | --- | --- | --- | --- |
| current build | `095` | Migrates to `095` | Migrates to `095` | Migrates to `095` |
| 094 image | `094` | Migrates to `094` | Keeps `094` | Refuses |
| 092 image | `092` | Keeps `092` | Refuses | Refuses |
| 084 image | `084` | Refuses | Refuses | Refuses |
| 083 image | `083` | Refuses | Refuses | Refuses |

Numbered migrations have no down path. A rollback restores a snapshot taken
before the upgrade and starts an image whose packaged catalog ends at that
snapshot's schema. Do not delete columns, tables or rows to imitate a
downgrade, and do not edit `schema_migration` or `schema_state`.

The refusal happens before any handler runs. The migration manager compares the
database's schema version with the highest packaged migration and stops with
`database schema version 78 is newer than this binary supports (77); use a
compatible Kikoto version`. Do not edit `schema_migration` or `schema_state` to
get past it.

`077` adds `source_catalog_scan.publish_cleanup_cursor` and the
`source_catalog_file_title` index. `078` adds
`source_catalog_entry.source_root` and
`source_catalog_directory.work_split`, both `NOT NULL DEFAULT ''`. `079` adds
`source_catalog_entry.sidecar_json TEXT NOT NULL DEFAULT ''`. `080` adds
`sidecar_pending`, `sidecar_attempts`, their partial index, and `modified_at`
on observed and published files. `081` adds `sidecar_revision TEXT NOT NULL
DEFAULT ''`, the credit-file revision used to reopen an exhausted queue card
when the published file changes. `082` adds `sidecar_transient_attempts`,
`sidecar_retry_at` and `sidecar_failure`, all with empty defaults, plus a
partial index over the due queue. `083` adds two partial indexes over
`source_catalog_entry`: one for the live `root_path` and one for a non-empty
`source_root`. `084` adds `source_catalog_scan.orphan_cleanup_cursor`, an
`INTEGER NOT NULL DEFAULT 0` cursor for the resumable orphan-card pass, and
replaces the `work_matching_subject` view so a retired card (one without a file
source) is not queued for matching. `085` adds the scan progress counters and
the publish cursor pair, seeded from the current generation. `086` adds the
search document table, its FTS5 trigram index and triggers, and the generated
`source_catalog_file.parent_path` column with its index. `087` adds source
directory exclusions and rebuilds `source_catalog_entry` to admit `excluded`,
copying every entry column and recreating its indexes, triggers and view. `088`
adds the directory sync columns, the sync run table and a partial unique index
for one running sync per source. `089` adds a unique index over non-empty
trimmed work codes; an existing duplicate aborts the upgrade before the index
is created. `090` records per-phase catalog timings. `091` keeps damaged source
names in a separate fix-list table. `092` stores the translation corpus, its
FTS5 index and the prefill cursor. `093` adds one covering index on the catalog
directory table. `094` adds one text column,
`source_catalog_file.source_root`, with an empty default so an existing row
keeps the card-wide base. `095` adds `source_field_translation`, a rebuildable
display cache for remote titles, tags and directory names; it has no FTS index
and participates in the same soft translation budget as the model rows of
`content_translation`.

No migration deletes or rewrites card or personal rows. A `068`–`094`
snapshot restored into the current image upgrades along the numbered chain; a
`095` database needs a binary that packages `095`. An `094` binary refuses
`095`, an `083` binary must be paired with an `083` snapshot, and an `084`
binary refuses `085` and later.

## Back Up Before A Deployment

Stop the service so the copy and its WAL files describe one moment:

```sh
docker compose stop
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
snapshot="rollbacks/release-$stamp"
mkdir -p "$snapshot"
chmod 700 "$snapshot"
cp -a config "$snapshot/config"
sha256sum "$snapshot/config/kikoto.db"
```

The paths above assume the default `./config:/config` bind of the production
Compose file. For the development stack, use `KIKOTO_DEV_CONFIG_DIR` (default
`./config`); for the Demo stack, use `KIKOTO_DEMO_CONFIG_DIR` (default
`./demo/config`) and replace `config/` in the commands accordingly.

The application also writes a `pre-migration` backup to
`KIKOTO_DB_BACKUP_DIR` (default `config/backups`) before the first numbered
migration of an existing database. Its name has the form
`kikoto-pre-migration-<UTC>-v084-to-v095.db`; the first version in the suffix
is the schema being left, so a `v084-to-v095` file is a `084` snapshot. Keep
the snapshot and the previous image reference (`KIKOTO_IMAGE` or the Compose
override) together. The `rollbacks/` directory is outside `config/`, so the
copy does not recurse into itself.

## Restore A Schema 79 Snapshot

Use this to return to the `079` image.

```sh
docker compose down
mv config/kikoto.db "config/kikoto.db.schema79.$(date -u +%Y%m%dT%H%M%SZ)"
rm -f config/kikoto.db-wal config/kikoto.db-shm
cp "rollbacks/<snapshot>/config/kikoto.db" config/kikoto.db
chmod 600 config/kikoto.db
KIKOTO_IMAGE=<079 image> docker compose up -d
```

`<snapshot>` is the `release-<UTC>` directory name created above; in both
restore commands it is the directory under `rollbacks/`.

`docker compose down` removes the container and keeps the bind mounts; do not
add `-v`. The `mv` keeps the `079` database for inspection. Remove the WAL and
SHM files before copying, because they belong to the database being replaced.
The bundled development and Demo Compose files define a container health check,
so those deployments can wait with `up -d --wait`. A host-networked production
deployment defines no Docker health check and has no `healthy` container state.
After `docker compose up -d`, poll `GET /health` as shown under
[Verify The Restored Instance](#verify-the-restored-instance). Treat HTTP 200
from that endpoint, not the container state, as the readiness gate.

## Restore A Schema 95 Snapshot

Use this to return to the current build after a failed start or to recover a
damaged `095` database. A `095` snapshot is readable by the current `095`
image only; to return to schema `094`, `092` or `083`, restore that schema's snapshot
and start an image that packages it.

```sh
docker compose down
mv config/kikoto.db "config/kikoto.db.broken.$(date -u +%Y%m%dT%H%M%SZ)"
rm -f config/kikoto.db-wal config/kikoto.db-shm
cp "rollbacks/<snapshot>/config/kikoto.db" config/kikoto.db
chmod 600 config/kikoto.db
KIKOTO_IMAGE=<current image> docker compose up -d
```

`docker compose up -d` returns when the container starts. The health poll below
is the gate; a missing 200 is a failed rollback. Read `docker compose ps` and
`docker compose logs --no-color kikoto` before retrying.

## Verify The Restored Instance

Run these from the deployment directory. They need a host `sqlite3` client;
the image itself installs only `ca-certificates` and `ffmpeg`, so do not assume
`docker compose exec kikoto sqlite3` works. `PRAGMA quick_check` and the reads
below are safe against a running instance.

```sh
sqlite3 config/kikoto.db "PRAGMA quick_check;"
sqlite3 config/kikoto.db "PRAGMA foreign_key_check;"
sqlite3 config/kikoto.db "SELECT current_version FROM schema_state WHERE id=1;"
sqlite3 config/kikoto.db "SELECT count(*) FROM source_catalog_entry;"
for attempt in $(seq 1 60); do
  curl -fsS http://127.0.0.1:7655/health && break
  sleep 2
done
curl -fsS http://127.0.0.1:7655/health
```

The expected output is `ok`, no foreign-key rows, the schema version that
matches the restored snapshot, and the catalog entry count recorded before the
upgrade. A database below `069` has no `source_catalog_entry` table, so that
last command reports `no such table` instead of a count. The health response is
`{"status":"ok","version":"..."}` with the restored image's version.

Then confirm:

1. `docker compose ps` reports the expected image and a running container. The
   production stack has no Docker health status; readiness is the successful
   HTTP probe below.
2. `GET /health` returns HTTP 200 with the restored version.
3. An authenticated `/api/works` read returns the works that existed before the
   upgrade, and a source and catalog entry remain readable.
4. An `081` instance whose index run was interrupted reports its saved progress
   and can be resumed. A `080` instance does not see `sidecar_revision`, so
   never give it an `081` database.
5. A `092` instance lists the expected `content_translation` row count and
   reads a translated title. A failed generation leaves no new row.
6. A `095` instance reports the scan progress endpoint below 20 ms with the
   catalog counters and `?exact=1` below 200 ms on the production copy.

The packaged `rollback-kikoto.sh` performs this restore with staged files,
checksum verification, schema/integrity checks and the health gate. Prefer it
over the manual procedure when the rollback directory was written by the
deployment helper.

Restoring only `kikoto.db` does not revert Fetch staging, backup, or trash
directories under `/data`. Review unmatched transaction directories as
described in [Database](../operations/database.md#backups) instead of deleting
them.
