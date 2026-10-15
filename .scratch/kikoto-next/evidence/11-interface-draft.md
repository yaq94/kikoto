# Catalog series interface draft

This is the prepared T11 interface contract, not a working release. The branch
requires its coordinated schema 071 after schema 070 is integrated. Actual
evidence and remaining work are recorded in [preparation status](11-preparation.md).

A catalog series groups independently addressable episodes. It is separate from
the provider-owned `party_series` metadata snapshot. Its persistent `seriesId`
does not encode a source path or display title. A series may have its own optional
`work_id`; each member retains its `catalogEntryId`, optional exact `work_id`,
source and directory. Reading or grouping does not register a work, create media
identities, download files or change a personal evaluation.

## Discovery

Automatic grouping accepts an authored `Episode 1`, `Ep. 1` or `第1集` directory
marker, or a child of an explicitly labelled `Series: Example`, `Example Series`,
`系列：示例` or `示例系列` directory. Title prefixes and the observed parent locate
evidence within one source; they are not cross-source identity matches. Plain
numeric track names, shared creators, similar titles and topics are insufficient.
Format and track subdirectories remain under their episode. A conflicting episode
number is kept independent for confirmation. A series contains at most 100 members;
additional discoveries stay independent rather than being dropped.

The finite scan partitions a new series root only when every playable file falls
under an evidenced direct child. Already catalogued roots retain their identity.
Grouping never proves that two members share a recording or a playback timeline.

## Shared grouping

The catalog detail's **Group into a series** action is available with
`library:write`, the same permission as shared metadata edits. The editor searches
the current source, selects independent entries in the desired order and names the
series. **Preview grouping** lists the members and source names before **Confirm
grouping** creates the shared relation. All readers see the resulting series card
and can open each episode's existing directory and source controls.

The public contract is:

- `POST /api/catalog-series/previews` accepts `title` and 2–100 ordered
  `catalogEntryIds`. The body is limited to 16 KiB; a title is at most 512 UTF-8
  bytes. The response contains `previewId`, title, pending status and public
  episode information. Only the preview record is persisted at this stage.
- `GET /api/catalog-series/previews/{id}` reopens the actor's preview; the browser
  preserves its id in the catalog URL. Previews expire after 24 hours, and each
  actor can have at most 32 unexpired pending previews.
- `POST /api/catalog-series/previews/{id}/confirm` rechecks the same actor's
  permission and the member snapshot before committing the series and members
  atomically. A repeated confirmation returns the completed series id.
- `GET /api/catalog-series/{id}` returns `seriesId`, title, optional series
  `workId` and ordered episodes. It contains no personal ratings or notes.
- Catalog detail includes its optional `seriesId`. Source browsing returns one
  series card instead of each member; searches still match each episode title.

Changing a member's identity, observed title/location, source visibility, existing
membership or personal rating/note invalidates its preview. A missing, changed or
already grouped member yields a conflict without changing the group or any user
record. Existing groups and evaluation objects cannot be merged by this contract.
Private records only contribute to an internal digest; responses never disclose
another user's rating or note.

## Translation and playback

`seriesId` selects the public series title in the shared content translation
contract. Each episode title keeps its catalog translation resource. Opening a
series, an episode or a format folder leaves the persistent player alone. A track
click uses the episode's existing catalog playback identity and real relative
path; the series has no inferred combined queue or shared time position.

## Verification

HTTP tests use migrated SQLite, real sessions and a bounded external directory
fixture. They cover shared confirmation, private-state preservation, stale
previews, explicit evidence, formats, stable references and restart recovery.
Selected browser functional tests exercise the series card, preview controls,
episode navigation and audio state with screenshots, trace and video disabled.
Synthetic audio does not establish real-provider or format compatibility.
