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
enforce its source visibility before loading files. Content translation can validate
`catalogEntryId` plus field/path against these reads without registering a work.
