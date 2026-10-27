/**
 * A recommendation badge is a claim about the viewer, not a rating. The score
 * of a work with no matching tag, cast, circle or favorite is exactly the
 * affinity baseline, so that row must not carry the claim. Only a score above
 * the baseline has personal evidence behind it.
 */
export function showsRecommendationBadge(scored: boolean, score: number | null | undefined, baseline: number): boolean {
  return scored && typeof score === "number" && Number.isFinite(score) && score > baseline;
}
