# Catalog prune dry-run

> Historical snapshot under the former alias policy. Current reports skip every
> source-only card that still owns a `personal_transfer_ref`; do not use these
> deletion counts as a current apply plan.

- Generated: 2026-10-21T11:59:46Z
- Note: a dry run on a stopped copy of the deployment database found 643 candidate cards / 114,648 files / 643 transfer refs. The historical plan proposed removing 642 / 114,412 / 642 because one path was inside the `/source/example/audio` whitelist.
- Candidate cards (failed admission): 643
- Candidate files: 114648 (audio 20, non-audio 114628)
- Candidate transfer refs: 643
- Cards planned for deletion: 642
- Files planned for deletion: 114412 (audio 20, non-audio 114392)
- Observations: 125587
- Directories: 1514
- Audio roots: 6379
- Transfer refs: 642
- Kept path rows (whitelist or preserved entry): 4770
- Whitelist: /source/example/audio

## Skipped

| entry | root | reason |
| --- | --- | --- |
| one | `/source/example/audio/whitelisted` | whitelisted |
