# ADR-0004: Work Identity and Personal State for Numberless Audio

## Status

Accepted.

## Context

Kikoto already keys a work by its normalized product code and shares personal
rating and notes across sources of the same work. The next source scope also
contains anonymously playable audio that has no product code. A product code
can therefore no longer be the only admission condition, while existing
numbered works, their references, and their personal state must keep working.

## Decision

Numberless audio receives a stable internal work identity derived from the
source catalog entry. A catalog entry can be searched, displayed, and opened
before it is registered as a library work. Saving a personal record registers
only the metadata needed for that identity; it does not create a download task
or write managed media.

A product code, when present, is never invented. Existing numbered works and
reliable work relations take precedence. A numberless entry is linked
automatically only when the evidence is high confidence and the candidate is
unique. Ambiguous or contradictory entries stay independent and surface a
confirmation entry point.

Automatic linking is still subject to personal-record conflicts. When the same
user has different ratings or notes on the two objects, both records are kept
and the merge pauses until that user decides. Different users' values are not
a conflict. Shared metadata editing permission never includes deciding another
user's personal record.

Numberless series display as one card that lists independently openable
episodes. Series and each episode keep separate personal records. Automatic
grouping requires an explicit series name, episode number, or series directory;
a shared creator, a similar title, or a related topic is not enough. Format
copies and track directories belong to the corresponding work or episode and
do not become new identities or cards.

Splitting a wrong association lets the record owner allocate merged ratings and
notes to one side or both sides. The system does not decide or discard that
record on the owner's behalf. Manual series grouping, work association, and
splitting are library-wide data and use the existing metadata-editing
permission.

## Consequences

- Search and cards no longer depend on registration or tracking.
- Personal records share across sources of one evaluation object while staying
  private per user.
- Ambiguous content stays separate instead of being silently merged.
- Work association does not imply that two editions share a track timeline.
