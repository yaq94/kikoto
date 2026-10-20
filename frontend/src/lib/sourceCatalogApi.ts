import { getJSON, patchJSONBody, postJSONBody } from "@/lib/apiTransport";
import type { RemoteTrack } from "@/lib/remoteSourceApi";
import type { PersonalReview, PersonalReviewPatch } from "@/lib/workApi";

export type CatalogStatus =
  "available" | "unavailable" | "maintenance" | "restricted" | "unconfirmed_empty" | "missing" | "disabled";
export type AssociationSubject = { kind: "work" | "catalog" | "series"; id: number };
export type WorkMatchResult = {
  status: "queued" | "no_match" | "review" | "paused" | "failed" | "pending" | "completed" | "existing";
  rule: string;
  candidateCount: number;
  operationId?: number;
  candidates: Array<{
    subject: AssociationSubject;
    title: string;
    rule: string;
    reasons: string[];
    confidence: string;
  }>;
};
export type AssociationReview = { rating: number | null; note: string };
export type SplitAllocation = "left" | "right" | "both";
export type SharedSplit = {
  id: number;
  associationId: number;
  status: "preview" | "pending" | "completed" | "stale";
  version: string;
  left: AssociationObject;
  right: AssociationObject;
  myBeforeLeft: AssociationReview;
  myBeforeRight: AssociationReview;
  myCurrentReview: AssociationReview;
  myRecordChange: "unchanged" | "added" | "modified";
  myAllocation: SplitAllocation | "";
};
export type AssociationObject = {
  subject: AssociationSubject;
  title: string;
  level: "work" | "episode" | "series";
  workId: number | null;
  sources: Array<{ catalogEntryId: number; sourceName: string; title: string; workId: number | null }>;
};
export type SharedAssociation = {
  id: number;
  status: "preview" | "pending" | "completed" | "stale" | "undone";
  version: string;
  left: AssociationObject;
  right: AssociationObject;
  myConflictFields: Array<"rating" | "note">;
  myLeftReview: AssociationReview;
  myRightReview: AssociationReview;
  myDecision: AssociationReview | null;
  waitingForOwners: boolean;
};

export type CatalogEntry = PersonalReview & {
  catalogEntryId: number;
  sourceId: number;
  sourceName: string;
  primaryCode: string;
  title: string;
  workId: number | null;
  seriesId?: number | null;
  status?: CatalogStatus;
  relocationCandidates?: Array<{ catalogEntryId: number; title: string; reason: string }>;
};
export type CatalogSeriesEpisode = Omit<CatalogEntry, keyof PersonalReview> & { position: number };
export type CatalogSeries = PersonalReview & {
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
  directoryType: "catalog" | "remote";
  status: CatalogStatus;
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
  /** Cards that left the automatic credit-file queue, split by stored category. */
  sidecarFailures: {
    stopped: number;
    temporarilyUnavailable: number;
    unreadable: number;
    requestBudget: number;
  };
};

export const sourceCatalogApi = {
  getWorkMatching: (subject: AssociationSubject, signal?: AbortSignal) =>
    getJSON<WorkMatchResult>(`/api/work-matching?kind=${subject.kind}&id=${subject.id}`, signal),
  runWorkMatching: (subject: AssociationSubject, signal?: AbortSignal) =>
    postJSONBody<WorkMatchResult>("/api/work-matching", subject, signal),
  previewSharedSplit: (associationId: number) =>
    postJSONBody<SharedSplit>(`/api/shared-associations/${associationId}/split-preview`, {}),
  listSharedSplits: (associationId: number, signal?: AbortSignal) =>
    getJSON<Array<Pick<SharedSplit, "id" | "status">>>(`/api/shared-associations/${associationId}/splits`, signal),
  getSharedSplit: (id: number, signal?: AbortSignal) => getJSON<SharedSplit>(`/api/shared-splits/${id}`, signal),
  startSharedSplit: (id: number, version: string) =>
    postJSONBody<SharedSplit>(`/api/shared-splits/${id}/start`, { version }),
  decideSharedSplit: (id: number, version: string, allocation: SplitAllocation) =>
    postJSONBody<SharedSplit>(`/api/shared-splits/${id}/decision`, { version, allocation }),
  previewSharedAssociation: (left: AssociationSubject, right: AssociationSubject) =>
    postJSONBody<SharedAssociation>("/api/shared-associations/previews", { left, right }),
  getSharedAssociation: (id: number, signal?: AbortSignal) =>
    getJSON<SharedAssociation>(`/api/shared-associations/${id}`, signal),
  listSharedAssociations: (subject: AssociationSubject, signal?: AbortSignal) =>
    getJSON<Array<Pick<SharedAssociation, "id" | "status">>>(
      `/api/shared-associations?kind=${subject.kind}&id=${subject.id}`,
      signal,
    ),
  startSharedAssociation: (id: number, version: string) =>
    postJSONBody<SharedAssociation>(`/api/shared-associations/${id}/start`, { version }),
  decideSharedAssociation: (id: number, version: string, review: AssociationReview) =>
    postJSONBody<SharedAssociation>(`/api/shared-associations/${id}/decision`, { version, ...review }),
  updateCatalogSeriesReview: (id: number, patch: PersonalReviewPatch) =>
    patchJSONBody<PersonalReview & { workId: number }>(`/api/catalog-series/${id}/user-state`, patch),
  getCatalogSeries: (id: number, signal?: AbortSignal) => getJSON<CatalogSeries>(`/api/catalog-series/${id}`, signal),
  previewCatalogGrouping: (title: string, catalogEntryIds: number[], signal?: AbortSignal) =>
    postJSONBody<CatalogGroupingPreview>(`/api/catalog-series/previews`, { title, catalogEntryIds }, signal),
  getCatalogGroupingPreview: (id: number, signal?: AbortSignal) =>
    getJSON<CatalogGroupingPreview>(`/api/catalog-series/previews/${id}`, signal),
  confirmCatalogGrouping: (id: number, signal?: AbortSignal) =>
    postJSONBody<CatalogGroupingPreview>(`/api/catalog-series/previews/${id}/confirm`, {}, signal),
  getCatalogIndex: (id: number, page = 1, signal?: AbortSignal) =>
    getJSON<CatalogIndexProgress>(`/api/remote-sources/${id}/catalog/index?page=${page}`, signal),
  startCatalogIndex: (id: number, options: Partial<CatalogIndexOptions>) =>
    postJSONBody<{ runId: number; existing: boolean }>(`/api/remote-sources/${id}/catalog/index`, options),
  retryCatalogSidecars: (id: number, signal?: AbortSignal) =>
    postJSONBody<{ sourceId: number; reopened: number }>(
      `/api/remote-sources/${id}/catalog/sidecars/retry`,
      {},
      signal,
    ),
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
