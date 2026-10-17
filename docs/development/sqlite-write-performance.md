# SQLite Write Performance

This page records how Kikoto bounds SQLite write-lock waiting, why the bounded
path exists, and how to measure it. The architecture is in
[Data model: SQLite write concurrency](../architecture/data-model.md#sqlite-write-concurrency).

## The Failure This Addresses

The catalog index on the 2-vCPU deployment spent 150 minutes on 2,520
directories and produced 253,031 log lines. The measured contention was:

| Signal | Observation |
| --- | --- |
| Throughput | 16.8 requests/min, far below the 150 ms throttle's ~400/min ceiling |
| `database is locked (5) (SQLITE_BUSY)` | 172 lines |
| `project queued metadata tags … context deadline exceeded` | 1,406 lines |
| Pool samples with `in_use=4 idle=0` | 840 lines, longest completed wait 64 s |
| HTTP 429 from the source | 4, one burst, then none |

The throttle was not the limit. Every transaction used `_txlock=immediate`, and
SQLite's busy handler waits synchronously inside the driver. A writer waiting
for the write lock therefore held its pooled connection for up to the full
30-second timeout. A four-connection pool fills with four such waiters, so the
reader that needs a connection, and the worker that would release the lock, both
wait behind them.

A direct measurement with the production DSN shows the driver ignores the
caller's deadline:

```
ctx-500ms:    elapsed=30.117s  err=database is locked (5) (SQLITE_BUSY)
ctx-40s:      elapsed=30.124s  err=database is locked (5) (SQLITE_BUSY)
after-release: elapsed=0s      err=<nil>
```

The context expired after 500 ms; the call returned after 30.1 s. Parked
writers also starved an unrelated read for 29.8 s on the same pool.

## The Bounded Path

`sqlutil.BeginBoundedTx` begins a write transaction in short attempts:

- one attempt shortens the connection's `busy_timeout` to 100 ms and tries
  `BEGIN IMMEDIATE`;
- a failed attempt restores the configured wait policy and returns the
  connection to the pool before retrying after 10 ms;
- once the lock is held, the configured `busy_timeout` is restored through the
  leased connection, so the transaction's own statements keep the pool policy;
- the wait stops at the caller's deadline, or at a five-second budget when the
  caller has none, and returns the last contention error;
- `BoundedTx.Commit` and `BoundedTx.Rollback` return the connection immediately,
  so a handler that reads the database after committing does not wait for its
  own pool slot.

Bounded writes are used by the catalog index writers, the recommendation
catalog and generation writers, the search and language-title projection
batches, the metadata tag projection queue, workflow progress and heartbeat
writes, and the HTTP write handlers that share `beginTxWithDatabaseBusyRetry`.
A contended background write fails inside its budget and leaves its durable
queue entry for the next pass. Read-only endpoints
(`GET /api/shared-associations`, `GET /api/shared-associations/{id}`,
`GET /api/work-matching`) use a read-only transaction, which bypasses
`_txlock=immediate` and takes no write lock.

## Measuring It

The experiment is opt-in and never asserts latency; it prints a JSON report.
Run both modes on an otherwise idle host, in this order, and keep the log:

```
cd backend
KIKOTO_SOURCE_INDEX_WRITE_PERF=1 KIKOTO_SOURCE_INDEX_WRITE_PERF_MODE=raw \
  go test ./internal/httpapi -run TestSourceIndexWriteContention -count=1 -v
KIKOTO_SOURCE_INDEX_WRITE_PERF=1 KIKOTO_SOURCE_INDEX_WRITE_PERF_MODE=bounded \
  go test ./internal/httpapi -run TestSourceIndexWriteContention -count=1 -v
```

Environment:

| Variable | Default | Meaning |
| --- | --- | --- |
| `KIKOTO_SOURCE_INDEX_WRITE_PERF` | unset | `1` enables the experiment |
| `KIKOTO_SOURCE_INDEX_WRITE_PERF_MODE` | `bounded` | `raw` uses `database/sql` directly |
| `KIKOTO_SOURCE_INDEX_WRITE_PERF_HOLD_MS` | `3000` | how long one transaction holds the write lock |
| `KIKOTO_SOURCE_INDEX_WRITE_PERF_WRITERS` | `4` | competing writers, matching the pool size |

The experiment holds the write lock in one transaction, starts four writers that
drain a fixed queue, and measures a user-facing read while they wait. `raw`
reproduces the pre-fix behavior; `bounded` uses the production path.

## Result

Both modes complete all 80 writes. Only the wait shape differs.

| Signal | raw | bounded |
| --- | --- | --- |
| Reader wait while writers held the pool | 29,606 ms | 55 ms |
| Reader starved (waited at least the lock hold) | yes | no |
| Drain after the write lock was released | 30.1 s | 0.58 s |
| Busy errors observed by writers | 2 | 0 |

The reader wait falls from the driver's 30-second busy timeout to tens of
milliseconds, and the queue drains as soon as the lock is free instead of after
the parked waits expire.

The bounded path bounds waiting; it does not remove SQLite's single-writer
ceiling. On a 2-vCPU host shared with the production service, index throughput
still depends on transaction size and on single-core work. Schedule long index
runs outside peak browsing, and keep index batches small: `pagesPerBatch` and
the publish batch bound how long any one writer holds the lock.

## Raw Data

Local A/B output: `E:/项目/临时/kikoto-final-validation/sqlite-write-ab.log`.
Production before-state log: `/tmp/kikoto-live-ba9ef02/instance/kikoto.log`
(catalog index run `ba9ef02`). After-deployment measurements belong with the
release record.
