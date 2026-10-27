import { describe, expect, it } from "vitest";

import { showsRecommendationBadge } from "./recommendationBadge";

describe("showsRecommendationBadge", () => {
  it("hides a baseline-only score", () => {
    expect(showsRecommendationBadge(true, 35, 35)).toBe(false);
  });

  it("shows a score that personal evidence lifted above the baseline", () => {
    expect(showsRecommendationBadge(true, 60, 35)).toBe(true);
  });

  it("hides a row the list did not score and a missing score", () => {
    expect(showsRecommendationBadge(false, 60, 35)).toBe(false);
    expect(showsRecommendationBadge(true, undefined, 35)).toBe(false);
    expect(showsRecommendationBadge(true, Number.NaN, 35)).toBe(false);
  });
});
