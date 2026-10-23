# Migrations

Database migrations live in `backend/migrations/`. The backend embeds the
numbered files, `baseline/`, and `compat/` into the executable, so a production
container needs no separate migrations mount. Runtime location, backups, and restore are
in [Database](../operations/database.md).

## Numbered Chain

The numbered files form one contiguous, three-digit chain:

```text
001_initial.sql
002_v0_1_1.sql
003_*.sql
...
```

Filenames match `NNN_<lowercase_words>.sql`. The highest numbered file is the
current schema version. The root `VERSION` file is the application release;
the two are independent, and a release that adds no numbered SQL keeps the
previous schema version.

- A numbered file is immutable once released. A schema change adds the next
  number after the highest file present. A collision between unreleased
  branches is resolved by renumbering the unreleased file, never by editing a
  released one.
- The catalog is rejected at startup when it has a gap, two files with the
  same version, an empty file, or a filename that does not match the pattern.
- A checksum is the SHA-256 of the file after CRLF and lone CR are normalized
  to LF, so a checkout's line endings never change it.

The `schema_migration` ledger holds one row per applied file: filename,
version, checksum, the application version that applied it, duration in
milliseconds, and commit time. On every start, before any new SQL runs, the
ledger must satisfy all of these:

- Every filename is known to the running catalog: a packaged numbered file, a
  baseline the mode reads, or a retained checksum-only baseline entry (see
  [Accepted And Refused Ledgers](#accepted-and-refused-ledgers)).
- Every recorded checksum matches the catalog's checksum for that filename.
- Each version appears once, and at most one row is a baseline.
- Rows are contiguous from the baseline row, or from `001` when the ledger has
  no baseline.

A ledger row without a version or checksum is filled in from the catalog and
its application version is recorded as `legacy`.

### Adding A Migration

1. Inspect `VERSION`, the highest numbered file, and the files in
   `backend/migrations/baseline/` and `backend/migrations/compat/`.
2. Add `NNN_<description>.sql` with the next number. The manager runs the file
   inside its own transaction, so the file contains no transaction control
   statements. Keep the SQL deterministic and make data backfills idempotent
   where a retry can reach them. A file that rebuilds a table other tables
   reference starts with the line `-- kikoto:foreign_keys=off`: the manager and
   the baseline generator then run it with foreign key enforcement off, as
   SQLite's table-rebuild procedure requires, so dropping the old table does
   not cascade into referencing rows. The foreign-key check still runs before
   the transaction commits, and enforcement is restored on the connection
   afterwards.
3. Keep the change aligned with the unified work model and the source and
   metadata boundaries, and update [Data model](../architecture/data-model.md)
   when schema meaning changes.
4. Regenerate the development baseline with `go generate ./migrations` (see
   [Baseline Generation](#baseline-generation)).
5. Add a storage regression test for user-visible behavior or a recovery
   invariant. `TestMigrateBaselineMatchesCompleteIncrementalChain` must pass:
   in production and in development mode, the selected baseline and the
   complete numbered chain produce the same schema and seed rows.

## Fresh Installs And Upgrades

The single-row `schema_state` table records the current schema version, the
baseline version and checksum (if the database started from one), a dirty
version, the application version of the last migration, and the last
application version that completed startup.

| Database state | Action |
| --- | --- |
| Empty SQLite database | Apply the highest-version baseline the mode reads (see [Selection And Retention](#selection-and-retention)), then any numbered migrations after that version. |
| Existing database with migration history | Validate the ledger and apply only the next numbered migrations. User data is never reconstructed from the baseline. |
| Application tables without migration history | Stop and require an operator decision; the manager never infers a version. |
| Dirty migration from an interrupted/failed start | Retry that exact version after the SQL or environment is repaired. |
| Future schema version | Stop and ask for a compatible/newer binary. |

Before the first numbered migration runs on an existing database, startup runs
an upgrade hook. It writes a pre-migration backup when the database has a
backup directory (see [Database](../operations/database.md)) and, for a
database below schema 035, preserves user-authored workflow definitions for
review (see [Preset Workflows](../architecture/workflows.md#preset-workflows)).
A hook failure stops startup before the schema changes. The hook does not run
for an empty database.

Each migration's SQL, a foreign-key check, the ledger row, and the
schema-state advance commit in one SQLite transaction. The state is marked
dirty before the transaction starts. A failed statement rolls back the schema
and ledger but leaves the dirty marker, so the failure is visible and the same
version is retried on the next start. Another
foreign-key check runs after a batch that applied anything.

`schema_state.last_successful_app_version` is written only after the rest of
startup and the bootstrap gates complete, which distinguishes a migration that
ran from an application version that started successfully.

Some migrations only queue existing rows for derived state such as search
documents, tag and title projections, or card summaries. The server drains
those queues in bounded background batches after startup;
[Data model](../architecture/data-model.md) describes each queue and how reads
behave while it drains.

## Baselines

A baseline is a fresh-install snapshot of the complete numbered chain, never an
upgrade path. Data transformations, backfills, and conflict resolution in
numbered migrations are not represented by a schema snapshot, so an existing
database always continues through the numbered files.

Two directories hold baselines:

| Directory | Filename | Contents | Read by |
| --- | --- | --- | --- |
| `baseline/` | `<schema>_v<major>.<minor>.<patch>.sql` | Released baselines | Every mode |
| `compat/` | `<schema>_dev.sql` | Development baselines of the unreleased chain | `KIKOTO_MODE=development` only |

A `.sql` file in `baseline/` with any other name, a baseline above the highest
numbered file, or two baselines with the same schema version rejects the
catalog. Development mode applies the same checks to `compat/`, where files
other than `.sql`, such as its README, are ignored.

### Development Baselines

A development baseline lets a development database start at the unreleased
chain head without publishing a released baseline for a schema no release has
shipped.

- Production and demo modes never read `compat/`. A database whose ledger
  records a `compat/` baseline stops at startup with an error that names the
  file and asks for `KIKOTO_MODE=development` or a new database.
- In development mode, every `compat/` baseline must be above the highest
  released baseline; otherwise the catalog is rejected until the stale file is
  removed.
- A development database whose `compat/` baseline is not packaged stops in
  every mode and is recreated. Release mode of the generator removes every
  development baseline, so development databases created from one are
  recreated after a release.

### Baseline Generation

```sh
cd backend
go generate ./migrations
go run ./cmd/schema-baseline -migrations ./migrations -version-file ../VERSION -release
```

The generator (`backend/cmd/schema-baseline`) applies the complete numbered
chain to an in-memory SQLite database with foreign keys enabled, verifies
foreign keys, and dumps the result. The file contains the tables, the rows the
migrations insert, and the indexes, views, and triggers, each group sorted by
name. It omits `schema_migration`, `schema_state`, FTS5 shadow tables, and
virtual-table rows, because `CREATE VIRTUAL TABLE` recreates them. Columns with
a `CURRENT_TIMESTAMP` default are left out of the seed rows, so timestamps are
evaluated on the target database rather than frozen to the generator's clock.
The generated file is reviewed and checksummed like any other packaged asset.

| Mode | Command | Result |
| --- | --- | --- |
| Development (default) | `go generate ./migrations` | Writes `compat/<schema>_dev.sql` for the highest numbered file, replacing a file of the same name. Older development baselines stay, because development databases created from them validate their ledger against them. Writes nothing when the highest numbered file already has a released baseline. |
| Release | `-release` | Reads the root `VERSION`, which must have the form `v<major>.<minor>.<patch>`. Writes `baseline/<schema>_<VERSION>.sql` only when the highest numbered file is above the highest released baseline, then deletes every `compat/*.sql`. |

A release whose numbered chain is unchanged therefore reuses the previous
released baseline, and no second released baseline exists for one schema
version.

### Selection And Retention

An empty database applies the highest-version baseline the mode reads: the
newest file in `baseline/` in production and demo modes, and the newest file
across `baseline/` and `compat/` in development mode. It records that file as
the first ledger row and in `schema_state`, and then applies any numbered files
after it. Every other packaged baseline exists only to validate the ledger of a
database that was created from it.

A released baseline's suffix is the release that generated it; later releases
with the same numbered chain reuse it. The packaged baselines and the releases
that shipped them are listed in
[Accepted And Refused Ledgers](#accepted-and-refused-ledgers) and
[Release To Schema Map](#release-to-schema-map).

A database created from a baseline records that file rather than the numbered
files it replaced. Removing a released baseline from the catalog makes those
databases refuse to start, so a released baseline is immutable and stays
packaged. A filename may leave the catalog only through a checksum-only ledger
entry for the same filename, version, and checksum.

## Accepted And Refused Ledgers

The first ledger row identifies how a database was created. Startup accepts
these first rows and continues through the numbered chain:

| First ledger row | Created by | Catalog entry |
| --- | --- | --- |
| `001_initial.sql` | The complete numbered chain, including every fresh install before v0.5.0 | Numbered file |
| `baseline/031_current.sql`, `baseline/032_current.sql` | Pre-release snapshots | Checksum-only |
| `baseline/032_v0.5.0.sql` | v0.5.0 through v0.5.5 | Packaged |
| `baseline/033_v0.5.5.sql` | Development builds between v0.5.5 and v0.6.0 | Packaged |
| `baseline/034_v0.5.5.sql` | The original v0.6.0 publication, before the baseline was renamed (see [v0.6.0](../history/v0.6.0.md)) | Checksum-only |
| `baseline/034_v0.6.0.sql` | v0.6.0 and v0.6.1 | Packaged |
| `baseline/044_v0.7.0.sql` | v0.7.0 | Packaged |
| `baseline/047_v0.7.1.sql` | v0.7.1 | Packaged |
| `baseline/059_v0.8.0.sql` | v0.8.0 | Packaged |
| `baseline/060_v0.8.0.sql`, `baseline/061_v0.8.0.sql`, `baseline/062_v0.8.0.sql` | Development builds after v0.8.0 | Checksum-only |
| `baseline/066_v0.8.1.sql` | v0.8.1 and v0.8.2 | Packaged |
| `baseline/068_v0.8.3.sql` | v0.8.3 | Packaged |
| `compat/<schema>_dev.sql` | Development builds of the unreleased chain | Development baseline; development mode only |

A checksum-only entry validates a recorded ledger row in every mode and is
never a fresh-install input. It applies only when no packaged file has the same
filename.

A `compat/<schema>_dev.sql` row stops startup in production and demo modes, and
in development mode when the file is not packaged (see
[Development Baselines](#development-baselines)).

Any other ledger filename stops startup with an unknown-migration-record error,
or with a newer-than-supported error when its version is above the current
schema. This refuses databases that only unreleased development builds could
create:

- baselines 035–044 with a `_v0.6.1` suffix;
- baselines 045 and 047 with a `_v0.7.0` suffix;
- baselines above 047 with a `_v0.7.1` suffix;
- the alternative metadata-branch records
  `051_metadata_tag_projection_queue.sql`, `052_language_scoped_titles.sql`,
  and `053_favorite_list_icon.sql`, including their `compat/metadata/` copies.

Such a database is recreated, or restored from a backup taken on a released
version.

## Release To Schema Map

Pre-migration backup filenames record schema versions (`v047-to-v059`, for
example). The first number identifies the release row below whose schema
matches the backup.

| Release | Highest numbered migration | Fresh-install baseline |
| --- | --- | --- |
| v0.1.0 | `001` | None; the numbered chain |
| v0.1.1 | `002` | None |
| v0.1.2, v0.1.3 | `004` | None |
| v0.2.0 | `006` | None |
| v0.2.1 | `007` | None |
| v0.2.2 | `010` | None |
| v0.3.0 | `022` | None |
| v0.3.1, v0.4.0 | `025` | None |
| v0.4.1 | `027` | None |
| v0.5.0 through v0.5.5 | `032` | `032_v0.5.0.sql` |
| v0.6.0, v0.6.1 | `034` | `034_v0.6.0.sql` |
| v0.7.0 | `044` | `044_v0.7.0.sql` |
| v0.7.1 | `047` | `047_v0.7.1.sql` |
| v0.8.0 | `059` | `059_v0.8.0.sql` |
| v0.8.1, v0.8.2 | `066` | `066_v0.8.1.sql` |
| v0.8.3 | `068` | `068_v0.8.3.sql` |
| development build (unreleased) | `095` | Development baseline only; `compat/095_dev.sql` |

Per-release upgrade steps are in the [release notes](../history/index.md).

## Kikoto-next Schema Additions

Migrations `069` through `095` extend the source catalog, shared associations,
personal transfer, and the recoverable index. Pending release notes and upgrade
steps are in [Unreleased](../history/unreleased.md).

| Migration | Change | Recovery and read behaviour |
| --- | --- | --- |
| `069_openlist_catalog.sql` | Catalog entries and files | Published rows survive a failed or repeated scan |
| `070_source_catalog_scan.sql` | Persistent scan and directory checkpoint | A run resumes from stored pages and cursors |
| `071_catalog_series.sql` | Series and members | Series membership does not change an entry's work |
| `072_source_catalog_lifecycle.sql` | Availability, relocation, retained entries | Missing evidence keeps the card and personal records |
| `073_shared_association.sql` | Shared identity, snapshot, decisions | A split restores its pre-merge groups |
| `074_work_matching.sql` | Matching evidence and durable cursor | One subject per transaction; no partial batch is lost |
| `075_personal_transfer.sql` | Portable references and progress | Export/import remain user scoped |
| `076_source_catalog_publish_queue.sql` | Publish queue and cleanup staging | A published run drains its queue before completing |
| `077_source_catalog_publish_cleanup.sql` | `source_catalog_scan.publish_cleanup_cursor` and `source_catalog_file_title` | The cursor retires only files the current generation republished; a later run continues from it |
| `078_source_catalog_loose_works.sql` | `source_catalog_entry.source_root` and `source_catalog_directory.work_split` | An empty `source_root` keeps the old `root_path` meaning; a rescan re-evaluates the split decision |
| `079_source_catalog_sidecar.sql` | `source_catalog_entry.sidecar_json` | Empty means no read; matching falls back to person rows, and a readme never supplies a product code |
| `080_source_catalog_sidecar_queue.sql` | `sidecar_pending`, `sidecar_attempts`, a partial queue index, and `modified_at` on observed and published files | Reads run after publication in a bounded pass; a failed read keeps its card and is retried up to five times |
| `081_source_catalog_sidecar_revision.sql` | `source_catalog_entry.sidecar_revision` | A rescan compares the stored credit-file revision; an unchanged exhausted card stays settled, a replaced file resets its attempts |
| `082_source_catalog_sidecar_retry.sql` | `sidecar_transient_attempts`, `sidecar_retry_at`, `sidecar_failure`, and a due-row index | A temporary outage waits on a bounded backoff instead of spending the five permanent attempts; a definitive 404/410 still settles, and an administrator can reopen the queue |
| `083_source_catalog_cleanup_indexes.sql` | Partial indexes on the live `root_path` and on non-empty `source_root` | The cleanup pass seeks the entries below a card instead of scanning every live entry for each card |
| `084_source_catalog_orphan_cursor.sql` | `source_catalog_scan.orphan_cleanup_cursor`; the `work_matching_subject` view excludes retired cards | The orphan pass resumes after a skipped root; a partial run still retires stale split cards of fully observed roots, and retired cards are not queued for matching |
| `085_source_catalog_progress_counters.sql` | `source_catalog_scan` progress counters and the publish cursor pair | The migration seeds the counters from the current generation; `?exact=1` rebuilds them from the directory and observation tables |
| `086_catalog_search_and_track_index.sql` | `source_catalog_search_doc`, its FTS5 trigram index and triggers, and the generated `source_catalog_file.parent_path` column | The search index is rebuilt from the document table; the track index is derived from `relative_path` |
| `087_source_catalog_exclusion.sql` | Source directory exclusions; `source_catalog_entry` admits `excluded` | Every entry column is copied; indexes, triggers and `work_matching_subject` are recreated. Excluded cards and personal records remain |
| `088_source_catalog_incremental_sync.sql` | Directory sync columns, `source_catalog_sync_run`, and a partial unique index for one running sync per source | A run row records its own progress; a busy source keeps its previous successful scan |
| `089_work_primary_code_trimmed_unique.sql` | Unique index over non-empty trimmed `work.primary_code` | Duplicate trimmed codes abort with an actionable message before the index is created; no row is rewritten |
| `090_catalog_phase_timing.sql` | `source_catalog_phase_timing` rows per workflow run | Timing rows are diagnostics; a missing row does not change catalog state |
| `091_catalog_encoding_issue.sql` | `source_catalog_encoding_issue` fix list | Damaged names stay recorded; the scanner never requests a guessed path |
| `092_content_translation_store.sql` | `content_translation`, its FTS5 trigram index, and `translation_prefill_state` | Stored translations and the prefill cursor survive restart; a failed generation writes no row |
| `093_source_catalog_partition_index.sql` | `source_catalog_directory_partition` on `(file_source_id, generation, partition_path)` | The progress endpoint groups and counts partitions from the index instead of building a temporary b-tree |
| `094_source_catalog_file_root.sql` | `source_catalog_file.source_root` | A folded format copy keeps the directory its files live in; an empty value keeps the card-wide base |
| `095_source_field_translation_cache.sql` | `source_field_translation` for remote titles, tags and directory display names | Model output is rebuildable display cache outside search; every write participates in the translation byte budget |

The Release To Schema Map above lists every numbered file. `069` rebuilds `work`
and `logical_work` to relax code uniqueness and creates the catalog entry and
file tables. `070` creates the scan, directory, and observation tables. `071`
adds series, members, and previews. `072` adds availability, generation,
fingerprint, and relocation columns plus relocation candidates. `073` adds
shared associations, decisions, identity members, and revisions. `074` adds
matching results, operations, control, a subject view, title indexes, and
`work_matching_result.revision`. `075` adds portable references and progress.
`076` adds the publish queue, its partial index, the directory publish index,
and `source_catalog_audio_root`.

`079` adds one text column with an empty default; it stores only explicitly
labelled credit evidence from a release readme, and an older binary that never
reads it still reads every other column. `080` adds the queue columns, their
partial index, and the source modification timestamp. `081` adds the stored
credit-file revision used to reopen an exhausted card when the file changes.
`082` separates retry bookkeeping from the permanent budget: a transient
failure records its category, a retry timestamp and a transient count, while a
missing or oversized file still spends one of the five permanent attempts. The
partial index keeps the worker from scanning settled rows. `083` adds the two
partial indexes the cleanup pass uses to find child entries by their real root:
one for plain cards and one for split cards. Each branch is a half-open path
range, so the pass reads the subtree instead of the whole entry table.
Every added column has a default, so an existing row stays readable; the queue
starts empty and a later scan enqueues cards that publish credit files.

`084` adds the orphan cleanup cursor and narrows `work_matching_subject` to
unretired cards. `085` replaces the per-poll aggregate with the scan progress
counters and the publish cursor; the migration seeds them from the current
generation, and `?exact=1` keeps the aggregate query for reconciliation. `086`
adds the search document table, its FTS5 trigram index and triggers, plus the
generated `parent_path` column and index used for first-level track reads; the
index is rebuilt from the stored documents. `087` adds source-level directory
exclusions and rebuilds `source_catalog_entry` to admit the `excluded`
availability value, recreating its indexes, triggers and the matching view.
`088` adds the directory sync columns, the sync run table and the partial
unique index that keeps one running sync per source. `089` adds a unique index
over non-empty trimmed work codes and aborts with an actionable message when an
existing database still contains duplicates. `090` records per-phase catalog
timings. `091` keeps damaged source names in a separate fix-list table. `092`
stores the durable translation corpus, its FTS5 index and the prefill cursor.
`093` adds one covering index; `094` adds one text column with an empty default;
`095` adds an independent display-cache table and two indexes. None changes an
existing catalog or personal row, and an older binary that never reads them
still runs every other query.

The `084` through `095` changes retain existing rows. `087` is the only table
rebuild; it copies every entry column and then recreates the indexes, triggers
and view. `085`, `088`, `090`, `091` and `092` add tables or columns with
defaults, `093` adds an index, `094` adds one column default and `095` adds a
rebuildable cache table, so an older binary that never reads them still reads
every other table. Startup refuses a database newer than the binary before running a
handler. Rolling back any of these schemas requires a database snapshot, not a
reverse migration; see [Release rollback](release-rollback.md).

`077` does not change entry identity. It lets a generation remove stale file
rows left by an older generation without touching an entry that the current
run did not publish. `078` adds identity and layout columns with defaults, so
upgrading an existing database keeps every row readable; the split decision is
derived again when a directory listing completes.

### Rollback Compatibility For 077 And 078

Numbered migrations cannot be reverted by the application. Rolling back means
stopping Kikoto, restoring a database snapshot taken at the older schema, and
starting a binary whose catalog ends at that schema.

| Database | Binary ending at `076` | Binary ending at `077` | Binary ending at `078` |
| --- | --- | --- | --- |
| Schema `076` | Reads and migrates to `076` | Reads and migrates to `077` | Reads and migrates to `078` |
| Schema `077` | Refuses: schema is newer than the binary supports | Reads and migrates to `077` | Reads and migrates to `078` |
| Schema `078` | Refuses | Refuses | Reads and keeps `078` |

`077` adds one column with a zero default and one covering index. Its data is
derived cleanup progress, so an older binary that never reads the column can
still read every other table; the application still refuses the newer schema
before running a handler. `078` adds two empty-string defaults. A `077` binary
that reads a `078` database would ignore `source_root` and `work_split`, but
startup refuses the schema version first, so do not start it against that
database. A schema `078` snapshot restored for a `077` binary is therefore
valid only after restoring the database to `077`; the columns cannot be dropped
by a supported command.

The full backup, restore, and verification procedure is in
[Release rollback](release-rollback.md).
