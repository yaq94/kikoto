import { api, type CatalogEntry } from "@/lib/api";
import { catalogPlaybackQueue } from "./catalogPlayback";
import { buildCatalogTree } from "./catalogTreeModel";
import { flattenTreeFiles } from "./media/mediaTreeModel";

/** The part of a card that identifies its catalog entry and playback. */
export type CatalogCardIdentity = Pick<
  CatalogEntry,
  "catalogEntryId" | "workId" | "sourceId" | "sourceName" | "primaryCode" | "title"
>;

/**
 * Loads every published track of one catalog entry into a player queue.
 *
 * A catalog card is not a work card: it has no stored media rows until someone
 * plays it, so the queue is built from the entry's own file list. Requesting
 * the tracks with no paging parameters returns the complete tree, which is what
 * "play all" means for a folder source.
 */
export async function catalogCardQueue(identity: CatalogCardIdentity, signal?: AbortSignal) {
  const page = await api.getCatalogTracks(identity.catalogEntryId, undefined, signal);
  signal?.throwIfAborted();
  const files = flattenTreeFiles(buildCatalogTree(identity.catalogEntryId, page.tracks ?? [])).filter(
    (file) => (file.kind === "audio" || file.kind === "video") && Boolean(file.streamUrl || file.downloadUrl),
  );
  if (files.length === 0) throw new Error("This catalog entry has no playable track.");
  return catalogPlaybackQueue(identity, files, files[0].locationId, signal);
}
