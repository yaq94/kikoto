import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useAuth } from "@/auth/AuthProvider";
import { ContentTranslationScope, TranslatedText } from "@/components/content-translation/ContentTranslation";
import { Button } from "@/components/ui/button";
import { api, type CatalogEntry, type CatalogTrack } from "@/lib/api";
import { usePlayer } from "@/player/PlayerProvider";
import { openWorkDetail } from "@/app/workDetailNavigation";
import { toastFromError, useToast } from "@/components/ui/toast";
import { DirectoryExplorer } from "./directory/DirectoryExplorer";
import { type TreeTrack } from "./media/mediaTreeModel";
import { PersonalReviewEditor } from "./PersonalReviewEditor";
import { buildCatalogTree } from "./catalogTreeModel";
import { catalogPlaybackQueue, catalogResumeQueue } from "./catalogPlayback";
import { useWorkPlaybackCursor } from "./media/useWorkPlaybackCursor";
import { CatalogGroupingEditor } from "./CatalogGroupingEditor";
import { CatalogAvailabilityNotice } from "./CatalogAvailabilityNotice";

export function CatalogWorkDetail({ id, onBack }: { id: number; onBack: () => void }) {
  const { t } = useTranslation();
  const auth = useAuth();
  const player = usePlayer();
  const toast = useToast();
  const [entry, setEntry] = useState<CatalogEntry | null>(null);
  const [tracks, setTracks] = useState<CatalogTrack[]>([]);
  const [failed, setFailed] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const request = useRef<AbortController | null>(null);
  const { cursor } = useWorkPlaybackCursor(entry?.workId ?? null);
  useEffect(() => {
    const controller = new AbortController();
    request.current = controller;
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
  const play = async (files: TreeTrack[], locationId: number) => {
    if (!entry) return;
    const signal = request.current?.signal;
    try {
      const queue = await catalogPlaybackQueue(entry, files, locationId, signal);
      if (!signal?.aborted) player.playQueue(queue.tracks, queue.locationId, 0);
    } catch (error) {
      if (!signal?.aborted) toast.notify(toastFromError(error, t("errors.unavailable")));
    }
  };
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
            {entry.workId && (
              <Button
                variant="outline"
                onClick={() =>
                  openWorkDetail(
                    { kind: "known", canonicalCode: String(entry.workId) },
                    { returnTo: `/catalog/${id}`, returnLabel: entry.title },
                  )
                }
              >
                {t("player.openWork")}
              </Button>
            )}
            {entry.workId && (
              <Button
                variant="outline"
                disabled={!cursor || cursor.positionSeconds <= 0 || cursor.completed}
                onClick={() =>
                  void catalogResumeQueue(entry.workId!)
                    .then((queue) => player.playQueue(queue.tracks, queue.locationId, queue.positionSeconds))
                    .catch((error) => toast.notify(toastFromError(error, t("errors.unavailable"))))
                }
              >
                {t("detailActions.resume")}
              </Button>
            )}
          </header>
          <CatalogAvailabilityNotice entry={entry} />
          {entry.seriesId ? (
            <Button
              variant="outline"
              onClick={() =>
                openWorkDetail(
                  { kind: "catalog-series", seriesId: entry.seriesId! },
                  { returnTo: `/catalog/${id}`, returnLabel: entry.title },
                )
              }
            >
              {t("catalogSeries.openSeries")}
            </Button>
          ) : (
            auth.hasPermission("library:write") && (
              <CatalogGroupingEditor key={`${id}:${auth.user?.id}`} entry={entry} />
            )
          )}
          <DirectoryExplorer
            root={tree}
            directoryRoutingRules={[]}
            currentLocationId={player.currentTrack?.locationId ?? null}
            currentPlaybackKey={player.currentTrack?.playbackKey ?? null}
            onPlayFolder={
              auth.hasPermission("playback:use") ? (files, locationId) => void play(files, locationId) : undefined
            }
          />
          <PersonalReviewEditor
            key={`${id}:${auth.user?.id}`}
            title={entry.seriesId ? t("catalogSeries.episodeReview", { title: entry.title }) : undefined}
            value={entry}
            needsRegistration={entry.workId === null}
            onSave={async (patch) => {
              const result = await api.updateCatalogReview(id, patch);
              setEntry((current) => (current ? { ...current, workId: result.workId } : current));
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
