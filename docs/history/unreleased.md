# Unreleased

- **Circles** and **Voice Actors** list each creator as a cover tile with an
  availability bar for local, remote-only, and missing works, and show the
  latest release date in place of the latest work code. Their views are
  one-tap chips beside the search field, which stays visible on a phone.
- Circle detail opens with a cover-tinted summary of its **Available**,
  **Local**, **Remote**, and **Unavailable** counts, and a circle that was
  never pulled says that only works the library already knows are listed.
- Circle and voice actor detail order their works by release date, code,
  title, rating, or sales, offer the **Full**, **Compact**, and **Mini** cards
  of the Library, and can show 96 works per page. The order and page size are
  remembered on the device. Voice actor works follow the chosen order instead
  of listing local works first.
- Library display options replace the phone-only **Compact cards** switch with
  a **Cards** choice of **Full**, **Compact**, and **Mini**, available on
  phones and wide screens and remembered separately for each. Compact on wide
  screens uses short tiles, and Mini shows only the cover and title so a large
  library can be scanned by its artwork. A device that had Compact cards on
  keeps compact phone cards.
- Local and Tracked can show 96 works per page.
- **Insights** is a sidebar page for the account's own listening. It opens
  with a calendar of the past year, keeps the 30-day, 12-month, and all-time
  listening report, and ranks tags, voices, and circles by preference (share
  of listening time over share of the Library) or by listening time. Listening
  history moved here from Settings; `/history` and `/settings?tab=history`
  redirect to it. Reports count listening per Library work, so listening
  stored against an edition appears under its work.
- Insights reports a calendar year as well as the named ranges, and a year
  opens with its highlights: the work, voice, and circle listened to most, the
  busiest day, the longest streak, and the works first heard that year.
- A taste profile compares the listening with the Library: the tags sought
  out most as a radar, where the listened works sit by release year, sales,
  and length, and how the account's ratings compare with the public ones.
- Listening is counted per hour of the day and shown as a chart in the
  listening rhythm. Hours start with the upgrade; earlier listening has none.
  Schema migration `068` adds the hour tables.
- A time zone decides where a listening day starts. Each account follows the
  zone of the device in use, or chooses one zone for every device in
  Settings → Account; Insights names the zone and links to the setting.
  Listening recorded before the upgrade, or before a time zone change,
  keeps the day it was counted on. Schema migration `067` adds the per-account
  setting.
- With **Hide from screenshots and recordings** on, a screenshot taken in the
  iOS app shows a blank page in place of Kikoto. The setting replaces **Hide
  while the screen is recorded or mirrored** and keeps the stored choice.
- The media controls choice under **Privacy on this device** is a segmented
  control that shows its three options at once.
- DLsite metadata synchronization accepts sparse numeric-keyed
  `language_editions` responses as well as arrays. Comma-separated language
  values are matched to their labeled edition so affected work families can
  synchronize normally.
- On wide screens the Library sort control is one button that names the
  current order and its direction. Its popover holds the orders and the
  direction, and the Recommended and Random orders keep a refresh button
  beside it.
- The Favorites resource filter offers All, Available, Local, Remote, and
  Missing in a narrower popover, in place of the cached and per-source
  choices. It follows the availability each card shows, so a work whose files
  belong to another language edition, or whose local folder has no indexed
  tracks, is filtered as Local instead of Missing. The grid or list view moved
  into display options.
