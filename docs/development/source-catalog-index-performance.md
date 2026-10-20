# Source Catalog Index Performance

This page records why the recoverable full index publishes from a durable
queue, what was measured on a real indexed database, and which SQLite guidance
applies to this deployment. The worker contract is in
[OpenList: Recoverable full index](openlist.md#recoverable-full-index); the
general write-lock policy is in
[SQLite write performance](sqlite-write-performance.md).

## Publisher query shape

The publisher drives each batch from the current generation's completed
directories: a candidate needs a non-empty `catalog_root`, an audio root for the
same generation, and a pending observation. The directory primary key orders the
scan and the partial index over pending rows seeks the observations, so the cost
follows the rows a batch returns and no temporary sort is needed. The join is
written as a `CROSS JOIN` so the planner cannot start from the pending index:
with a large backlog left by an earlier generation that plan sorts every queued
row before `LIMIT` applies. A plan guard test asserts the directory search comes
first and no temporary B-tree is used.

Two earlier shapes are no longer used. Reading the pending queue first and
joining a materialized root set with `WITH roots AS MATERIALIZED` scanned the
whole root set per batch before `LIMIT` applied; filtering complete directories
with a correlated `EXISTS` from the pending side still walked queued rows a
purge had not cleared yet. The queue columns, indexes, root maintenance and
batch drain are specified in
[OpenList: Recoverable full index](openlist.md#recoverable-full-index).

## Measured result

The historical comparison on a 34 MB database with 3,917 directories and
47,670 observations measured the two earlier shapes:

| Case | Correlated `EXISTS` query | Queue query |
| --- | --- | --- |
| Five-row tail | 36.1–36.9 s | 3.2–4.0 ms |
| Empty queue | first 100 unpublished, 0.3 ms | 0.0 s |
| 3,000-directory synthetic tail | 36.5 s | about 5 ms |
| Plan | correlated `EXISTS` per complete directory | partial index seek, root join, ordered limit |

On a production-scale copy (14,459 current-generation directories, 14,459 audio
roots, 266,296 rows left pending by an earlier generation) the current
directory-first query returned a 100-row batch in 0.2 ms; the worker now asks
for 50 rows with the same plan. The
`WITH roots AS MATERIALIZED` query took 88.3 ms on the same copy, and a
pending-first variant with `EXISTS` took 0.3 ms. `EXPLAIN QUERY PLAN` for the
current query uses the directory primary key, seeks the pending partial index
and probes the audio-root covering index, without a temporary sort or a scan of
a materialized root set.

`TestSourceCatalogPublishTailStaysBounded` asserts the bounded tail;
`TestSourceCatalogPublishTailPerformance` and
`make source-index-tail-performance` print the current plan and the legacy
comparison.

## Choosing the rate

The request rate is configured per source and the saved value wins over the
built-in starting value. The starting value exists so an uncalibrated source
cannot be flooded; it is not a ceiling and not a capacity answer. Measure the
deployment host with `make source-index-performance`, then raise concurrency or
shorten the interval until the origin rate limit or the host write capacity is
the binding constraint. The option ranges and the measurement procedure are in
[OpenList: Recoverable full index](openlist.md#recoverable-full-index).

Bounding the rate matters for a different reason than throughput: an
unbounded writer starves other SQLite writers regardless of how fast the source
answers. The publisher therefore bounds each transaction and releases the write
lock between them, so a higher rate does not turn into lock contention.

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
