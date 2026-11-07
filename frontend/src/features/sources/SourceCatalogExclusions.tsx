import { Plus, Trash2 } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { useAuth } from "@/auth/AuthProvider";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useStableCallback } from "@/hooks/useStableCallback";
import { apiSessionVersion } from "@/lib/apiSession";
import { workflowApi } from "@/lib/workflowApi";

import {
  CATALOG_EXCLUSION_MODES,
  catalogExclusionApi,
  catalogExclusionMode,
  catalogExclusionPathProblem,
  normalizeCatalogExclusionPath,
  watchCatalogExclusionTask,
  type CatalogExclusion,
  type CatalogExclusionMode,
  type CatalogExclusionTask,
} from "./catalogExclusions";

/**
 * Directory rules that keep a subtree out of the catalog index. Removing a rule
 * only makes the path discoverable again; the next scan decides what is there.
 */
export function SourceCatalogExclusions({ sourceId, onDone }: { sourceId: number; onDone: () => void }) {
  const auth = useAuth();
  if (!auth.hasPermission("sources:write") || auth.demoMode) return null;
  const scope = JSON.stringify([sourceId, auth.user?.id, auth.user?.permissions, apiSessionVersion()]);
  return <CatalogExclusionsForm key={scope} sourceId={sourceId} onDone={onDone} />;
}

