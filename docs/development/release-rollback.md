# Release Rollback

This page gives the operator procedure for rolling back a Kikoto deployment
whose schema reached `103`. It states which image can read which schema, how to
take and restore a database snapshot, and what to check afterwards. It records
no deployment result; run the commands on the target host and keep their output
with the release record.

## Compatibility Matrix

Numbered migrations cannot be reverted by the application. A rollback restores
a database snapshot and starts a binary whose packaged migration catalog ends
at that snapshot's schema. The image names are the ones used by the current
next-release deployment; prefer a digest when the deployment recorded one.

| Image | Highest packaged migration | `094` | `095` | `096` | `097` | `098` | `099` | `100` | `101` | `102` | `103` |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| current build | `103` | to `103` | to `103` | to `103` | to `103` | to `103` | to `103` | to `103` | to `103` | to `103` | Keeps `103` |
| 102 image | `102` | to `102` | to `102` | to `102` | to `102` | to `102` | to `102` | to `102` | to `102` | Keeps `102` | Refuses |
| 101 image | `101` | to `101` | to `101` | to `101` | to `101` | to `101` | to `101` | to `101` | Keeps `101` | Refuses | Refuses |
| 100 image | `100` | to `100` | to `100` | to `100` | to `100` | to `100` | to `100` | Keeps `100` | Refuses | Refuses | Refuses |
| 099 image | `099` | to `099` | to `099` | to `099` | to `099` | to `099` | Keeps `099` | Refuses | Refuses | Refuses | Refuses |
| 098 image | `098` | to `098` | to `098` | to `098` | to `098` | Keeps `098` | Refuses | Refuses | Refuses | Refuses | Refuses |
| 097 image | `097` | to `097` | to `097` | to `097` | Keeps `097` | Refuses | Refuses | Refuses | Refuses | Refuses | Refuses |
| 095 image | `095` | to `095` | Keeps `095` | Refuses | Refuses | Refuses | Refuses | Refuses | Refuses | Refuses | Refuses |
| 094 image | `094` | Keeps `094` | Refuses | Refuses | Refuses | Refuses | Refuses | Refuses | Refuses | Refuses | Refuses |
| 092 image | `092` | Refuses | Refuses | Refuses | Refuses | Refuses | Refuses | Refuses | Refuses | Refuses | Refuses |
| 084 image | `084` | Refuses | Refuses | Refuses | Refuses | Refuses | Refuses | Refuses | Refuses | Refuses | Refuses |
| 083 image | `083` | Refuses | Refuses | Refuses | Refuses | Refuses | Refuses | Refuses | Refuses | Refuses | Refuses |

