import { describe, expect, it } from "vitest";

import { availabilityDotClassName } from "./WorkCardShell";
import { visibleBadgeCountForRows } from "./tagLayout";

describe("work card tag measurement", () => {
  it("keeps every tag when two rows fit at a common desktop card width", () => {
    expect(visibleBadgeCountForRows([58, 72, 64, 76], 240, 42)).toBe(4);
  });

  it("reserves the overflow badge inside two rows at a common mobile card width", () => {
    expect(visibleBadgeCountForRows([76, 84, 68, 92, 64], 176, 38)).toBe(3);
  });

  it("clamps an oversized tag to one row without hiding tags that fit on the second", () => {
    expect(visibleBadgeCountForRows([320, 68, 68], 176, 38)).toBe(3);
  });
});

describe("work card source dot", () => {
  it("keeps a healthy source mark green", () => {
    expect(availabilityDotClassName({ key: "source:remote:7", label: "Example Remote" })).toBe("bg-success");
  });

  it("does not paint a verdict mark green", () => {
    // The chip reads "unavailable"; a success dot next to it states the
    // opposite of the label.
    expect(
      availabilityDotClassName({
        key: "source:remote:7:verdict",
        label: "Example Remote (unavailable)",
        needsLabel: true,
      }),
    ).toBe("bg-warning");
    expect(
      availabilityDotClassName({ key: "source:remote:7:unavailable", label: "Example Remote", variant: "warning" }),
    ).toBe("bg-warning");
  });

  it("keeps a source mark green when only its key spells a verdict", () => {
    // The key is an identity for the Library scope, not a statement about the
    // mark; a card that forgets `needsLabel` must not silently get the warning dot.
    expect(availabilityDotClassName({ key: "source:remote:7:verdict", label: "Example Remote" })).toBe("bg-success");
  });
});