function CatalogExclusionsForm({ sourceId, onDone }: { sourceId: number; onDone: () => void }) {
  const { t } = useTranslation();
  const auth = useAuth();
  const canObserve = auth.hasPermission("workflows:run");
  const [session] = useState(apiSessionVersion);
  const mounted = useRef(false);
  const current = () => mounted.current && session === apiSessionVersion();
  const notify = useStableCallback(onDone);
  const [items, setItems] = useState<CatalogExclusion[] | null>(null);
  const [path, setPath] = useState("");
  const [mode, setMode] = useState<CatalogExclusionMode>("whole");
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [deleting, setDeleting] = useState<number | null>(null);
  const [error, setError] = useState(false);
  const [listError, setListError] = useState(false);
  const [refresh, setRefresh] = useState(0);
  const [runId, setRunId] = useState<number | null>(null);
  const [task, setTask] = useState<CatalogExclusionTask | null>(null);
  const [taskRefresh, setTaskRefresh] = useState(0);
  const [checking, setChecking] = useState(canObserve);
  const [taskError, setTaskError] = useState(false);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  useEffect(() => {
    if (!canObserve) return;
    const abort = new AbortController();
    setChecking(true);
    void watchCatalogExclusionTask({
      sourceId,
      runId,
      signal: abort.signal,
      onTask: (next) => {
        setTask(next);
        setTaskError(false);
        setChecking(false);
      },
      onError: () => {
        setTaskError(true);
        setChecking(false);
      },
      onComplete: () => {
        setRefresh((value) => value + 1);
        notify();
      },
    });
    return () => abort.abort();
  }, [sourceId, canObserve, runId, taskRefresh, notify]);

  useEffect(() => {
    const abort = new AbortController();
    setListError(false);
    catalogExclusionApi
      .list(sourceId, abort.signal)
      .then((result) => {
        if (!abort.signal.aborted && session === apiSessionVersion()) setItems(result);
      })
      .catch(() => {
        if (!abort.signal.aborted && session === apiSessionVersion()) setListError(true);
      });
    return () => abort.abort();
  }, [sourceId, session, refresh]);

  const locked =
    busy || deleting !== null || checking || taskError || (canObserve && task !== null && task.status !== "succeeded");

  const saved = (id: number) => {
    setTask({ runId: id, status: "saved", canRetry: false, pauseReason: "" });
    setRunId(id);
    setRefresh((value) => value + 1);
    setTaskRefresh((value) => value + 1);
  };
  const recover = () => {
    setRunId(null);
    setTaskRefresh((value) => value + 1);
    setRefresh((value) => value + 1);
  };

  const add = async () => {
    if (catalogExclusionPathProblem(path)) {
      setError(true);
      return;
    }
    setBusy(true);
    setError(false);
    try {
      const result = await catalogExclusionApi.create(sourceId, {
        path: normalizeCatalogExclusionPath(path),
        mode,
        note: note.trim(),
        enabled: true,
      });
      if (!current()) return;
      setPath("");
      setNote("");
      saved(result.runId);
    } catch {
      if (current()) {
        setError(true);
        recover();
      }
    } finally {
      if (current()) setBusy(false);
    }
  };

  const remove = async (exclusionId: number) => {
    setDeleting(exclusionId);
    setError(false);
    try {
      const result = await catalogExclusionApi.remove(sourceId, exclusionId);
      if (current()) saved(result.runId);
    } catch {
      if (current()) {
        setError(true);
        recover();
      }
    } finally {
      if (current()) setDeleting(null);
    }
  };

  const retry = async () => {
    if (!task?.canRetry || busy) return;
    setBusy(true);
    setError(false);
    try {
      const result = await workflowApi.retryWorkflowRun(task.runId);
      if (current()) saved(result.runId);
    } catch {
      if (current()) {
        setError(true);
        setTaskRefresh((value) => value + 1);
      }
    } finally {
      if (current()) setBusy(false);
    }
  };

  return (
    <section aria-label={t("sourceIndex.exclusions.title")} className="w-full space-y-2 border-t border-border pt-3">
      <p className="text-sm font-medium">{t("sourceIndex.exclusions.title")}</p>
      <p className="text-xs text-muted-foreground">{t("sourceIndex.exclusions.hint")}</p>
      <div className="flex flex-wrap items-end gap-2">
        <label className="grid min-w-52 flex-1 gap-1 text-sm">
          <span>{t("sourceIndex.exclusions.path")}</span>
          <Input
            value={path}
            placeholder={t("sourceIndex.exclusions.pathPlaceholder")}
            spellCheck={false}
            onChange={(event) => setPath(event.target.value)}
          />
        </label>
        <label className="grid min-w-40 gap-1 text-sm">
          <span>{t("sourceIndex.exclusions.mode")}</span>
          <select
            className="h-9 rounded-md border bg-background px-2 text-sm"
            aria-label={t("sourceIndex.exclusions.mode")}
            value={mode}
            disabled={busy}
            onChange={(event) => setMode(event.target.value === "whitelist" ? "whitelist" : "whole")}
          >
            {CATALOG_EXCLUSION_MODES.map((option) => (
              <option key={option} value={option}>
                {option === "whitelist"
                  ? t("sourceIndex.exclusions.modeWhitelist")
                  : t("sourceIndex.exclusions.modeWhole")}
              </option>
            ))}
          </select>
        </label>
        <label className="grid min-w-40 flex-1 gap-1 text-sm">
          <span>{t("sourceIndex.exclusions.note")}</span>
          <Input value={note} onChange={(event) => setNote(event.target.value)} />
        </label>
        <Button size="sm" disabled={locked} onClick={() => void add()}>
          <Plus className="h-4 w-4" />
          {t(busy ? "sourceIndex.exclusions.adding" : "sourceIndex.exclusions.add")}
        </Button>
      </div>
      {(checking || task) && (
        <p role="status" className="text-sm text-muted-foreground">
          {t(
            !canObserve
              ? "sourceIndex.exclusions.savedWithoutAccess"
              : checking && !task
                ? "sourceIndex.exclusions.checking"
                : `sourceIndex.exclusions.task.${task?.status ?? "unknown"}`,
          )}
          {task?.pauseReason && ` ${t(`sourceIndex.exclusions.pause.${task.pauseReason}`)}`}
        </p>
      )}
      {taskError && (
        <p role="alert" className="text-sm text-destructive">
          {t("sourceIndex.exclusions.statusUnavailable")}
        </p>
      )}
      {canObserve && task?.canRetry && (
        <Button variant="outline" size="sm" disabled={busy || checking} onClick={() => void retry()}>
          {t("sourceIndex.exclusions.retryTask")}
        </Button>
      )}
      {(error ||
        listError ||
        taskError ||
        task?.status === "unknown" ||
        (task && !task.canRetry && ["failed", "cancelled", "partial"].includes(task.status))) && (
        <Button
          variant="ghost"
          size="sm"
          disabled={busy || deleting !== null || checking}
          onClick={() => {
            setRefresh((value) => value + 1);
            setTaskRefresh((value) => value + 1);
          }}
        >
          {t("sourceIndex.refresh")}
        </Button>
      )}
      {(error || listError) && (
        <p role="alert" className="text-sm text-destructive">
          {t("sourceIndex.exclusions.failed")}
        </p>
      )}
      {items && items.length === 0 && (
        <p className="text-sm text-muted-foreground">{t("sourceIndex.exclusions.empty")}</p>
      )}
      {items && items.length > 0 && (
        <ul className="divide-y divide-border rounded-[var(--control-radius)] border border-border text-sm">
          {items.map((item) => (
            <li key={item.id} className="flex min-w-0 items-center gap-2 px-3 py-2">
              <span className="min-w-0 flex-1">
                <span className="block truncate font-medium">{item.path}</span>
                {item.note && <span className="block truncate text-xs text-muted-foreground">{item.note}</span>}
              </span>
              <span className="shrink-0 rounded-full border border-border px-2 py-0.5 text-xs text-muted-foreground">
                {catalogExclusionMode(item.mode) === "whitelist"
                  ? t("sourceIndex.exclusions.modeWhitelist")
                  : t("sourceIndex.exclusions.modeWhole")}
              </span>
              <Button
                variant="ghost"
                size="icon"
                className="h-8 w-8 text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
                aria-label={t("sourceIndex.exclusions.remove")}
                title={t("sourceIndex.exclusions.remove")}
                disabled={locked}
                onClick={() => void remove(item.id)}
              >
                <Trash2 className="h-4 w-4" />
              </Button>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
