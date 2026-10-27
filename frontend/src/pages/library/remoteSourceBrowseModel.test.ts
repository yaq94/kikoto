import { describe, expect, it } from "vitest";

import type { RemoteWorksResponse } from "@/lib/api";

import {
  remoteSourceBrowseModel,
  remoteSourcePanelModel,
  remoteWorkActionCode,
  remoteWorkRowKey,
} from "./remoteSourceBrowseModel";

function work(overrides: Partial<RemoteWorksResponse["works"][number]>) {
  return {
    remoteId: "1",
    primaryCode: "RJ00000001",
    remoteCode: "RJ00000001",
    title: "Example",
    releaseDate: "",
    updatedAt: "",
    coverUrl: "",
    circle: "",
    ageRating: "",
    rating: null,
    sales: null,
    price: null,
    tags: [],
    voiceActors: [],
    voiceRefs: [],
    importStatus: "remote",
    remotePlayable: true,
    workId: null,
    favorite: false,
    listeningStatus: "none" as const,
    recommendScore: 0,
    ...overrides,
  };
}

const viewState = { page: 1, pageSize: 24 };
const onPageChange = () => undefined;

function response(overrides: Partial<RemoteWorksResponse>): RemoteWorksResponse {
  return {
    sourceId: 1,
    works: [],
    page: 1,
    pageSize: 24,
    total: 0,
    status: "ok",
    sort: "recent",
    direction: "desc",
    sortApplied: true,
    ...overrides,
  };
}

describe("remote source browse model", () => {
  it("reports a bounded short search as truncated", () => {
    const model = remoteSourceBrowseModel({
      result: response({ total: 1000, truncated: true }),
      viewState,
      onPageChange,
    });
    expect(model.truncated).toBe(true);
    expect(model.totalItems).toBe(1000);
  });

  it("keeps an exact total when the response is not truncated", () => {
    const model = remoteSourceBrowseModel({
      result: response({ total: 12 }),
      viewState,
      onPageChange,
    });
    expect(model.truncated).toBe(false);
    expect(model.totalPages).toBe(1);
  });

  it("selects only numbered remote codes", () => {
    const model = remoteSourceBrowseModel({
      result: response({
        works: [
          {
            remoteId: "1",
            primaryCode: "RJ00000001",
            remoteCode: "RJ00000001",
            title: "Example",
            releaseDate: "",
            updatedAt: "",
            coverUrl: "",
            circle: "",
            ageRating: "",
            rating: null,
            sales: null,
            price: null,
            tags: [],
            voiceActors: [],
            voiceRefs: [],
            importStatus: "remote",
            remotePlayable: true,
            workId: null,
            favorite: false,
            listeningStatus: "none",
            recommendScore: 0,
          },
          {
            remoteId: "2",
            primaryCode: "",
            remoteCode: "",
            title: "Numberless",
            releaseDate: "",
            updatedAt: "",
            coverUrl: "",
            circle: "",
            ageRating: "",
            rating: null,
            sales: null,
            price: null,
            tags: [],
            voiceActors: [],
            voiceRefs: [],
            importStatus: "remote",
            remotePlayable: true,
            workId: null,
            favorite: false,
            listeningStatus: "none",
            recommendScore: 0,
          },
        ],
      }),
      viewState,
      onPageChange,
    });
    expect(model.selectableWorks.map((work) => work.primaryCode)).toEqual(["RJ00000001"]);
  });
});

describe("remote row identity", () => {
  it("keeps two numberless catalog cards apart", () => {
    const left = work({ remoteId: "1913", primaryCode: "", remoteCode: "", catalogEntryId: 1913 });
    const right = work({ remoteId: "2324", primaryCode: "", remoteCode: "", catalogEntryId: 2324 });

    expect(remoteWorkRowKey(left)).not.toBe(remoteWorkRowKey(right));
    expect(remoteWorkActionCode(left)).toBe("1913");
  });

  it("keeps catalog rows out of a bulk action that has nothing to act on", () => {
    const entry = work({ remoteId: "1913", primaryCode: "", remoteCode: "", catalogEntryId: 1913 });
    const browse = remoteSourceBrowseModel({
      result: response({ works: [entry], total: 1 }),
      viewState,
      onPageChange,
    });
    const model = remoteSourcePanelModel({ browse, bulkCodes: new Set([remoteWorkRowKey(entry)]) });

    expect(model.selectedWorks).toEqual([]);
  });
});
