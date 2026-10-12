import { describe, expect, it } from "vitest";

import { creatorWorksBrowseFromValue, defaultCreatorWorksBrowse } from "@/lib/creatorWorksBrowse";

describe("creator works browse preference", () => {
  it("keeps a stored order, direction, and page size", () => {
    expect(creatorWorksBrowseFromValue({ sort: "sales", direction: "asc", pageSize: 96 })).toEqual({
      sort: "sales",
      direction: "asc",
      pageSize: 96,
    });
  });

  it("defaults each field a creator's works do not offer", () => {
    // A seeded or account-based Library order has no creator equivalent.
    expect(creatorWorksBrowseFromValue({ sort: "random", direction: "up", pageSize: 36 })).toEqual(
      defaultCreatorWorksBrowse,
    );
    expect(creatorWorksBrowseFromValue({ sort: "title" })).toEqual({ ...defaultCreatorWorksBrowse, sort: "title" });
    expect(creatorWorksBrowseFromValue(null)).toEqual(defaultCreatorWorksBrowse);
  });
});
