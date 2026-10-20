import type { CatalogIndexProgress } from "@/lib/api";

type SidecarFailures = CatalogIndexProgress["sidecarFailures"];

/**
 * The stopped-card summary is only worth showing when a card actually left the
 * automatic queue. Zero is the normal state, so it must not add noise to the
 * panel; the categories explain which unrecovered case an operator is looking
 * at. The order is fixed so the copy is stable regardless of which counts
 * happen to be non-zero.
 */
export function sidecarFailureSummary(failures: SidecarFailures | undefined) {
  if (!failures || failures.stopped <= 0) return null;
  const categories = (
    [
      ["temporarilyUnavailable", failures.temporarilyUnavailable],
      ["unreadable", failures.unreadable],
      ["requestBudget", failures.requestBudget],
    ] as const
  ).filter(([, count]) => count > 0);
  return { stopped: failures.stopped, categories };
}
