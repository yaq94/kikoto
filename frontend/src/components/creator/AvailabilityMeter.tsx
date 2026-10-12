import { cn } from "@/lib/tailwindClassNames";

/**
 * Share of a catalog that can be played: a solid segment for works with a
 * local copy, a lighter one for works only a remote source holds, and the
 * empty track for the rest. Both segments use the success role, so the bar
 * reads as availability rather than as source identity.
 */
export function AvailabilityMeter({
  total,
  local,
  available,
  label,
  className,
}: {
  total: number;
  local: number;
  /** Works with any playable copy; never less than `local`. */
  available: number;
  label: string;
  className?: string;
}) {
  const safeTotal = Math.max(total, available, local, 0);
  const localShare = safeTotal > 0 ? Math.min(local, safeTotal) / safeTotal : 0;
  const remoteShare = safeTotal > 0 ? Math.max(0, Math.min(available, safeTotal) - local) / safeTotal : 0;
  return (
    <div
      role="img"
      aria-label={label}
      title={label}
      className={cn("flex h-1.5 w-full gap-px overflow-hidden rounded-full bg-muted", className)}
    >
      {localShare > 0 && <span className="h-full bg-success" style={{ width: `${localShare * 100}%` }} />}
      {remoteShare > 0 && <span className="h-full bg-success/45" style={{ width: `${remoteShare * 100}%` }} />}
    </div>
  );
}
