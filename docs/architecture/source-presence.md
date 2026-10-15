# Source Presence

Source presence records source-level facts about a work.

## Presence Types

- `local`: a local folder scan found a durable local work folder.
- `source`: a configured non-local source reported or can provide the work.
- `tracked`: the user intentionally kept the work/source relationship.

## Boundaries

`work_source_presence` should not be used as a concrete file tree. Concrete
stream URLs, download URLs, cache files, and local paths belong in
`media_file_location`.

Remote source availability checks should create or update `source` presence.
User actions such as mark, track, sync, cache, or fetch may create tracked state
or concrete locations.

## Outages

If a source is offline during a batch check, Kikoto updates source health and
skips per-work checks for that source. A source outage should not mark every
candidate work as missing.

## Availability API

`GET /api/works/{code}/source-availability` reads recorded observations without
network probes. Every source has its own `checkedAt` UTC timestamp, or an empty
string if no valid observation time exists. The response-level timestamp is a
summary only; it cannot establish another source's freshness.

`POST` on the same route requires `remote:track`. Optional `sourceId` selects one
source; zero checks all supported sources. The returned list contains only the
checked sources. Each source failure produces its own sanitized `error` status,
keeps the work and personal records, and can be checked again. An explicit
`force: true` invalidates the metadata and directory preview caches of sources
that answered available, so the next directory read retrieves current files.
Failed sources retain their cached previews. These reads and probes use the
existing configured-origin transport, request limits and redirect policy.

The browser keeps loaded directories while checks run or fail, and merges
partial responses by source id. A source tab selects a directory only; it does
not replace the global player queue or seek. The normal file action starts at
zero, while Resume uses the persisted cursor's exact media item and location.
