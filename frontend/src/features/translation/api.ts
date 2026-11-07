import { apiTransport } from "@/lib/apiTransport";
import { CONTENT_TRANSLATION_CONFIG_CHANGED } from "@/lib/contentTranslationEvents";

export type TranslationSettings = {
  baseUrl: string;
  model: string;
  hasApiKey: boolean;
  apiKeyMask: string;
  timeoutSeconds: number;
  disableThinking: boolean;
  concurrency: number;
  queueLimit: number;
  maxEntries: number;
  cacheBytes: number;
  minFreeBytes: number;
};

export type TranslationSettingsPatch = Partial<
  Pick<
    TranslationSettings,
    | "baseUrl"
    | "model"
    | "timeoutSeconds"
    | "disableThinking"
    | "concurrency"
    | "queueLimit"
    | "maxEntries"
    | "cacheBytes"
    | "minFreeBytes"
  >
> & { apiKey?: string };
export type TranslationResult = { text: string; model: string; templateVersion: string; elapsedMs: number };
export type TranslationTarget = "en" | "zh-Hans" | "ja" | "ko";

const path = "/api/settings/translation";
export const translationApi = {
  settings: (signal?: AbortSignal) => apiTransport.getJSON<TranslationSettings>(path, signal),
  update: async (patch: TranslationSettingsPatch, signal?: AbortSignal) => {
    const result = await apiTransport.sendJSONBody<TranslationSettings>("PATCH", path, patch, { signal });
    window.dispatchEvent(new Event(CONTENT_TRANSLATION_CONFIG_CHANGED));
    return result;
  },
  test: (text: string, targetLanguage: TranslationTarget, signal?: AbortSignal) =>
    apiTransport.sendJSONBody<TranslationResult>("POST", `${path}/test`, { text, targetLanguage }, { signal }),
};
