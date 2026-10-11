# Unreleased

- Library display options replace the phone-only **Compact cards** switch with
  a **Cards** choice of **Full**, **Compact**, and **Mini**, available on
  phones and wide screens and remembered separately for each. Compact on wide
  screens uses short tiles, and Mini shows only the cover and title so a large
  library can be scanned by its artwork. A device that had Compact cards on
  keeps compact phone cards.
- Local and Tracked can show 96 works per page.
- DLsite metadata synchronization accepts sparse numeric-keyed
  `language_editions` responses as well as arrays. Comma-separated language
  values are matched to their labeled edition so affected work families can
  synchronize normally.
