# Source Catalog Index Performance

This page records why the recoverable full index publishes from a durable
queue, what was measured on a real indexed database, and which SQLite guidance
applies to this deployment. The worker contract is in
[OpenList: Recoverable full index](openlist.md#recoverable-full-index); the
general write-lock policy is in
[SQLite write performance](sqlite-write-performance.md).

## The tail defect

The original publisher joined `source_catalog_directory` and
`source_catalog_observation` and added a correlated `EXISTS` over the same two
tables for every candidate directory. `LIMIT 100` was applied only after that
filter. On the 2-vCPU deployment a full index reached 3,504 complete
directories and 47,670 observations; with five unpublished rows left, one
publisher call took **36.1–36.9 s**. Setting all 47,670 observations back to
unpublished took 0.3 ms because the first 100 rows then satisfied the limit.
That contrast rules out row count as the cause: the cost was the per-directory
audio-root check when few rows matched.

## The queue

`076_source_catalog_publish_queue.sql` adds one column, two indexes and one
table:

- `source_catalog_observation.publish_pending`, set by page persistence and
  cleared when a batch publishes the observation;
- a partial index
  `source_catalog_observation(file_source_id,directory_path,name) WHERE publish_pending=1`;
- `source_catalog_directory_publish_root`, a partial covering index over
  complete directory roots that supplies the revision and path without a table
  lookup;
- `source_catalog_audio_root(file_source_id,generation,catalog_root)`, one row
  per generation and root that has at least one complete directory with audio.

The publisher materializes the root set for the generation, seeks pending rows
through the partial index, and joins complete directories through
`source_catalog_directory_publish_root`. It orders by directory and name so a
batch stops after rows it can publish, not after rows it must reject.

`refreshCatalogAudioRootTx` runs after each directory status change. It adds a
root when a completed directory has audio and removes it when the last such
directory becomes incomplete, so a rescan that deletes tracks retires the root
as well.

The worker drains at most four batches of 100 rows per invocation, then returns
the job to the workflow queue. An exhausted queue returns without another
publication query, so the worker cannot spin on an empty tail.

## Measured result

| Case | Legacy query | Queue query |
| --- | --- | --- |
| Five-row tail, real 34 MB database | 36.1–36.9 s | 3.2–4.0 ms |
| Empty queue | first 100 unpublished, 0.3 ms | 0.0 s |
| 3,000-directory synthetic tail | 36.5 s | about 5 ms |
| Plan | correlated `EXISTS` per complete directory | partial index seek, root join, ordered limit |

The measured copy was a 34 MB database with 3,917 directories and 47,670
observations. The synthetic fixture is the regression in
`TestSourceCatalogPublishTailStaysBounded`; the opt-in comparison is
`TestSourceCatalogPublishTailPerformance` and
`make source-index-tail-performance`.

## Guidance that applies here

SQLite admits one writer at a time even in WAL mode; WAL only stops readers and
writers from blocking each other. This is why the publisher bounds each batch
and returns the rest to the queue rather than holding a transaction across the
whole backlog. See [Write-Ahead Logging](https://www.sqlite.org/wal.html) and
[BEGIN IMMEDIATE](https://www.sqlite.org/lang_transaction.html). The official
WAL page also notes that a long read transaction can block checkpointing.

A partial index is used only when the query predicate implies the index
condition. The publisher filters on `publish_pending=1`, which matches the
index predicate exactly. A predicate such as `published_generation<>?` would
not use an index declared `WHERE published_generation=0`. See
[Partial indexes](https://www.sqlite.org/partialindex.html).

The driver is `modernc.org/sqlite` v1.60.1. Its own documentation reports
CPU-bound queries at roughly 1.3–2.0 times the C build while I/O-bound work is
dominated by the operating system, and notes that four connections scaled at
least as well as the C build. That is a reason to fix query plans and
transaction boundaries first, not to switch to a CGO driver. See
[modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite).
`mattn/go-sqlite3` requires CGO and a C compiler
([README](https://github.com/mattn/go-sqlite3)), which would change the
release toolchain without evidence that the driver is the bottleneck.

Queued-write systems merge requests and batch commits, but their durability and
ordering contracts differ. rqlite merges queued writes in memory before a Raft
commit ([Queued Writes](https://rqlite.io/docs/api/queued-writes/)); River
documents SQLite support as experimental and recommends a single connection
for its queue
([River SQLite](https://riverqueue.com/docs/sqlite)). Kikoto keeps its queue in
SQLite so a restart resumes the same rows; it does not copy those parameters.

libSQL MVCC and `BEGIN CONCURRENT` address write concurrency inside one
embedded database, but do not remove the need to bound batch work, and the
project does not adopt an external database. See
[libSQL concurrency](https://docs.turso.tech/features/concurrent-writes).

## Reproducing

```
make source-index-tail-performance
KIKOTO_SOURCE_INDEX_TAIL_DB=/path/to/kikoto.db make source-index-tail-performance
KIKOTO_SOURCE_INDEX_TAIL_QUERY=legacy make source-index-tail-performance
```

The second form copies through `VACUUM INTO` and opens the source read-only.
The experiment prints the plan, elapsed time, CPU allocation and pool counters;
it asserts no budget. The regression test owns the bounded tail assertion.
