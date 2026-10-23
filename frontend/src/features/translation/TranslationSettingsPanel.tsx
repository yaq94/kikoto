import { useEffect, useRef, useState, type FormEvent } from "react";
import { useTranslation } from "react-i18next";

import { SettingsRow, SettingsSection } from "@/components/settings/SettingsSection";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input, NativeSelect, Textarea, otherCredentialFieldProps } from "@/components/ui/input";
import { ApiError } from "@/lib/apiTransport";
import { translationApi, type TranslationResult, type TranslationSettings, type TranslationTarget } from "./api";

export function TranslationSettingsPanel({ readOnly = false }: { readOnly?: boolean }) {
  const { t } = useTranslation();
  const [settings, setSettings] = useState<TranslationSettings | null>(null);
  const [saved, setSaved] = useState<TranslationSettings | null>(null);
  const [apiKey, setApiKey] = useState("");
  const [clearKey, setClearKey] = useState(false);
  const [text, setText] = useState("");
  const [target, setTarget] = useState<TranslationTarget>("zh-Hans");
  const [result, setResult] = useState<TranslationResult | null>(null);
  const [busy, setBusy] = useState<"save" | "test" | null>(null);
  const [errorCode, setErrorCode] = useState<string | null>(null);
  const [savedMessage, setSavedMessage] = useState(false);
  const [loadVersion, setLoadVersion] = useState(0);
  const lifetime = useRef<AbortController | null>(null);
  const trial = useRef<AbortController | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    lifetime.current = controller;
    translationApi
      .settings(controller.signal)
      .then((value) => {
        if (controller.signal.aborted) return;
        setSettings(value);
        setSaved(value);
        setErrorCode(null);
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted)
          setErrorCode(error instanceof ApiError ? error.code : "translation_unavailable");
      });
    return () => {
      controller.abort();
      trial.current?.abort();
    };
  }, [loadVersion]);

  const dirty = apiKey !== "" || clearKey || JSON.stringify(settings) !== JSON.stringify(saved);
  const disabled = readOnly || busy !== null;
  const change = (patch: Partial<TranslationSettings>) => {
    setSettings((current) => (current ? { ...current, ...patch } : current));
    setSavedMessage(false);
    setResult(null);
    setErrorCode(null);
  };
  const save = async (event: FormEvent) => {
    event.preventDefault();
    if (!settings || disabled) return;
    setBusy("save");
    setSavedMessage(false);
    setErrorCode(null);
    setResult(null);
    const signal = lifetime.current?.signal;
    try {
      const value = await translationApi.update(
        {
          baseUrl: settings.baseUrl,
          model: settings.model,
          timeoutSeconds: settings.timeoutSeconds,
          disableThinking: settings.disableThinking,
          concurrency: settings.concurrency,
          queueLimit: settings.queueLimit,
          maxEntries: settings.maxEntries,
          cacheBytes: settings.cacheBytes,
          minFreeBytes: settings.minFreeBytes,
          ...(clearKey ? { apiKey: "" } : apiKey !== "" ? { apiKey } : {}),
        },
        signal,
      );
      if (signal?.aborted) return;
      setSettings(value);
      setSaved(value);
      setApiKey("");
      setClearKey(false);
      setSavedMessage(true);
    } catch (error) {
      if (!signal?.aborted) setErrorCode(error instanceof ApiError ? error.code : "translation_unavailable");
    } finally {
      if (!signal?.aborted) setBusy(null);
    }
  };
  const testSaved = async (event: FormEvent) => {
    event.preventDefault();
    if (disabled || dirty || !text.trim()) return;
    const controller = new AbortController();
    trial.current = controller;
    setBusy("test");
    setErrorCode(null);
    setResult(null);
    setSavedMessage(false);
    try {
      const value = await translationApi.test(text, target, controller.signal);
      if (!controller.signal.aborted) setResult(value);
    } catch (error) {
      if (!controller.signal.aborted) setErrorCode(error instanceof ApiError ? error.code : "translation_unavailable");
    } finally {
      if (!controller.signal.aborted) setBusy(null);
    }
  };
  const cancel = () => {
    trial.current?.abort();
    setBusy(null);
    setErrorCode("translation_cancelled");
  };
  const error = errorCode
    ? t(`translationAdmin.errors.${errorCode}`, { defaultValue: t("translationAdmin.errors.translation_unavailable") })
    : null;

  if (!settings)
    return (
      <div className="space-y-3">
        <p role={error ? "alert" : "status"}>{error ?? t("translationAdmin.loading")}</p>
        {error && (
          <Button variant="outline" onClick={() => setLoadVersion((value) => value + 1)}>
            {t("translationAdmin.reload")}
          </Button>
        )}
      </div>
    );

  return (
    <div className="space-y-8">
      <form onSubmit={save}>
        <SettingsSection
          title={t("translationAdmin.title")}
          description={t("translationAdmin.description")}
          footer={
            <Button type="submit" disabled={disabled || !dirty}>
              {t(busy === "save" ? "translationAdmin.saving" : "translationAdmin.save")}
            </Button>
          }
        >
          <SettingsRow
            title={t("translationAdmin.baseUrl")}
            htmlFor="translation-base-url"
            description={t("translationAdmin.baseUrlHint")}
            stack
          >
            <Input
              id="translation-base-url"
              aria-label={t("translationAdmin.baseUrl")}
              type="url"
              value={settings.baseUrl}
              onChange={(event) => change({ baseUrl: event.target.value })}
              disabled={disabled}
              className="w-full"
              maxLength={2048}
            />
          </SettingsRow>
          <SettingsRow
            title={t("translationAdmin.model")}
            htmlFor="translation-model"
            description={t("translationAdmin.modelHint")}
            stack
          >
            <Input
              id="translation-model"
              aria-label={t("translationAdmin.model")}
              value={settings.model}
              onChange={(event) => change({ model: event.target.value })}
              disabled={disabled}
              required
              maxLength={256}
              className="w-full"
            />
          </SettingsRow>
          <SettingsRow
            title={t("translationAdmin.credentialLabel")}
            htmlFor="translation-api-key"
            description={
              settings.hasApiKey
                ? t("translationAdmin.keySaved", { mask: settings.apiKeyMask })
                : t("translationAdmin.keyMissing")
            }
            stack
          >
            <Input
              {...otherCredentialFieldProps}
              id="translation-api-key"
              aria-label={t("translationAdmin.credentialLabel")}
              type="password"
              value={apiKey}
              onChange={(event) => {
                setApiKey(event.target.value);
                setClearKey(false);
                setSavedMessage(false);
              }}
              disabled={disabled || clearKey}
              className="w-full"
              maxLength={4096}
            />
          </SettingsRow>
          {settings.hasApiKey && (
            <SettingsRow title={t("translationAdmin.clearKey")} htmlFor="translation-clear-key">
              <Checkbox
                id="translation-clear-key"
                aria-label={t("translationAdmin.clearKey")}
                checked={clearKey}
                onCheckedChange={(value) => {
                  setClearKey(value);
                  setApiKey("");
                  setSavedMessage(false);
                }}
                disabled={disabled}
              />
            </SettingsRow>
          )}
          {(
            [
              ["concurrency", 1, 16],
              ["queueLimit", 0, 4096],
              ["maxEntries", 0, 2000000],
              ["cacheBytes", 4096, 4294967296],
              ["minFreeBytes", 0, 1099511627776],
            ] as const
          ).map(([key, min, max]) => (
            <SettingsRow key={key} title={t(`translationAdmin.content.${key}`)} htmlFor={`translation-${key}`}>
              <Input
                id={`translation-${key}`}
                aria-label={t(`translationAdmin.content.${key}`)}
                type="number"
                min={min}
                max={max}
                required
                value={settings[key]}
                onChange={(event) => change({ [key]: Number(event.target.value) })}
                disabled={disabled}
              />
            </SettingsRow>
          ))}
          <SettingsRow title={t("translationAdmin.timeout")} htmlFor="translation-timeout">
            <Input
              id="translation-timeout"
              aria-label={t("translationAdmin.timeout")}
              type="number"
              min={1}
              max={60}
              required
              value={settings.timeoutSeconds}
              onChange={(event) => change({ timeoutSeconds: Number(event.target.value) })}
              disabled={disabled}
            />
          </SettingsRow>
          <SettingsRow
            title={t("translationAdmin.disableThinking")}
            htmlFor="translation-disable-thinking"
            description={t("translationAdmin.thinkingHint")}
          >
            <Checkbox
              id="translation-disable-thinking"
              aria-label={t("translationAdmin.disableThinking")}
              checked={settings.disableThinking}
              onCheckedChange={(value) => change({ disableThinking: value })}
              disabled={disabled}
            />
          </SettingsRow>
        </SettingsSection>
      </form>
      {savedMessage && (
        <p role="status" className="text-sm text-success">
          {t("translationAdmin.saved")}
        </p>
      )}
      {error && (
        <p role="alert" className="text-sm text-error">
          {error}
        </p>
      )}
      <form onSubmit={testSaved}>
        <SettingsSection
          title={t("translationAdmin.trial")}
          description={t("translationAdmin.trialHint")}
          footer={
            <>
              {busy === "test" && (
                <Button type="button" variant="outline" onClick={cancel}>
                  {t("translationAdmin.cancel")}
                </Button>
              )}
              <Button type="submit" disabled={disabled || dirty || !text.trim() || !saved?.baseUrl}>
                {t(busy === "test" ? "translationAdmin.testing" : "translationAdmin.test")}
              </Button>
            </>
          }
        >
          <SettingsRow title={t("translationAdmin.text")} htmlFor="translation-trial-text" stack>
            <Textarea
              id="translation-trial-text"
              aria-label={t("translationAdmin.text")}
              value={text}
              onChange={(event) => {
                setText(event.target.value);
                setResult(null);
              }}
              disabled={disabled}
              required
              maxLength={8192}
              className="w-full"
            />
          </SettingsRow>
          <SettingsRow title={t("translationAdmin.target")} htmlFor="translation-target">
            <NativeSelect
              id="translation-target"
              aria-label={t("translationAdmin.target")}
              value={target}
              onChange={(event) => {
                setTarget(event.target.value as TranslationTarget);
                setResult(null);
              }}
              disabled={disabled}
            >
              <option value="zh-Hans">{t("languageOptions.zhHans")}</option>
              <option value="en">{t("languageOptions.en")}</option>
              <option value="ja">{t("languageOptions.ja")}</option>
              <option value="ko">{t("languageOptions.ko")}</option>
            </NativeSelect>
          </SettingsRow>
        </SettingsSection>
        {dirty && <p className="mt-3 text-sm text-muted-foreground">{t("translationAdmin.saveFirst")}</p>}
      </form>
      {result && (
        <section aria-label={t("translationAdmin.resultSection")} aria-live="polite" className="space-y-2">
          <p className="text-sm text-muted-foreground">
            {t("translationAdmin.resultMetadata", { model: result.model, milliseconds: result.elapsedMs })}
          </p>
          <p aria-label={t("translationAdmin.result")} className="whitespace-pre-wrap break-words text-sm">
            {result.text}
          </p>
        </section>
      )}
    </div>
  );
}
