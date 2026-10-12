import type { ReactNode } from "react";

import { AvailabilityMeter } from "@/components/creator/AvailabilityMeter";
import type { CreatorAvailability } from "@/components/creator/CreatorCard";
import { cn } from "@/lib/tailwindClassNames";

export type CatalogStat<Value extends string> = {
  value: Value;
  label: string;
  count: number;
};

/**
 * Catalog counts that double as the availability filter: pressing a count
 * narrows the works below to it, and pressing it again shows every work.
 * The meter and note beside them describe the whole catalog.
 */
export function CatalogStatStrip<Value extends string>({
  label,
  stats,
  selected,
  onSelect,
  availability,
  availabilityLabel,
  note,
}: {
  label: string;
  stats: readonly CatalogStat<Value>[];
  selected: Value | null;
  /** Receives null when the pressed count is released. */
  onSelect: (value: Value | null) => void;
  availability: CreatorAvailability;
  availabilityLabel: string;
  note?: ReactNode;
}) {
  return (
    <div className="flex flex-col gap-3 lg:flex-row lg:items-center lg:gap-6">
      <div role="group" aria-label={label} className="-mx-1.5 grid grid-cols-4 gap-1 lg:mx-0 lg:flex lg:shrink-0">
        {stats.map((stat) => {
          const active = stat.value === selected;
          return (
            <button
              key={stat.value}
              type="button"
              className={cn(
                "flex min-h-11 min-w-0 flex-col justify-center rounded-md px-2.5 py-1 text-left transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring active:bg-muted lg:min-w-[5.25rem] lg:px-3",
                active ? "bg-primary/10 ring-1 ring-inset ring-primary/20" : "hover:bg-muted/70",
              )}
              aria-pressed={active}
              onClick={() => onSelect(active ? null : stat.value)}
            >
              <span className={cn("truncate text-2xs font-medium", active ? "text-primary" : "text-muted-foreground")}>
                {stat.label}
              </span>{" "}
              <span
                className={cn(
                  "text-lg font-semibold leading-6 tabular-nums",
                  active ? "text-primary" : stat.count > 0 ? "text-foreground" : "text-muted-foreground",
                )}
              >
                {stat.count}
              </span>
            </button>
          );
        })}
      </div>
      <div className="min-w-0 flex-1 space-y-2">
        <AvailabilityMeter
          total={availability.total}
          local={availability.local}
          available={availability.available}
          label={availabilityLabel}
          className="h-2"
        />
        {note && <p className="text-xs leading-snug text-muted-foreground">{note}</p>}
      </div>
    </div>
  );
}
