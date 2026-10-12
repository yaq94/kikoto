import { Loader2, Sparkles } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { Dialog, DialogBody, DialogFooter, DialogHeader } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { toastFromError, useToast } from "@/components/ui/toast";
import { api, type LLMProvider, type LLMTranslation, type WorkDetail } from "@/lib/api";

export function LLMTranslationDialog({
  work,
  onClose,
  onApply,
}: {
  work: WorkDetail;
  onClose: () => void;
  onApply: (translation: LLMTranslation) => void;
}) {
  const { t } = useTranslation();
  const toast = useToast();
  const [providers, setProviders] = useState<LLMProvider[]>([]);
  const [providerID, setProviderID] = useState("");
  const [targetLanguage, setTargetLanguage] = useState("zh-cn");
  const [fields, setFields] = useState({ title: true, description: true });
  const [preview, setPreview] = useState<LLMTranslation | null>(null);
  const [loading, setLoading] = useState(false);
  const [loadingProviders, setLoadingProviders] = useState(true);

  useEffect(() => {
    void api
      .listProviders()
      .then((result) => {
        const enabled = result.providers.filter((provider) => provider.enabled && provider.defaultModel);
        setProviders(enabled);
        setProviderID(enabled[0]?.id ?? "");
      })
      .catch((error) => toast.notify(toastFromError(error, t("llmTranslation.providersFailed"))))
      .finally(() => setLoadingProviders(false));
  }, [toast, t]);

  const translate = async () => {
    if (!providerID || !targetLanguage.trim() || (!fields.title && !fields.description)) return;
    setLoading(true);
    try {
      const result = await api.translateWork(work.id, {
        providerId: providerID,
        targetLanguage: targetLanguage.trim(),
        fields: [fields.title && "title", fields.description && "description"].filter(Boolean) as string[],
      });
      setPreview(result);
    } catch (error) {
      toast.notify(toastFromError(error, t("llmTranslation.failed")));
    } finally {
      setLoading(false);
    }
  };

  return (
    <Dialog onClose={onClose} size="lg" dismissible={!loading}>
      <DialogHeader title={t("llmTranslation.title")} description={work.title} />
      <DialogBody>
        {preview ? (
          <div className="space-y-4">
            <div className="rounded-lg border bg-muted/25 px-3 py-3 text-sm">
              <div className="font-medium">
                {t("llmTranslation.previewSummary", { provider: preview.providerName, model: preview.model })}
              </div>
              <div className="mt-1 text-xs text-muted-foreground">{preview.assessment.reason}</div>
            </div>
            {fields.title && (
              <PreviewField
                label={t("metadataEditor.sections.title")}
                source={preview.source.title}
                result={preview.translation.title}
              />
            )}
            {fields.description && (
              <PreviewField
                label={t("workTitles.description")}
                source={preview.source.description}
                result={preview.translation.description}
                multiline
              />
            )}
          </div>
        ) : (
          <div className="space-y-4">
            <label className="block text-sm font-medium">
              {t("llmTranslation.provider")}
              <select
                aria-label={t("llmTranslation.provider")}
                value={providerID}
                disabled={loadingProviders || loading}
                onChange={(event) => setProviderID(event.target.value)}
                className="mt-1 h-[var(--control-height)] w-full rounded-[var(--control-radius)] border border-input bg-background px-3 text-sm"
              >
                <option value="">
                  {loadingProviders ? t("llmTranslation.loading") : t("llmTranslation.noProvider")}
                </option>
                {providers.map((provider) => (
                  <option key={provider.id} value={provider.id}>
                    {provider.name} · {provider.defaultModel}
                  </option>
                ))}
              </select>
            </label>
            <label className="block text-sm font-medium">
              {t("llmTranslation.targetLanguage")}
              <Input
                className="mt-1"
                value={targetLanguage}
                disabled={loading}
                placeholder="zh-cn"
                onChange={(event) => setTargetLanguage(event.target.value)}
              />
            </label>
            <fieldset className="space-y-2 rounded-lg border px-3 py-3">
              <legend className="px-1 text-sm font-medium">{t("llmTranslation.fields")}</legend>
              <label className="flex items-center gap-2 text-sm">
                <input
                  type="checkbox"
                  checked={fields.title}
                  disabled={loading}
                  onChange={(event) => setFields((current) => ({ ...current, title: event.target.checked }))}
                />
                {t("metadataEditor.sections.title")}
              </label>
              <label className="flex items-center gap-2 text-sm">
                <input
                  type="checkbox"
                  checked={fields.description}
                  disabled={loading}
                  onChange={(event) => setFields((current) => ({ ...current, description: event.target.checked }))}
                />
                {t("llmTranslation.description")}
              </label>
            </fieldset>
            <p className="text-xs leading-5 text-muted-foreground">{t("llmTranslation.safetyHint")}</p>
          </div>
        )}
      </DialogBody>
      <DialogFooter>
        {preview ? (
          <>
            <Button variant="outline" size="sm" onClick={() => setPreview(null)}>
              {t("llmTranslation.back")}
            </Button>
            <Button size="sm" onClick={() => onApply(preview)}>
              <Sparkles className="h-4 w-4" />
              {t("llmTranslation.apply")}
            </Button>
          </>
        ) : (
          <Button
            size="sm"
            disabled={loading || loadingProviders || !providerID || (!fields.title && !fields.description)}
            onClick={() => void translate()}
          >
            {loading ? <Loader2 className="h-4 w-4 animate-spin" /> : <Sparkles className="h-4 w-4" />}
            {t("llmTranslation.translate")}
          </Button>
        )}
      </DialogFooter>
    </Dialog>
  );
}

function PreviewField({
  label,
  source,
  result,
  multiline = false,
}: {
  label: string;
  source?: string;
  result?: string;
  multiline?: boolean;
}) {
  return (
    <div className="grid gap-2 sm:grid-cols-2">
      <div>
        <div className="mb-1 text-xs font-medium text-muted-foreground">{label} · original</div>
        <div
          className={
            multiline
              ? "max-h-48 overflow-auto whitespace-pre-wrap rounded border px-3 py-2 text-sm"
              : "rounded border px-3 py-2 text-sm"
          }
        >
          {source || "—"}
        </div>
      </div>
      <div>
        <div className="mb-1 text-xs font-medium text-muted-foreground">{label} · translation</div>
        <div
          className={
            multiline
              ? "max-h-48 overflow-auto whitespace-pre-wrap rounded border px-3 py-2 text-sm"
              : "rounded border px-3 py-2 text-sm"
          }
        >
          {result || "—"}
        </div>
      </div>
    </div>
  );
}
