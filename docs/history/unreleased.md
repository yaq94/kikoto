# Unreleased

- **Insights** is a sidebar page for the account's own listening. It opens
  with a calendar of the past year, keeps the 30-day, 12-month, and all-time
  listening report, and ranks tags, voices, and circles by preference (share
  of listening time over share of the Library) or by listening time. Listening
  history moved here from Settings; `/history` and `/settings?tab=history`
  redirect to it. Reports count listening per Library work, so listening
  stored against an edition appears under its work.
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
