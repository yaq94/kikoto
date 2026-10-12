# Unreleased

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
- DLsite metadata synchronization accepts sparse numeric-keyed
  `language_editions` responses as well as arrays. Comma-separated language
  values are matched to their labeled edition so affected work families can
  synchronize normally.
