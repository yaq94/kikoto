import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
  type RefObject,
} from "react";
import { Languages } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useAuth } from "@/auth/AuthProvider";
import { AnchoredPopover } from "@/components/ui/anchored-popover";
import { Button } from "@/components/ui/button";
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
  type ContentTranslationStatus,
} from "@/lib/contentTranslationApi";
import { contentTranslationCache } from "@/lib/contentTranslationCache";
import { USER_PREFERENCES_CHANGED } from "@/lib/recommendationSession";
import { CONTENT_LANGUAGE_CHANGED } from "@/lib/contentLanguageEvents";
import { CONTENT_TRANSLATION_CONFIG_CHANGED } from "@/lib/contentTranslationEvents";
import { NAVIGATION_EVENT } from "@/lib/browserHistory";

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
export type ContentTranslationDisplayState = ContentTranslationStatus | "pending" | "timeout";

export function contentTranslationDisplayState(input: {
  enabled: boolean;
  /** False until the field is near the viewport at the current request key. */
  visible: boolean;
  status?: ContentTranslationStatus;
  timedOut: boolean;
}): ContentTranslationDisplayState {
  if (!input.enabled) return "original";
  // A settled answer, including the batch route's `native`, outranks the
  // bound: the note stops claiming progress as soon as text arrives.
  if (input.status) return input.status;
  // A field outside the viewport has not asked for a translation yet. Calling
  // that `pending` claims work that was never started, so a list with one
  // visible row reads as a page of stalled translations while the network is
  // idle.
  if (!input.visible) return "original";
  return input.timedOut ? "timeout" : "pending";
}

