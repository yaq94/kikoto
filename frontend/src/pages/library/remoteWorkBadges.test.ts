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
    expect(badges[0].label).toContain("Example Remote");
    expect(badges[0].label).toContain("unavailable");
  });

  it("warns when the source can no longer open the card", () => {
    const badges = remoteSourceBadges({ remotePlayable: false, availability: "missing" }, 7, "Example Remote");
    expect(badges).toEqual([
      { key: "source:remote:7:unavailable", label: "Example Remote unavailable", variant: "warning" },
    ]);
  });

  it("asks for a readable mark only when a verdict or warning needs one", () => {
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
});
