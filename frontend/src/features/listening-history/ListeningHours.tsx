import { useTranslation } from "react-i18next";

import { cn } from "@/lib/tailwindClassNames";

import { barPercent, hourRangeLabel, peakListeningHour } from "./listeningHistoryModel";

// Hours that carry a label under the chart.
const labelledHours = [0, 6, 12, 18];

/**
 * A report's listening by hour of the day, midnight first, with the busiest
 * hour named. It covers only the listening whose hours were recorded.
 */
export function ListeningHours({
  hours,
  formatDuration,
}: {
  hours: readonly number[];
  formatDuration: (seconds: number) => string;
}) {
  const { t } = useTranslation();
  const peak = peakListeningHour(hours);
  if (peak === null) return null;
  const peakSeconds = hours[peak];
  return (
    <div className="space-y-1.5">
      <div className="flex items-baseline justify-between gap-3">
        <h4 className="text-xs font-medium text-muted-foreground">{t("personal.history.hours.title")}</h4>
        <span className="text-2xs tabular-nums text-muted-foreground">
          {t("personal.history.hours.peak", { hours: hourRangeLabel(peak) })}
        </span>
      </div>
      <div className="flex h-14 items-end gap-px" aria-hidden="true">
        {hours.map((seconds, hour) => (
          <span
            key={hour}
            title={`${hourRangeLabel(hour)} · ${formatDuration(seconds)}`}
            className="flex h-full min-w-0 flex-1 items-end rounded-sm bg-muted/60"
          >
            <span
              className={cn("block w-full rounded-sm", hour === peak ? "bg-primary" : "bg-primary/45")}
              style={{ height: `${barPercent(seconds, peakSeconds)}%` }}
            />
          </span>
        ))}
      </div>
      <div className="grid grid-cols-4 text-2xs tabular-nums text-muted-foreground" aria-hidden="true">
        {labelledHours.map((hour) => (
          <span key={hour}>{String(hour).padStart(2, "0")}</span>
        ))}
      </div>
      <p className="text-2xs text-muted-foreground">{t("personal.history.hours.basis")}</p>
    </div>
  );
}
