# catalog-bench

Read-only replay of the source-catalog hot paths. The database named by `--db`
is opened with `mode=ro&query_only(1)`. With `--vacuum-to` the tool first makes
a consistent copy with `VACUUM INTO` and runs every benchmark against that
copy. Write benchmarks run inside `BEGIN IMMEDIATE ... ROLLBACK`, so the copy is
left unchanged as well.

## Build

The module lives in `backend/`. Named-file builds resolve imports from it:

```sh
cd backend
go build -o ../.kikoto-work/bench/catalog-bench ../scripts/catalog-bench/main.go
```

`go run ../scripts/catalog-bench/main.go -- ...` works the same way.

## Run

```sh
catalog-bench \
  --db /path/to/kikoto.db \
  --vacuum-to /tmp/kikoto-bench.db \
  --logs /tmp/docker-logs.txt \
  --label before \
  --runs 5 --write-runs 3 \
  --cleanup-cursor 0 --lifecycle-cursor 0 \
  --json-out /tmp/before.json
```

Useful flags:

| Flag | Meaning |
| --- | --- |
| `--db` | source SQLite path, always opened read-only |
| `--vacuum-to` | optional consistent copy; refuses to overwrite without `--force` |
| `--write-db` | disposable copy for write benchmarks; must differ from `--db`; falls back to `--vacuum-to` |
| `--source-id`, `--generation` | override auto-detection |
| `--root` | root for `catalogRootIncomplete`, default `/source/example` |
| `--runs` | read benchmark repetitions |
| `--write-runs` | write-path repetitions, each rolled back |
| `--cleanup-cursor` | starting entry id for the 100-card cleanup replay |
| `--lifecycle-cursor` | starting entry id for the 100-entry observe replay |
| `--only` | comma-separated query names |
| `--logs` | `docker logs` text file |
| `--json-out` | write the report to a file; stdout still receives it |

Query names: `progress_exact`, `purge_empty_pending`, `root_incomplete`,
`publish_1000_rows`, `publish_1000_rows_fixture`, `cleanup_100_cards`,
`lifecycle_100_entries`.

`publish_1000_rows_fixture`, `cleanup_100_cards` and `lifecycle_100_entries`
require `--vacuum-to` or `--write-db`; without a disposable copy the tool
refuses to run them so the source database stays read-only.

## What is measured

- `progress_exact`: the six statements of `sourceCatalogProgress` (scan row,
  staging counts, directory aggregates, observed/indexed join, partition page,
  detail page), timed as one unit. Per-statement times are in
  `details.breakdownMs`.
- `purge_empty_pending`: the `EXISTS` probe at the start of
  `purgeStaleCatalogPending`.
- `root_incomplete`: the exact two-arm query of `catalogRootIncomplete`.
- `publish_1000_rows`: the `catalogPublishQuery` SELECT with `LIMIT 1000`.
  This is the query stage only, not the per-row write loop.
- `publish_1000_rows_fixture`: marks up to 1,000 currently-unpublished
  observations in audio-bearing directories as pending inside a transaction,
  then times only the SELECT, then rolls back. Use this when the real backlog
  has zero publishable rows.
- `cleanup_100_cards`: the per-card body of `cleanupCatalogPublishBatch` for
  100 entries from `--cleanup-cursor`: effective root, range uncertainty, old
  file delete, directory-row check, child-path delete, cursor update. Rolled
  back.
- `lifecycle_100_entries`: the observe stage of `settleCatalogLifecycleBatch`
  for 100 entries from `--lifecycle-cursor`: range check, fingerprint, old file
  delete, three status updates. Rolled back.

Every query report includes `plan`, the `EXPLAIN QUERY PLAN` details with the
same bound parameters.

## Log parser

`--logs` counts lines containing `slow` and `SQLITE_BUSY`, groups slow lines by
`kind=`, and reports min/p50/max `elapsed_ms` per kind. It does not infer a time
window; record the command and the container start time separately.

## Caveats

- The product uses `modernc.org/sqlite`. C SQLite is faster on the same copy;
  the baseline includes a C cross-check only to validate that the copy is
  representative. Compare before/after with the same driver.
- `cleanup_100_cards` uses a fixed cursor (default 0) so runs are comparable.
  Production's cursor may be far ahead; the 3-hour logs are the production
  reference.
- The publish benchmark times the SELECT stage. The full publish transaction
  also performs entry/file/series writes and is not covered here.
