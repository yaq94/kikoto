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

export type CatalogIndexOptions = {
  concurrency: number;
  intervalMs: number;
  maxResponseBytes: number;
  pageSize: number;
  pagesPerBatch: number;
  maxPagesPerDirectory: number;
  minFreeBytes: number;
};
export type CatalogIndexProgress = {
  runId: number;
  status: string;
  pauseReason: string;
  options: CatalogIndexOptions;
  requests: number;
  directories: number;
  completedDirectories: number;
  pendingDirectories: number;
  failedDirectories: number;
  restrictedDirectories: number;
  unconfirmedEmptyDirectories: number;
  observedEntries: number;
  indexedEntries: number;
  startedAt: string;
  updatedAt: string;
  partitions: Array<{
    path: string;
    directories: number;
    completed: number;
    failed: number;
    restricted: number;
    unconfirmedEmpty: number;
  }>;
  partitionTotal: number;
  details: Array<{ path: string; status: string; nextPage: number; errorCode: string }>;
  detailsTotal: number;
};

export const sourceCatalogApi = {
  getCatalogIndex: (id: number, page = 1, signal?: AbortSignal) =>
    getJSON<CatalogIndexProgress>(`/api/remote-sources/${id}/catalog/index?page=${page}`, signal),
  startCatalogIndex: (id: number, options: Partial<CatalogIndexOptions>) =>
    postJSONBody<{ runId: number; existing: boolean }>(`/api/remote-sources/${id}/catalog/index`, options),
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
