import { describe, expect, it } from "vitest";

import { sidecarFailureSummary } from "./sourceIndexSidecars";

describe("sidecarFailureSummary", () => {
  it("stays hidden while no card has stopped", () => {
    expect(sidecarFailureSummary(undefined)).toBeNull();
    expect(
      sidecarFailureSummary({ stopped: 0, temporarilyUnavailable: 0, unreadable: 0, requestBudget: 0 }),
    ).toBeNull();
  });

  it("lists only the categories that actually stopped", () => {
    const summary = sidecarFailureSummary({
      stopped: 3,
      temporarilyUnavailable: 2,
      unreadable: 0,
      requestBudget: 1,
    });
    expect(summary).toEqual({
      stopped: 3,
      categories: [
        ["temporarilyUnavailable", 2],
        ["requestBudget", 1],
      ],
    });
  });

  it("keeps the total when a category count is unknown to the UI", () => {
    // The server total also covers categories this client does not know yet, so
    // the header must still report the stopped count.
    const summary = sidecarFailureSummary({
      stopped: 5,
      temporarilyUnavailable: 1,
      unreadable: 1,
      requestBudget: 0,
    });
    expect(summary?.stopped).toBe(5);
    expect(summary?.categories.map(([key]) => key)).toEqual(["temporarilyUnavailable", "unreadable"]);
  });
});
