import { useCallback, useEffect, useRef, useState } from "react";
import { api, type CatalogEntryDetail, type CatalogTrack, type PersonalReviewPatch } from "@/lib/api";
import { mergeCatalogTracks } from "./catalogTreeModel";

type DetailRequest = { id: number; viewerId: number | undefined; controller: AbortController };

/** Saved catalog reads and explicit refreshes share the displayed directory,
 * not the lifetime of a source mutation or the global player. */
export function useCatalogWorkDetail(id: number, viewerId: number | undefined, canCheck: boolean) {
  const [entry, setEntry] = useState<CatalogEntryDetail | null>(null);
  const [tracks, setTracks] = useState<CatalogTrack[]>([]);
  const [failed, setFailed] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
  const [readbacks, setReadbacks] = useState(0);
  const request = useRef<DetailRequest | null>(null);
  const refreshRequest = useRef<DetailRequest | null>(null);
  const entryRead = useRef(0);
  const directoryRead = useRef(0);

  const isCurrent = useCallback(
    (scope: DetailRequest) => request.current === scope && !scope.controller.signal.aborted,
    [],
  );
  const currentRequest = () => {
    const scope = request.current;
    return scope && scope.id === id && scope.viewerId === viewerId && isCurrent(scope) ? scope : null;
  };
  const readEntry = useCallback(
    async (scope: DetailRequest) => {
      const version = ++entryRead.current;
      try {
        const result = await api.getCatalogEntry(scope.id, scope.controller.signal);
        if (isCurrent(scope) && entryRead.current === version) {
          setEntry(result);
          return result;
        }
      } catch {
        if (isCurrent(scope) && entryRead.current === version) setFailed(true);
      }
      return null;
    },
    [isCurrent],
  );
  const readTracks = useCallback(
    async (scope: DetailRequest) => {
      const version = ++directoryRead.current;
      try {
        const result = await api.getCatalogTracks(scope.id, { depth: 1 }, scope.controller.signal);
        if (isCurrent(scope) && directoryRead.current === version) setTracks(result.tracks);
      } catch {
        if (isCurrent(scope) && directoryRead.current === version) setFailed(true);
      }
    },
    [isCurrent],
  );
  useEffect(() => {
    const scope = { id, viewerId, controller: new AbortController() };
    request.current = scope;
    setEntry(null);
    setTracks([]);
    setFailed(false);
    setRefreshing(false);
    setReadbacks(0);
    void readEntry(scope);
    void readTracks(scope);
    return () => scope.controller.abort();
  }, [id, viewerId, readEntry, readTracks]);

  const checking = refreshing || (canCheck && !!entry?.checkPending && !failed && readbacks < 10);
  useEffect(() => {
    if (!canCheck || !entry?.checkPending || failed || refreshing || readbacks >= 10) return;
    const scope = request.current;
    if (!scope) return;
    const timer = window.setTimeout(() => {
      void readEntry(scope).then((result) => {
        if (!isCurrent(scope)) return;
        setReadbacks((count) => count + 1);
        if (result && !result.checkPending) void readTracks(scope);
      });
    }, 2_000);
    return () => window.clearTimeout(timer);
  }, [canCheck, entry?.checkPending, failed, refreshing, readbacks, isCurrent, readEntry, readTracks]);

  const reload = async () => {
    const scope = currentRequest();
    if (!scope) return;
    setFailed(false);
    setReadbacks(0);
    await Promise.all([readEntry(scope), readTracks(scope)]);
  };
  const refresh = async () => {
    const scope = currentRequest();
    if (!scope || refreshRequest.current === scope) return;
    refreshRequest.current = scope;
    setRefreshing(true);
    setFailed(false);
    try {
      // A submitted source refresh finishes when its detail closes. The saved
      // reads below are scoped to this page and ignore an obsolete response.
      await api.refreshCatalogDirectory(scope.id);
      if (isCurrent(scope)) await readTracks(scope);
    } catch {
      if (isCurrent(scope)) setFailed(true);
    } finally {
      if (isCurrent(scope)) {
        // A failed refresh still publishes a source verdict, while the old
        // directory remains useful. Read that verdict without clearing files.
        await readEntry(scope);
        if (isCurrent(scope)) setRefreshing(false);
      }
      if (refreshRequest.current === scope) refreshRequest.current = null;
    }
  };
  const loadFolder = async (folderPath: string) => {
    const scope = currentRequest();
    if (!scope) return;
    try {
      const result = await api.getCatalogTracks(scope.id, { path: folderPath, depth: 1 }, scope.controller.signal);
      if (isCurrent(scope)) setTracks((current) => mergeCatalogTracks(current, folderPath, result.tracks));
    } catch {
      if (isCurrent(scope)) setFailed(true);
    }
  };
  const saveReview = async (patch: PersonalReviewPatch) => {
    const scope = currentRequest();
    const result = await api.updateCatalogReview(id, patch);
    if (scope && isCurrent(scope)) {
      setEntry((current) => (current ? { ...current, ...result } : current));
    }
    return result;
  };
  return {
    entry,
    tracks,
    failed,
    refreshing,
    checking,
    reload,
    refresh,
    loadFolder,
    saveReview,
    signal: () => currentRequest()?.controller.signal,
  };
}
