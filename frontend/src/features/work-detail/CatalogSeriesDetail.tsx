import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { useAuth } from "@/auth/AuthProvider";
import { api, type CatalogSeries, type CatalogSeriesEpisode } from "@/lib/api";
import { Button } from "@/components/ui/button";
import {
  ContentTranslationScope,
  ContentResourceScope,
  TranslatedText,
} from "@/components/content-translation/ContentTranslation";
import { openWorkDetail } from "@/app/workDetailNavigation";
import { PersonalReviewEditor } from "./PersonalReviewEditor";
import { SharedAssociationPanel } from "./SharedAssociationPanel";

/**
 * A personal-only episode has no catalog entry; its work is the only address
 * the series card can open. Source-backed episodes keep the catalog route.
 */
export function catalogSeriesEpisodeIntent(episode: Pick<CatalogSeriesEpisode, "catalogEntryId" | "workId">) {
  if (episode.catalogEntryId > 0) return { kind: "catalog" as const, catalogEntryId: episode.catalogEntryId };
  if (episode.workId !== null && episode.workId > 0)
    return { kind: "known" as const, canonicalCode: String(episode.workId), view: "local" as const };
  return null;
}

export function openCatalogSeriesEpisode(
  seriesId: number,
  seriesTitle: string,
  episode: Pick<CatalogSeriesEpisode, "catalogEntryId" | "workId">,
) {
  const intent = catalogSeriesEpisodeIntent(episode);
  if (!intent) return false;
  return openWorkDetail(intent, { returnTo: `/catalog-series/${seriesId}`, returnLabel: seriesTitle });
}

export function CatalogSeriesEpisodeItem({
  episode,
  seriesId,
  seriesTitle,
}: {
  episode: CatalogSeriesEpisode;
  seriesId: number;
  seriesTitle: string;
}) {
  const { t } = useTranslation();
  const missing = episode.status === "missing";
  const resource =
    episode.catalogEntryId > 0 ? { catalogEntryId: episode.catalogEntryId } : { workId: episode.workId ?? undefined };
  return (
    <li className="flex items-center justify-between gap-3 py-3" data-episode-status={episode.status ?? "available"}>
      <ContentResourceScope resource={resource}>
        <span className="flex min-w-0 flex-wrap items-center gap-2">
          <span>
            {episode.position}. <TranslatedText text={episode.title} field="title" />
          </span>
          {missing && (
            <span className="rounded bg-muted px-1.5 py-0.5 text-xs font-medium text-muted-foreground">
              {t("catalogSeries.missing")}
            </span>
          )}
        </span>
      </ContentResourceScope>
      <span className="text-sm text-muted-foreground">{episode.sourceName}</span>
      <Button variant="outline" onClick={() => openCatalogSeriesEpisode(seriesId, seriesTitle, episode)}>
        {t("catalogSeries.openEpisode")}
      </Button>
    </li>
  );
}

export function CatalogSeriesDetail({ id, onBack }: { id: number; onBack: () => void }) {
  const { t } = useTranslation();
  const auth = useAuth();
  const [series, setSeries] = useState<CatalogSeries | null>(null);
  const [error, setError] = useState(false);
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    setSeries(null);
    setError(false);
    void api
      .getCatalogSeries(id, controller.signal)
      .then((value) => {
        if (!controller.signal.aborted) setSeries(value);
      })
      .catch(() => {
        if (!controller.signal.aborted) setError(true);
      });
    return () => controller.abort();
  }, [id, auth.user?.id, attempt]);
  return (
    <section className="space-y-5" aria-label={t("catalogSeries.series")}>
      <Button variant="outline" onClick={onBack}>
        {t("common.back")}
      </Button>
      {series && (
        <ContentTranslationScope resource={{ seriesId: id }}>
          <header>
            <p className="text-sm text-muted-foreground">{t("catalogSeries.series")}</p>
            <h1 className="text-2xl font-semibold">
              <TranslatedText text={series.title} field="title" />
            </h1>
          </header>
          <ol className="divide-y divide-border" aria-label={t("catalogSeries.episodes")}>
            {series.episodes.map((episode) => (
              <CatalogSeriesEpisodeItem
                key={episode.catalogEntryId > 0 ? `entry:${episode.catalogEntryId}` : `work:${episode.workId ?? 0}`}
                episode={episode}
                seriesId={id}
                seriesTitle={series.title}
              />
            ))}
          </ol>
          <SharedAssociationPanel
            subject={{ kind: "series", id }}
            onCompleted={() => {
              void api
                .getCatalogSeries(id)
                .then(setSeries)
                .catch(() => setError(true));
            }}
          />
          <PersonalReviewEditor
            key={`${id}:${auth.user?.id}`}
            title={t("catalogSeries.seriesReview")}
            value={series}
            needsRegistration={series.workId === null}
            onSave={async (patch) => {
              const result = await api.updateCatalogSeriesReview(id, patch);
              setSeries((current) => (current?.seriesId === id ? { ...current, workId: result.workId } : current));
              return result;
            }}
          />
        </ContentTranslationScope>
      )}
      {!series && !error && <p role="status">{t("app.loadingPage")}</p>}
      {error && (
        <div role="alert">
          {t("errors.unavailable")} <Button onClick={() => setAttempt((n) => n + 1)}>{t("common.retry")}</Button>
        </div>
      )}
    </section>
  );
}
