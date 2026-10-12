import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { dateTimeFormat, formatNumber } from "@/i18n/format";
import { useLocale } from "@/i18n/LocaleProvider";
import type { ListeningBreakdownEntry, ListeningStatistics } from "@/lib/listeningApi";

import type { ListeningEntityLinkFactory } from "./ListeningBreakdownCard";
import { hourRangeLabel, peakListeningHour, periodStart } from "./listeningHistoryModel";
import {
  ListeningWorkRow,
  openLinkInPlace,
  type ListeningHistoryWorkLink,
  type ListeningHistoryWorkLinkFactory,
} from "./ListeningWorkRow";

/**
 * A calendar year at a glance: its total, the work, voice, and circle
 * listened to most, the busiest day, the longest run of listening days, the
 * works first heard that year, and the busiest hour of the day.
 */
export function ListeningYearHighlights({
  year,
  statistics,
  longestStreak,
  formatDuration,
  workLink,
  entityLink,
}: {
  year: string;
  statistics: ListeningStatistics;
  /** The longest run of consecutive listening days in the year; null while its calendar loads. */
  longestStreak: number | null;
  formatDuration: (seconds: number) => string;
  workLink: ListeningHistoryWorkLinkFactory;
  entityLink: ListeningEntityLinkFactory;
}) {
  const { t } = useTranslation();
  const { resolvedLocale } = useLocale();
  const highlights = statistics.highlights;
  const topWork = statistics.topWorks[0];
  if (!highlights || statistics.listenedSeconds <= 0) return null;
  const count = (value: number) => formatNumber(value, resolvedLocale);
  const peakHour = peakListeningHour(statistics.hours);
  const busiest = highlights.busiestDay;
  const entity = (kind: "voices" | "circles", entry: ListeningBreakdownEntry | undefined) =>
    entry && (
      <Highlight
        label={t(`personal.history.highlights.${kind}`)}
        value={entry.name}
        detail={formatDuration(entry.listenedSeconds)}
        link={entityLink(kind, entry)}
      />
    );

  return (
    <section
      aria-labelledby="listening-highlights-heading"
      className="theme-card-surface space-y-3 rounded-xl border bg-card p-4"
    >
      <div>
        <h2 id="listening-highlights-heading" className="text-base font-semibold">
          {t("personal.history.highlights.title", { year })}
        </h2>
        <p className="mt-0.5 text-xs leading-5 text-muted-foreground">
          {t("personal.history.highlights.summary", {
            duration: formatDuration(statistics.listenedSeconds),
            works: count(statistics.workCount),
            days: count(statistics.activeDays),
          })}
        </p>
      </div>
      {topWork && (
        <div className="rounded-lg bg-muted/50 px-1 py-1">
          <p className="px-2 pt-1 text-2xs text-muted-foreground">{t("personal.history.highlights.work")}</p>
          <ListeningWorkRow
            item={topWork}
            link={workLink(topWork.primaryCode)}
            value={formatDuration(topWork.listenedSeconds)}
          />
        </div>
      )}
      <dl className="grid grid-cols-2 gap-2 lg:grid-cols-3">
        {entity("voices", highlights.topVoice)}
        {entity("circles", highlights.topCircle)}
        {busiest && (
          <Highlight
            label={t("personal.history.highlights.busiestDay")}
            value={dateTimeFormat(resolvedLocale, { month: "short", day: "numeric", timeZone: "UTC" }).format(
              periodStart(busiest.date),
            )}
            detail={
              busiest.topWork
                ? t("personal.history.highlights.busiestDayDetail", {
                    duration: formatDuration(busiest.listenedSeconds),
                    title: busiest.topWork.title || busiest.topWork.primaryCode,
                  })
                : formatDuration(busiest.listenedSeconds)
            }
          />
        )}
        {longestStreak !== null && longestStreak > 0 && (
          <Highlight
            label={t("personal.history.longestStreak")}
            value={t("personal.history.periodCount.day", { count: longestStreak })}
          />
        )}
        <Highlight
          label={t("personal.history.highlights.newWorks")}
          value={t("personal.history.breakdown.workCount", { count: highlights.newWorkCount })}
        />
        {peakHour !== null && (
          <Highlight label={t("personal.history.highlights.peakHour")} value={hourRangeLabel(peakHour)} />
        )}
      </dl>
    </section>
  );
}

function Highlight({
  label,
  value,
  detail,
  link,
}: {
  label: string;
  value: string;
  detail?: ReactNode;
  link?: ListeningHistoryWorkLink | null;
}) {
  return (
    <div className="min-w-0 rounded-lg bg-muted/50 px-3 py-2">
      <dt className="truncate text-2xs text-muted-foreground">{label}</dt>
      <dd className="truncate text-base font-semibold">
        {link ? (
          <a
            href={link.href}
            className="rounded-sm hover:text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            onClick={(event) => openLinkInPlace(event, link)}
          >
            {value}
          </a>
        ) : (
          value
        )}
      </dd>
      {detail && <dd className="truncate text-2xs text-muted-foreground">{detail}</dd>}
    </div>
  );
}
