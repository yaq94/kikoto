# Release Rollback

This page gives the operator procedure for rolling back a Kikoto deployment
whose schema reached `084`. It states which image can read which schema, how to
take and restore a database snapshot, and what to check afterwards. It records
no deployment result; run the commands on the target host and keep their output
with the release record.

## Compatibility Matrix

Numbered migrations cannot be reverted by the application. A rollback restores
a database snapshot and starts a binary whose packaged migration catalog ends
at that snapshot's schema. The image names are the ones used by the current
next-release deployment; prefer a digest when the deployment recorded one.

| Image | Highest packaged migration | Schema `081` database | Schema `082` database | Schema `083` database | Schema `084` database |
| --- | --- | --- | --- | --- | --- |
| current build | `084` | Reads and migrates to `084` | Reads and migrates to `084` | Reads and migrates to `084` | Reads and keeps `084` |
| 083 image | `083` | Reads and migrates to `083` | Reads and migrates to `083` | Reads and keeps `083` | Refuses |
| 081 image | `081` | Reads and migrates to `081` | Refuses | Refuses | Refuses |
| 080 image | `080` | Refuses | Refuses | Refuses | Refuses |
| 079 image | `079` | Refuses | Refuses | Refuses | Refuses |

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
source) is not queued for matching. No migration deletes or rewrites card or
personal rows. An `080`–`083` snapshot restored into an `084` image upgrades by
adding the empty column and recreating the view; an `084` database needs a
binary that packages `084`, and an `083` binary must be paired with an `083`
snapshot.

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
`kikoto-pre-migration-<UTC>-v083-to-v084.db`; the first version in the suffix
is the schema being left, so a `v083-to-v084` file is an `083` snapshot. Keep
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
The bundled `deploy/compose` files have a health check, so a deployment that
uses one can wait with `up -d --wait`. The production stack in this repository
does not define that check; poll `/health` as shown under
[Verify The Restored Instance](#verify-the-restored-instance).

## Restore A Schema 84 Snapshot

Use this to return to the current build after a failed start or to recover a
damaged `084` database. A `084` snapshot is readable by the current `084`
image only; to return to schema `083`, restore an `083` snapshot and start an
`083` image instead.

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

1. `docker compose ps` reports the expected image and `healthy`.
2. `GET /health` returns HTTP 200 with the restored version.
3. An authenticated `/api/works` read returns the works that existed before the
   upgrade, and a source and catalog entry remain readable.
4. An `081` instance whose index run was interrupted reports its saved progress
   and can be resumed. A `080` instance does not see `sidecar_revision`, so
   never give it an `081` database.

Restoring only `kikoto.db` does not revert Fetch staging, backup, or trash
directories under `/data`. Review unmatched transaction directories as
described in [Database](../operations/database.md#backups) instead of deleting
them.
