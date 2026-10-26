import type { WorkCardBadge } from "@/components/work-card/WorkCardShell";
import i18n from "@/i18n";
import type { RemoteWork } from "@/lib/api";

/**
 * A remote card answers two separate questions: which source it comes from,
 * and whether that source's last verdict still allows opening it. A failed,
 * restricted, maintenance or unconfirmed range keeps its published files and
 * stays playable, so the badge names the source with the verdict as its title.
 * A confirmed absence, an excluded card or a disabled source is the warning.
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
  if (work.availability && work.availability !== "available") {
    return [
      {
        key,
        label: sourceLabel,
        variant: "outline",
        title: i18n.t(`sourceLifecycle.${work.availability}`, { defaultValue: work.availability }),
      },
    ];
  }
  return [{ key, label: sourceLabel, variant: "outline" }];
}
