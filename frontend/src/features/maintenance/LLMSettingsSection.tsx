import { Loader2, Plus, RefreshCw, Save, Trash2 } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { SettingsRow, SettingsSection } from "@/components/settings/SettingsSection";
import { toastFromError, useToast } from "@/components/ui/toast";
import { LLMPolicyEditor } from "./LLMPolicyEditor";
import { api, type LLMPolicy, type LLMProvider, type LLMProviderPreset } from "@/lib/api";

type ProviderDraft = LLMProvider & { apiKey: string; clearKey: boolean };

function draftFromProvider(provider: LLMProvider): ProviderDraft {
  return { ...provider, apiKey: "", clearKey: false, models: [...provider.models] };
}

export function LLMSettingsSection({ readOnly = false }: { readOnly?: boolean }) {
  const { t } = useTranslation();
  const toast = useToast();
  const [providers, setProviders] = useState<LLMProvider[]>([]);
  const [presets, setPresets] = useState<LLMProviderPreset[]>([]);
  const [policy, setPolicy] = useState<LLMPolicy>({ alwaysScanAllAges: false, tagIds: [], keywords: [] });
  const [savingPolicy, setSavingPolicy] = useState(false);
  const [selectedPreset, setSelectedPreset] = useState("openai");
  const [newName, setNewName] = useState("");
  const [newBaseUrl, setNewBaseUrl] = useState("");
  const [newAPIKey, setNewAPIKey] = useState("");
  const [newOllamaScope, setNewOllamaScope] = useState<"local_machine" | "other_machine">("local_machine");
  const [drafts, setDrafts] = useState<Record<string, ProviderDraft>>({});
  const [loading, setLoading] = useState(true);
  const [savingID, setSavingID] = useState<string | null>(null);
  const [fetchingID, setFetchingID] = useState<string | null>(null);
  const [deletingID, setDeletingID] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const result = await api.listProviders();
      setProviders(result.providers);
      setPresets(result.presets);
      setPolicy(result.policy);
      setDrafts(Object.fromEntries(result.providers.map((provider) => [provider.id, draftFromProvider(provider)])));
    } catch (error) {
      toast.notify(toastFromError(error, t("maintenance.llm.loadFailed")));
    } finally {
      setLoading(false);
    }
  }, [t, toast]);

  useEffect(() => {
    if (!readOnly) void load();
  }, [load, readOnly]);

  const selectedPresetData = useMemo(
    () => presets.find((preset) => preset.id === selectedPreset) ?? null,
    [presets, selectedPreset],
  );

  const addProvider = async () => {
    if (!selectedPresetData || readOnly) return;
    setAdding(true);
    try {
      await api.createProvider({
        presetId: selectedPresetData.id,
        name: newName.trim() || selectedPresetData.name,
        kind: selectedPresetData.kind,
        baseUrl: selectedPresetData.id === "ollama" && newOllamaScope === "local_machine" ? undefined : newBaseUrl,
        apiKey: newAPIKey,
        boundary: selectedPresetData.boundary,
        ollamaScope: selectedPresetData.id === "ollama" ? newOllamaScope : undefined,
        enabled: true,
      });
      setNewName("");
      setNewBaseUrl("");
      setNewAPIKey("");
      await load();
      toast.success(t("maintenance.llm.added"));
    } catch (error) {
      toast.notify(toastFromError(error, t("maintenance.llm.saveFailed")));
    } finally {
      setAdding(false);
    }
  };

  const saveProvider = async (draft: ProviderDraft) => {
    setSavingID(draft.id);
    try {
      const payload: Record<string, unknown> = {
        name: draft.name.trim(),
        enabled: draft.enabled,
        defaultModel: draft.defaultModel ?? "",
        ollamaScope: draft.ollamaScope,
        boundary: draft.boundary,
        allowSensitive: draft.allowSensitive,
      };
      if (!(draft.kind === "ollama" && draft.ollamaScope === "local_machine")) payload.baseUrl = draft.baseUrl;
      if (draft.clearKey || draft.apiKey) payload.apiKey = draft.clearKey ? "" : draft.apiKey;
      const next = await api.updateProvider(draft.id, payload);
      setProviders((current) => current.map((provider) => (provider.id === next.id ? next : provider)));
      setDrafts((current) => ({ ...current, [next.id]: draftFromProvider(next) }));
      toast.success(t("maintenance.llm.saved"));
    } catch (error) {
      toast.notify(toastFromError(error, t("maintenance.llm.saveFailed")));
    } finally {
      setSavingID(null);
    }
  };

  const fetchModels = async (draft: ProviderDraft) => {
    setFetchingID(draft.id);
    try {
      const payload = {
        name: draft.name,
        enabled: draft.enabled,
        defaultModel: draft.defaultModel ?? "",
        boundary: draft.boundary,
        allowSensitive: draft.allowSensitive,
        ollamaScope: draft.ollamaScope,
        ...(draft.kind === "ollama" && draft.ollamaScope === "local_machine" ? {} : { baseUrl: draft.baseUrl }),
        ...(draft.clearKey || draft.apiKey ? { apiKey: draft.clearKey ? "" : draft.apiKey } : {}),
      };
      const saved = await api.updateProvider(draft.id, payload);
      setProviders((current) => current.map((provider) => (provider.id === saved.id ? saved : provider)));
      setDrafts((current) => ({ ...current, [saved.id]: draftFromProvider(saved) }));
      const next = await api.fetchModels(draft.id);
      setProviders((current) => current.map((provider) => (provider.id === next.id ? next : provider)));
      setDrafts((current) => ({ ...current, [next.id]: draftFromProvider(next) }));
      toast.success(t("maintenance.llm.modelsFetched", { count: next.models.length }));
    } catch (error) {
      toast.notify(toastFromError(error, t("maintenance.llm.fetchFailed")));
    } finally {
      setFetchingID(null);
    }
  };

  const deleteProvider = async (provider: LLMProvider) => {
    if (readOnly) return;
    setDeletingID(provider.id);
    try {
      await api.deleteProvider(provider.id);
      setProviders((current) => current.filter((item) => item.id !== provider.id));
      setDrafts((current) => {
        const next = { ...current };
        delete next[provider.id];
        return next;
      });
      toast.success(t("maintenance.llm.deleted"));
    } catch (error) {
      toast.notify(toastFromError(error, t("maintenance.llm.deleteFailed")));
    } finally {
      setDeletingID(null);
    }
  };

  if (readOnly) return <p className="text-sm text-muted-foreground">{t("maintenance.llm.demoDisabled")}</p>;
  return (
    <div className="space-y-6">
      <SettingsSection
        title={t("maintenance.llm.title")}
        description={t("maintenance.llm.description")}
        action={
          <Button variant="outline" size="sm" disabled={loading} onClick={() => void load()}>
            <RefreshCw className={loading ? "h-4 w-4 animate-spin" : "h-4 w-4"} />
            {t("maintenance.llm.refresh")}
          </Button>
        }
      >
        <SettingsRow title={t("maintenance.llm.provider")} description={t("maintenance.llm.providerDescription")}>
          <select
            aria-label={t("maintenance.llm.provider")}
            value={selectedPreset}
            disabled={readOnly || adding}
            className="h-[var(--control-height)] min-w-52 rounded-[var(--control-radius)] border border-input bg-background px-3 text-sm"
            onChange={(event) => setSelectedPreset(event.target.value)}
          >
            {presets.map((preset) => (
              <option key={preset.id} value={preset.id}>
                {preset.name}
              </option>
            ))}
          </select>
        </SettingsRow>
        <SettingsRow title={t("maintenance.llm.name")} description={t("maintenance.llm.nameDescription")}>
          <Input
            aria-label={t("maintenance.llm.name")}
            className="w-full sm:w-72"
            value={newName}
            disabled={readOnly || adding}
            placeholder={selectedPresetData?.name}
            onChange={(event) => setNewName(event.target.value)}
          />
        </SettingsRow>
        {selectedPresetData?.id === "ollama" && (
          <SettingsRow
            title={t("maintenance.llm.ollamaLocation")}
            description={t("maintenance.llm.ollamaLocationDescription")}
          >
            <select
              aria-label={t("maintenance.llm.ollamaLocation")}
              value={newOllamaScope}
              disabled={readOnly || adding}
              className="h-[var(--control-height)] min-w-52 rounded-[var(--control-radius)] border border-input bg-background px-3 text-sm"
              onChange={(event) => setNewOllamaScope(event.target.value as typeof newOllamaScope)}
            >
              <option value="local_machine">{t("maintenance.llm.ollamaLocal")}</option>
              <option value="other_machine">{t("maintenance.llm.ollamaOther")}</option>
            </select>
          </SettingsRow>
        )}
        {selectedPresetData?.id !== "ollama" || newOllamaScope === "other_machine" ? (
          <SettingsRow title={t("maintenance.llm.endpoint")} description={t("maintenance.llm.endpointDescription")}>
            <Input
              aria-label={t("maintenance.llm.endpoint")}
              className="w-full sm:w-96"
              value={newBaseUrl}
              disabled={readOnly || adding}
              placeholder={selectedPresetData?.defaultUrl || "https://example.invalid"}
              onChange={(event) => setNewBaseUrl(event.target.value)}
            />
          </SettingsRow>
        ) : (
          <SettingsRow title={t("maintenance.llm.endpoint")} description={t("maintenance.llm.ollamaLocalEndpoint")}>
            <span className="max-w-96 truncate text-sm text-muted-foreground">
              {t("maintenance.llm.ollamaDerivedEndpoint")}
            </span>
          </SettingsRow>
        )}
        <SettingsRow title={t("maintenance.llm.apiKey")} description={t("maintenance.llm.apiKeyDescription")}>
          <Input
            aria-label={t("maintenance.llm.apiKey")}
            className="w-full sm:w-72"
            type="password"
            value={newAPIKey}
            disabled={readOnly || adding}
            autoComplete="new-password"
            onChange={(event) => setNewAPIKey(event.target.value)}
          />
        </SettingsRow>
        <div className="flex justify-end border-t px-4 py-3">
          <Button
            size="sm"
            disabled={
              readOnly ||
              adding ||
              !selectedPresetData ||
              ((selectedPresetData.id === "custom-openai" || selectedPresetData.id === "custom-anthropic") &&
                !newBaseUrl.trim()) ||
              (selectedPresetData.id === "ollama" && newOllamaScope === "other_machine" && !newBaseUrl.trim())
            }
            onClick={() => void addProvider()}
          >
            {adding ? <Loader2 className="h-4 w-4 animate-spin" /> : <Plus className="h-4 w-4" />}
            {t("maintenance.llm.add")}
          </Button>
        </div>
      </SettingsSection>

      {loading ? (
        <p className="px-1 text-sm text-muted-foreground">{t("maintenance.llm.loading")}</p>
      ) : providers.length === 0 ? (
        <p className="px-1 text-sm text-muted-foreground">{t("maintenance.llm.empty")}</p>
      ) : (
        providers.map((provider) => {
          const draft = drafts[provider.id];
          return draft ? (
            <LLMProviderCard
              key={provider.id}
              draft={draft}
              readOnly={readOnly}
              saving={savingID === provider.id}
              fetching={fetchingID === provider.id}
              deleting={deletingID === provider.id}
              onChange={(next) => setDrafts((current) => ({ ...current, [provider.id]: { ...draft, ...next } }))}
              onSave={() => void saveProvider(draft)}
              onFetch={() => void fetchModels(draft)}
              onDelete={() => void deleteProvider(provider)}
            />
          ) : null;
        })
      )}
      <SettingsSection
        title={t("maintenance.llm.policyTitle")}
        description={t("maintenance.llm.policyDescription")}
        footer={
          <Button
            size="sm"
            disabled={readOnly || savingPolicy}
            onClick={async () => {
              setSavingPolicy(true);
              try {
                const next = await api.updatePolicy(policy);
                setPolicy(next);
                toast.success(t("maintenance.llm.policySaved"));
              } catch (error) {
                toast.notify(toastFromError(error, t("maintenance.llm.policySaveFailed")));
              } finally {
                setSavingPolicy(false);
              }
            }}
          >
            {savingPolicy ? <Loader2 className="h-4 w-4 animate-spin" /> : <Save className="h-4 w-4" />}
            {t("maintenance.llm.save")}
          </Button>
        }
      >
        <LLMPolicyEditor policy={policy} onChange={setPolicy} readOnly={readOnly} />
      </SettingsSection>
    </div>
  );
}

