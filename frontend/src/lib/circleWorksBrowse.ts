import type { LibrarySort, SortDirection } from "@/lib/libraryApi";

/** The orders a circle catalog offers: the Library orders that need no seed or account history. */
export const circleWorkSortValues = [
  "release",
  "code",
  "title",
  "rating",
  "sales",
] as const satisfies readonly LibrarySort[];
export type CircleWorkSort = (typeof circleWorkSortValues)[number];

export const circleWorkPageSizeOptions = [24, 48, 96] as const;
export type CircleWorkPageSize = (typeof circleWorkPageSizeOptions)[number];

export type CircleWorksBrowse = {
  sort: CircleWorkSort;
  direction: SortDirection;
  pageSize: CircleWorkPageSize;
};

export const defaultCircleWorksBrowse: CircleWorksBrowse = { sort: "release", direction: "desc", pageSize: 24 };

const storagePrefix = "kikoto:circle-works-browse:";

/** Keeps each valid stored field and defaults the rest. */
export function circleWorksBrowseFromValue(value: unknown): CircleWorksBrowse {
  const stored = (value && typeof value === "object" ? value : {}) as Partial<Record<keyof CircleWorksBrowse, unknown>>;
  return {
    sort: circleWorkSortValues.find((option) => option === stored.sort) ?? defaultCircleWorksBrowse.sort,
    direction: stored.direction === "asc" || stored.direction === "desc" ? stored.direction : "desc",
    pageSize:
      circleWorkPageSizeOptions.find((option) => option === stored.pageSize) ?? defaultCircleWorksBrowse.pageSize,
  };
}

/** The order and page size chosen for circle catalogs, kept per server and account on this device. */
export function readCircleWorksBrowse(storageScope: string): CircleWorksBrowse {
  try {
    return circleWorksBrowseFromValue(JSON.parse(window.localStorage.getItem(storagePrefix + storageScope) ?? "{}"));
  } catch {
    return defaultCircleWorksBrowse;
  }
}

export function writeCircleWorksBrowse(storageScope: string, browse: CircleWorksBrowse) {
  try {
    window.localStorage.setItem(storagePrefix + storageScope, JSON.stringify(browse));
  } catch {
    // The choice still applies to this visit when storage is unavailable.
  }
}
