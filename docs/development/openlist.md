# OpenList catalog boundary

OpenList sources use `sourceType: "openlist"` and an administrator-configured
HTTP(S) API endpoint. `config.openListRoots` contains at most 8 explicit audio
work directories. A root owns its audio tracks and format subdirectories; it
does not infer series or episodes. An explicit bounded scan visits at most 32
directories, 8 levels, 2,000 entries, and 100 pages per root. Only a completely
read root containing audio becomes a catalog entry. Archives and subtitles
alone are excluded. Password-protected directories are never unlocked.

In Settings, add a remote source, select OpenList, enter its site base URL and
one absolute work-directory path per line, then save. Open that source in
Library and choose **Scan configured directories**. Search uses the stored
titles and product codes. Open a card to browse format folders and play a track;
saving its personal review registers only the necessary work information.
Directories must be chosen explicitly; the site root is not a valid work root.

`POST /api/remote-sources/{id}/catalog/scan` requires `sources:write` and scans
only the configured roots. It returns per-root outcomes; partial, inaccessible,
cancelled, and over-limit observations are not complete coverage. This finite
entry point does not perform a whole-site crawl or schedule daily updates.

## Recoverable full index

`POST /api/remote-sources/{id}/catalog/index` requires `sources:write` and
queues a `source_catalog_index` workflow. It traverses anonymous directories
from `/`, regardless of the finite work-root selection. A repeated start joins
the active run. Starting a cancelled, failed, paused or partial scan resumes
its saved directory/page cursors; starting a complete scan begins a new
observation generation. Completed directories survive retries. Changing page
size restarts only incomplete page sequences.

`GET /api/remote-sources/{id}/catalog/index?page=1` requires the same permission.
It returns run id, timestamps, attempted pages, observed entries, indexed entries,
completed/pending/failed/restricted/unconfirmed-empty directory counts and
paginated partition and unresolved-directory details (50 per list). It never
reports a whole-source percentage with an unknown denominator. An empty answer
remains unconfirmed; permission failures and unfinished ranges prevent complete
coverage. The existing workflow cancel and retry endpoints stop or resume the
same scan without discarding its published catalog or private reviews.

Scan options are saved with the source's checkpoint and reused by an empty
start request. An active run keeps its options until it is stopped and resumed.

| Option | Default | Accepted range |
| --- | --- | --- |
| `concurrency` | 1 | 1–4 metadata requests within the single workflow executor |
| `intervalMs` | 1000 | 100–60000 between metadata starts, shared per origin |
| `maxResponseBytes` | 2097152 | 1024–8388608 |
| `pageSize` | 100 | 1–500 |
| `pagesPerBatch` | 8 | 1–32 page or publication steps |
| `maxPagesPerDirectory` | 10000 | 1–100000; reaching the bound leaves an explicit retryable range |
| `minFreeBytes` | 2147483648 | 0–1099511627776 |

The worker releases its executor after each batch. Each page commits validated
names and its next cursor together. Child discovery and catalog publication
write at most 100 objects per transaction. Low or unknown free space on the
database volume pauses new observations and publication while stored cards
remain readable. The disk check reserves additional response/WAL headroom;
the database volume must also be monitored by the operator.

Numbered directories own their descendants. A directory with audio, or with
only recognized format subdirectories, owns its track/format tree; other
directories remain traversal containers. Series and episode relationships are
separate from this admission rule. No title matching merges catalog identities.
Only roots with observed playable audio become searchable cards. Source-returned
URLs are discarded; neither discovery nor publication registers works or
downloads media. Missing observations do not delete cards or private records.

Directory pages with changing totals restart their bounded sequence up to two
times, then remain failed. Repeated pages cannot count as progress. A manual
retry resets failed/restricted/empty observations while retaining completed
ones. The `source_catalog_scan`, `source_catalog_directory` and
`source_catalog_observation` tables retain the current source generation and
stable staging keys rather than copying the whole tree on each batch.

Full indexing uses the existing outbound source policy and a dedicated paced
metadata lane. Metadata redirects are refused, response size and 30-second
request deadlines apply, and cancellation releases every in-flight request.
Routine tests use synthetic complete trees; a real index run must separately
record its actual ranges, timestamps and unresolved directories.

`GET /api/remote-sources/{id}/works` uses the existing paged source card and
search response. OpenList cards add `catalogEntryId` and may have an empty
`primaryCode`; an absent `workId` means the entry has not been registered.
`GET /api/source-catalog/{id}` reads the current entry and only the viewer's
personal review. `GET .../tracks` reads its persisted directory snapshot.
`GET .../media?path=<relative path>` selects a file from that snapshot, resolves
its current URL from OpenList, and streams it without creating a media cache.

The persistent catalog id is independent of display text, source paths and
signed links. `source_catalog_entry` keeps source id, observed root path, title,
optional product code, and optional `work_id`; `source_catalog_file` keeps the
entry's real relative file paths and media kinds. Paths locate observations;
renaming a root does not transfer an old identity or review by title matching.
Temporary URLs are never persisted as identities.

`PATCH /api/source-catalog/{id}/user-state` uses the existing personal review
payload and response. It requires `favorites:write` and, only if the work is
absent, `remote:track`. Registration and the personal update commit together,
return `workId`, and register necessary information only. Repeated or concurrent
registration of the same entry uses that same work. Numbered entries resolve
existing canonical and alias relationships. Empty codes never enter numbered
resolvers. Registered objects use `/api/works/{workId}` and its existing personal
state API; numeric edition semantics remain unchanged.

`work.primary_code` and `logical_work.canonical_code` use an empty string for
code absence, with uniqueness applying only to nonempty values.
`work_edition.primary_code` can likewise be empty. Work ids, existing foreign
keys and personal state are retained through upgrades. This representation is
not a generated product code.

Metadata reads use the shared source transport with a serialized lane, a default
one-second minimum request interval, a 30-second request deadline, a 2 MiB body
limit and cancellable queue admission. The explicit API origin is the metadata
destination; media uses the configured source origin/host policy. Only configured
origins have the private-address exception. Media URLs and redirects are checked
by the shared policy; errors expose fixed messages. Range requests are passed
through, streamed bytes and request lifetime are bounded, and every new playback
request resolves a fresh URL. A failed expired URL is refreshed at most once.

The reusable server read boundary is `loadSourceCatalogEntry(ctx, entryID)`;
it checks the enabled source type before returning stored original text and
source identity. `loadSourceCatalogFiles(ctx, entryID)` returns immutable real
relative paths separately from display names. Callers first load the entry to
enforce its source visibility before loading files. Content translation validates
`catalogEntryId` plus field/path against these reads without registering a work.
Cards and catalog details use the shared translation resource scope; directory
labels retain their real paths and use the same original/retry controls as titles.
See [public content translation](translation.md#public-content).

For local functional checks, `go run ./tests/openlist-fixture` accepts
`OPENLIST_HTTP_ADDR` (default `127.0.0.1:17617`) and `OPENLIST_FRONTEND_ORIGIN`
(default `http://127.0.0.1:3117`). Point Vite at that API, set
`PLAYWRIGHT_BASE_URL` and `OPENLIST_API_URL`, then select `openlist-catalog.spec.ts`
through `make frontend-e2e`. The fixture replaces only the external source/model;
the two tests use real API sessions, catalog reads, translations and reviews.
`POST /__fixture/stop` shuts down the fixture and its disposable database.
