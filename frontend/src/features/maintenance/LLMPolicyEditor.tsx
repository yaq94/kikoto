import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { SuggestionCombobox } from "@/components/ui/suggestion-combobox";
import { SettingsRow } from "@/components/settings/SettingsSection";
import { useMetadataEntrySuggestions } from "@/hooks/useMetadataEntrySuggestions";
import { api, type LLMPolicy, type MetadataTag } from "@/lib/api";

export function LLMPolicyEditor({
  policy,
  onChange,
  readOnly,
}: {
  policy: LLMPolicy;
  onChange: (policy: LLMPolicy) => void;
  readOnly: boolean;
}) {
  const { t } = useTranslation();
  const [query, setQuery] = useState("");
  const [names, setNames] = useState<Record<number, string>>({});
  const suggestions = useMetadataEntrySuggestions("tags", query);
  useEffect(() => {
    let active = true;
    void Promise.allSettled(policy.tagIds.map((id) => api.getMetadataTag(id))).then((results) => {
      if (!active) return;
      setNames((current) => ({
        ...current,
        ...Object.fromEntries(
          results.flatMap((result) =>
            result.status === "fulfilled" ? [[result.value.id, result.value.displayName]] : [],
          ),
        ),
      }));
    });
    return () => {
      active = false;
    };
  }, [policy.tagIds]);
  const updateRule = (index: number, change: Partial<LLMPolicy["keywords"][number]>) =>
    onChange({ ...policy, keywords: policy.keywords.map((rule, i) => (i === index ? { ...rule, ...change } : rule)) });
  return (
    <>
      <SettingsRow title={t("maintenance.llm.remoteLyrics")} description={t("maintenance.llm.remoteLyricsDescription")}>
        <input
          type="checkbox"
          aria-label={t("maintenance.llm.remoteLyrics")}
          disabled={readOnly}
          checked={policy.remoteLyrics ?? false}
          onChange={(event) => onChange({ ...policy, remoteLyrics: event.target.checked })}
        />
      </SettingsRow>
      <SettingsRow
        title={t("maintenance.llm.alwaysScanAllAges")}
        description={t("maintenance.llm.alwaysScanAllAgesDescription")}
      >
        <input
          type="checkbox"
          aria-label={t("maintenance.llm.alwaysScanAllAges")}
          disabled={readOnly}
          checked={policy.alwaysScanAllAges}
          onChange={(event) => onChange({ ...policy, alwaysScanAllAges: event.target.checked })}
        />
      </SettingsRow>
      <SettingsRow
        title={t("maintenance.llm.sensitiveTags")}
        description={t("maintenance.llm.sensitiveTagsDescription")}
        stack
      >
        {!readOnly && (
          <SuggestionCombobox
            ariaLabel={t("maintenance.llm.sensitiveTags")}
            value={query}
            onChange={setQuery}
            options={suggestions.entries
              .filter((tag) => !policy.tagIds.includes(tag.id))
              .map((entry) => {
                const tag = entry as MetadataTag;
                return {
                  key: String(tag.id),
                  label: tag.displayName,
                  onSelect: () => {
                    setNames((current) => ({ ...current, [tag.id]: tag.displayName }));
                    onChange({ ...policy, tagIds: [...policy.tagIds, tag.id] });
                    setQuery("");
                  },
                };
              })}
          />
        )}
        {suggestions.failed && <p role="alert">{t("metadataEntries.loadFailed")}</p>}
        <ul className="space-y-1">
          {policy.tagIds.map((id) => (
            <li key={id} className="flex items-center justify-between gap-2 text-sm">
              <span>{names[id] ?? `tag:${id}`}</span>
              <Button
                variant="ghost"
                size="sm"
                disabled={readOnly}
                onClick={() => onChange({ ...policy, tagIds: policy.tagIds.filter((value) => value !== id) })}
              >
                {t("common.remove")}
              </Button>
            </li>
          ))}
        </ul>
      </SettingsRow>
      <SettingsRow title={t("maintenance.llm.keywords")} description={t("maintenance.llm.keywordsDescription")} stack>
        <div className="space-y-3">
          {policy.keywords.map((rule, index) => (
            <fieldset key={rule.id ?? index} className="space-y-2 rounded border p-3">
              <legend className="px-1 text-sm">{t("maintenance.llm.ruleNumber", { number: index + 1 })}</legend>
              <Input
                aria-label={t("maintenance.llm.ruleTerm", { number: index + 1 })}
                value={rule.term}
                disabled={readOnly}
                onChange={(event) => updateRule(index, { term: event.target.value })}
              />
              <div className="flex flex-wrap gap-3">
                {["title", "description", "tags", "lyrics", "all"].map((field) => (
                  <label key={field} className="flex items-center gap-1 text-sm">
                    <input
                      type="checkbox"
                      disabled={readOnly}
                      checked={rule.fields.includes(field)}
                      onChange={(event) =>
                        updateRule(index, {
                          fields: event.target.checked
                            ? [...rule.fields, field]
                            : rule.fields.filter((value) => value !== field),
                        })
                      }
                    />
                    {t(`maintenance.llm.ruleFields.${field}`)}
                  </label>
                ))}
              </div>
              <div className="flex items-center justify-between gap-2">
                <select
                  aria-label={t("maintenance.llm.ruleAction", { number: index + 1 })}
                  className="rounded border bg-background px-2 py-1 text-sm"
                  disabled={readOnly}
                  value={rule.action}
                  onChange={(event) => updateRule(index, { action: event.target.value as "blocked" | "sensitive" })}
                >
                  <option value="sensitive">{t("maintenance.llm.sensitive")}</option>
                  <option value="blocked">{t("maintenance.llm.blocked")}</option>
                </select>
                <Button
                  variant="ghost"
                  size="sm"
                  disabled={readOnly}
                  onClick={() => onChange({ ...policy, keywords: policy.keywords.filter((_, i) => i !== index) })}
                >
                  {t("common.remove")}
                </Button>
              </div>
            </fieldset>
          ))}
        </div>
        <Button
          variant="outline"
          size="sm"
          disabled={readOnly || policy.keywords.length >= 512}
          onClick={() =>
            onChange({
              ...policy,
              keywords: [...policy.keywords, { term: "", fields: ["all"], action: "sensitive" }],
            })
          }
        >
          {t("maintenance.llm.addRule")}
        </Button>
      </SettingsRow>
    </>
  );
}
