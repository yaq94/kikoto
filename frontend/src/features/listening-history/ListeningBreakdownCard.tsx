import { ChevronDown } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import type { ListeningBreakdownEntry, ListeningBreakdownKind } from "@/lib/listeningApi";
import { cn } from "@/lib/tailwindClassNames";

import { barPercent } from "./listeningHistoryModel";
import { openLinkInPlace, type ListeningHistoryWorkLink } from "./ListeningWorkRow";

/** Where a ranked tag, voice, or circle opens, composed by the app shell; null when it has no destination. */
export type ListeningEntityLinkFactory = (
  kind: ListeningBreakdownKind,
  entry: ListeningBreakdownEntry,
) => ListeningHistoryWorkLink | null;

// The ranking opens with this many entries; the rest of the server's ranking shows on request.
const TOP_ENTRY_COUNT = 5;

/**
 * One dimension of a listening report: tags, voices, or circles ranked by the
 * listening time of their works, each with a bar relative to the leader. An
 * entry with a destination opens it in place on a plain primary click.
 */
export function ListeningBreakdownCard({
  kind,
  entries,
  formatDuration,
  link,
}: {
  kind: ListeningBreakdownKind;
  entries: readonly ListeningBreakdownEntry[];
  formatDuration: (seconds: number) => string;
  link: ListeningEntityLinkFactory;
}) {
  const { t } = useTranslation();
  const [expanded, setExpanded] = useState(false);
  const headingId = `listening-breakdown-${kind}-heading`;
  const title = t(`personal.history.breakdown.titles.${kind}`);
  const shown = expanded ? entries : entries.slice(0, TOP_ENTRY_COUNT);
  const hasMore = entries.length > TOP_ENTRY_COUNT;
  const leaderSeconds = entries[0]?.listenedSeconds ?? 0;
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
                    <span className="mt-1 block h-1.5 overflow-hidden rounded-full bg-muted" aria-hidden="true">
                      <span
                        className="block h-full rounded-full bg-primary/80"
                        style={{ width: `${barPercent(entry.listenedSeconds, leaderSeconds)}%` }}
                      />
                    </span>
                  </span>
                  <span className="shrink-0 text-right">
                    <span className="block text-sm font-medium tabular-nums">
                      {formatDuration(entry.listenedSeconds)}
                    </span>
                    <span className="block text-2xs tabular-nums text-muted-foreground">
                      {t("personal.history.breakdown.workCount", { count: entry.workCount })}
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
          {t(`personal.history.breakdown.empty.${kind}`)}
        </p>
      )}
    </section>
  );
}
