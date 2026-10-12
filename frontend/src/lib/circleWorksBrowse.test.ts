import { describe, expect, it } from "vitest";

import { circleWorksBrowseFromValue, defaultCircleWorksBrowse } from "@/lib/circleWorksBrowse";

describe("circle works browse preference", () => {
  it("keeps a stored order, direction, and page size", () => {
    expect(circleWorksBrowseFromValue({ sort: "sales", direction: "asc", pageSize: 96 })).toEqual({
      sort: "sales",
      direction: "asc",
      pageSize: 96,
    });
  });

  it("defaults each field a circle catalog does not offer", () => {
    // A seeded or account-based Library order has no circle catalog equivalent.
    expect(circleWorksBrowseFromValue({ sort: "random", direction: "up", pageSize: 36 })).toEqual(
      defaultCircleWorksBrowse,
    );
    expect(circleWorksBrowseFromValue({ sort: "title" })).toEqual({ ...defaultCircleWorksBrowse, sort: "title" });
    expect(circleWorksBrowseFromValue(null)).toEqual(defaultCircleWorksBrowse);
  });
});
