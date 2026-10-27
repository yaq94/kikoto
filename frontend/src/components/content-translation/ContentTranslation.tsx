import { createContext, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { useAuth } from "@/auth/AuthProvider";
import { api } from "@/lib/api";
import { ApiError } from "@/lib/apiTransport";
import { apiSessionVersion } from "@/lib/apiSession";
import { currentClientStorageScope } from "@/lib/clientStorageScope";
import {
  ContentTranslationBatchError,
  contentTranslationApi,
  translateCoalesced,
  type ContentTranslationInput,
  type ContentTranslationRequest,
  type ContentTranslationResource,
  type ContentTranslationResult,
} from "@/lib/contentTranslationApi";
import { contentTranslationCache } from "@/lib/contentTranslationCache";
import { USER_PREFERENCES_CHANGED } from "@/lib/recommendationSession";
import { CONTENT_LANGUAGE_CHANGED } from "@/lib/contentLanguageEvents";

/**
 * How long a visible field may wait for a translation before it stops claiming
 * to be translating. The original text is already on screen, so the bound only
 * decides when the pending note becomes an explicit, retryable state.
 */
export const CONTENT_TRANSLATION_PENDING_TIMEOUT_MS = 20_000;

/**
 * The state a field shows. `pending` means a request is expected to answer;
 * `timeout` means the bound passed without one, so the reader gets the original
 * text plus a retry instead of an endless progress note.
 */
export function contentTranslationDisplayState(input: {
  enabled: boolean;
  status?: string;
  timedOut: boolean;
}): "original" | "pending" | "timeout" | "translated" | "existing" | "native" | "failed" {
  if (!input.enabled) return "original";
  if (input.status) {
    return input.status as "translated" | "existing" | "native" | "failed";
  }
  return input.timedOut ? "timeout" : "pending";
}

const LanguageContext = createContext({
  language: "origin",
  accountScope: "",
  session: "",
  revision: 0,
  canRead: false,
  /** Model/template that produced the newest response; "" until one arrives. */
  cacheVersion: "",
  noteVersion: (_version?: string): void => undefined,
});
const ScopeContext = createContext<{
  resource?: ContentTranslationResource;
  original: boolean;
  toggle: () => void;
  retry: () => void;
  revision: number;
}>({ original: false, toggle: () => undefined, retry: () => undefined, revision: 0 });

/**
 * Returns a notifier with a stable identity. The Provider creates it once, so
 * an effect that lists it as a dependency does not re-run on every render:
 * an inline arrow here used to feed a render loop.
 */
export function createCacheVersionNotifier(setVersion: (update: (current: string) => string) => void) {
  return (next?: string) => {
    if (!next) return;
    setVersion((current) => (current === next ? current : next));
  };
}

// Account language is shared by visible content, but never changes media state.
export function ContentLanguageProvider({ children }: { children: ReactNode }) {
  const { user, demoMode } = useAuth();
  const userId = user?.id;
  const scope = currentClientStorageScope(user?.id ?? null);
  const session = scope + ":" + apiSessionVersion();
  const [revision, setRevision] = useState(0);
  const [cacheVersion, setCacheVersion] = useState("");
  const noteVersion = useMemo(() => createCacheVersionNotifier(setCacheVersion), []);
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
  // The version is a server-wide fact, not a result of this session's first
  // request. Reading it here means a cached row from an older model is
  // rejected before it paints, instead of staying visible until some later
  // translation happens to announce the new version.
  useEffect(() => {
    if (!userId || demoMode) return;
    const controller = new AbortController();
    void contentTranslationApi
      .version(controller.signal)
      .then((value) => {
        if (!controller.signal.aborted) noteVersion(value?.version);
      })
      .catch(() => {
        // An unknown version keeps the permissive read; a later response
        // announces the version it used.
      });
    return () => controller.abort();
  }, [userId, demoMode, noteVersion]);
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
  const canRead = Boolean(userId && !demoMode);
  const value = useMemo(
    () => ({ language, accountScope: scope, session, revision, canRead, cacheVersion, noteVersion }),
    [language, scope, session, revision, canRead, cacheVersion, noteVersion],
  );
  return <LanguageContext.Provider value={value}>{children}</LanguageContext.Provider>;
}

/** The selected display language, shared by visible titles and list queries. */
export function useContentLanguage() {
  return useContext(LanguageContext).language;
}

/** The account storage scope used by the persistent translation cache. */
export function useContentLanguageScope() {
  return useContext(LanguageContext).accountScope;
}

/**
 * The model/template version of the newest response, or "" while this session
 * has not seen one. An unknown version keeps the permissive cache read.
 */
export function useContentLanguageCacheVersion() {
  return useContext(LanguageContext).cacheVersion;
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
  const { language, accountScope, session, revision, canRead, cacheVersion, noteVersion } = useContext(LanguageContext);
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
  // A translation that never settles must not read as progress forever. The
  // wait is bounded so the original text and a retry action appear even when a
  // request is never answered.
  const [timedOut, setTimedOut] = useState(false);
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
    const translationInput: ContentTranslationInput = input;
    let cancelled = false;
    setTimedOut(false);
    const fail = (code: string) => {
      if (cancelled || controller.signal.aborted) return;
      setSettled({ key, result: { text, original: text, language, status: "failed", code } });
    };
    const timeout = window.setTimeout(() => {
      if (!cancelled && !controller.signal.aborted) setTimedOut(true);
    }, CONTENT_TRANSLATION_PENDING_TIMEOUT_MS);
    void (async () => {
      // A cached display language paints without touching the network.
      const cached = await contentTranslationCache.get(accountScope, translationInput, cacheVersion || undefined);
      if (cancelled || controller.signal.aborted) return;
      if (cached) {
        setSettled({
          key,
          result: { text: cached.text, original: cached.original, language, status: cached.status },
        });
        return;
      }
      try {
        const result = await translateCoalesced(translationInput, controller.signal);
        if (result.status !== "failed") {
          // `put` stores `result.version`, so a later read with the same
          // expected version finds this row and a different one does not.
          // Announce the version only once the row is cached: publishing it
          // first re-runs every visible effect, and a lookup that outran the
          // write would start a second translation of the same text. A
          // source-level field has no key, so a failed put never announces.
          void contentTranslationCache
            .put(accountScope, translationInput, result)
            .then((cached) => {
              if (cached && result.version) noteVersion(result.version);
            })
            .catch(() => undefined);
        }
        if (!cancelled && !controller.signal.aborted) setSettled({ key, result });
      } catch (error) {
        if (error instanceof DOMException && error.name === "AbortError") return;
        fail(
          error instanceof ContentTranslationBatchError
            ? error.code
            : error instanceof ApiError
              ? error.code
              : "translation_unavailable",
        );
      }
    })();
    return () => {
      cancelled = true;
      window.clearTimeout(timeout);
      controller.abort();
    };
  }, [enabled, visible, key, input, text, language, accountScope, cacheVersion, noteVersion]);
  const result = settled?.key === key ? settled.result : null;
  const success =
    enabled && result && (result.status === "translated" || result.status === "existing" || result.status === "native");
  const waiting = enabled && !result && !timedOut;
  const status = contentTranslationDisplayState({ enabled, status: result?.status, timedOut });
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
          {waiting && <span role="status">{t("translationAdmin.content.pending")}</span>}
          {status === "timeout" && (
            <>
              <span role="status">{t("translationAdmin.content.pendingTooLong")}</span>
              <button type="button" className="underline" onClick={scope.retry}>
                {t("translationAdmin.content.retry")}
              </button>
            </>
          )}
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
      {enabled && !controls && (status === "failed" || status === "timeout") && (
        <span role="img" aria-label={status === "timeout" ? t("translationAdmin.content.pendingTooLong") : failure}>
          {" "}
          !
        </span>
      )}
    </span>
  );
}
