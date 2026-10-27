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
  // A distinct key keeps this exception visible when the Library scope would
  // otherwise hide the healthy mark for the same source, and lets a card choose
  // a readable chip for it instead of the compact icon.
  return [{ key: `${key}:verdict`, label, variant: "outline", title: label }];
}

/**
 * Whether these marks must be readable as text. A healthy mark may stay an
 * icon on a dense grid; a verdict or a warning is the reason to look at the
 * card, and a hover-only title is unreadable on touch.
 */
export function sourceBadgesNeedLabel(badges: Array<{ key?: string; variant?: string }>): boolean {
  return badges.some((badge) => badge.variant === "warning" || badge.key?.endsWith(":verdict") === true);
}

function verdictLabel(availability: string): string {
  return i18n.t(`sourceLifecycle.${availability}`, { defaultValue: availability });
}
