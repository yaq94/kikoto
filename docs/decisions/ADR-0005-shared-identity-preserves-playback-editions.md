# ADR-0005: Shared Identity Preserves Playback Editions

## Status

Accepted.

## Context

Work editions take part in media selection. Merging two objects into one
identity must not move media, rewrite a timeline, or discard a user's existing
rating and note. A shared association also has to be reversible.

## Decision

Shared association uses a separate, revocable identity mapping. The original
works, editions, series membership, and source references remain in place.
Evidence before the association and every user's rating and note are kept in a
protected durable operation snapshot. Identity and review ownership commit in
one transaction only after every required owner confirmation.

Ratings and notes of one shared identity stay consistent in the database so
every existing read path returns the same value. Listening state, favorites,
and playback progress keep their original per-object semantics. Import handles
the owner's conflicts by shared identity before writing; row order never
decides which value wins.

Splitting removes the mapping and restores reviews from the pre-association
evidence and each owner's allocation. Media and timelines do not move with the
association.

## Consequences

- A work association is not evidence that two editions share tracks or time.
- Personal review merges wait for the affected owners instead of overwriting.
- Every shared operation keeps a preview, a version check, and a recovery path.
