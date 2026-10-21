import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useAuth } from "@/auth/AuthProvider";
import { ContentResourceScope } from "@/components/content-translation/ContentTranslation";
import { toastFromError, useToast } from "@/components/ui/toast";
import { api, type CatalogDirectory, type CatalogTrack, type DirectoryRoutingRule } from "@/lib/api";
import { usePlayer } from "@/player/PlayerProvider";
import { buildCatalogTree, mergeCatalogTracks } from "./catalogTreeModel";
import { catalogPlaybackQueue } from "./catalogPlayback";
import { DirectoryLoadErrorPanel, SourceDirectoryPanel } from "./directory/SourceDirectoryPanels";
import { DirectorySkeleton } from "./WorkDetailLayout";
import { formatTreeStats, treeStats } from "./media/mediaTreeModel";
import type { SourceTabInfo } from "./source/sourceContextModel";
import { CatalogAvailabilityNotice } from "./CatalogAvailabilityNotice";

export function CatalogSourceDirectory({
  entry,
  tabs,
  onSelect,
  directoryRoutingRules,
}: {
  entry: CatalogDirectory;
  tabs: SourceTabInfo[];
  onSelect: (key: string) => void;
  directoryRoutingRules: DirectoryRoutingRule[];
}) {
  const { t } = useTranslation();
  const auth = useAuth();
  const toast = useToast();
  const player = usePlayer();
  const [tracks, setTracks] = useState<CatalogTrack[] | null>(null);
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  const [refreshing, setRefreshing] = useState(false);
  const [status, setStatus] = useState(entry.status);
  useEffect(() => setStatus(entry.status), [entry.status, entry.catalogEntryId]);
  const request = useRef<AbortController | null>(null);
  const refreshRequest = useRef<AbortController | null>(null);
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      refreshRequest.current?.abort();
    };
  }, []);
  useEffect(() => {
    const controller = new AbortController();
    request.current = controller;
    setTracks(null);
    setError("");
    void api
      .getCatalogTracks(entry.catalogEntryId, { depth: 1 }, controller.signal)
      .then((result) => {
        if (!controller.signal.aborted) setTracks(result.tracks);
      })
      .catch((error) => {
        if (!controller.signal.aborted) setError(error.message);
      });
    return () => controller.abort();
  }, [entry.catalogEntryId, attempt]);
  const tree = useMemo(() => buildCatalogTree(entry.catalogEntryId, tracks ?? []), [entry.catalogEntryId, tracks]);
  const loadFolder = (folderPath: string) => {
    const signal = request.current?.signal;
    void api
      .getCatalogTracks(entry.catalogEntryId, { path: folderPath, depth: 1 }, signal)
      .then((result) => {
        if (!signal?.aborted) {
          setTracks((current) => mergeCatalogTracks(current ?? [], folderPath, result.tracks));
        }
      })
      .catch(() => {
        // The folder stays collapsed; the next open retries the request.
      });
  };
  const refresh = async () => {
    if (refreshing) return;
    // A refresh writes source state, so it needs its own lifetime. Reusing the
    // track-loading signal cancelled it whenever a failed refresh moved the
    // panel to another tab, which unmounts this component: the request never
    // reached the caller and the outcome was lost even though the server had
    // already recorded it.
    refreshRequest.current?.abort();
    const controller = new AbortController();
    refreshRequest.current = controller;
    const signal = controller.signal;
    setRefreshing(true);
    try {
      if (auth.hasPermission("sources:write")) await api.refreshCatalogDirectory(entry.catalogEntryId, signal);
      const result = await api.getCatalogTracks(entry.catalogEntryId, { depth: 1 }, signal);
      if (signal.aborted || !mounted.current) return;
      setTracks(result.tracks);
      setError("");
    } catch (error) {
      if (!signal.aborted && mounted.current) {
        setError(error instanceof Error ? error.message : t("errors.unavailable"));
      }
    } finally {
      try {
        const detail = await api.getCatalogEntry(entry.catalogEntryId, signal);
        if (!signal.aborted && mounted.current) setStatus(detail.status ?? entry.status);
      } catch {
        // Keep the last known status when even the saved detail is unavailable.
      }
      if (!signal.aborted && mounted.current) setRefreshing(false);
      if (refreshRequest.current === controller) refreshRequest.current = null;
    }
  };
  const play = async (files: Parameters<typeof catalogPlaybackQueue>[1], locationId: number) => {
    const signal = request.current?.signal;
    try {
      const queue = await catalogPlaybackQueue(entry, files, locationId, signal);
      if (!signal?.aborted) player.playQueue(queue.tracks, queue.locationId, 0);
    } catch (error) {
      if (!signal?.aborted) toast.notify(toastFromError(error, t("errors.unavailable")));
    }
  };
  return (
    <ContentResourceScope resource={{ catalogEntryId: entry.catalogEntryId }}>
      <CatalogAvailabilityNotice entry={{ ...entry, status }} />
      <SourceDirectoryPanel
        key={entry.catalogEntryId}
        title={t("libraryDetail.directory")}
        description={entry.sourceName}
        tabs={tabs}
        activeKey={`catalog:${entry.catalogEntryId}`}
        onActiveKeyChange={onSelect}
        statsLabel={formatTreeStats(treeStats(tree))}
        root={tree}
        checkingSources={refreshing}
        checkedAt={entry.checkedAt}
        onCheckSources={() => void refresh()}
        directoryRoutingRules={directoryRoutingRules}
        currentLocationId={player.currentTrack?.locationId ?? null}
        currentPlaybackKey={player.currentTrack?.playbackKey ?? null}
        emptyLabel={t("libraryDetail.noRemoteFiles")}
        loadingMessage={tracks ? error : ""}
        emptyState={
          !tracks ? (
            error ? (
              <DirectoryLoadErrorPanel message={error} onRetry={() => setAttempt((value) => value + 1)} />
            ) : (
              <DirectorySkeleton />
            )
          ) : undefined
        }
        onPlayFolder={
          auth.hasPermission("playback:use") ? (files, locationId) => void play(files, locationId) : undefined
        }
        onFolderOpen={loadFolder}
      />
    </ContentResourceScope>
  );
}
