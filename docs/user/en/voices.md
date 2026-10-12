# Voices
[English](../en/voices.md) · [简体中文](../zh-Hans/voices.md) · [繁體中文](../zh-Hant/voices.md) · [日本語](../ja/voices.md) · [한국어](../ko/voices.md)

Voice pages present persisted person and credit data.

## Current Behavior

- Lists voice actors from local provider credits and user-maintained person
  data as cover tiles with Local and Remote counts and an availability bar: a
  solid segment for works with a local copy, a lighter segment for works only
  a remote source holds, and an empty track for the rest.
- Uses server-paged, URL-backed list search and filters. Each tile shows the
  latest known credited-work cover with the favorite control on it, the name,
  aliases, the latest release date, and a dynamic user-tag row, with
  Library-style pagination. Tiles fill the row on wide layouts and form two
  columns on a phone.
- Keeps search, one-tap view chips, and the page-size action on one wrapping
  toolbar row. The chips are `All voices`, `Favorite`, `Tagged`,
  `Available`, `Local`, `Remote`, and `Missing`, and the pressed chip names
  the current view. On a phone the search field keeps its own row with page
  size beside it, and the chips scroll sideways below. User tags remain
  visible and searchable, but the list does not expose a tag filter.
- Shows favorite, rating, note, and user tag state.
- Shows confirmed aliases under the detail title. Alias review, duplicate
  merge, and merge undo live in the Metadata page's **Voice actors** view;
  the detail **More** menu links there for users with `metadata:sync`.
- Groups works with no provider voice credits under an `unknown` bucket.
- Shows Local and Remote availability badges followed by user tags on the
  cover-tinted voice detail summary. Cache remains available to playback and filtering data, but is not
  presented as Local.
- Presents known and remote works with the same responsive grid and shared work
  cards as the Library. Selecting a DLsite tag or personal tag on a work card
  opens a local Library search for it.
- Loads person detail, known works, and remote matches independently. Remote
  source searches use bounded concurrency, while voice counts and user tags are
  aggregated in batches.
- Keeps Favorite, the common refresh actions, and a **More** menu in the
  detail summary. **First pull** or **Refresh** queues a Follow a voice actor
  run with only known works' metadata across every compatible remote source, and
  **Retry metadata** appears only while known works still lack metadata. The
  button shows **Refreshing** until the run settles. A warning beside the sync
  state counts remote sources that failed their last pass and opens that run
  in Activity. **More** holds **Follow this voice actor…**, which opens the
  preset with this voice actor selected for source, filter, metadata, and tag
  choices, and **Manage aliases**. The works toolbar mirrors the Library: a bounded search on the
  left with columns, page size, availability filter, and selection as quiet
  icon actions on the right, and the shared collection pagination above and
  below the grid. Mobile work search keeps one row and opens filter, column,
  and selection controls from a separate options sheet.
- Treats the mobile detail back control as Up to the last
  server-and-user-scoped Voice Actors list location, including when the detail
  was opened from Library or Favorites. Wide layouts keep the source-aware
  history return.

## Boundary

DLsite remains a metadata provider for known works. Kikoto does not treat DLsite
keyword crawling as a reliable voice actor catalog.

## Review Behavior

Alias and merge tools should preserve user state and keep merge undo visible
when duplicate person records are consolidated.

## Related Docs

- [Library](library.md)
- [Work detail](work-detail.md)
- [Data model](../../architecture/data-model.md)
