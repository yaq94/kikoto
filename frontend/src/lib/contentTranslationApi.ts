import { apiTransport } from "@/lib/apiTransport";

export type ContentTranslationResource = {
  workId?: number;
  sourceId?: number;
  code?: string;
  catalogEntryId?: number;
  seriesId?: number;
};
export type ContentTranslationRequest = ContentTranslationResource & {
  field: "title" | "description" | "tag" | "directory";
  text: string;
  language: string;
  mediaId?: number;
  path?: string;
};
export type ContentTranslationResult = {
  text: string;
  original: string;
  language: string;
  status: "original" | "existing" | "translated" | "failed";
  code?: string;
  cacheStatus?: string;
};
export const contentTranslationApi = {
  translate: (input: ContentTranslationRequest, signal: AbortSignal) =>
    apiTransport.sendJSONBody<ContentTranslationResult>("POST", "/api/content-translations", input, { signal }),
};
