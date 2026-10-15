import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { api, type CatalogEntry, type CatalogGroupingPreview, type RemoteWork } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { openWorkDetail } from "@/app/workDetailNavigation";
import { toastFromError, useToast } from "@/components/ui/toast";

export function CatalogGroupingEditor({ entry }: { entry: CatalogEntry }) {
  const { t } = useTranslation();
  const toast = useToast();
  const [open, setOpen] = useState(false);
  const [title, setTitle] = useState("");
  const [query, setQuery] = useState("");
  const [page, setPage] = useState(1);
  const [candidates, setCandidates] = useState<RemoteWork[]>([]);
  const [total, setTotal] = useState(0);
  const [selected, setSelected] = useState<number[]>([entry.catalogEntryId]);
  const [preview, setPreview] = useState<CatalogGroupingPreview | null>(null);
  const [busy, setBusy] = useState(false);
  const [failed, setFailed] = useState(false);
  const operation = useRef(new AbortController());
  useEffect(() => {
    const controller = new AbortController();
    operation.current = controller;
    return () => controller.abort();
  }, [entry.catalogEntryId]);
  useEffect(() => {
    const saved = Number(new URLSearchParams(window.location.search).get("groupingPreview"));
    if (!saved) return;
    const controller = new AbortController();
    void api
      .getCatalogGroupingPreview(saved, controller.signal)
      .then((value) => {
        if (!controller.signal.aborted) {
          setOpen(true);
          setPreview(value);
          setTitle(value.title);
          setSelected(value.episodes.map((item) => item.catalogEntryId));
        }
      })
      .catch(() => {
        if (!controller.signal.aborted) setFailed(true);
      });
    return () => controller.abort();
  }, [entry.catalogEntryId]);
  useEffect(() => {
    if (!open || preview) return;
    const controller = new AbortController();
    setFailed(false);
    void api
      .listRemoteSourceWorks(entry.sourceId, page, 24, query, "recent", "desc", 1, false, controller.signal)
      .then((result) => {
        if (!controller.signal.aborted) {
          setCandidates(result.works.filter((item) => item.catalogEntryId && !item.seriesId));
          setTotal(result.total);
        }
      })
      .catch(() => {
        if (!controller.signal.aborted) setFailed(true);
      });
    return () => controller.abort();
  }, [open, entry.sourceId, page, query, preview]);
  const submit = async () => {
    if (busy) return;
    const signal = operation.current.signal;
    setBusy(true);
    setFailed(false);
    try {
      if (preview) {
        const result = await api.confirmCatalogGrouping(preview.previewId, signal);
        if (signal.aborted) return;
        if (result.seriesId)
          openWorkDetail(
            { kind: "catalog-series", seriesId: result.seriesId },
            { returnTo: `/catalog/${entry.catalogEntryId}`, returnLabel: entry.title },
          );
      } else {
        const result = await api.previewCatalogGrouping(title, selected, signal);
        if (signal.aborted) return;
        setPreview(result);
        const url = new URL(window.location.href);
        url.searchParams.set("groupingPreview", String(result.previewId));
        window.history.replaceState(window.history.state, "", url);
      }
    } catch (error) {
      if (signal.aborted) return;
      setFailed(true);
      toast.notify(toastFromError(error, t("catalogSeries.stale")));
    } finally {
      if (!signal.aborted) setBusy(false);
    }
  };
  const revise = () => {
    setPreview(null);
    setFailed(false);
    const url = new URL(window.location.href);
    url.searchParams.delete("groupingPreview");
    window.history.replaceState(window.history.state, "", url);
  };
  if (!open)
    return (
      <Button variant="outline" onClick={() => setOpen(true)}>
        {t("catalogSeries.group")}
      </Button>
    );
  return (
    <section className="space-y-3 rounded-lg border p-4" aria-label={t("catalogSeries.group")}>
      <p className="text-sm text-muted-foreground">{t("catalogSeries.hint")}</p>
      {preview ? (
        <>
          <h2 className="font-semibold">{preview.title}</h2>
          <ol aria-label={t("catalogSeries.preview")}>
            {preview.episodes.map((episode) => (
              <li key={episode.catalogEntryId}>
                {episode.position}. {episode.title} · {episode.sourceName}
              </li>
            ))}
          </ol>
          <Button variant="outline" disabled={busy} onClick={revise}>
            {t("catalogSeries.revise")}
          </Button>
        </>
      ) : (
        <>
          <label className="block">
            {t("catalogSeries.title")}
            <Input value={title} maxLength={128} onChange={(event) => setTitle(event.target.value)} />
          </label>
          <label className="block">
            {t("catalogSeries.search")}
            <Input
              value={query}
              onChange={(event) => {
                setQuery(event.target.value);
                setPage(1);
              }}
            />
          </label>
          <p>{t("catalogSeries.selected", { count: selected.length })}</p>
          {candidates.map((item) => (
            <label key={item.catalogEntryId} className="flex items-center gap-2">
              <input
                type="checkbox"
                checked={selected.includes(item.catalogEntryId!)}
                disabled={
                  item.catalogEntryId === entry.catalogEntryId ||
                  (selected.length >= 100 && !selected.includes(item.catalogEntryId!))
                }
                onChange={(event) =>
                  setSelected((ids) =>
                    event.target.checked
                      ? [...ids, item.catalogEntryId!]
                      : ids.filter((id) => id !== item.catalogEntryId),
                  )
                }
              />
              {item.title}
            </label>
          ))}
          <div className="flex gap-2">
            <Button variant="outline" disabled={page <= 1} onClick={() => setPage((n) => n - 1)}>
              {t("catalogSeries.previous")}
            </Button>
            <Button variant="outline" disabled={page * 24 >= total} onClick={() => setPage((n) => n + 1)}>
              {t("catalogSeries.next")}
            </Button>
          </div>
        </>
      )}
      <Button disabled={busy || (!preview && (!title.trim() || selected.length < 2))} onClick={() => void submit()}>
        {t(preview ? "catalogSeries.confirm" : "catalogSeries.preview")}
      </Button>
      {failed && <p role="alert">{t("catalogSeries.stale")}</p>}
    </section>
  );
}
