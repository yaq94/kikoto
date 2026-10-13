import type { MediaItem, MediaLyricsAssignment } from "@/lib/mediaApi";
import { metadataTagLanguages } from "@/lib/metadataTagModel";

/**
 * The languages library lyrics can declare, with their label keys: every
 * language first, then the languages titles and tag names are saved in.
 */
export const lyricsLanguages = metadataTagLanguages;

/** The lyrics that apply to every language. */
export const allLyricsLanguages = "";

const providerSpellings: Record<string, string> = {
  ja: "ja-jp",
  jp: "ja-jp",
  jpn: "ja-jp",
  en: "en-us",
  eng: "en-us",
  zh: "zh-cn",
  cn: "zh-cn",
  "chi-hans": "zh-cn",
  "chi-simp": "zh-cn",
  "chi-hans-cn": "zh-cn",
  tw: "zh-tw",
  "chi-hant": "zh-tw",
  "chi-trad": "zh-tw",
  "chi-hant-tw": "zh-tw",
  ko: "ko-kr",
  kor: "ko-kr",
};

/**
 * Maps an edition's declared language onto a lyrics language, and onto every
 * language when lyrics cannot declare it.
 */
export function editionLyricsLanguage(editionLanguage: string): string {
  const language = editionLanguage.trim().toLowerCase().replace(/_/g, "-");
  const normalized = providerSpellings[language] ?? language;
  return lyricsLanguages.some(([value]) => value === normalized) ? normalized : allLyricsLanguages;
}

/**
 * The folder inside a work folder that holds the lyrics Kikoto adds for a
 * language. It mirrors the server's naming and is shown before a download.
 */
export function lyricsLanguageFolderName(language: string): string {
  switch (language) {
    case "ja-jp":
      return "lyrics_ja";
    case "en-us":
      return "lyrics_en";
    case "zh-cn":
      return "lyrics_zh_hans";
    case "zh-tw":
      return "lyrics_zh_hant";
    case "ko-kr":
      return "lyrics_ko";
    default:
      return "lyrics";
  }
}

/** The translation key naming a lyrics language, or null for an unknown one. */
export function lyricsLanguageLabelKey(language: string) {
  return lyricsLanguages.find(([value]) => value === language)?.[1] ?? null;
}

/**
 * A track's library lyrics in playback order. Media loaded before lyrics had
 * languages carries only the single assigned file.
 */
export function mediaItemLyricsAssignments(
  item: Pick<MediaItem, "lyricsAssignments" | "assignedLyricsMediaItemId"> | null | undefined,
): MediaLyricsAssignment[] {
  if (!item) return [];
  if (item.lyricsAssignments?.length) return item.lyricsAssignments;
  return item.assignedLyricsMediaItemId
    ? [{ language: allLyricsLanguages, lyricsMediaItemId: item.assignedLyricsMediaItemId }]
    : [];
}
