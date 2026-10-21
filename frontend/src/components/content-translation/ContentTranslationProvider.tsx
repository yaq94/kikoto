import { createContext, useContext, useMemo, type ReactNode } from "react";

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
  const value = useMemo<ContentTranslationPrefetchValue>(() => {
    const queue = createTitlePrefetchQueue({
      cache: contentTranslationCache,
      translateBatch: (inputs) => contentTranslationApi.translateBatch(inputs),
      isInFlight: isContentTranslationInFlight,
    });
    return {
      warmTitles: (request) => {
        void queue.enqueue(request);
      },
    };
  }, []);
  return <PrefetchContext.Provider value={value}>{children}</PrefetchContext.Provider>;
}

export function useContentTranslationPrefetch() {
  return useContext(PrefetchContext);
}
