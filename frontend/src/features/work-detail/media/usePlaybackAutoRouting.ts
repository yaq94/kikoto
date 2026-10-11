import { useEffect, useMemo, useRef, useState } from "react";

import type { PlayerTrack } from "@/player/playerTypes";
import type { TreeNode } from "@/features/work-detail/media/mediaTreeModel";
import {
  captureInitialPlaybackTrack,
  currentPlaybackDirectoryPath,
  type PlaybackRouteSnapshot,
  playbackSourceSelection,
} from "@/features/work-detail/media/playbackDirectoryRouting";
import type { SourceTabInfo, TrackedPresenceOption } from "@/features/work-detail/source/sourceContextModel";

/**
 * Anchors a detail route to the track that was playing when it opened. The
 * directory follows that track until the user navigates by hand; `disable`
 * records that choice for the current route key only.
 */
export function usePlaybackAutoRouting({
  routeKey,
  currentTrack,
  isPlaying,
  matchesWork,
}: {
  routeKey: string;
  currentTrack: PlayerTrack | null;
  isPlaying: boolean;
  matchesWork: boolean;
}) {
  const [manualRouteKey, setManualRouteKey] = useState("");
  useEffect(() => setManualRouteKey(""), [routeKey]);
  const enabled = manualRouteKey !== routeKey;
  const snapshotRef = useRef<PlaybackRouteSnapshot>({ routeKey: "", track: null });
  const track = captureInitialPlaybackTrack(snapshotRef, routeKey, currentTrack, isPlaying, matchesWork);
  return {
    routeKey,
    enabled,
    track,
    routeStateKey: `${routeKey}:${enabled ? "automatic" : "manual"}`,
    disable: () => setManualRouteKey(routeKey),
  };
}

type PlaybackAutoRouting = ReturnType<typeof usePlaybackAutoRouting>;

/** The directory path of the anchored track in `tree`, while routing is automatic. */
export function usePlaybackRoutePath(
  { enabled, track }: Pick<PlaybackAutoRouting, "enabled" | "track">,
  tree: TreeNode,
) {
  return useMemo(() => (enabled ? currentPlaybackDirectoryPath(tree, track) : null), [enabled, track, tree]);
}

/**
 * Selects, once per route, the source that holds the anchored track, so the
 * directory opens where playback is coming from.
 */
export function usePlaybackSourceSelection({
  routing,
  active,
  sourceTabs,
  trackedPresenceOptions,
  resolvedActiveSourceKey,
  selectedTrackedPresenceKey,
  selectSource,
  selectTrackedPresence,
}: {
  routing: Pick<PlaybackAutoRouting, "routeKey" | "enabled" | "track">;
  /** False when the route asked for a specific source. */
  active: boolean;
  sourceTabs: SourceTabInfo[];
  trackedPresenceOptions: TrackedPresenceOption[];
  resolvedActiveSourceKey: string;
  selectedTrackedPresenceKey: string;
  selectSource: (key: string) => void;
  selectTrackedPresence: (key: string) => void;
}) {
  const { routeKey, enabled, track } = routing;
  const selectedRouteRef = useRef<string | null>(null);
  useEffect(() => {
    selectedRouteRef.current = null;
  }, [routeKey]);
  useEffect(() => {
    if (!enabled || !active || !track) return;
    const selection = playbackSourceSelection(track, sourceTabs, trackedPresenceOptions);
    if (!selection) return;
    if (selectedRouteRef.current === routeKey) return;
    selectedRouteRef.current = routeKey;
    if (selection.trackedKey) {
      if (selectedTrackedPresenceKey !== selection.trackedKey || resolvedActiveSourceKey !== "tracked") {
        selectTrackedPresence(selection.trackedKey);
      }
      return;
    }
    if (resolvedActiveSourceKey !== selection.sourceKey) selectSource(selection.sourceKey);
  }, [
    routeKey,
    enabled,
    track,
    active,
    resolvedActiveSourceKey,
    selectedTrackedPresenceKey,
    selectSource,
    selectTrackedPresence,
    sourceTabs,
    trackedPresenceOptions,
  ]);
}
