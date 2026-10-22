import { apiTransport } from "@/lib/apiTransport";

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
  update: (patch: TranslationSettingsPatch, signal?: AbortSignal) =>
    apiTransport.sendJSONBody<TranslationSettings>("PATCH", path, patch, { signal }),
  test: (text: string, targetLanguage: TranslationTarget, signal?: AbortSignal) =>
    apiTransport.sendJSONBody<TranslationResult>("POST", `${path}/test`, { text, targetLanguage }, { signal }),
};
