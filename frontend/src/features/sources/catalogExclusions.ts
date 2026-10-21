import { deleteJSON, getJSON, postJSONBody } from "@/lib/apiTransport";

export type CatalogExclusion = {
  id: number;
  path: string;
  mode: string;
  action: string;
  note: string;
  enabled: boolean;
  createdAt: string;
};

export type CatalogExclusionInput = {
  path: string;
  mode?: string;
  note?: string;
};

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
  return value
    .trim()
    .replace(/\/+/g, "/")
    .replace(/\/+$/, "");
}

export const catalogExclusionApi = {
  list: (sourceId: number, signal?: AbortSignal) =>
    getJSON<CatalogExclusion[]>(`/api/remote-sources/${sourceId}/catalog/exclusions`, signal),
  create: (sourceId: number, input: CatalogExclusionInput) =>
    postJSONBody<CatalogExclusion>(`/api/remote-sources/${sourceId}/catalog/exclusions`, input),
  remove: (sourceId: number, exclusionId: number) =>
    deleteJSON<{ ok: boolean; path: string }>(
      `/api/remote-sources/${sourceId}/catalog/exclusions/${exclusionId}`,
    ),
};
