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
  seriesId?: number | null;
};
export type CatalogSeriesEpisode = Omit<CatalogEntry, keyof PersonalReview> & { position: number };
export type CatalogSeries = {
  seriesId: number;
  title: string;
  workId: number | null;
  episodes: CatalogSeriesEpisode[];
};
export type CatalogGroupingPreview = {
  previewId: number;
  title: string;
  status: "pending" | "completed";
  seriesId: number | null;
  episodes: CatalogSeriesEpisode[];
};
export type CatalogTrack = RemoteTrack & { path: string; children: CatalogTrack[] };
export type CatalogDirectory = Omit<CatalogEntry, keyof PersonalReview> & {
  directoryType: "catalog";
  status: "available" | "disabled";
  checkedAt: string;
};
export type CatalogPlaybackTarget = {
  path: string;
  workId: number;
  mediaItemId: number;
  locationId: number;
  streamUrl: string;
};
export type CatalogScanResult = {
  status: "complete" | "partial";
  roots: Array<{ root: string; status: string; catalogEntryId?: number }>;
};

export const sourceCatalogApi = {
  getCatalogSeries: (id: number, signal?: AbortSignal) => getJSON<CatalogSeries>(`/api/catalog-series/${id}`, signal),
  previewCatalogGrouping: (title: string, catalogEntryIds: number[], signal?: AbortSignal) =>
    postJSONBody<CatalogGroupingPreview>(`/api/catalog-series/previews`, { title, catalogEntryIds }, signal),
  getCatalogGroupingPreview: (id: number, signal?: AbortSignal) =>
    getJSON<CatalogGroupingPreview>(`/api/catalog-series/previews/${id}`, signal),
  confirmCatalogGrouping: (id: number, signal?: AbortSignal) =>
    postJSONBody<CatalogGroupingPreview>(`/api/catalog-series/previews/${id}/confirm`, {}, signal),
  getWorkCatalogDirectories: (id: number, signal?: AbortSignal) =>
    getJSON<{ directories: CatalogDirectory[] }>(`/api/works/${id}/catalog-directories`, signal),
  prepareCatalogPlayback: (id: number, paths: string[], signal?: AbortSignal) =>
    postJSONBody<{ targets: CatalogPlaybackTarget[] }>(`/api/source-catalog/${id}/playback`, { paths }, signal),
  refreshCatalogDirectory: (id: number, signal?: AbortSignal) =>
    postJSONBody<{ status: string }>(`/api/source-catalog/${id}/refresh`, {}, signal),
  getCatalogEntry: (id: number, signal?: AbortSignal) => getJSON<CatalogEntry>(`/api/source-catalog/${id}`, signal),
  getCatalogTracks: (id: number, signal?: AbortSignal) =>
    getJSON<{ tracks: CatalogTrack[] }>(`/api/source-catalog/${id}/tracks`, signal),
  updateCatalogReview: (id: number, patch: PersonalReviewPatch) =>
    patchJSONBody<PersonalReview & { workId: number }>(`/api/source-catalog/${id}/user-state`, patch),
  scanSourceCatalog: (id: number, signal?: AbortSignal) =>
    postJSONBody<CatalogScanResult>(`/api/remote-sources/${id}/catalog/scan`, {}, signal),
};
