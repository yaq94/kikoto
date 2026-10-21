import type { CatalogTrack } from "@/lib/sourceCatalogApi";
import { buildRemoteTree, type TreeNode } from "./media/mediaTreeModel";

/** Existing tree and ordering rules, with source paths independent of labels. */
export function buildCatalogTree(entryId: number, tracks: CatalogTrack[]): TreeNode {
  const labels = new Map<string, string>();
  const originalNames = (nodes: CatalogTrack[]): CatalogTrack[] =>
    nodes.map((node) => {
      labels.set(node.path, node.title);
      return { ...node, title: node.path.split("/").pop() ?? node.title, children: originalNames(node.children ?? []) };
    });
  const root = buildRemoteTree(originalNames(tracks));
  const present = (node: TreeNode) => {
    node.name = labels.get(node.path) ?? node.name;
    for (const file of node.files) {
      file.title = labels.get(file.sourcePath) ?? file.title;
      file.playbackKey = `catalog:${entryId}:${file.sourcePath}`;
    }
    for (const child of node.children.values()) present(child);
  };
  present(root);
  return root;
}

function uniqueCatalogTracks(tracks: CatalogTrack[]): CatalogTrack[] {
  const seen = new Set<string>();
  return tracks.filter((track) => {
    if (seen.has(track.path)) return false;
    seen.add(track.path);
    return true;
  });
}

/** Merges a lazily fetched folder page into the tree already on screen. */
export function mergeCatalogTracks(
  tracks: CatalogTrack[],
  folderPath: string,
  children: CatalogTrack[],
): CatalogTrack[] {
  const target = folderPath.replace(/^\/+|\/+$/g, "");
  if (target === "") return uniqueCatalogTracks([...tracks, ...children]);
  const merge = (nodes: CatalogTrack[]): CatalogTrack[] =>
    nodes.map((node) => {
      if (node.path === target) {
        return { ...node, children: uniqueCatalogTracks([...(node.children ?? []), ...children]) };
      }
      if (!node.children?.length) return node;
      return { ...node, children: merge(node.children) };
    });
  return merge(tracks);
}
