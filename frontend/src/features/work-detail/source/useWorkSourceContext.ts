import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { useStableCallback } from "@/hooks/useStableCallback";
import i18n from "@/i18n";

import {
  api,
  type LibrarySource,
  type RemoteWorkDetail,
  type SourceAvailabilityResponse,
  type WorkDetail,
  type CatalogDirectory,
} from "@/lib/api";
import {
  buildCatalogTabs,
  buildSourceTabs,
  buildTrackedPresenceOptions,
  currentRemoteSourceWorkCode,
  remoteSourceCanLoad,
  remoteSourceForTrackedPresence,
  remoteSourceTabKey,
  sourceProvidesDirectory,
  sourceAvailabilityNeedsCheck,
  trackedPresenceForked,
  trackedPresenceSourceID,
  type DetailSourceIntent,
  type RemoteSourceAvailability,
} from "@/features/work-detail/source/sourceContextModel";

export function useWorkSourceContext({
  code,
  work,
  sources,
  initialSourceIntent,
  initialTrackedSourceID,
  initialRemoteCode,
  canCheckSources,
  catalogDirectories,
}: {
  code: string;
  work: WorkDetail | null;
  sources: LibrarySource[];
  initialSourceIntent: DetailSourceIntent;
  initialTrackedSourceID: number | null;
  initialRemoteCode: string;
  canCheckSources: boolean;
  catalogDirectories: CatalogDirectory[];
}) {
  const [remoteSources, setRemoteSources] = useState<RemoteSourceAvailability[]>(() =>
    seedRemoteSources(sources, code),
  );
  const [isCheckingSources, setIsCheckingSources] = useState(false);
  const [sourceCheckedAt, setSourceCheckedAt] = useState("");
  const [activeSourceKey, setActiveSourceKey] = useState<string>(initialSourceIntent);
  const [selectedTrackedPresenceKey, setSelectedTrackedPresenceKey] = useState("");
  const [remoteLoadVersion, setRemoteLoadVersion] = useState(0);
  const remoteRequestSequence = useRef(0);
  const availabilityRequest = useRef<AbortController | null>(null);
  const initialCatalogWork = useRef<number | null>(null);
  const trackedPresenceOptions = useMemo(
    () => buildTrackedPresenceOptions(work?.mediaItems ?? [], remoteSources, work?.sourcePresence ?? []),
    [remoteSources, work?.mediaItems, work?.sourcePresence],
  );
  const selectedTrackedOption = useMemo(
    () =>
      trackedPresenceOptions.find((option) => option.key === selectedTrackedPresenceKey) ??
      trackedPresenceOptions.find((option) => option.presence.fileSourceId === initialTrackedSourceID) ??
      trackedPresenceOptions.find((option) => option.forked) ??
      trackedPresenceOptions[0],
    [initialTrackedSourceID, selectedTrackedPresenceKey, trackedPresenceOptions],
  );
  const sourceTabs = useMemo(
    () => [
      ...buildSourceTabs(work?.mediaItems ?? [], remoteSources, work?.sourcePresence ?? [], selectedTrackedOption),
      ...buildCatalogTabs(catalogDirectories, i18n.t("sourceLifecycle.removedSource")),
    ],
    [remoteSources, selectedTrackedOption, work?.mediaItems, work?.sourcePresence, catalogDirectories],
  );
  const selectedSource =
    sourceTabs.find((source) => source.key === activeSourceKey) ??
    sourceTabs.find((source) => source.kind === activeSourceKey) ??
    sourceTabs[0];
  const resolvedActiveSourceKey = selectedSource?.key ?? activeSourceKey;
  const selectedRemoteSource =
    selectedSource?.kind === "remote"
      ? remoteSources.find((item) => selectedSource.key === remoteSourceTabKey(item.source.id))
      : undefined;
  const selectedTrackedPresence = selectedSource?.kind === "tracked" ? (selectedTrackedOption?.presence ?? null) : null;
  const selectedTrackedForked = trackedPresenceForked(selectedTrackedPresence, work?.mediaItems ?? []);
  const selectedTrackedSourceID = trackedPresenceSourceID(selectedTrackedPresence);
  const selectedTrackedRemoteSource = remoteSourceForTrackedPresence(selectedTrackedPresence, remoteSources);
  const selectedRemoteDetail = selectedRemoteSource?.detail ?? null;
  const selectedRemoteTreeLoading = Boolean(selectedRemoteSource?.treeLoading);
  const selectedRemoteTreeError = selectedRemoteSource?.treeError ?? "";
  const selectedRemoteSourceID = selectedRemoteSource?.source.id ?? null;
  const selectedRemoteWorkCode = selectedRemoteSource
    ? currentRemoteSourceWorkCode(
        selectedRemoteSource.detail,
        remoteSourceTabKey(selectedRemoteSource.source.id) === initialSourceIntent ? initialRemoteCode : "",
        selectedRemoteSource.summary,
        work?.primaryCode || code,
      )
    : work?.primaryCode || code;

  // An unnumbered work has no release code, but a Kikoeru presence still
  // carries the code that source knows it by. Lookups use that code so the
  // source tab is not hidden for every work without a release number.
  // Presence rows also carry local and stale locations. A lookup must use a
  // code from an available source row; picking the first remoteCode can query
  // a source that no longer holds the work.
  const presenceCode =
    work?.sourcePresence?.find(
      (presence) =>
        presence.type === "source" && presence.availability === "available" && Boolean(presence.remoteCode),
    )?.remoteCode ?? "";
  const availabilityCode = work?.primaryCode || presenceCode;

  const applyAvailability = useCallback(
    (result: SourceAvailabilityResponse) => {
      const knownSources = sources.filter(sourceProvidesDirectory).map((source) => ({
        source,
        summary:
          result.sources.find((summary) => summary.sourceId === source.id) ??
          seedRemoteSources([source], result.workCode)[0].summary,
      }));
      setRemoteSources((current) =>
        knownSources.map((next) => {
          const previous = current.find((item) => item.source.id === next.source.id);
          if (previous && !result.sources.some((summary) => summary.sourceId === next.source.id)) return previous;
          return previous?.detail
            ? {
                ...next,
                detail: previous.detail,
                loading: previous.loading,
                error: previous.error,
                treeLoading: previous.treeLoading,
                treeLoaded: previous.treeLoaded,
                treeError: previous.treeError,
              }
            : next;
        }),
      );
      setSourceCheckedAt(result.checkedAt);
      setRemoteLoadVersion((version) => version + 1);
    },
    [sources],
  );

  const refreshAvailability = useCallback(async () => {
    // An unnumbered work has no release code, but its source presence still
    // carries one. The automatic path already resolves availabilityCode; the
    // manual check must use the same identifier or it silently does nothing.
    if (!availabilityCode) return null;
    availabilityRequest.current?.abort();
    const controller = new AbortController();
    availabilityRequest.current = controller;
    setIsCheckingSources(true);
    try {
      const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(60_000)]);
      const result = await api.checkSourceAvailability(availabilityCode, 0, signal, true);
      signal.throwIfAborted();
      applyAvailability(result);
      setRemoteSources((items) =>
        items.map((item) =>
          result.sources.some((summary) => summary.sourceId === item.source.id && summary.status === "available")
            ? { ...item, treeLoaded: false, error: "", treeError: "" }
            : item,
        ),
      );
      return result;
    } finally {
      if (!controller.signal.aborted) setIsCheckingSources(false);
    }
  }, [applyAvailability, availabilityCode]);

  const selectSource = useCallback((key: string) => {
    setActiveSourceKey(key);
    if (!key.startsWith("remote-source:")) return;
    setRemoteSources((items) =>
      items.map((item) => (remoteSourceTabKey(item.source.id) === key ? { ...item, error: "", treeError: "" } : item)),
    );
    setRemoteLoadVersion((version) => version + 1);
  }, []);

  const selectTrackedPresence = useCallback((key: string) => {
    setSelectedTrackedPresenceKey(key);
    setActiveSourceKey("tracked");
  }, []);

  const selectRemoteEdition = useCallback(
    async (remoteCode: string) => {
      if (!selectedRemoteSource) return false;
      const sourceID = selectedRemoteSource.source.id;
      setRemoteSources((items) =>
        items.map((item) =>
          item.source.id === sourceID ? { ...item, loading: true, error: "", treeLoading: false, treeError: "" } : item,
        ),
      );
      const requestID = ++remoteRequestSequence.current;
      const controller = new AbortController();
      let timedOut = false;
      const timeout = window.setTimeout(() => {
        timedOut = true;
        controller.abort();
      }, 20_000);
      try {
        const metadata = await api.getRemoteSourceWorkMetadata(sourceID, remoteCode, controller.signal);
        if (requestID !== remoteRequestSequence.current) return false;
        const detail: RemoteWorkDetail = { ...metadata, tracks: [] };
        setRemoteSources((items) =>
          items.map((item) =>
            item.source.id === sourceID
              ? {
                  ...item,
                  detail,
                  loading: false,
                  error: "",
                  treeLoading: true,
                  treeLoaded: false,
                  treeError: "",
                  summary: {
                    ...item.summary,
                    status: "available",
                    primaryCode: detail.primaryCode,
                    remoteId: detail.remoteId,
                    title: detail.title,
                    coverUrl: detail.coverUrl,
                  },
                }
              : item,
          ),
        );
        const tree = await api.getRemoteSourceWorkTracks(
          sourceID,
          metadata.remoteCode || remoteCode,
          controller.signal,
        );
        if (requestID !== remoteRequestSequence.current) return false;
        setRemoteSources((items) =>
          items.map((item) =>
            item.source.id === sourceID
              ? {
                  ...item,
                  detail: item.detail ? { ...item.detail, tracks: tree.tracks } : { ...detail, tracks: tree.tracks },
                  treeLoading: false,
                  treeLoaded: true,
                  treeError: "",
                }
              : item,
          ),
        );
        return true;
      } catch (error) {
        if (requestID !== remoteRequestSequence.current) return false;
        if (error instanceof DOMException && error.name === "AbortError" && !timedOut) return false;
        setRemoteSources((items) =>
          items.map((item) =>
            item.source.id === sourceID
              ? {
                  ...item,
                  loading: false,
                  error: item.detail
                    ? ""
                    : timedOut
                      ? "Remote source timed out. Retry to try again."
                      : error instanceof Error
                        ? error.message
                        : "Remote detail failed.",
                  treeLoading: false,
                  treeError: item.detail
                    ? timedOut
                      ? "Remote directory timed out. Retry to try again."
                      : error instanceof Error
                        ? error.message
                        : "Remote directory failed."
                    : "",
                }
              : item,
          ),
        );
        return false;
      } finally {
        window.clearTimeout(timeout);
      }
    },
    [selectedRemoteSource],
  );

  useEffect(() => {
    if (!work || sourceTabs.length === 0 || sourceTabs.some((source) => source.key === activeSourceKey)) return;
    if (activeSourceKey.startsWith("remote-source:") && !sourceCheckedAt) return;
    const intendedSource = sourceTabs.find((source) => source.kind === activeSourceKey);
    setActiveSourceKey(intendedSource?.key ?? sourceTabs[0].key);
  }, [activeSourceKey, sourceCheckedAt, sourceTabs, work]);

  const supportsLookup = !work || Boolean(availabilityCode);
  // Before the work loads, remote tabs are seeded with the requested code.
  const seedCode = work?.primaryCode || presenceCode || code;
  useEffect(() => {
    setRemoteSources(supportsLookup ? seedRemoteSources(sources, seedCode) : []);
    setSourceCheckedAt("");
    setIsCheckingSources(false);
    if (!availabilityCode || sources.length === 0) return;
    const controller = new AbortController();
    availabilityRequest.current = controller;
    void (async () => {
      let summaries = seedRemoteSources(sources, availabilityCode).map((remote) => remote.summary);
      try {
        const result = await api.getSourceAvailability(availabilityCode, controller.signal);
        if (controller.signal.aborted) return;
        applyAvailability(result);
        summaries = result.sources;
      } catch {
        if (controller.signal.aborted) return;
      }
      if (!canCheckSources) return;
      const pending = summaries.filter(
        (summary) =>
          sources.some(
            (source) => source.id === summary.sourceId && source.enabled && sourceProvidesDirectory(source),
          ) && sourceAvailabilityNeedsCheck(summary),
      );
      if (pending.length === 0) return;
      setIsCheckingSources(true);
      try {
        // One bounded probe at a time; publish each answer without waiting for all sources.
        for (const summary of pending) {
          if (controller.signal.aborted) return;
          try {
            const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(20_000)]);
            const result = await api.checkSourceAvailability(availabilityCode, summary.sourceId, signal);
            if (!controller.signal.aborted) applyAvailability(result);
          } catch {
            // A failed probe leaves the cached directory and other sources intact.
          }
        }
      } finally {
        if (!controller.signal.aborted) setIsCheckingSources(false);
      }
    })();
    return () => {
      controller.abort();
      availabilityRequest.current?.abort();
      availabilityRequest.current = null;
    };
  }, [applyAvailability, availabilityCode, canCheckSources, seedCode, sources, supportsLookup]);

  useEffect(() => {
    const preferredCatalog =
      catalogDirectories.find((entry) => entry.directoryType === "catalog" && entry.status === "available") ??
      catalogDirectories.find((entry) => entry.directoryType === "catalog");
    if (
      work &&
      !work.primaryCode &&
      activeSourceKey === "local" &&
      preferredCatalog &&
      initialCatalogWork.current !== work.id &&
      !work.mediaItems.some((item) =>
        item.locations.some((location) => location.locationType === "local" && location.availability === "available"),
      )
    ) {
      initialCatalogWork.current = work.id;
      setActiveSourceKey(`catalog:${preferredCatalog.catalogEntryId}`);
    }
  }, [work, activeSourceKey, catalogDirectories]);

  // The load below marks the selected source as loading, so it reads the
  // selection without subscribing to it: re-running on that update would abort
  // the request it just started. Explicit triggers are the source, the work
  // code, and remoteLoadVersion (availability refresh or a retry).
  const readSelectedRemoteSource = useStableCallback(() => selectedRemoteSource);
  useEffect(() => {
    const selectedRemoteSource = readSelectedRemoteSource();
    const routedRemoteSource = Boolean(
      selectedRemoteSource &&
      initialRemoteCode &&
      remoteSourceTabKey(selectedRemoteSource.source.id) === initialSourceIntent,
    );
    if (
      !selectedRemoteSource ||
      !selectedRemoteSource.source.enabled ||
      selectedRemoteSource.summary.status === "disabled" ||
      (!remoteSourceCanLoad(selectedRemoteSource.summary) && !routedRemoteSource) ||
      selectedRemoteSource.treeLoaded ||
      selectedRemoteSource.error
    )
      return;
    const controller = new AbortController();
    const requestID = ++remoteRequestSequence.current;
    let timedOut = false;
    const timeout = window.setTimeout(() => {
      timedOut = true;
      controller.abort();
    }, 20_000);
    const sourceID = selectedRemoteSource.source.id;
    setRemoteSources((items) =>
      items.map((item) =>
        item.source.id === sourceID ? { ...item, loading: true, error: "", treeLoading: false, treeError: "" } : item,
      ),
    );
    void (async () => {
      try {
        const metadata =
          selectedRemoteSource.detail ??
          (await api.getRemoteSourceWorkMetadata(sourceID, selectedRemoteWorkCode, controller.signal));
        if (controller.signal.aborted || requestID !== remoteRequestSequence.current) return;
        const detail: RemoteWorkDetail = { ...metadata, tracks: [] };
        setRemoteSources((items) =>
          items.map((item) =>
            item.source.id === sourceID
              ? {
                  ...item,
                  detail: item.detail ?? detail,
                  loading: false,
                  error: "",
                  treeLoading: true,
                  treeError: "",
                  summary: {
                    ...item.summary,
                    status: "available",
                    primaryCode: detail.primaryCode,
                    remoteId: detail.remoteId,
                    title: detail.title,
                    coverUrl: detail.coverUrl,
                  },
                }
              : item,
          ),
        );
        const tree = await api.getRemoteSourceWorkTracks(
          sourceID,
          metadata.remoteCode || selectedRemoteWorkCode,
          controller.signal,
        );
        if (controller.signal.aborted || requestID !== remoteRequestSequence.current) return;
        setRemoteSources((items) =>
          items.map((item) =>
            item.source.id === sourceID
              ? {
                  ...item,
                  detail: item.detail ? { ...item.detail, tracks: tree.tracks } : { ...detail, tracks: tree.tracks },
                  treeLoading: false,
                  treeLoaded: true,
                  treeError: "",
                }
              : item,
          ),
        );
      } catch (error) {
        if (requestID !== remoteRequestSequence.current) return;
        if (error instanceof DOMException && error.name === "AbortError" && !timedOut) return;
        setRemoteSources((items) =>
          items.map((item) =>
            item.source.id === sourceID
              ? {
                  ...item,
                  loading: false,
                  error: item.detail
                    ? ""
                    : timedOut
                      ? "Remote source timed out. Retry to try again."
                      : error instanceof Error
                        ? error.message
                        : "Remote detail failed.",
                  treeLoading: false,
                  treeError: item.detail
                    ? timedOut
                      ? "Remote directory timed out. Retry to try again."
                      : error instanceof Error
                        ? error.message
                        : "Remote directory failed."
                    : "",
                }
              : item,
          ),
        );
      } finally {
        window.clearTimeout(timeout);
        if (!controller.signal.aborted && requestID === remoteRequestSequence.current) {
          setRemoteSources((items) =>
            items.map((item) => (item.source.id === sourceID ? { ...item, loading: false, treeLoading: false } : item)),
          );
        }
      }
    })();
    return () => {
      window.clearTimeout(timeout);
      controller.abort();
      setRemoteSources((items) =>
        items.map((item) => (item.source.id === sourceID ? { ...item, loading: false, treeLoading: false } : item)),
      );
    };
  }, [
    initialRemoteCode,
    initialSourceIntent,
    readSelectedRemoteSource,
    remoteLoadVersion,
    selectedRemoteSourceID,
    selectedRemoteWorkCode,
  ]);

  useEffect(() => {
    setActiveSourceKey(initialSourceIntent);
    setSelectedTrackedPresenceKey("");
  }, [initialRemoteCode, initialSourceIntent, initialTrackedSourceID, work?.id]);

  return {
    remoteSources,
    sourceTabs,
    activeSourceKey,
    setActiveSourceKey,
    selectSource,
    selectTrackedPresence,
    selectRemoteEdition,
    trackedPresenceOptions,
    selectedTrackedPresenceKey: selectedTrackedOption?.key ?? "",
    selectedSource,
    resolvedActiveSourceKey,
    selectedRemoteSource,
    selectedTrackedPresence,
    selectedTrackedForked,
    selectedTrackedSourceID,
    selectedTrackedRemoteSource,
    selectedRemoteDetail,
    selectedRemoteTreeLoading,
    selectedRemoteTreeError,
    selectedRemoteSourceID,
    selectedRemoteWorkCode,
    isCheckingSources,
    sourceCheckedAt,
    refreshAvailability,
  };
}

function seedRemoteSources(sources: LibrarySource[], workCode: string): RemoteSourceAvailability[] {
  return sources.filter(sourceProvidesDirectory).map((source) => ({
    source,
    summary: {
      sourceId: source.id,
      sourceCode: source.code,
      displayName: source.displayName,
      status: source.enabled ? ("unknown" as const) : ("disabled" as const),
      remoteId: "",
      primaryCode: workCode,
      title: "",
      coverUrl: "",
      workId: null,
      hasRemote: false,
      hasTracked: false,
      hasCache: false,
      hasLocal: false,
      error: "",
      elapsedMs: 0,
    },
  }));
}
