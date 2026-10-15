import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useAuth } from "@/auth/AuthProvider";
import { ContentTranslationScope, TranslatedText } from "@/components/content-translation/ContentTranslation";
import { Button } from "@/components/ui/button";
import { api, type CatalogEntry, type CatalogTrack } from "@/lib/api";
import { usePlayer } from "@/player/PlayerProvider";
import type { PlayerTrack } from "@/player/playerTypes";
import { DirectoryExplorer } from "./directory/DirectoryExplorer";
import { type TreeTrack } from "./media/mediaTreeModel";
import { PersonalReviewEditor } from "./PersonalReviewEditor";
import { buildCatalogTree } from "./catalogTreeModel";

export function CatalogWorkDetail({ id, onBack }: { id: number; onBack: () => void }) {
  const { t } = useTranslation();
  const auth = useAuth();
  const player = usePlayer();
  const [entry, setEntry] = useState<CatalogEntry | null>(null);
  const [tracks, setTracks] = useState<CatalogTrack[]>([]);
  const [failed, setFailed] = useState(false);
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    setEntry(null);
    setTracks([]);
    setFailed(false);
    void api
      .getCatalogEntry(id, controller.signal)
      .then(setEntry)
      .catch(() => {
        if (!controller.signal.aborted) setFailed(true);
      });
    void api
      .getCatalogTracks(id, controller.signal)
      .then((result) => setTracks(result.tracks))
      .catch(() => {
        if (!controller.signal.aborted) setFailed(true);
      });
    return () => controller.abort();
  }, [id, auth.user?.id, attempt]);
  const tree = useMemo(() => buildCatalogTree(id, tracks), [id, tracks]);
  const asPlayerTrack = (track: TreeTrack): PlayerTrack => ({
    ...track,
    kind: "audio",
    workId: entry?.workId ?? 0,
    workCode: entry?.primaryCode ?? "",
    workTitle: entry?.title ?? "",
    coverUrl: "",
    circle: "",
    progress: null,
    progressRecordable: false,
    lyricsLocationId: null,
    lyricsTitle: "",
    originWorkLocation: `/catalog/${id}`,
  });
  return (
    <section className="space-y-5" aria-label={t("openlist.detail")}>
      <Button variant="outline" onClick={onBack}>
        {t("common.back")}
      </Button>
      {entry ? (
        <ContentTranslationScope key={id} resource={{ catalogEntryId: id }}>
          <header>
            <p className="text-sm text-muted-foreground">
              {entry.sourceName} · {entry.primaryCode || t("openlist.numberless")}
            </p>
            <h1 className="text-2xl font-semibold">
              <TranslatedText text={entry.title} field="title" />
            </h1>
          </header>
          <DirectoryExplorer
            root={tree}
            directoryRoutingRules={[]}
            currentLocationId={player.currentTrack?.locationId ?? null}
            currentPlaybackKey={player.currentTrack?.playbackKey ?? null}
            onPlayFolder={
              auth.hasPermission("playback:use")
                ? (files, locationId) => player.playQueue(files.map(asPlayerTrack), locationId, 0)
                : undefined
            }
          />
          <PersonalReviewEditor
            value={entry}
            needsRegistration={entry.workId === null}
            onSave={async (patch) => {
              const result = await api.updateCatalogReview(id, patch);
              setEntry((current) => (current ? { ...current, ...result } : current));
              return result;
            }}
          />
        </ContentTranslationScope>
      ) : (
        !failed && <p role="status">{t("app.loadingPage")}</p>
      )}
      {failed && (
        <div role="alert">
          <p>{t("errors.unavailable")}</p>
          <Button onClick={() => setAttempt((n) => n + 1)}>{t("common.retry")}</Button>
        </div>
      )}
    </section>
  );
}
