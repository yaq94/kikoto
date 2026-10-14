import { describe, expect, it } from "vitest";

import { pageJumpTarget, paginationItems } from "./paginationModel";

describe("paginationItems", () => {
  it("includes the first, last, current, and adjacent pages", () => {
    expect(paginationItems(6, 20)).toEqual([1, "ellipsis-left", 5, 6, 7, "ellipsis-right", 20]);
  });

  it("does not add ellipses for contiguous pages", () => {
    expect(paginationItems(2, 4)).toEqual([1, 2, 3, 4]);
  });

  it("clamps invalid page values", () => {
    expect(paginationItems(30, 5)).toEqual([1, "ellipsis-left", 4, 5]);
  });
});

describe("pageJumpTarget", () => {
  it("lands on the typed page", () => {
    expect(pageJumpTarget(" 12 ", 20)).toBe(12);
  });

  it("clamps a page outside the existing pages", () => {
    expect(pageJumpTarget("0", 20)).toBe(1);
    expect(pageJumpTarget("999", 20)).toBe(20);
  });

  it("names no page for empty or non-numeric text", () => {
    expect(pageJumpTarget("", 20)).toBeNull();
    expect(pageJumpTarget("3.5", 20)).toBeNull();
    expect(pageJumpTarget("-2", 20)).toBeNull();
  });
});
