import {
  contentTranslationRequestKey,
  type ContentTranslationInput,
  type ContentTranslationResult,
} from "@/lib/contentTranslationApi";
import { isCacheableTranslationStatus, type ContentTranslationCache } from "@/lib/contentTranslationCache";

export const DEFAULT_PREFETCH_AHEAD_PAGES = 9;
export const DEFAULT_PREFETCH_PAGE_SIZE = 24;
export const PREFETCH_TITLE_BATCH_SIZE = 200;

export type PrefetchWindow = {
  /** Current page plus the next `ahead` pages. */
  pages: number[];
  pageSize: number;
  /** Upper bound on titles warmed by this window. */
  maxTitles: number;
};

/**
 * The page window warmed behind the visible list: the current page and the
 * following `ahead` pages, at 24 cards per page. Invalid input clamps to the
 * first page so a restored route cannot request page zero.
 */
export function buildPrefetchWindow(
  currentPage: number,
  pageSize: number = DEFAULT_PREFETCH_PAGE_SIZE,
  ahead: number = DEFAULT_PREFETCH_AHEAD_PAGES,
): PrefetchWindow {
  const page = Number.isFinite(currentPage) ? Math.max(1, Math.floor(currentPage)) : 1;
  const size = Number.isFinite(pageSize) ? Math.max(1, Math.floor(pageSize)) : DEFAULT_PREFETCH_PAGE_SIZE;
  const extra = Number.isFinite(ahead) ? Math.max(0, Math.floor(ahead)) : 0;
  const pages = Array.from({ length: extra + 1 }, (_, index) => page + index);
  return { pages, pageSize: size, maxTitles: pages.length * size };
}

export type TitlePrefetchRequest = {
  /** Identifies a list view; pages are deduplicated per namespace. */
  namespace: string;
  scope: string;
  language: string;
  /**
   * Model/template version the caller already knows. A row from another
   * version is not a warm hit; an unknown version keeps the permissive read.
   */
  version?: string;
  window: PrefetchWindow;
  /** Loads one page and maps it to title inputs; unmappable entries may be omitted. */
  loadPage: (page: number, pageSize: number) => Promise<ContentTranslationInput[]>;
};

export type TitlePrefetchDeps = {
  cache: Pick<ContentTranslationCache, "has" | "put">;
  translateBatch: (inputs: ContentTranslationInput[]) => Promise<Map<string, ContentTranslationResult>>;
  isInFlight?: (key: string) => boolean;
};

export type TitlePrefetchQueue = {
  /** Resolves with the number of titles stored; never rejects. */
  enqueue(request: TitlePrefetchRequest): Promise<number>;
  /** Pages currently being loaded or batched. */
  inFlightPages(): number;
};

/**
 * Warms titles for a page window. It never aborts a request that has already
 * been sent; a new page only adds its own window, and pages already in flight
 * are skipped. Work is chunked to the server's 200-item limit, and failed or
 * uncacheable results stay out of the cache.
 */
export function createTitlePrefetchQueue(deps: TitlePrefetchDeps): TitlePrefetchQueue {
  const inFlightPages = new Set<string>();
  const inFlightKeys = new Set<string>();

  async function run(request: TitlePrefetchRequest): Promise<number> {
    const { pages, pageSize, maxTitles } = request.window;
    const inputs: ContentTranslationInput[] = [];
    const claimedPages: string[] = [];
    const claimedKeys: string[] = [];

    try {
      for (const page of pages) {
        if (inputs.length >= maxTitles) break;
        const pageKey = `${request.namespace}|${request.language}|${page}`;
        if (inFlightPages.has(pageKey)) continue;
        inFlightPages.add(pageKey);
        claimedPages.push(pageKey);
        try {
          const pageInputs = await request.loadPage(page, pageSize);
          for (const input of pageInputs.slice(0, pageSize)) {
            if (inputs.length >= maxTitles) break;
            // Only titles are warmed in this round.
            if (input.field !== "title") continue;
            const key = input.key ?? contentTranslationRequestKey(input);
            if (inFlightKeys.has(key)) continue;
            if (deps.isInFlight?.(key)) continue;
            if (await deps.cache.has(request.scope, input, request.version)) continue;
            inFlightKeys.add(key);
            claimedKeys.push(key);
            inputs.push(input);
          }
        } catch {
          // A failed page load must not stop the remaining pages.
        }
      }
      if (inputs.length === 0) return 0;
      let translated = 0;
      try {
        for (let offset = 0; offset < inputs.length; offset += PREFETCH_TITLE_BATCH_SIZE) {
          const chunk = inputs.slice(offset, offset + PREFETCH_TITLE_BATCH_SIZE);
          const results = await deps.translateBatch(chunk);
          for (const input of chunk) {
            const result = results.get(input.key ?? contentTranslationRequestKey(input));
            if (!result || !isCacheableTranslationStatus(result.status)) continue;
            if (await deps.cache.put(request.scope, input, result)) translated += 1;
          }
        }
      } catch {
        // Prefetch is best effort and never surfaces an error to the page.
      }
      return translated;
    } finally {
      for (const key of claimedKeys) inFlightKeys.delete(key);
      for (const pageKey of claimedPages) inFlightPages.delete(pageKey);
    }
  }

  return {
    enqueue(request) {
      return run(request);
    },
    inFlightPages() {
      return inFlightPages.size;
    },
  };
}

/** A queue with no storage or network side effects, replaced by the app provider. */
export const noopTitlePrefetchQueue: TitlePrefetchQueue = {
  enqueue: async () => 0,
  inFlightPages: () => 0,
};
