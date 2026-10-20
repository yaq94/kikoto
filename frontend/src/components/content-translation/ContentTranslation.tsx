import { createContext, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { useAuth } from "@/auth/AuthProvider";
import { api } from "@/lib/api";
import { ApiError } from "@/lib/apiTransport";
import { apiSessionVersion } from "@/lib/apiSession";
import { currentClientStorageScope } from "@/lib/clientStorageScope";
import {
  contentTranslationApi,
  type ContentTranslationRequest,
  type ContentTranslationResource,
  type ContentTranslationResult,
} from "@/lib/contentTranslationApi";
import { USER_PREFERENCES_CHANGED } from "@/lib/recommendationSession";
import { CONTENT_LANGUAGE_CHANGED } from "@/lib/contentLanguageEvents";

const LanguageContext = createContext({ language: "origin", session: "", revision: 0, canRead: false });
const ScopeContext = createContext<{
  resource?: ContentTranslationResource;
  original: boolean;
  toggle: () => void;
  retry: () => void;
  revision: number;
}>({ original: false, toggle: () => undefined, retry: () => undefined, revision: 0 });

// Account language is shared by visible content, but never changes media state.
export function ContentLanguageProvider({ children }: { children: ReactNode }) {
  const { user, demoMode } = useAuth();
  const userId = user?.id;
  const scope = currentClientStorageScope(user?.id ?? null);
  const session = scope + ":" + apiSessionVersion();
  const [revision, setRevision] = useState(0);
  const [loaded, setLoaded] = useState({ session: "", revision: 0, language: "origin" });
  useEffect(() => {
    const refresh = (event: Event) => {
      if ((event as CustomEvent<string>).detail === scope) setRevision((value) => value + 1);
    };
    window.addEventListener(USER_PREFERENCES_CHANGED, refresh);
    window.addEventListener(CONTENT_LANGUAGE_CHANGED, refresh);
    return () => {
      window.removeEventListener(USER_PREFERENCES_CHANGED, refresh);
      window.removeEventListener(CONTENT_LANGUAGE_CHANGED, refresh);
    };
  }, [scope]);
  useEffect(() => {
    if (!userId || demoMode) return;
    const controller = new AbortController();
    void api
      .getUserPreferences(controller.signal)
      .then((value) => {
        if (!controller.signal.aborted)
          setLoaded({
            session,
            revision,
            language: value.metadataLanguages?.find((language) => language !== "origin") ?? "origin",
          });
      })
      .catch(() => {
        // A transient read failure must not pin the reader to the origin
        // language. Keep the last successful choice for this session; the next
        // revision or a reload retries the request.
        if (!controller.signal.aborted) {
          setLoaded((current) => (current.session === session ? current : { session, revision, language: "origin" }));
        }
      });
    return () => controller.abort();
  }, [userId, demoMode, session, revision]);
  // The session key contains the account and the API session version, so a
  // value from another session is never shown. A newer revision keeps the last
  // successful language until its own read completes.
  const language = loaded.session === session ? loaded.language : "origin";
  return (
    <LanguageContext.Provider value={{ language, session, revision, canRead: Boolean(userId && !demoMode) }}>
      {children}
    </LanguageContext.Provider>
  );
}

export function ContentTranslationScope({
  resource,
  children,
}: {
  resource?: ContentTranslationResource;
  children: ReactNode;
}) {
  const [original, setOriginal] = useState(false);
  const [revision, setRevision] = useState(0);
  return (
    <ScopeContext.Provider
      value={{
        resource,
        original,
        toggle: () => setOriginal((value) => !value),
        revision,
        retry: () => setRevision((value) => value + 1),
      }}
    >
      {children}
    </ScopeContext.Provider>
  );
}

export function ContentResourceScope({
  resource,
  children,
}: {
  resource: ContentTranslationResource;
  children: ReactNode;
}) {
  const scope = useContext(ScopeContext);
  return <ScopeContext.Provider value={{ ...scope, resource }}>{children}</ScopeContext.Provider>;
}

// These components render strings only. File names, paths, source selection and
// player models remain the original objects owned by their existing callers.
export function TranslatedText({
  text,
  field,
  resource: explicit,
  mediaId,
  path,
  controls = true,
}: {
  text: string;
  field: ContentTranslationRequest["field"];
  resource?: ContentTranslationResource;
  mediaId?: number;
  path?: string;
  controls?: boolean;
}) {
  const { t } = useTranslation();
  const { language, session, revision, canRead } = useContext(LanguageContext);
  const scope = useContext(ScopeContext);
  const resource = explicit ?? scope.resource;
  const [element, setElement] = useState<HTMLSpanElement | null>(null);
  const [visible, setVisible] = useState(false);
  const key = JSON.stringify([
    session,
    revision,
    language,
    resource?.workId,
    resource?.sourceId,
    resource?.code,
    resource?.catalogEntryId,
    resource?.seriesId,
    field,
    text,
    mediaId,
    path,
    scope.revision,
  ]);
  const workId = resource?.workId,
    sourceId = resource?.sourceId,
    code = resource?.code,
    catalogEntryId = resource?.catalogEntryId,
    seriesId = resource?.seriesId;
  const input = useMemo(
    () => ({ workId, sourceId, code, catalogEntryId, seriesId, field, text, language, mediaId, path }),
    [workId, sourceId, code, catalogEntryId, seriesId, field, text, language, mediaId, path],
  );
  const [settled, setSettled] = useState<{ key: string; result: ContentTranslationResult } | null>(null);
  const readable = Boolean(
    canRead &&
    (resource?.workId || resource?.sourceId || resource?.code || resource?.catalogEntryId || resource?.seriesId) &&
    text.trim(),
  );
  const enabled = readable && language !== "origin";
  useEffect(() => {
    if (!element) return;
    const observer = new IntersectionObserver((entries) => setVisible(entries.some((entry) => entry.isIntersecting)));
    observer.observe(element);
    return () => observer.disconnect();
  }, [element]);
  useEffect(() => {
    if (!enabled || !visible) return;
    const controller = new AbortController();
    void contentTranslationApi
      .translate(input, controller.signal)
      .then((result) => {
        if (!controller.signal.aborted) setSettled({ key, result });
      })
      .catch((error) => {
        if (!controller.signal.aborted)
          setSettled({
            key,
            result: {
              text,
              original: text,
              language,
              status: "failed",
              code: error instanceof ApiError ? error.code : "translation_unavailable",
            },
          });
      });
    return () => controller.abort();
  }, [enabled, visible, key, input, text, language]);
  const result = settled?.key === key ? settled.result : null;
  const success = enabled && result && (result.status === "translated" || result.status === "existing");
  const status = !enabled ? "original" : (result?.status ?? "pending");
  const shown = success && !scope.original ? result.text : (result?.original ?? text);
  const failure = result?.code
    ? t(`translationAdmin.errors.${result.code}`, { defaultValue: t("translationAdmin.content.failed") })
    : t("translationAdmin.content.failed");
  return (
    <span
      ref={setElement}
      data-translation-state={status}
      title={success ? text : status === "failed" ? failure : undefined}
    >
      <span>{shown}</span>
      {enabled && controls && (
        <span
          className="ml-1 inline-flex flex-wrap items-center gap-1 text-xs font-normal text-muted-foreground"
          onClick={(event) => event.stopPropagation()}
        >
          {status === "pending" && <span role="status">{t("translationAdmin.content.pending")}</span>}
          {status === "failed" && (
            <>
              <span role="status">{failure}</span>
              <button type="button" className="underline" onClick={scope.retry}>
                {t("translationAdmin.content.retry")}
              </button>
            </>
          )}
          {success && (
            <button type="button" className="underline" onClick={scope.toggle}>
              {t(scope.original ? "translationAdmin.content.translated" : "translationAdmin.content.original")}
            </button>
          )}
          {success && field === "title" && (
            <button type="button" className="underline" onClick={scope.retry}>
              {t("translationAdmin.content.retry")}
            </button>
          )}
          {result?.cacheStatus === "paused_low_disk" && (
            <span role="status">{t("translationAdmin.content.paused")}</span>
          )}
        </span>
      )}
      {enabled && !controls && status === "failed" && (
        <span role="img" aria-label={failure}>
          {" "}
          !
        </span>
      )}
    </span>
  );
}
