import { ChevronDown } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import type { ListeningBreakdown, ListeningBreakdownEntry, ListeningBreakdownKind } from "@/lib/listeningApi";
import { cn } from "@/lib/tailwindClassNames";

import { barPercent, sharePercent } from "./listeningHistoryModel";
import { openLinkInPlace, type ListeningHistoryWorkLink } from "./ListeningWorkRow";

/** Where a ranked tag, voice, or circle opens, composed by the app shell; null when it has no destination. */
export type ListeningEntityLinkFactory = (
  kind: ListeningBreakdownKind,
  entry: ListeningBreakdownEntry,
) => ListeningHistoryWorkLink | null;

// The ranking opens with this many entries; the rest of the server's ranking shows on request.
const TOP_ENTRY_COUNT = 5;

/**
 * One dimension of a listening report: tags, voices, or circles in the
 * report's ranking. Ranked by time, an entry shows its listening time on a bar
 * relative to the leader. Ranked by preference, it shows its share of
 * listening time on the bar and its share of the library's works as a mark on
 * the same scale. An entry with a destination opens it in place on a plain
 * primary click.
 */
export function ListeningBreakdownCard({
  kind,
  breakdown,
  formatDuration,
  link,
}: {
  kind: ListeningBreakdownKind;
  breakdown: ListeningBreakdown;
  formatDuration: (seconds: number) => string;
  link: ListeningEntityLinkFactory;
}) {
  const { t } = useTranslation();
  const [expanded, setExpanded] = useState(false);
  const entries = breakdown[kind];
  const byPreference = breakdown.rank === "preference";
  const libraryWorks = breakdown.libraryWorkCount ?? 0;
  const headingId = `listening-breakdown-${kind}-heading`;
  const title = t(`personal.history.breakdown.titles.${kind}`);
  const shown = expanded ? entries : entries.slice(0, TOP_ENTRY_COUNT);
  const hasMore = entries.length > TOP_ENTRY_COUNT;
  const leaderSeconds = Math.max(0, ...entries.map((entry) => entry.listenedSeconds));
  const libraryShare = (entry: ListeningBreakdownEntry) =>
    libraryWorks > 0 ? (entry.libraryWorkCount ?? 0) / libraryWorks : 0;
  const listeningShare = (entry: ListeningBreakdownEntry) =>
    breakdown.listenedSeconds > 0 ? entry.listenedSeconds / breakdown.listenedSeconds : 0;
  // Both shares of a preference ranking sit on one scale, the largest of them.
  const shareScale = Math.max(0, ...entries.flatMap((entry) => [listeningShare(entry), libraryShare(entry)]));
  const rowClassName = "group flex min-h-11 min-w-0 items-center gap-3 rounded-lg px-2 py-1.5";

  return (
    <section aria-labelledby={headingId} className="theme-card-surface min-w-0 rounded-xl border bg-card p-4">
      <h3 id={headingId} className="mb-2 text-sm font-semibold">
        {title}
      </h3>
      {shown.length > 0 ? (
        <>
          <ol aria-label={title} className="-mx-2 space-y-0.5">
            {shown.map((entry, index) => {
              const destination = link(kind, entry);
              const content = (
                <>
                  <span
                    className={cn(
                      "w-5 shrink-0 text-center text-sm font-semibold tabular-nums",
                      index === 0 ? "text-primary" : "text-muted-foreground",
                    )}
                  >
                    <span className="sr-only">{t("personal.history.rank", { rank: index + 1 })}</span>
                    <span aria-hidden="true">{index + 1}</span>
                  </span>
                  <span className="min-w-0 flex-1">
                    <span
                      className={cn(
                        "block truncate text-sm font-medium leading-snug",
                        destination && "group-hover:text-primary",
                      )}
                    >
                      {entry.name}
                    </span>
                    <span className="relative mt-1 block h-1.5 rounded-full bg-muted" aria-hidden="true">
                      <span
                        className="block h-full rounded-full bg-primary/80"
                        style={{
                          width: `${
                            byPreference
                              ? barPercent(listeningShare(entry), shareScale)
                              : barPercent(entry.listenedSeconds, leaderSeconds)
                          }%`,
                        }}
                      />
                      {byPreference && (
                        <span
                          className="absolute -top-1 h-3.5 w-0.5 -translate-x-1/2 rounded-full bg-foreground/55"
                          style={{ left: `${shareScale > 0 ? (libraryShare(entry) / shareScale) * 100 : 0}%` }}
                        />
                      )}
                    </span>
                  </span>
                  <span className="shrink-0 text-right">
                    <span className="block text-sm font-medium tabular-nums">
                      {byPreference
                        ? t("personal.history.shareOfTime", {
                            percent: sharePercent(entry.listenedSeconds, breakdown.listenedSeconds),
                          })
                        : formatDuration(entry.listenedSeconds)}
                    </span>
                    <span className="block text-2xs tabular-nums text-muted-foreground">
                      {byPreference
                        ? t("personal.history.breakdown.libraryShare", {
                            percent: sharePercent(entry.libraryWorkCount ?? 0, libraryWorks),
                          })
                        : t("personal.history.breakdown.workCount", { count: entry.workCount })}
                    </span>
                  </span>
                </>
              );
              return (
                <li key={entry.id}>
                  {destination ? (
                    <a
                      href={destination.href}
                      onClick={(event) => openLinkInPlace(event, destination)}
                      title={t(`personal.history.breakdown.open.${kind}`, { name: entry.name })}
                      className={cn(
                        rowClassName,
                        "transition-colors hover:bg-muted/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                      )}
                    >
                      {content}
                    </a>
                  ) : (
                    <div className={rowClassName}>{content}</div>
                  )}
                </li>
              );
            })}
          </ol>
          {hasMore && (
            <Button
              variant="ghost"
              size="sm"
              className="mt-1 w-full text-muted-foreground"
              aria-expanded={expanded}
              onClick={() => setExpanded((value) => !value)}
            >
              <ChevronDown
                className={cn("h-4 w-4 transition-transform", expanded && "rotate-180")}
                aria-hidden="true"
              />
              {expanded
                ? t("personal.history.showFewerTopWorks")
                : t("personal.history.breakdown.showAll", { count: entries.length })}
            </Button>
          )}
        </>
      ) : (
        <p className="py-6 text-center text-xs text-muted-foreground">
          {byPreference
            ? t("personal.history.breakdown.emptyPreference")
            : t(`personal.history.breakdown.empty.${kind}`)}
        </p>
      )}
    </section>
  );
}
