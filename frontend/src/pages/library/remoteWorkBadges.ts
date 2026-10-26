import type { WorkCardBadge } from "@/components/work-card/WorkCardShell";
import i18n from "@/i18n";
import type { RemoteWork } from "@/lib/api";

/**
 * A remote card answers two separate questions: which source it comes from,
 * and whether that source's last verdict still allows opening it. A failed,
 * restricted, maintenance or unconfirmed range keeps its published files and
 * stays playable, so the badge keeps the source name and states the verdict in
 * the label; a tooltip alone would be unreadable on touch. A confirmed absence,
 * an excluded card or a disabled source replaces the badge with a warning.
 */
export function remoteSourceBadges(
  work: Pick<RemoteWork, "remotePlayable" | "availability">,
  sourceId: number,
  sourceLabel: string,
): WorkCardBadge[] {
  const key = `source:remote:${sourceId}`;
  if (!work.remotePlayable) {
    return [
      {
        key: `${key}:unavailable`,
        label: i18n.t("workCard.namedSourceUnavailable", { name: sourceLabel }),
        variant: "warning",
      },
    ];
  }
  const verdict = work.availability && work.availability !== "available" ? verdictLabel(work.availability) : "";
  if (verdict === "") return [{ key, label: sourceLabel, variant: "outline" }];
  const label = i18n.t("workCard.namedSourceVerdict", { name: sourceLabel, verdict });
  return [{ key, label, variant: "outline", title: label }];
}

function verdictLabel(availability: string): string {
  return i18n.t(`sourceLifecycle.${availability}`, { defaultValue: availability });
}
