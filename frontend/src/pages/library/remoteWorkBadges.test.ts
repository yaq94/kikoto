import { describe, expect, it } from "vitest";

import { remoteSourceBadges } from "./remoteWorkBadges";

describe("remote source badges", () => {
  it("marks a card that can still be opened with its source", () => {
    const badges = remoteSourceBadges({ remotePlayable: true, availability: "available" }, 7, "Example Remote");
    expect(badges).toEqual([{ key: "source:remote:7", label: "Example Remote", variant: "outline" }]);
  });

  it("keeps a failed range playable and names the verdict", () => {
    const badges = remoteSourceBadges({ remotePlayable: true, availability: "unavailable" }, 7, "Example Remote");
    expect(badges).toHaveLength(1);
    expect(badges[0].key).toBe("source:remote:7");
    expect(badges[0].variant).toBe("outline");
    expect(badges[0].title).toBeTruthy();
  });

  it("warns when the source can no longer open the card", () => {
    const badges = remoteSourceBadges({ remotePlayable: false, availability: "missing" }, 7, "Example Remote");
    expect(badges).toEqual([
      { key: "source:remote:7:unavailable", label: "Example Remote unavailable", variant: "warning" },
    ]);
  });
});
