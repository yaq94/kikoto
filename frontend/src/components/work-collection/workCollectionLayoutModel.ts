import type { CSSProperties } from "react";

import type { CompactWorkCardLayout } from "@/components/work-card/CompactWorkCard";

export const workCollectionColumnOptions = [1, 2, 3, 4, 5, 6, 7, 8] as const;
export type WorkCollectionColumnCount = (typeof workCollectionColumnOptions)[number];
export type WorkCollectionColumnSetting = "auto" | WorkCollectionColumnCount;

/** Narrow screens only fit one or two cards per row. */
export const workCollectionMobileColumnOptions = [1, 2] as const satisfies readonly WorkCollectionColumnCount[];
/** One or two cards leave most of a desktop row empty, so desktop starts at three. */
export const workCollectionDesktopColumnOptions = [
  3, 4, 5, 6, 7, 8,
] as const satisfies readonly WorkCollectionColumnCount[];

/** How much of a work each card shows, from the regular card down to the cover with its title. */
export const workCardModes = ["full", "compact", "mini"] as const;
export type WorkCardMode = (typeof workCardModes)[number];

export type StoredWorkCollectionLayout = {
  mobileColumns: WorkCollectionColumnSetting;
  desktopColumns: WorkCollectionColumnSetting;
  /** Card modes apply to the collections that offer the choice, one per screen width. */
  mobileCardMode: WorkCardMode;
  desktopCardMode: WorkCardMode;
};

export type WorkCollectionColumnDefaults = Pick<StoredWorkCollectionLayout, "mobileColumns" | "desktopColumns">;

const autoColumnMinWidth = "16rem";

export function workCollectionClassName() {
  return "grid gap-3 sm:gap-4 [grid-template-columns:var(--mobile-grid-template)] lg:[grid-template-columns:var(--desktop-grid-template)]";
}

export function workCollectionStyle(
  mobileColumns: WorkCollectionColumnSetting,
  desktopColumns: WorkCollectionColumnSetting,
  cardLayout: CompactWorkCardLayout | null = null,
) {
  return {
    // One cover card fills a phone row, so automatic phone columns pair them up.
    "--mobile-grid-template": gridTemplate(cardLayout === "cover" && mobileColumns === "auto" ? 2 : mobileColumns),
    "--desktop-grid-template": gridTemplate(desktopColumns),
  } as CSSProperties;
}

/**
 * The reduced card layout a mode uses at the current width, or null for the
 * regular card. Compact reads as rows in a single phone column and as short
 * tiles everywhere else.
 */
export function workCardLayout(
  mode: WorkCardMode,
  wide: boolean,
  mobileColumns: WorkCollectionColumnSetting,
): CompactWorkCardLayout | null {
  if (mode === "mini") return "cover";
  if (mode === "compact") return wide || mobileColumns === 2 ? "tile" : "row";
  return null;
}

export function isWorkCardMode(value: unknown): value is WorkCardMode {
  return (workCardModes as readonly unknown[]).includes(value);
}

/** Reads the stored layout, keeping each valid field and defaulting the rest. */
export function storedWorkCollectionLayout(
  raw: string | null,
  defaults: WorkCollectionColumnDefaults,
): StoredWorkCollectionLayout {
  const fallback: StoredWorkCollectionLayout = { ...defaults, mobileCardMode: "full", desktopCardMode: "full" };
  try {
    const value = JSON.parse(raw ?? "{}") as Partial<StoredWorkCollectionLayout> & { mobileCompact?: unknown };
    return {
      mobileColumns: isWorkCollectionMobileColumnSetting(value.mobileColumns)
        ? value.mobileColumns
        : fallback.mobileColumns,
      // A stored desktop column choice outside the supported options reverts to the default.
      desktopColumns: isWorkCollectionDesktopColumnSetting(value.desktopColumns)
        ? value.desktopColumns
        : isWorkCollectionDesktopColumnSetting(fallback.desktopColumns)
          ? fallback.desktopColumns
          : "auto",
      // A stored `mobileCompact` flag is the compact phone mode.
      mobileCardMode: isWorkCardMode(value.mobileCardMode)
        ? value.mobileCardMode
        : value.mobileCompact === true
          ? "compact"
          : fallback.mobileCardMode,
      desktopCardMode: isWorkCardMode(value.desktopCardMode) ? value.desktopCardMode : fallback.desktopCardMode,
    };
  } catch {
    return fallback;
  }
}

export function isWorkCollectionColumnCount(value: unknown): value is WorkCollectionColumnCount {
  return workCollectionColumnOptions.includes(value as WorkCollectionColumnCount);
}

export function isWorkCollectionMobileColumnSetting(value: unknown): value is WorkCollectionColumnSetting {
  return value === "auto" || (workCollectionMobileColumnOptions as readonly unknown[]).includes(value);
}

export function isWorkCollectionDesktopColumnSetting(value: unknown): value is WorkCollectionColumnSetting {
  return value === "auto" || (workCollectionDesktopColumnOptions as readonly unknown[]).includes(value);
}

function gridTemplate(setting: WorkCollectionColumnSetting) {
  if (setting === "auto") {
    // The percentage term caps wide layouts at five tracks while the fixed
    // minimum lets narrower containers step down naturally.
    return `repeat(auto-fill, minmax(min(100%, max(${autoColumnMinWidth}, calc(20% - 0.8rem))), 1fr))`;
  }
  return `repeat(${setting}, minmax(0, 1fr))`;
}
