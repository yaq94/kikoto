import { describe, expect, it } from "vitest";

import { remoteSourceBadges, sourceBadgesNeedLabel } from "@/pages/library/remoteWorkBadges";

import { circleSourceBadges, sortSourceBadges, sourcePresenceBadges } from "./sourceBadges";

describe("source presence badges", () => {
  it("orders local, tracked, and remote presence and distinguishes unforked tracking", () => {
    const badges = sourcePresenceBadges([
      { type: "source", availability: "available", fileSourceId: 4, fileSourceName: "Example Remote" },
      { type: "tracked", availability: "available", fileSourceId: 5, fileSourceName: "Example Tracked" },
      { type: "local", availability: "available" },
    ]);

    expect(badges.map((badge) => badge.key)).toEqual(["source:local", "source:tracked:5", "source:4"]);
    expect(badges[1]).toMatchObject({ label: "Unforked", variant: "warning" });
  });

  it("uses playable availability to label tracked sources and falls back to availability badges", () => {
    const tracked = sourcePresenceBadges(
      [{ type: "tracked", availability: "available", fileSourceId: 5, fileSourceName: "Example Tracked" }],
      ["remote"],
    );
    expect(tracked[0]).toMatchObject({ key: "source:tracked:5", label: "Tracked", variant: "outline" });

    const fallback = sourcePresenceBadges([], ["missing", "cache", "remote"]);
    expect(fallback.map((badge) => badge.key)).toEqual(["source:cache", "source:missing"]);

    const noSource = sourcePresenceBadges([], []);
    expect(noSource).toMatchObject([{ key: "source:no-source", variant: "warning" }]);
  });
});

describe("circle source badges", () => {
  it("filters non-playable source summaries, deduplicates keys, and keeps local/cache first", () => {
    const badges = circleSourceBadges({
      local: true,
      cache: true,
      sourceTags: [
        { key: "remote", sourceId: 1, displayName: "Ignored Remote", status: "available", count: 4 },
        { key: "example_remote", sourceId: 7, displayName: "Example Remote", status: "available", count: 1 },
        { key: "example_remote_duplicate", sourceId: 7, displayName: "Duplicate", status: "available", count: 2 },
        { key: "empty", sourceId: 8, displayName: "Empty", status: "unknown", count: 0 },
        { key: "degraded", sourceId: 9, displayName: "Degraded", status: "error", count: 2 },
      ],
    });

    expect(badges.map((badge) => badge.key)).toEqual([
      "source:local",
      "source:remote:9",
      "source:remote:7",
      "source:cache",
    ]);
    expect(badges[2].label).toBe("Example Remote");
  });

  it("treats a null source summary from an older API as empty", () => {
    expect(
      circleSourceBadges({
        sourceTags: null,
      }),
    ).toEqual([]);
  });
});

describe("source badge order", () => {
  it("puts a readable mark before the healthy mark of its own class", () => {
    // The state a Library card reaches in the "always" cover-source mode: the
    // mark the scope implies plus the verdict that overrides it.
    const healthy = remoteSourceBadges({ remotePlayable: true, availability: "available" }, 7, "Example Remote");
    const verdict = remoteSourceBadges({ remotePlayable: true, availability: "unavailable" }, 7, "Example Remote");
    const badges = [...healthy, ...verdict];

    expect(sourceBadgesNeedLabel(badges)).toBe(true);
    const sorted = sortSourceBadges(badges);
    expect(sorted.map((badge) => badge.key)).toEqual(["source:remote:7:verdict", "source:remote:7"]);
    // The compact card shows only the first mark; it has to be the readable one.
    expect(sorted[0].readable).toBe(true);
  });

  it("keeps the class order and only moves readable marks inside their class", () => {
    const sorted = sortSourceBadges([
      { key: "source:remote:7", label: "Example Remote", variant: "outline" },
      { key: "source:cache", label: "Cache", variant: "secondary" },
      { key: "source:remote:7:verdict", label: "Example Remote (unavailable)", variant: "outline", readable: true },
      { key: "source:local", label: "Local", variant: "secondary" },
    ]);

    expect(sorted.map((badge) => badge.key)).toEqual([
      "source:local",
      "source:remote:7:verdict",
      "source:remote:7",
      "source:cache",
    ]);
  });
});
