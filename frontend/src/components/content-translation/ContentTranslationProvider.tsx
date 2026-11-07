import { createContext, useContext, useMemo, type ReactNode } from "react";

import {
  useContentLanguageCacheVersion,
  useContentLanguageScope,
} from "@/components/content-translation/ContentTranslation";
import { contentTranslationApi, isContentTranslationInFlight } from "@/lib/contentTranslationApi";
import { contentTranslationCache } from "@/lib/contentTranslationCache";
import { createTitlePrefetchQueue, type TitlePrefetchRequest } from "@/lib/contentTranslationPrefetch";

type ContentTranslationPrefetchValue = {
  warmTitles: (request: TitlePrefetchRequest) => void;
};

const PrefetchContext = createContext<ContentTranslationPrefetchValue>({ warmTitles: () => undefined });

/**
 * Owns the background title queue. One queue per app keeps page-level warmups
 * from duplicating each other, while each view namespaces its own pages.
 */
export function ContentTranslationPrefetchProvider({ children }: { children: ReactNode }) {
  const cacheVersion = useContentLanguageCacheVersion();
  const accountScope = useContentLanguageScope();
  const value = useMemo<ContentTranslationPrefetchValue>(() => {
    const queue = createTitlePrefetchQueue({
      cache: contentTranslationCache,
      translateBatch: (inputs) => contentTranslationApi.translateBatch(inputs),
      isInFlight: isContentTranslationInFlight,
    });
    return {
      // The version is read when the warm-up starts; a request that names its
      // own version keeps it.
      warmTitles: (request) => {
        if (cacheVersion) void queue.enqueue({ ...request, scope: accountScope, version: cacheVersion });
      },
    };
  }, [accountScope, cacheVersion]);
  return <PrefetchContext.Provider value={value}>{children}</PrefetchContext.Provider>;
}

export function useContentTranslationPrefetch() {
  return useContext(PrefetchContext);
}
