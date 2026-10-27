import type { RemoteWork, RemoteWorksResponse } from "@/lib/api";

import type { RemoteSourceViewState } from "./libraryBrowseModel";

const emptyRemoteWorks: RemoteWork[] = [];

type RemoteSourceBrowseModel = {
  visibleWorks: RemoteWork[];
  selectableWorks: RemoteWork[];
  totalItems: number;
  totalPages: number;
  currentPage: number;
  // True when a 1-2 character translated search stopped at the result budget,
  // so total is the number inside the budget rather than the whole set.
  truncated: boolean;
  remotePaginationProps: {
    page: number;
    pageSize: number;
    totalItems: number;
    totalPages: number;
    onPageChange: (page: number) => void;
  };
  remoteError: NonNullable<RemoteWorksResponse["error"]> | null;
};

type RemoteSourcePanelModel = RemoteSourceBrowseModel & {
  selectedWorks: RemoteWork[];
  selectedSyncable: RemoteWork[];
  selectedSaveable: RemoteWork[];
};

type RemoteSourceBrowseInput = {
  result: RemoteWorksResponse | null;
  viewState: RemoteSourceViewState;
  onPageChange: (page: number) => void;
};

export function remoteSourceBrowseModel({
  result,
  viewState,
  onPageChange,
}: RemoteSourceBrowseInput): RemoteSourceBrowseModel {
  const { page, pageSize } = viewState;
  const visibleWorks = result?.works ?? emptyRemoteWorks;
  const selectableWorks = visibleWorks.filter((work) => work.primaryCode && !work.catalogEntryId);
  const totalItems = result?.total ?? 0;
  const totalPages = Math.max(1, Math.ceil(totalItems / pageSize));
  const currentPage = Math.min(page, totalPages);
  return {
    visibleWorks,
    selectableWorks,
    totalItems,
    totalPages,
    currentPage,
    truncated: Boolean(result?.truncated),
    remotePaginationProps: { page: currentPage, pageSize, totalItems, totalPages, onPageChange },
    remoteError:
      result?.error ??
      (result?.status === "disabled"
        ? { code: "disabled", message: "", retryable: false }
        : result?.status === "unavailable"
          ? { code: "unavailable", message: "", retryable: true }
          : null),
  };
}

export function remoteSourcePanelModel({
  browse,
  bulkCodes,
}: {
  browse: RemoteSourceBrowseModel;
  bulkCodes: Set<string>;
}): RemoteSourcePanelModel {
  const selectedWorks = browse.selectableWorks.filter((work) => bulkCodes.has(remoteWorkRowKey(work)));
  return {
    ...browse,
    selectedWorks,
    selectedSyncable: selectedWorks.filter((work) => work.workId === null),
    selectedSaveable: selectedWorks,
  };
}

/**
 * Identifies one card in the page. A numberless catalog entry has no printed
 * code, so the code alone would make every such card the same row and let one
 * card's work id overwrite another's.
 */
export function remoteWorkRowKey(work: RemoteWork) {
  if (work.catalogEntryId) return `catalog:${work.catalogEntryId}`;
  if (work.seriesId) return `series:${work.seriesId}`;
  if (work.primaryCode) return `code:${work.primaryCode.toUpperCase()}`;
  return `remote:${work.remoteId}`;
}

export function remoteWorkActionCode(work: RemoteWork) {
  return work.remoteCode || work.primaryCode || work.remoteId;
}
