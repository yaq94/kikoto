import { describe, expect, it } from "vitest";

import type { MediaItem } from "@/lib/api";

import { lyricsManagerEntries, rebaseLyricsAssignmentDrafts } from "./lyricsManagerModel";
import { suggestRemoteLyricsMapping } from "./remoteLyricsModel";

function audioItem(id: number, path: string, assignedLyricsMediaItemId: number | null = null): MediaItem {
  return {
    id,
    parentId: null,
    kind: "audio",
    title: path,
    discNo: null,
    trackNo: null,
    durationSeconds: 60,
    sizeBytes: null,
    fingerprint: path,
    progress: null,
    assignedLyricsMediaItemId,
    locations: [
      {
        id: id + 100,
        fileSourceId: 1,
        fileSourceCode: "local",
        fileSourceName: "Local",
        locationType: "local",
        path,
        streamUrl: "",
        downloadUrl: "",
        remoteHash: "",
        sizeBytes: null,
        durationSeconds: 60,
        availability: "available",
        lastCheckedAt: null,
      },
    ],
  };
}

describe("suggestRemoteLyricsMapping", () => {
  it("matches names first and aligns translated names by track number", () => {
    const { audio } = lyricsManagerEntries([
      audioItem(1, "Library/RJ00000001/mp3/Track1.Original opening.mp3"),
      audioItem(2, "Library/RJ00000001/mp3/Track2.Original ending.mp3"),
      audioItem(3, "Library/RJ00000001/mp3/Bonus talk.mp3"),
    ]);
    const files = [
      "mp3/Track1.Translated opening.mp3.vtt",
      "mp3/Track2.Translated ending.mp3.vtt",
      "mp3/Track3.Translated bonus.mp3.vtt",
      "extra/Bonus talk.lrc",
    ].map((path) => {
      const slash = path.lastIndexOf("/");
      return { path, title: path.slice(slash + 1), folder: path.slice(0, slash), sizeBytes: null };
    });
    expect(Object.fromEntries(suggestRemoteLyricsMapping(audio, files))).toEqual({
      1: "mp3/Track1.Translated opening.mp3.vtt",
      2: "mp3/Track2.Translated ending.mp3.vtt",
      3: "extra/Bonus talk.lrc",
    });
  });
});

describe("rebaseLyricsAssignmentDraft", () => {
  it("keeps unsaved edits and takes newly stored assignments elsewhere", () => {
    const before = lyricsManagerEntries([audioItem(1, "w/01.mp3", 10), audioItem(2, "w/02.mp3")]).audio;
    const after = lyricsManagerEntries([audioItem(1, "w/01.mp3", 10), audioItem(2, "w/02.mp3", 21)]).audio;
    const drafts = new Map([
      [
        "",
        new Map<number, number | null>([
          [1, null],
          [2, null],
        ]),
      ],
    ]);
    const rebased = rebaseLyricsAssignmentDrafts(before, after, drafts);
    expect(Object.fromEntries(rebased.get("") ?? [])).toEqual({ 1: null, 2: 21 });
    // A language without a draft takes the stored assignments.
    expect(Object.fromEntries(rebased.get("zh-cn") ?? [])).toEqual({ 1: null, 2: null });
  });
});
