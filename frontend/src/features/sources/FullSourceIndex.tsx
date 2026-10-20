import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useStableCallback } from "@/hooks/useStableCallback";
import { useAuth } from "@/auth/AuthProvider";
import { api, type CatalogIndexOptions, type CatalogIndexProgress } from "@/lib/api";
import { sidecarFailureSummary } from "./sourceIndexSidecars";

const fields: Array<{ key: keyof CatalogIndexOptions; min: number; max: number }> = [
  { key: "concurrency", min: 1, max: 4 },
  { key: "intervalMs", min: 100, max: 60000 },
  { key: "maxResponseBytes", min: 1024, max: 8388608 },
  { key: "pageSize", min: 1, max: 500 },
  { key: "pagesPerBatch", min: 1, max: 32 },
  { key: "maxPagesPerDirectory", min: 1, max: 100000 },
  { key: "minFreeBytes", min: 0, max: 1099511627776 },
];

export function FullSourceIndex({ sourceId, onDone }: { sourceId: number; onDone: () => void }) {
  const { t } = useTranslation();
  const auth = useAuth();
  const [progress, setProgress] = useState<CatalogIndexProgress | null>(null);
  const [options, setOptions] = useState<CatalogIndexOptions | null>(null);
  const [page, setPage] = useState(1);
  const [refresh, setRefresh] = useState(0);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(false);
  const [reopening, setReopening] = useState(false);
  const indexed = useRef(-1);
  const notify = useStableCallback(onDone);
  const active = progress?.status === "queued" || progress?.status === "running";
  useEffect(() => {
    const abort = new AbortController();
    let timer: ReturnType<typeof setTimeout> | undefined;
    const load = async () => {
      try {
        const result = await api.getCatalogIndex(sourceId, page, abort.signal);
        if (abort.signal.aborted) return;
        setProgress(result);
        setOptions((current) => current ?? result.options);
        setError(false);
        if (indexed.current >= 0 && indexed.current !== result.indexedEntries) notify();
        indexed.current = result.indexedEntries;
        if (result.status === "queued" || result.status === "running") timer = setTimeout(load, 2000);
      } catch {
        if (!abort.signal.aborted) setError(true);
      }
    };
    void load();
    return () => {
      abort.abort();
      clearTimeout(timer);
    };
  }, [sourceId, page, refresh, notify]);
  const start = async () => {
    if (!options) return;
    setBusy(true);
    try {
      await api.startCatalogIndex(sourceId, options);
      setRefresh((n) => n + 1);
    } catch {
      setError(true);
    } finally {
      setBusy(false);
    }
  };
  const cancel = async () => {
    if (!progress) return;
    setBusy(true);
    try {
      await api.cancelWorkflowRun(progress.runId);
      setRefresh((n) => n + 1);
    } catch {
      setError(true);
    } finally {
      setBusy(false);
    }
  };
  const reopen = async () => {
    setBusy(true);
    setReopening(true);
    try {
      await api.retryCatalogSidecars(sourceId);
      setRefresh((n) => n + 1);
    } catch {
      setError(true);
    } finally {
      setReopening(false);
      setBusy(false);
    }
  };
  const resume = progress && !["idle", "complete", "queued", "running"].includes(progress.status);
  const pages = progress ? Math.max(1, Math.ceil(Math.max(progress.partitionTotal, progress.detailsTotal) / 50)) : 1;
  const stoppedSidecars = sidecarFailureSummary(progress?.sidecarFailures);
  const canReopenSidecars = auth.hasPermission("sources:write") && Boolean(stoppedSidecars) && !busy;
  return (
    <section aria-label={t("sourceIndex.title")} className="w-full space-y-3 border-t border-border pt-3">
      <div className="flex flex-wrap items-center gap-2">
        <Button variant="outline" disabled={active || busy || !options} onClick={() => void start()}>
          {t(resume ? "sourceIndex.resume" : "sourceIndex.start")}
        </Button>
        {active && (
          <Button variant="outline" disabled={busy} onClick={() => void cancel()}>
            {t("common.cancel")}
          </Button>
        )}
        <Button variant="ghost" disabled={busy} onClick={() => setRefresh((n) => n + 1)}>
          {t("sourceIndex.refresh")}
        </Button>
        {progress && (
          <p role="status" className="text-sm">
            {t(`sourceIndex.state.${progress.status}`)}
          </p>
        )}
      </div>
      {error && (
        <p role="alert" className="text-sm text-destructive">
          {t("sourceIndex.error")}
        </p>
      )}
      {progress?.pauseReason && (
        <p className="text-sm text-muted-foreground">{t(`sourceIndex.reason.${progress.pauseReason}`)}</p>
      )}
      {stoppedSidecars && (
        <div className="space-y-1 rounded-[var(--control-radius)] border border-border p-2 text-sm">
          <p>
            {t("sourceIndex.sidecarStopped", { count: stoppedSidecars.stopped })}
            {stoppedSidecars.categories.length > 0 &&
              ` · ${stoppedSidecars.categories
                .map(([category, count]) => t(`sourceIndex.sidecarCategory.${category}`, { count }))
                .join(" · ")}`}
          </p>
          <Button variant="outline" size="sm" disabled={!canReopenSidecars} onClick={() => void reopen()}>
            {t(reopening ? "sourceIndex.sidecarReopening" : "sourceIndex.sidecarReopen")}
          </Button>
        </div>
      )}
      <details>
        <summary className="cursor-pointer text-sm">{t("sourceIndex.limits")}</summary>
        <p className="my-2 text-xs text-muted-foreground">{t("sourceIndex.limitsHint")}</p>
        {options && (
          <fieldset disabled={active || busy} className="grid gap-3 sm:grid-cols-2">
            {fields.map(({ key, min, max }) => (
              <label key={key} className="grid gap-1 text-sm">
                <span>{t(`sourceIndex.field.${key}`)}</span>
                <Input
                  type="number"
                  min={min}
                  max={max}
                  value={options[key]}
                  onChange={(event) => setOptions({ ...options, [key]: Number(event.target.value) })}
                />
              </label>
            ))}
          </fieldset>
        )}
      </details>
      {progress && progress.status !== "idle" && (
        <>
          <p className="text-sm">
            {t("sourceIndex.counts", {
              completed: progress.completedDirectories,
              pending: progress.pendingDirectories,
              entries: progress.observedEntries,
              indexed: progress.indexedEntries,
            })}
          </p>
          <p className="text-sm text-muted-foreground">
            {t("sourceIndex.issues", {
              failed: progress.failedDirectories,
              restricted: progress.restrictedDirectories,
              empty: progress.unconfirmedEmptyDirectories,
            })}
          </p>
          <p className="text-xs text-muted-foreground">
            {t("sourceIndex.times", {
              started: progress.startedAt,
              updated: progress.updatedAt,
              requests: progress.requests,
            })}
          </p>
          <div className="overflow-x-auto">
            <table className="w-full text-left text-sm">
              <caption className="text-left">{t("sourceIndex.partitions")}</caption>
              <thead>
                <tr>
                  <th>{t("sourceIndex.path")}</th>
                  <th>{t("sourceIndex.completed")}</th>
                  <th>{t("sourceIndex.failed")}</th>
                  <th>{t("sourceIndex.restricted")}</th>
                  <th>{t("sourceIndex.empty")}</th>
                </tr>
              </thead>
              <tbody>
                {progress.partitions.map((part) => (
                  <tr key={part.path}>
                    <td>{part.path}</td>
                    <td>
                      {part.completed} / {part.directories}
                    </td>
                    <td>{part.failed}</td>
                    <td>{part.restricted}</td>
                    <td>{part.unconfirmedEmpty}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <ul className="space-y-1 text-sm">
            {progress.details.map((dir) => (
              <li key={dir.path}>
                {dir.path} — {t(`sourceIndex.state.${dir.status}`)}
                {dir.errorCode && <> · {t(`sourceIndex.reason.${dir.errorCode}`)}</>}
              </li>
            ))}
          </ul>
          {pages > 1 && (
            <div className="flex gap-2">
              <Button variant="ghost" disabled={page <= 1} onClick={() => setPage(page - 1)}>
                {t("sourceIndex.previous")}
              </Button>
              <span>
                {page} / {pages}
              </span>
              <Button variant="ghost" disabled={page >= pages} onClick={() => setPage(page + 1)}>
                {t("sourceIndex.next")}
              </Button>
            </div>
          )}
        </>
      )}
    </section>
  );
}