Numbered migrations have no down path. A rollback restores a snapshot taken
before the upgrade and starts an image whose packaged catalog ends at that
snapshot's schema. Do not delete columns, tables or rows to imitate a
downgrade, and do not edit `schema_migration` or `schema_state`. The sole
exception is the packaged, fingerprint-guarded legacy 095 checksum preflight
described in [Migrations](migrations.md#legacy-095-checksum-preflight); it
backs up first and refuses any schema that does not exactly match the known
legacy fingerprint.

The refusal happens before any handler runs. The migration manager compares the
database's schema version with the highest packaged migration and stops with
`database schema version 78 is newer than this binary supports (77); use a
compatible Kikoto version`. Do not edit `schema_migration` or `schema_state` to
get past it; use only the documented legacy preflight when it applies.

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
and is bounded by the same translation cache budget as `content_translation`.
`096` adds pending episode staging and one publication-state row per source and
parent; a staged split remains outside public reads until it is switched to
`published`. `097` rebuilds the display cache with a cascading source
reference. `098` adds the persisted catalog schedule. `099` adds the
translation config key and prefill cursor columns. `100` adds
`work.parent_work_id` and its index. `101` adds the parent-backfill report and
an idempotent backfill. `102` rebuilds `translation_prefill_state` to allow the
`failed` status and indexes the prefill failure table by
`(config_key, source_hash)`. `103` replaces the two 099 short-query indexes
with `idx_content_translation_short_scan` on
`(language, entity_kind, field, id, origin, config_key, translated_text)`.

No migration deletes or rewrites card or personal rows. A `068`–`102`
snapshot restored into the current image upgrades along the numbered chain; a
`103` database needs a binary that packages `103`. A `102` binary refuses
`103`, a `101` binary refuses `102`, a `100` binary refuses `101`, an `095`
binary refuses `096`, an `094` binary refuses `095`, an `083` binary must be
paired with an `083` snapshot, and an `084` binary refuses `085` and later.

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
`kikoto-pre-migration-<UTC>-v084-to-v096.db`; the first version in the suffix
is the schema being left, so a `v084-to-v096` file is a `084` snapshot. Keep
the snapshot and the previous image reference (`KIKOTO_IMAGE` or the Compose
override) together. The `rollbacks/` directory is outside `config/`, so the
copy does not recurse into itself.

Keep a named snapshot for every schema the rollback matrix lists:
`rollbacks/schema-095`, `rollbacks/schema-096`, `rollbacks/schema-097`,
`rollbacks/schema-098`, `rollbacks/schema-099`, `rollbacks/schema-100`,
`rollbacks/schema-101`, `rollbacks/schema-102` and `rollbacks/schema-103`. The automatic
pre-migration backup is named after the schema it leaves, so copy it to the
matching `schema-NNN` directory before the next upgrade overwrites the
reference. A snapshot only helps if its image is still available; record the
image digest beside the directory.

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

## Restore A Schema 95 Through 103 Snapshot

Use this to return to the current build after a failed start or to recover a
damaged `095` through `103` database. Use the image that packages the snapshot's
schema; to return to schema `094`, `092` or `083`, restore that schema's
snapshot and start an image that packages it.

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

1. `docker compose ps` reports the expected image; a stack with a Compose healthcheck also reports `healthy`, while the production stack without one only needs `running`.
2. `GET /health` returns HTTP 200 with the restored version.
3. An authenticated `/api/works` read returns the works that existed before the
   upgrade, and a source and catalog entry remain readable.
4. An `081` instance whose index run was interrupted reports its saved progress
   and can be resumed. A `080` instance does not see `sidecar_revision`, so
   never give it an `081` database.
5. A `092` instance lists the expected `content_translation` row count and
   reads a translated title. A failed generation leaves no new row.
6. A `095` or `096` instance reports the scan progress endpoint below 20 ms with the
   catalog counters and `?exact=1` below 200 ms on the production copy.
7. A `101` instance reads `work.parent_work_id` and the
   `work_parent_backfill_report` table. A `102` instance also reads the rebuilt
   prefill state. A `103` instance also has `idx_content_translation_short_scan`
   and neither of the two 099 short-query indexes. In all cases the status query
   returns the snapshot's version and the parent report does not contain rows
   for works that already have a parent.

The repository packages [rollback-kikoto.sh](../../scripts/rollback-kikoto.sh)
for the restore procedure above. The manual commands remain the reference for a
host that cannot run the helper. Keep the snapshot directory, checksum and
previous image reference in the access-controlled deployment record.

Restoring only `kikoto.db` does not revert Fetch staging, backup, or trash
directories under `/data`. Review unmatched transaction directories as
described in [Database](../operations/database.md#backups) instead of deleting
them.

## Executable Entry Points

The release candidate uses three repository scripts. Each one fails closed:
missing prerequisites and unexpected responses produce a nonzero exit status
rather than a partial pass.

### `scripts/mixed-load.mjs`

Runs concurrent browse, target-language search, media Range, personal review
and translation-coverage requests against a running instance. It uses only the
Node standard library and prints one JSON summary with per-category counts,
p50/p95 latency, status counts, unexpected statuses, timeouts and validation
errors.

```sh
KIKOTO_LOAD_BASE_URL=https://kikoto.example \
KIKOTO_LOAD_COOKIE='session=...' \
node scripts/mixed-load.mjs
```

Required environment:

- `KIKOTO_LOAD_BASE_URL`: HTTP or HTTPS origin, without a path.
- `KIKOTO_LOAD_COOKIE`: `Cookie` header value for an authenticated session.

Optional environment:

- `KIKOTO_LOAD_SECONDS`: load duration in seconds, default `60`.
- `KIKOTO_LOAD_CONCURRENCY`: concurrent workers, default `8`, range `1` to
  `128`.
- `KIKOTO_LOAD_WORK_ID`: work id to exercise. Without it, the script selects
  one of the first library works that exposes a playable audio or video stream.

The session needs library read access, `favorites:write` for the personal
review, and `sources:write` for the translation coverage endpoint. The script
writes a rating and a generated note to the selected work, reads them back, and
restores the original values at exit. It needs a library with at least one
playable media item. It does not hard-code a host, port, work id or credential.

Exit codes: `0` all requests matched and the final state was restored; `1`
unexpected status, timeout, malformed response or failed restore; `2` missing or
invalid environment, including a base URL with a path; `130` interrupted by
`SIGINT` or `SIGTERM`.

### `scripts/production-smoke.mjs`

Runs the HTTP smoke flow used by `make production-smoke` against a locally
built production image:

```sh
node scripts/production-smoke.mjs kikoto:ci
node scripts/production-smoke.mjs kikoto:ci --browser
```

The image must exist locally. The script starts an isolated container on a
loopback port, creates deterministic media fixtures, completes first-run setup,
scans the local library and logs in. At the end of the authenticated flow it
checks translation coverage (`GET /api/settings/translation/prefill`), writes
the current translation settings back unchanged with
`PATCH /api/settings/translation`, lists catalogue sources
(`GET /api/library-sources`), searches the scanned work by code and with
`lang=zh-cn`, and round-trips a personal rating and note through
`PATCH /api/works/{id}/user-state` and `GET /api/works/{id}`. It restores the
previous review and logs out before the optional browser check.

Prerequisites: Docker, a locally built image, enough temporary disk for the
fixtures, and the image's bundled `ffmpeg`. Exit codes: `0` all assertions
passed; `1` a setup step, assertion, request or container operation failed.

### `scripts/rollback-kikoto.sh`

Restores a normalized backup over a Compose deployment. Run it from the
deployment directory, or point `KIKOTO_DEPLOY_DIR` at that directory:

```sh
scripts/rollback-kikoto.sh \
  --backup-dir rollbacks/schema-101 \
  --image ghcr.io/example/kikoto@sha256:... \
  --expected-schema 101 \
  --timeout 600
```

Required arguments:

- `--backup-dir DIR`: directory containing `kikoto.db`, `.env`,
  `docker-compose.yml` and `SHA256SUMS`.
- `--image TAG`: image to start after the files are restored. It must already
  exist locally.
- `--expected-schema N`: expected `schema_state.current_version`.

Optional arguments:

- `--timeout SECONDS`: health-check timeout, default `600`.

The script reads `KIKOTO_DEPLOY_DIR` (default: current directory),
`KIKOTO_CONFIG_DIR` (default: `KIKOTO_DEPLOY_DIR/config`), `KIKOTO_DB_PATH`,
`KIKOTO_ENV_FILE` and `KIKOTO_COMPOSE_FILE`. The default health target is
`KIKOTO_HEALTH_SCHEME` (default `http`), `KIKOTO_HEALTH_HOST` (default
`127.0.0.1`) and `KIKOTO_HEALTH_PORT` (default `7655`); set
`KIKOTO_HEALTH_URL` to override the whole URL. Set `KIKOTO_COMPOSE_SERVICE` to
recreate one service instead of every service in the Compose file. It requires
`docker compose` (or `docker-compose`), `curl`, `sqlite3` and either
`sha256sum` or `shasum`.

The script verifies the manifest and that it covers all three files, stops the
Compose service, keeps a copy of the current files under
`rollback-previous-<UTC>-<pid>`, moves the old database aside, restores the backup,
starts the requested image, waits for `/health` to return HTTP 200, and checks
`schema_state`, `PRAGMA quick_check` and `PRAGMA foreign_key_check`. A failure
prints the completed steps and exits nonzero; the old database and safety copy
remain on disk for inspection.

Exit codes: `0` restore and verification passed; `1` any validation, Docker,
health or SQLite check failed; `2` missing, unknown or invalid arguments.