function LLMProviderCard({
  draft,
  readOnly,
  saving,
  fetching,
  deleting,
  onChange,
  onSave,
  onFetch,
  onDelete,
}: {
  draft: ProviderDraft;
  readOnly: boolean;
  saving: boolean;
  fetching: boolean;
  deleting: boolean;
  onChange: (next: Partial<ProviderDraft>) => void;
  onSave: () => void;
  onFetch: () => void;
  onDelete: () => void;
}) {
  const { t } = useTranslation();
  const localOllama = draft.kind === "ollama" && draft.ollamaScope === "local_machine";
  return (
    <SettingsSection
      title={draft.name}
      description={`${draft.kind === "anthropic" ? "Anthropic" : draft.kind === "ollama" ? "Ollama" : "OpenAI-compatible"} · ${draft.boundary === "private" ? t("maintenance.llm.privateBoundary") : t("maintenance.llm.externalBoundary")}`}
      footer={
        <>
          <Button variant="destructive" size="sm" disabled={readOnly || saving || deleting} onClick={onDelete}>
            {deleting ? <Loader2 className="h-4 w-4 animate-spin" /> : <Trash2 className="h-4 w-4" />}
            {t("maintenance.llm.delete")}
          </Button>
          <Button size="sm" disabled={readOnly || saving || deleting} onClick={onSave}>
            {saving ? <Loader2 className="h-4 w-4 animate-spin" /> : <Save className="h-4 w-4" />}
            {t("maintenance.llm.save")}
          </Button>
        </>
      }
    >
      <SettingsRow title={t("maintenance.llm.name")}>
        <Input
          aria-label={t("maintenance.llm.name")}
          className="w-full sm:w-72"
          value={draft.name}
          disabled={readOnly}
          onChange={(event) => onChange({ name: event.target.value })}
        />
      </SettingsRow>
      <SettingsRow
        title={t("maintenance.llm.endpoint")}
        description={localOllama ? t("maintenance.llm.ollamaLocalEndpoint") : t("maintenance.llm.endpointDescription")}
      >
        <Input
          aria-label={t("maintenance.llm.endpoint")}
          className="w-full sm:w-96"
          value={draft.baseUrl}
          disabled={readOnly || localOllama}
          onChange={(event) => onChange({ baseUrl: event.target.value })}
        />
      </SettingsRow>
      <SettingsRow
        title={t("maintenance.llm.apiKey")}
        description={draft.hasApiKey ? t("maintenance.llm.keyConfigured") : t("maintenance.llm.keyMissing")}
      >
        <Input
          aria-label={t("maintenance.llm.apiKey")}
          className="w-full sm:w-72"
          type="password"
          value={draft.apiKey}
          disabled={readOnly}
          autoComplete="new-password"
          placeholder={draft.hasApiKey ? "••••••••" : ""}
          onChange={(event) => onChange({ apiKey: event.target.value })}
        />
      </SettingsRow>
      <SettingsRow title={t("maintenance.llm.model")} description={t("maintenance.llm.modelDescription")}>
        <div className="flex w-full min-w-0 gap-2 sm:w-96">
          <Input
            aria-label={t("maintenance.llm.model")}
            value={draft.defaultModel ?? ""}
            disabled={readOnly}
            list={draft.id + "-models"}
            onChange={(event) => onChange({ defaultModel: event.target.value })}
          />
          <datalist id={draft.id + "-models"}>
            {draft.models.map((model) => (
              <option key={model.id} value={model.id}>
                {model.name}
              </option>
            ))}
          </datalist>
          <Button variant="outline" size="sm" disabled={readOnly || fetching} onClick={onFetch}>
            {fetching ? <Loader2 className="h-4 w-4 animate-spin" /> : <RefreshCw className="h-4 w-4" />}
            {t("maintenance.llm.fetchModels")}
          </Button>
        </div>
      </SettingsRow>
      <SettingsRow title={t("maintenance.llm.boundary")}>
        <select
          aria-label={t("maintenance.llm.boundary")}
          value={draft.boundary}
          disabled={readOnly}
          onChange={(event) => onChange({ boundary: event.target.value as LLMProvider["boundary"] })}
          className="rounded border bg-background px-3 py-2 text-sm"
        >
          <option value="external">{t("maintenance.llm.externalBoundary")}</option>
          <option value="private">{t("maintenance.llm.privateBoundary")}</option>
        </select>
      </SettingsRow>
      <SettingsRow title={t("maintenance.llm.allowSensitive")} description={t("maintenance.llm.boundaryHint")}>
        <input
          type="checkbox"
          aria-label={t("maintenance.llm.allowSensitive")}
          checked={draft.allowSensitive ?? false}
          disabled={readOnly || draft.boundary !== "private"}
          onChange={(event) => onChange({ allowSensitive: event.target.checked })}
        />
      </SettingsRow>
      <SettingsRow title={t("maintenance.llm.clearKey")}>
        <input
          type="checkbox"
          aria-label={t("maintenance.llm.clearKey")}
          checked={draft.clearKey}
          disabled={readOnly}
          onChange={(event) => onChange({ clearKey: event.target.checked })}
        />
      </SettingsRow>
      {draft.kind === "ollama" && (
        <SettingsRow title={t("maintenance.llm.ollamaLocation")}>
          <select
            aria-label={t("maintenance.llm.ollamaLocation")}
            disabled={readOnly}
            value={draft.ollamaScope}
            onChange={(event) => onChange({ ollamaScope: event.target.value as LLMProvider["ollamaScope"] })}
            className="rounded border bg-background px-3 py-2 text-sm"
          >
            <option value="local_machine">{t("maintenance.llm.ollamaLocal")}</option>
            <option value="other_machine">{t("maintenance.llm.ollamaOther")}</option>
          </select>
        </SettingsRow>
      )}
      <SettingsRow title={t("maintenance.llm.enabled")}>
        <input
          type="checkbox"
          aria-label={t("maintenance.llm.enabled")}
          checked={draft.enabled}
          disabled={readOnly}
          onChange={(event) => onChange({ enabled: event.target.checked })}
        />
      </SettingsRow>
    </SettingsSection>
  );
}
