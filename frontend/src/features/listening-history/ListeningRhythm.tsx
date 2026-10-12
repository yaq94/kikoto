import { useMemo } from "react";
import { useTranslation } from "react-i18next";

import { dateTimeFormat } from "@/i18n/format";
import { useLocale } from "@/i18n/LocaleProvider";
import type { ListeningPeriodStatistic } from "@/lib/listeningApi";
import { cn } from "@/lib/tailwindClassNames";

import { MONDAY_UTC, barPercent, weekPartAverages, weekdayAverages } from "./listeningHistoryModel";

/**
 * How a report's listening spreads over the week: the average day from Monday
 * to Friday beside the average weekend day, then the average per weekday. It
 * reads daily listening, so a report charted by month or year passes the days
 * the listening calendar holds for it.
 */
export function ListeningRhythm({
  days,
  pastYearOnly,
  formatDuration,
}: {
  days: readonly ListeningPeriodStatistic[];
  /** The report reaches further back than the days given. */
  pastYearOnly: boolean;
  formatDuration: (seconds: number) => string;
}) {
  const { t } = useTranslation();
  const { resolvedLocale } = useLocale();
  const weekdays = useMemo(() => weekdayAverages(days), [days]);
  const weekParts = useMemo(() => weekPartAverages(days), [days]);
  const weekdayMax = Math.max(0, ...weekdays.map((entry) => entry.averageSeconds));
  const weekdayName = (weekday: number) =>
    dateTimeFormat(resolvedLocale, { weekday: "short", timeZone: "UTC" }).format(MONDAY_UTC + weekday * 86_400_000);
  const average = (seconds: number) => (seconds > 0 ? formatDuration(seconds) : "–");

  return (
    <div className="space-y-4">
      <dl className="grid grid-cols-2 gap-2">
        <RhythmFigure label={t("personal.history.workdayAverage")} value={average(weekParts.workdaySeconds)} />
        <RhythmFigure label={t("personal.history.weekendAverage")} value={average(weekParts.weekendSeconds)} />
      </dl>
      <div className="space-y-1.5">
        <h4 className="text-xs font-medium text-muted-foreground">{t("personal.history.weekdayAverage")}</h4>
        <dl className="space-y-1">
          {weekdays.map((entry) => {
            const busiest = weekdayMax > 0 && entry.averageSeconds === weekdayMax;
            return (
              <div key={entry.weekday} className="grid grid-cols-[2.5rem_minmax(0,1fr)_auto] items-center gap-2">
                <dt className="truncate text-xs text-muted-foreground">{weekdayName(entry.weekday)}</dt>
                <dd className="h-2 overflow-hidden rounded-full bg-muted" aria-hidden="true">
                  <span
                    className={cn("block h-full rounded-full", busiest ? "bg-primary" : "bg-primary/45")}
                    style={{ width: `${barPercent(entry.averageSeconds, weekdayMax)}%` }}
                  />
                </dd>
                <dd
                  className={cn(
                    "min-w-14 text-right text-2xs tabular-nums",
                    busiest ? "font-medium text-foreground" : "text-muted-foreground",
                  )}
                >
                  {average(entry.averageSeconds)}
                </dd>
              </div>
            );
          })}
        </dl>
      </div>
      {pastYearOnly && <p className="text-2xs text-muted-foreground">{t("personal.history.rhythmPastYear")}</p>}
    </div>
  );
}

function RhythmFigure({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-0 rounded-lg bg-muted/50 px-3 py-2">
      <dt className="truncate text-2xs text-muted-foreground">{label}</dt>
      <dd className="truncate text-base font-semibold tabular-nums">{value}</dd>
    </div>
  );
}
