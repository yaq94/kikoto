import { getJSON, patchJSONBody, postJSONBody } from "@/lib/apiTransport";
import type { RemoteTrack } from "@/lib/remoteSourceApi";
import type { PersonalReview, PersonalReviewPatch } from "@/lib/workApi";

export type CatalogEntry = PersonalReview & {
  catalogEntryId: number;
  sourceId: number;
  sourceName: string;
  primaryCode: string;
  title: string;
  workId: number | null;
};
export type CatalogTrack = RemoteTrack & { path: string; children: CatalogTrack[] };
export type CatalogScanResult = {
  status: "complete" | "partial";
  roots: Array<{ root: string; status: string; catalogEntryId?: number }>;
};

export const sourceCatalogApi = {
  getCatalogEntry: (id: number, signal?: AbortSignal) => getJSON<CatalogEntry>(`/api/source-catalog/${id}`, signal),
  getCatalogTracks: (id: number, signal?: AbortSignal) =>
    getJSON<{ tracks: CatalogTrack[] }>(`/api/source-catalog/${id}/tracks`, signal),
  updateCatalogReview: (id: number, patch: PersonalReviewPatch) =>
    patchJSONBody<PersonalReview & { workId: number }>(`/api/source-catalog/${id}/user-state`, patch),
  scanSourceCatalog: (id: number, signal?: AbortSignal) =>
    postJSONBody<CatalogScanResult>(`/api/remote-sources/${id}/catalog/scan`, {}, signal),
};
