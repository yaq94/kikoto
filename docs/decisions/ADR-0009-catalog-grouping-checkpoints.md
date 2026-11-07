# ADR-0009: Catalog Grouping Checkpoints

## Status

Accepted. The grouping and finite-input persistence model implements the
publication boundary in [ADR-0008](ADR-0008-catalog-publication-snapshots.md).

## Decision

Directory classification stores derived evidence by source, root and complete
observation revision, or by durable finite-listing id. A grouping is preparation
state, not a work identity, a published catalog or a user record. Its names,
folder evidence, aggregate buckets and targets use indexed keys. Name, bucket,
target and parent-retention phases commit their input cursor with each bounded
batch. A bucket containing many names is reduced in input batches; limiting only
the number of output groups does not bound a transaction.

Global sequence rules use fixed-size numeric accumulators. Per-file rules keep
the existing independent-work, episode, numbered-release and format-copy
semantics. Immediate-parent comparison uses the existing normalized-name rule;
ancestor comparison uses a canonical Unicode SimpleFold key equivalent to
`strings.EqualFold`. Matching records consume parent candidates one-to-one and
preserve unique bonus recordings. A publisher retrieves completed targets by
key and does not rebuild the root's complete name or group map for each batch.

Finite scans persist a directory queue and page checkpoints in the existing
listing store. Each provider page, discovered directories and next-page cursor
share one transaction. The remote page, request, directory and depth budgets
bound remote work; they do not impose one card's admission limit on a creator
directory containing independent legal cards. At most one provider page or one
bounded local batch resides in memory. Inputs of at most 500 rows retain the
bounded compatibility partitioner, including its episode compensation rules.
Larger inputs partition into durable target and file tables.

Target creation, previous-file removal, file application and target readiness
use separate bounded checkpoints. These writes enter the shared publication
operation; only its committed marker changes the readable source projection.
Cancellation, low disk space and a failed transaction preserve completed
checkpoints. A retry opens the same listing and publication operation. Source
configuration and permissions remain live.

Obsolete grouping evidence is deleted in batches. The current revision and
finite groupings whose listing still exists remain available to their owners.
Finite target and queue rows are drained before listing-header reclamation so
foreign-key cascades cannot hide an unbounded cleanup transaction. This derived
state never owns personal records or replaces stable catalog entry ids.

## Consequences

Classification uses bounded memory and indexed durable evidence in exchange
for preparation writes. Resource evidence reports all SQLite row writes,
including FTS shadow-table and trigger work where measured, separately
from the input batch size. These row counts do not measure index B-tree pages. Commit wall time includes WAL checkpoint work after
the SQLite writer lock may have been released; writer contention measurements
must distinguish those durations.

The publication reader audit, exclusion lifecycle and retirement of incoming
aliases remain separate implementation responsibilities under ADR-0008.
