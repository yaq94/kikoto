import { deleteJSON, getJSON, postJSONBody, putJSONBody } from "@/lib/apiTransport";

export type LLMModel = {
  id: string;
  name?: string;
  ownedBy?: string;
  contextSize?: number;
};

export type LLMProviderPreset = {
  id: string;
  name: string;
  kind: "openai" | "anthropic" | "ollama";
  defaultUrl: string;
  modelPath: string;
  boundary: "external" | "private";
  ollamaScope?: "local_machine";
};

export type LLMProvider = {
  id: string;
  presetId: string;
  name: string;
  kind: "openai" | "anthropic" | "ollama";
  baseUrl: string;
  boundary: "external" | "private";
  ollamaScope?: "local_machine" | "other_machine";
  enabled: boolean;
  hasApiKey: boolean;
  defaultModel?: string;
  models: LLMModel[];
};

export type LLMProviderPayload = {
  id?: string;
  presetId: string;
  name: string;
  kind: LLMProvider["kind"];
  baseUrl?: string;
  apiKey?: string;
  boundary?: LLMProvider["boundary"];
  ollamaScope?: LLMProvider["ollamaScope"];
  enabled?: boolean;
  defaultModel?: string;
};

export type LLMProviderList = {
  providers: LLMProvider[];
  presets: LLMProviderPreset[];
  policy: LLMPolicy;
};

export type LLMKeywordRule = { term: string; fields: string[]; action: "sensitive" | "blocked" };
export type LLMPolicy = { alwaysScanAllAges: boolean; tagIds: number[]; keywords: LLMKeywordRule[] };
export type LLMAssessment = {
  class: "safe" | "sensitive" | "unknown" | "blocked";
  reason: string;
  matches: Array<{ kind: string; rule: string; field?: string }>;
};
export type LLMTranslation = {
  previewId: string;
  providerId: string;
  providerName: string;
  model: string;
  targetLanguage: string;
  assessment: LLMAssessment;
  source: { title?: string; description?: string };
  translation: { title?: string; description?: string };
};

export const llmApi = {
  listTranslationModels: () =>
    getJSON<Array<Pick<LLMProvider, "id" | "name" | "defaultModel" | "boundary">>>("/api/llm/translation-models"),
  listProviders: () => getJSON<LLMProviderList>("/api/llm/providers"),
  createProvider: (payload: LLMProviderPayload) => postJSONBody<LLMProvider>("/api/llm/providers", payload),
  updateProvider: (id: string, payload: Partial<LLMProviderPayload>) =>
    putJSONBody<LLMProvider>(`/api/llm/providers/${encodeURIComponent(id)}`, payload),
  deleteProvider: (id: string) => deleteJSON<{ ok: boolean }>(`/api/llm/providers/${encodeURIComponent(id)}`),
  fetchModels: (id: string) =>
    postJSONBody<LLMProvider>(`/api/llm/providers/${encodeURIComponent(id)}/fetch-models`, {}),
  updatePolicy: (policy: LLMPolicy) => putJSONBody<LLMPolicy>("/api/llm/policy", policy),
  translateWork: (
    id: number,
    payload: { providerId: string; targetLanguage: string; fields: string[] },
    signal?: AbortSignal,
  ) => postJSONBody<LLMTranslation>(`/api/works/${id}/llm-translate`, payload, signal),
  applyTranslation: (id: number, previewId: string) =>
    putJSONBody<{ ok: boolean }>(`/api/works/${id}/llm-translation`, { previewId }),
};
