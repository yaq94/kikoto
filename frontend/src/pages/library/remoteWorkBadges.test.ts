import { describe, expect, it } from "vitest";

import { remoteSourceBadges, sourceBadgesNeedLabel } from "./remoteWorkBadges";

describe("remote source badges", () => {
  it("marks a card that can still be opened with its source", () => {
    const badges = remoteSourceBadges({ remotePlayable: true, availability: "available" }, 7, "Example Remote");
    expect(badges).toEqual([{ key: "source:remote:7", label: "Example Remote", variant: "outline" }]);
  });

  it("keeps a failed range playable and states the verdict in the label", () => {
    const badges = remoteSourceBadges({ remotePlayable: true, availability: "unavailable" }, 7, "Example Remote");
    expect(badges).toHaveLength(1);
    expect(badges[0].key).toBe("source:remote:7:verdict");
    expect(badges[0].variant).toBe("outline");
    expect(badges[0].needsLabel).toBe(true);
    expect(badges[0].label).toContain("Example Remote");
    expect(badges[0].label).toContain("unavailable");
  });

  it("warns when the source can no longer open the card", () => {
    const badges = remoteSourceBadges({ remotePlayable: false, availability: "missing" }, 7, "Example Remote");
    expect(badges).toEqual([
      {
        key: "source:remote:7:unavailable",
        label: "Example Remote unavailable",
        variant: "warning",
        needsLabel: true,
      },
    ]);
  });

  it("asks for text only when a verdict or warning needs it", () => {
    const healthy = remoteSourceBadges({ remotePlayable: true, availability: "available" }, 7, "Example Remote");
    expect(sourceBadgesNeedLabel(healthy)).toBe(false);
    const verdict = remoteSourceBadges(
      { remotePlayable: true, availability: "unconfirmed_empty" },
      7,
      "Example Remote",
    );
    expect(sourceBadgesNeedLabel(verdict)).toBe(true);
    const warning = remoteSourceBadges({ remotePlayable: false, availability: "missing" }, 7, "Example Remote");
    expect(sourceBadgesNeedLabel(warning)).toBe(true);
  });

  it("reads the badge field instead of the key spelling", () => {
    // A mark whose key says nothing about a verdict still needs a label when it
    // asks for one, and a key ending in `:verdict` alone is not a reason.
    expect(sourceBadgesNeedLabel([{ key: "source:local", label: "Local", variant: "outline", needsLabel: true }])).toBe(
      true,
    );
    expect(
      sourceBadgesNeedLabel([{ key: "source:remote:7:verdict", label: "Example Remote", variant: "outline" }]),
    ).toBe(false);
    expect(sourceBadgesNeedLabel([{ key: "source:remote:7", label: "Example Remote", variant: "outline" }])).toBe(
      false,
    );
  });
});