const LanguageContext = createContext({
  language: "origin",
  accountScope: "",
  session: "",
  revision: 0,
  canRead: false,
  /** Authoritative server configuration; "" until the version read succeeds. */
  cacheVersion: "",
  refreshVersion: (): void => undefined,
  noteVersion: (_version?: string): void => undefined,
});
const ScopeContext = createContext<{
  resource?: ContentTranslationResource;
  original: boolean;
  toggleAll: () => void;
  viewRevision: number;
  countTranslation?: (change: 1 | -1) => void;
}>({ original: false, toggleAll: () => undefined, viewRevision: 0 });
const HasTranslatedFieldsContext = createContext(false);

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
  const { user, demoMode, hasPermission } = useAuth();
  const userId = user?.id;
  const scope = currentClientStorageScope(user?.id ?? null);
  const accountScope = scope + ":" + JSON.stringify([user?.username, [...(user?.permissions ?? [])].sort()]);
  const session = accountScope + ":" + apiSessionVersion();
  const canRead = Boolean(userId && !demoMode && hasPermission("library:read"));
  const [revision, setRevision] = useState(0);
  const [versionState, setVersionState] = useState({ session: "", version: "" });
  const cacheVersion = versionState.session === session ? versionState.version : "";
  const [versionRevision, setVersionRevision] = useState(0);
  const refreshVersion = useCallback(() => setVersionRevision((value) => value + 1), []);
  // Responses may describe an earlier model. Only the current-version endpoint
  // is authoritative; a differing response asks it to refresh, never rewinds it.
  const noteVersion = useCallback(
    (version?: string) => {
      if (version && version !== cacheVersion) setVersionRevision((value) => value + 1);
    },
    [cacheVersion],
  );
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
    if (!canRead) return;
    let controller: AbortController | undefined;
    const refresh = () => {
      controller?.abort();
      const current = new AbortController();
      controller = current;
      void contentTranslationApi
        .version(current.signal)
        .then((value) => {
          if (!current.signal.aborted && value.version) {
            contentTranslationCache.setVersion(accountScope, value.version);
            setVersionState((previous) =>
              previous.session === session && previous.version === value.version
                ? previous
                : { session, version: value.version },
            );
          }
        })
        .catch(() => undefined);
    };
    const visible = () => {
      if (document.visibilityState === "visible") refresh();
    };
    refresh();
    window.addEventListener(NAVIGATION_EVENT, refresh);
    window.addEventListener("popstate", refresh);
    window.addEventListener("focus", refresh);
    window.addEventListener(CONTENT_TRANSLATION_CONFIG_CHANGED, refresh);
    document.addEventListener("visibilitychange", visible);
    const timer = window.setInterval(visible, 60_000);
    return () => {
      controller?.abort();
      window.clearInterval(timer);
      window.removeEventListener(NAVIGATION_EVENT, refresh);
      window.removeEventListener("popstate", refresh);
      window.removeEventListener("focus", refresh);
      window.removeEventListener(CONTENT_TRANSLATION_CONFIG_CHANGED, refresh);
      document.removeEventListener("visibilitychange", visible);
    };
  }, [canRead, session, accountScope, versionRevision]);
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
  const value = useMemo(
    () => ({ language, accountScope, session, revision, canRead, cacheVersion, noteVersion, refreshVersion }),
    [language, accountScope, session, revision, canRead, cacheVersion, noteVersion, refreshVersion],
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
 * The authoritative configuration version, or "" until this session reads it.
 * Visible fields and prefetch wait for it before looking up or generating text.
 */
export function useContentLanguageCacheVersion() {
  return useContext(LanguageContext).cacheVersion;
}

export function ContentTranslationScope({
  resource,
  allOriginal = false,
  children,
}: {
  resource?: ContentTranslationResource;
  allOriginal?: boolean;
  children: ReactNode;
}) {
  const { session, language } = useContext(LanguageContext);
  const viewKey = JSON.stringify([session, language, resource]);
  const [view, setView] = useState({ key: viewKey, original: false, revision: 0 });
  if (view.key !== viewKey) setView({ key: viewKey, original: false, revision: view.revision + 1 });
  const original = view.key === viewKey && view.original;
  const toggleAll = useCallback(() => {
    setView((current) => ({ ...current, original: !current.original, revision: current.revision + 1 }));
  }, []);
  const [translatedFields, setTranslatedFields] = useState(0);
  const countTranslation = useCallback((change: 1 | -1) => {
    setTranslatedFields((current) => current + change);
  }, []);
  const scope = useMemo(
    () => ({
      resource,
      original,
      toggleAll,
      viewRevision: view.revision,
      countTranslation: allOriginal ? countTranslation : undefined,
    }),
    [resource, original, toggleAll, view.revision, allOriginal, countTranslation],
  );
  return (
    <ScopeContext.Provider value={scope}>
      <HasTranslatedFieldsContext.Provider value={translatedFields > 0}>{children}</HasTranslatedFieldsContext.Provider>
    </ScopeContext.Provider>
  );
}

/** Explicitly toggles the originals of every translated field in a work detail. */
export function ContentOriginalToggle() {
  const { t } = useTranslation();
  const { language, canRead } = useContext(LanguageContext);
  const { original, toggleAll } = useContext(ScopeContext);
  const hasTranslatedFields = useContext(HasTranslatedFieldsContext);
  if (!canRead || language === "origin" || (!hasTranslatedFields && !original)) return null;
  return (
    <button type="button" className="relative min-h-11 text-sm text-muted-foreground underline" onClick={toggleAll}>
      {t(original ? "translationAdmin.content.translatedAll" : "translationAdmin.content.originalAll")}
    </button>
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

function CompactTranslationControls({
  children,
  returnFocusRef,
}: {
  children: ReactNode;
  returnFocusRef?: RefObject<HTMLButtonElement | null>;
}) {
  const { t } = useTranslation();
  const label = t("translationAdmin.content.options");
  const [open, setOpen] = useState(false);
  const anchorRef = useRef<HTMLButtonElement>(null);
  const actionsRef = useRef<HTMLDivElement>(null);
  const returnToTitleIfFocused = useCallback(
    (anchor: HTMLButtonElement | null) => {
      const active = document.activeElement;
      if (active === anchor || actionsRef.current?.contains(active)) returnFocusRef?.current?.focus();
    },
    [returnFocusRef],
  );
  useLayoutEffect(() => {
    const anchor = anchorRef.current;
    // A late native result can remove the translation controls, including
    // the portal which may have opened after this component mounted.
    return () => returnToTitleIfFocused(anchor);
  }, [returnToTitleIfFocused]);
  const close = useCallback(() => {
    if (actionsRef.current?.contains(document.activeElement)) anchorRef.current?.focus();
    setOpen(false);
  }, []);
  useEffect(() => {
    if (!open) return;
    const frame = requestAnimationFrame(() => {
      const actions = actionsRef.current;
      (actions?.querySelector("button") ?? actions)?.focus();
    });
    return () => cancelAnimationFrame(frame);
  }, [open]);
  return (
    <>
      <Button
        ref={anchorRef}
        type="button"
        size="icon-sm"
        variant="ghost"
        className="shrink-0"
        aria-label={label}
        aria-expanded={open}
        aria-haspopup="dialog"
        title={label}
        onClick={() => setOpen(!open)}
      >
        <Languages aria-hidden="true" />
      </Button>
      <AnchoredPopover open={open} anchorRef={anchorRef} ariaLabel={label} onOpenChange={close}>
        <div
          ref={actionsRef}
          tabIndex={-1}
          className="flex max-w-60 flex-col px-3 py-1 text-sm"
          onClick={(event) => event.stopPropagation()}
        >
          {children}
        </div>
      </AnchoredPopover>
    </>
  );
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
  renderText,
  blockControls = false,
  returnFocusRef,
}: {
  text: string;
  field: ContentTranslationRequest["field"];
  resource?: ContentTranslationResource;
  mediaId?: number;
  path?: string;
  controls?: boolean | "compact";
  renderText?: (text: ReactNode) => ReactNode;
  blockControls?: boolean;
  returnFocusRef?: RefObject<HTMLButtonElement | null>;
}) {
  const { t } = useTranslation();
  const { language, accountScope, session, revision, canRead, cacheVersion, noteVersion, refreshVersion } =
    useContext(LanguageContext);
  const scope = useContext(ScopeContext);
  const resource = explicit ?? scope.resource;
  const [element, setElement] = useState<HTMLSpanElement | null>(null);
  // The request key this field last came near the viewport for. Comparing it
  // with the current key keeps a translated card from re-requesting when it
  // scrolls out, while a language or resource change drops back to "not
  // requested" so switching language translates what is on screen instead of
  // the whole page the reader has ever visited.
  const [visibleKey, setVisibleKey] = useState<string | null>(null);
  const fieldKey = JSON.stringify([
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
    cacheVersion,
  ]);
  const [retryAttempt, setRetryAttempt] = useState({ fieldKey, count: 0 });
  if (retryAttempt.fieldKey !== fieldKey) setRetryAttempt({ fieldKey, count: 0 });
  const attempt = retryAttempt.fieldKey === fieldKey ? retryAttempt.count : 0;
  const key = JSON.stringify([fieldKey, attempt]);
  const viewKey = JSON.stringify([fieldKey, scope.viewRevision]);
  const [view, setView] = useState<{ key: string; original?: boolean }>({ key: viewKey });
  if (view.key !== viewKey) setView({ key: viewKey });
  const original = (view.key === viewKey ? view.original : undefined) ?? scope.original;
  const toggle = () => setView({ key: viewKey, original: !original });
  const retry = () => {
    if (!cacheVersion) refreshVersion();
    setRetryAttempt({ fieldKey, count: attempt + 1 });
  };
  const workId = resource?.workId,
    sourceId = resource?.sourceId,
    code = resource?.code,
    catalogEntryId = resource?.catalogEntryId,
    seriesId = resource?.seriesId;
  const input = useMemo(
    () => ({ workId, sourceId, code, catalogEntryId, seriesId, field, text, language, mediaId, path }),
    [workId, sourceId, code, catalogEntryId, seriesId, field, text, language, mediaId, path],
  );
  const [settled, setSettled] = useState<{ fieldKey: string; key: string; result: ContentTranslationResult } | null>(
    null,
  );
  // A translation that never settles must not read as progress forever. The
  // wait is bounded so the original text and a retry action appear even when a
  // request is never answered.
  const [timedOutKey, setTimedOutKey] = useState<string>();
  const timedOut = timedOutKey === key;
  const readable = Boolean(
    canRead &&
    (resource?.workId || resource?.sourceId || resource?.code || resource?.catalogEntryId || resource?.seriesId) &&
    text.trim(),
  );
  const enabled = readable && language !== "origin";
  const visible = visibleKey === key;
  useEffect(() => {
    if (!element) return;
    // Observing restarts per request key, and an observer reports the current
    // intersection as soon as it starts. A field already on screen therefore
    // asks for the new language at once, while one that is off screen waits
    // until the reader reaches it instead of translating the whole page. The
    // margin starts the request just before the field is actually needed.
    const observer = new IntersectionObserver(
      (entries) => {
        if (entries.some((entry) => entry.isIntersecting)) setVisibleKey(key);
      },
      { rootMargin: "400px 0px" },
    );
    observer.observe(element);
    return () => observer.disconnect();
  }, [element, key]);
  useEffect(() => {
    if (!enabled || !visible) return;
    const controller = new AbortController();
    const translationInput: ContentTranslationInput = input;
    let cancelled = false;
    const fail = (code: string) => {
      if (cancelled || controller.signal.aborted) return;
      setSettled({ fieldKey, key, result: { text, original: text, language, status: "failed", code } });
    };
    const timeout = window.setTimeout(() => {
      if (!cancelled && !controller.signal.aborted) setTimedOutKey(key);
    }, CONTENT_TRANSLATION_PENDING_TIMEOUT_MS);
    void (async () => {
      // Preferences can arrive before the authoritative version. Keep the
      // original and the same bounded pending state until cache lookup is safe;
      // generating here would bypass durable rows on every such reload.
      if (!cacheVersion) return;
      // A cached display language paints without touching the network. A
      // storage failure must not surface as an unhandled rejection, so the
      // lookup is contained here rather than trusted to stay non-throwing.
      // An explicit retry checks with the server. Its own shared cache can
      // still answer authoritatively without requesting another generation.
      const cached =
        attempt > 0
          ? undefined
          : await contentTranslationCache.get(accountScope, translationInput, cacheVersion).catch(() => undefined);
      if (cancelled || controller.signal.aborted) return;
      if (cached) {
        setSettled({
          fieldKey,
          key,
          result: { text: cached.text, original: cached.original, language, status: cached.status },
        });
        return;
      }
      try {
        const result = await translateCoalesced(translationInput, controller.signal, session + ":" + cacheVersion);
        if (cancelled || controller.signal.aborted) return;
        if (cacheVersion && result.version && result.version !== cacheVersion) {
          noteVersion(result.version);
          return;
        }
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
              if (cached && result.version && !cancelled && !controller.signal.aborted) noteVersion(result.version);
            })
            .catch(() => undefined);
        }
        if (!cancelled && !controller.signal.aborted) setSettled({ fieldKey, key, result });
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
  }, [
    enabled,
    visible,
    fieldKey,
    key,
    input,
    text,
    language,
    accountScope,
    cacheVersion,
    noteVersion,
    session,
    attempt,
  ]);
  const last = settled?.fieldKey === fieldKey ? settled.result : null;
  const result = settled?.key === key ? settled.result : null;
  const translated =
    enabled &&
    last &&
    (last.status === "translated" || last.status === "existing" || last.status === "native") &&
    last.text !== last.original;
  const hasTranslatedText = Boolean(translated);
  const { countTranslation } = scope;
  useEffect(() => {
    if (!hasTranslatedText || !countTranslation) return;
    countTranslation(1);
    return () => countTranslation(-1);
  }, [hasTranslatedText, countTranslation]);
  const status = contentTranslationDisplayState({ enabled, visible, status: result?.status, timedOut });
  const shown = result && translated && !original ? result.text : (result?.original ?? text);
  const failure = result?.code
    ? t(`translationAdmin.errors.${result.code}`, { defaultValue: t("translationAdmin.content.failed") })
    : t("translationAdmin.content.failed");
  const retryable = status === "failed" || status === "timeout";
  const retryInProgress = enabled && attempt > 0 && !result && !timedOut;
  const showRetry = retryable || retryInProgress || translated;
  const paused = result?.cacheStatus === "paused_low_disk";
  const compact = controls === "compact";
  const canToggle = (controls === true || compact) && translated;
  const showControls = enabled && (controls === true || compact);
  const value = (
    <span ref={setElement} data-translation-state={status} title={translated ? text : retryable ? failure : undefined}>
      {shown}
      {enabled && controls === false && retryable && (
        <span role="status" aria-label={status === "timeout" ? t("translationAdmin.content.pendingTooLong") : failure}>
          {" "}
          !
        </span>
      )}
    </span>
  );
  const actions = showControls && (status === "pending" || showRetry || canToggle || paused) && (
    <>
      {status === "pending" && <span role="status">{t("translationAdmin.content.pending")}</span>}
      {retryable && (
        <span role="status">{status === "failed" ? failure : t("translationAdmin.content.pendingTooLong")}</span>
      )}
      {canToggle && (
        <button type="button" className="relative min-h-11 underline" onClick={toggle}>
          {t(original ? "translationAdmin.content.translated" : "translationAdmin.content.original")}
        </button>
      )}
      {showRetry && (
        <button
          type="button"
          className="relative min-h-11 underline aria-disabled:opacity-60"
          aria-disabled={retryInProgress || undefined}
          onClick={retryInProgress ? undefined : retry}
        >
          {t("translationAdmin.content.retry")}
        </button>
      )}
      {paused && <span role="status">{t("translationAdmin.content.paused")}</span>}
    </>
  );
  return (
    <>
      {renderText ? renderText(value) : value}
      {actions &&
        (compact ? (
          <CompactTranslationControls returnFocusRef={returnFocusRef}>{actions}</CompactTranslationControls>
        ) : (
          <span
            className={`${blockControls ? "flex" : "ml-1 inline-flex"} min-w-0 flex-wrap items-center gap-x-2 text-xs font-normal text-muted-foreground`}
            onClick={(event) => event.stopPropagation()}
          >
            {actions}
          </span>
        ))}
    </>
  );
}
