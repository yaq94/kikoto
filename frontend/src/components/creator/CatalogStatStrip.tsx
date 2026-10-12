import type { ReactNode } from "react";

import { AvailabilityMeter } from "@/components/creator/AvailabilityMeter";
import type { CreatorAvailability } from "@/components/creator/CreatorCard";
import { cn } from "@/lib/tailwindClassNames";

export type CatalogStat = {
  key: string;
  label: string;
  count: number;
};

/**
 * Catalog counts for a creator's summary, with a meter and a note that
 * describe the whole catalog. The counts only report; the works toolbar owns
 * filtering.
 */
export function CatalogStatStrip({
  label,
  stats,
  availability,
  availabilityLabel,
  note,
}: {
  label: string;
  stats: readonly CatalogStat[];
  availability: CreatorAvailability;
  availabilityLabel: string;
  note?: ReactNode;
}) {
  return (
    <div className="flex flex-col gap-3 lg:flex-row lg:items-center lg:gap-8">
      <dl role="group" aria-label={label} className="grid grid-cols-4 gap-x-3 lg:flex lg:shrink-0 lg:gap-x-8">
        {stats.map((stat) => (
          <div key={stat.key} className="min-w-0">
            <dt className="truncate text-2xs font-medium text-muted-foreground">{stat.label}</dt>
            <dd
              className={cn(
                "text-lg font-semibold leading-6 tabular-nums",
                stat.count > 0 ? "text-foreground" : "text-muted-foreground",
              )}
            >
              {stat.count}
            </dd>
          </div>
        ))}
      </dl>
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
