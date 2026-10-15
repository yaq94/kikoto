import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { useAuth } from "@/auth/AuthProvider";
import { api, type CatalogSeries } from "@/lib/api";
import { Button } from "@/components/ui/button";
import {
  ContentTranslationScope,
  ContentResourceScope,
  TranslatedText,
} from "@/components/content-translation/ContentTranslation";
import { openWorkDetail } from "@/app/workDetailNavigation";

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
              <li key={episode.catalogEntryId} className="flex items-center justify-between gap-3 py-3">
                <ContentResourceScope resource={{ catalogEntryId: episode.catalogEntryId }}>
                  <span>
                    {episode.position}. <TranslatedText text={episode.title} field="title" />
                  </span>
                </ContentResourceScope>
                <span className="text-sm text-muted-foreground">{episode.sourceName}</span>
                <Button
                  variant="outline"
                  onClick={() =>
                    openWorkDetail(
                      { kind: "catalog", catalogEntryId: episode.catalogEntryId },
                      { returnTo: `/catalog-series/${id}`, returnLabel: series.title },
                    )
                  }
                >
                  {t("catalogSeries.openEpisode")}
                </Button>
              </li>
            ))}
          </ol>
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
