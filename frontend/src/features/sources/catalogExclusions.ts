import { deleteJSON, getJSON, postJSONBody } from "@/lib/apiTransport";

export type CatalogExclusion = {
  id: number;
  path: string;
  /** `whole` or `whitelist`; read `catalogExclusionMode` for the normalised value. */
  mode: string;
  action: string;
  note: string;
  enabled: boolean;
  createdAt: string;
};

/**
 * `whole` removes the path and everything below it. `whitelist` is a subtree
 * exception: it keeps a path indexed even when an ancestor is excluded.
 */
export type CatalogExclusionMode = "whole" | "whitelist";

export const CATALOG_EXCLUSION_MODES: readonly CatalogExclusionMode[] = ["whole", "whitelist"];

export type CatalogExclusionInput = {
  path: string;
  mode?: CatalogExclusionMode;
  note?: string;
  /** The server creates rules enabled; the field is part of the wire contract. */
  enabled?: boolean;
};

/** Unknown or empty values from older rows fall back to the default rule. */
export function catalogExclusionMode(value: string | undefined): CatalogExclusionMode {
  return value === "whitelist" ? "whitelist" : "whole";
}

export type CatalogExclusionPathProblem = "empty" | "relative" | "parent" | "root" | null;

/** The server repeats this validation; the form needs the same answer without a round trip. */
export function catalogExclusionPathProblem(value: string): CatalogExclusionPathProblem {
  const trimmed = value.trim();
  if (!trimmed) return "empty";
  if (!trimmed.startsWith("/")) return "relative";
  if (trimmed.split("/").includes("..")) return "parent";
  if (normalizeCatalogExclusionPath(trimmed) === "") return "root";
  return null;
}

export function normalizeCatalogExclusionPath(value: string): string {
  return value.trim().replace(/\/+/g, "/").replace(/\/+$/, "");
}

export const catalogExclusionApi = {
  list: (sourceId: number, signal?: AbortSignal) =>
    getJSON<CatalogExclusion[]>(`/api/remote-sources/${sourceId}/catalog/exclusions`, signal),
  create: (sourceId: number, input: CatalogExclusionInput) =>
    postJSONBody<CatalogExclusion>(`/api/remote-sources/${sourceId}/catalog/exclusions`, input),
  remove: (sourceId: number, exclusionId: number) =>
    deleteJSON<{ ok: boolean; path: string }>(`/api/remote-sources/${sourceId}/catalog/exclusions/${exclusionId}`),
};
