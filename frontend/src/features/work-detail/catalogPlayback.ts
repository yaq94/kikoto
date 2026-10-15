import { api, type CatalogEntry } from "@/lib/api";
import type { PlayerTrack } from "@/player/playerTypes";
import { buildTree, buildWorkResumeQueue, flattenTracks, type TreeTrack } from "./media/mediaTreeModel";

/** Both catalog entry pages and persisted work tabs use this exact file identity. */
export async function catalogPlaybackQueue(
  entry: Pick<CatalogEntry, "catalogEntryId" | "workId" | "sourceId" | "sourceName" | "primaryCode" | "title">,
  files: TreeTrack[],
  locationId: number,
  signal?: AbortSignal,
) {
  const selected = files.find((file) => file.locationId === locationId);
  if (!selected) throw new Error("Selected catalog track is unavailable.");
  const targets = entry.workId
    ? (
        await api.prepareCatalogPlayback(
          entry.catalogEntryId,
          files.map((file) => file.sourcePath),
          signal,
        )
      ).targets
    : [];
  signal?.throwIfAborted();
  const tracks: PlayerTrack[] = files.map((file) => {
    const target = targets.find((target) => target.path === file.sourcePath);
    const location = {
      locationId: target?.locationId ?? file.locationId,
      locationType: "catalog_stream",
      streamUrl: target?.streamUrl ?? file.streamUrl,
      sourceId: entry.sourceId,
      sourceName: entry.sourceName,
      availability: "available",
    };
    return {
      ...file,
      ...location,
      mediaItemId: target?.mediaItemId ?? file.mediaItemId,
      kind: "audio",
      workId: entry.workId ?? 0,
      workCode: entry.primaryCode,
      workTitle: entry.title,
      coverUrl: "",
      circle: "",
      progress: null,
      progressRecordable: Boolean(target),
      lyricsLocationId: null,
      lyricsTitle: "",
      locations: [location],
      playbackKey: `catalog:${entry.catalogEntryId}:${file.sourcePath}`,
      originWorkLocation: `/catalog/${entry.catalogEntryId}`,
    };
  });
  return { tracks, locationId: tracks[files.indexOf(selected)].locationId };
}

export async function catalogResumeQueue(workId: number) {
  const [work, result] = await Promise.all([api.getWork(workId), api.getWorkPlaybackCursor(workId)]);
  const queue = buildWorkResumeQueue(
    flattenTracks(buildTree(work.mediaItems, null, work.primaryCode)),
    work,
    result.cursor,
  );
  if (!queue) throw new Error("Saved catalog track is unavailable.");
  return queue;
}
