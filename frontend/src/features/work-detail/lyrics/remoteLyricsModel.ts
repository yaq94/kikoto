import type { MediaItem, RemoteTrack, WorkFolderLocation, WorkTranslation } from "@/lib/api";
import { findRemoteLyricsMatches } from "@/player/lyricsMatching";

import {
  alignLyricsByOrder,
  lyricsFolders,
  type LyricsManagerAudio,
  type LyricsManagerFile,
} from "./lyricsManagerModel";

/** Text formats the server accepts as downloadable lyrics. */
const remoteLyricsExtensions = [".lrc", ".vtt", ".srt", ".ass", ".txt"];

/**
 * A lyrics file of the source being added from, addressed by its path in that
 * source: a remote edition's tree path, or a path below a library folder.
 */
export type RemoteLyricsFile = {
  path: string;
  title: string;
  folder: string;
  sizeBytes: number | null;
  /** The file's local location when the source is a library folder. */
  locationId?: number;
};

/** Remote file path chosen for each local audio media item; null leaves it unassigned. */
export type RemoteLyricsMapping = ReadonlyMap<number, string | null>;

export function remoteLyricsFiles(tracks: RemoteTrack[]): RemoteLyricsFile[] {
  const files: RemoteLyricsFile[] = [];
  const walk = (nodes: RemoteTrack[], folder: string) => {
    nodes.forEach((node, index) => {
      const title = cleanTitle(node.title) || `Track ${index + 1}`;
      const path = folder ? `${folder}/${title}` : title;
      if (node.children.length > 0 || node.type === "folder") {
        walk(node.children, path);
        return;
      }
      const lower = title.toLowerCase();
      if (!remoteLyricsExtensions.some((extension) => lower.endsWith(extension))) return;
      if (!node.downloadUrl && !node.streamUrl) return;
      files.push({ path, title, folder, sizeBytes: node.sizeBytes });
    });
  };
  walk(tracks, "");
  return files;
}

/** The lyrics files of a library folder, with paths relative to that folder. */
export function localLyricsFiles(items: MediaItem[], rootPath: string): RemoteLyricsFile[] {
  const root = normalizePath(rootPath);
  const files: RemoteLyricsFile[] = [];
  for (const item of items) {
    if (item.kind === "audio" || item.kind === "video") continue;
    for (const location of item.locations) {
      if (location.locationType !== "local" || location.availability !== "available") continue;
      const absolute = normalizePath(location.path);
      if (!root || !absolute.startsWith(`${root}/`)) continue;
      const path = absolute.slice(root.length + 1);
      const lower = path.toLowerCase();
      if (!remoteLyricsExtensions.some((extension) => lower.endsWith(extension))) continue;
      const slash = path.lastIndexOf("/");
      files.push({
        path,
        title: slash >= 0 ? path.slice(slash + 1) : path,
        folder: slash >= 0 ? path.slice(0, slash) : "",
        sizeBytes: location.sizeBytes ?? item.sizeBytes,
        locationId: location.id,
      });
    }
  }
  return files.sort((left, right) =>
    left.path.localeCompare(right.path, undefined, { numeric: true, sensitivity: "base" }),
  );
}

/** The family's other editions that have local files, whose lyrics can be copied into this one. */
export function localLyricsEditions(editions: WorkTranslation[], currentWorkId: number) {
  return editions.filter((edition) => edition.workId && edition.workId !== currentWorkId && edition.localAvailable);
}

/**
 * DLsite ships lyrics mostly with translated editions, so a translation is the
 * first edition worth listing; the original follows when there is none.
 */
export function defaultLyricsEditionCode(translations: WorkTranslation[], fallbackCode: string) {
  return (
    translations.find((edition) => !edition.origin)?.primaryCode ??
    translations.find((edition) => edition.origin)?.primaryCode ??
    fallbackCode
  );
}

/** The active local folders that hold this work's audio, which can receive downloaded lyrics. */
export function lyricsTargetFolders(folders: WorkFolderLocation[], items: MediaItem[]) {
  const audioPaths = items.flatMap((item) =>
    item.kind === "audio"
      ? item.locations
          .filter((location) => location.locationType === "local" && location.availability === "available")
          .map((location) => normalizePath(location.path))
      : [],
  );
  return folders.filter((folder) => {
    const root = normalizePath(folder.rootPath);
    return folder.state === "active" && root !== "" && audioPaths.some((path) => path.startsWith(`${root}/`));
  });
}

/**
 * Suggests a remote lyrics file for each local track: a matching file name
 * first, then track order against the remote folder holding the most lyrics.
 */
export function suggestRemoteLyricsMapping(
  audio: LyricsManagerAudio[],
  files: RemoteLyricsFile[],
): Map<number, string | null> {
  const candidates = files.map((file, index) => ({
    mediaItemId: -(index + 1),
    locationId: -(index + 1),
    title: file.title,
    path: file.path,
    url: file.path,
  }));
  const result = new Map<number, string | null>();
  for (const entry of audio) {
    result.set(entry.mediaItemId, findRemoteLyricsMatches(entry.path, candidates)[0]?.path ?? null);
  }
  const pseudoLyrics: LyricsManagerFile[] = files.map((file, index) => ({
    mediaItemId: -(index + 1),
    locationId: -(index + 1),
    title: file.title,
    path: file.path,
    folder: file.folder,
  }));
  const folder = lyricsFolders(pseudoLyrics)[0]?.folder;
  if (folder === undefined) return result;
  const unmatched = audio.filter((entry) => result.get(entry.mediaItemId) === null);
  for (const [audioID, pseudoID] of alignLyricsByOrder(unmatched, pseudoLyrics, folder)) {
    result.set(audioID, files[-pseudoID - 1]?.path ?? null);
  }
  return result;
}

/** The download request: every selected file, plus assignments that name a selected file. */
export function remoteLyricsRequest(
  selected: ReadonlySet<string>,
  mapping: RemoteLyricsMapping,
  includeAssignments: boolean,
) {
  const files = [...selected];
  const assignments = includeAssignments
    ? [...mapping.entries()].flatMap(([audioMediaItemId, path]) =>
        path && selected.has(path) ? [{ audioMediaItemId, path }] : [],
      )
    : [];
  return { files, assignments };
}

/** The translation key naming an edition's declared language, or null when it is not a known language. */
export function editionLanguageKey(language: string) {
  switch (language.trim().toLowerCase()) {
    case "jpn":
    case "ja":
    case "ja-jp":
      return "metadata.japanese";
    case "eng":
    case "en":
    case "en-us":
      return "metadata.english";
    case "chi_hans":
    case "zh-cn":
      return "metadata.simplifiedChinese";
    case "chi_hant":
    case "zh-tw":
      return "metadata.traditionalChinese";
    case "ko_kr":
    case "ko":
    case "ko-kr":
      return "metadata.korean";
    default:
      return null;
  }
}

/** The last path segment, for messages that should not show library paths. */
export function folderDisplayName(path: string) {
  const parts = normalizePath(path).split("/").filter(Boolean);
  return parts[parts.length - 1] ?? path;
}

function cleanTitle(value: string) {
  return value
    .replace(/\\/g, "/")
    .split("/")
    .filter((part) => part.trim() && part !== "." && part !== "..")
    .join("/")
    .trim();
}

function normalizePath(path: string) {
  return path.replace(/\\/g, "/").replace(/^\/+|\/+$/g, "");
}
