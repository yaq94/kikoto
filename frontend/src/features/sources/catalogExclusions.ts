import { deleteJSON, getJSON, postJSONBody } from "@/lib/apiTransport";
import { apiSessionSignal } from "@/lib/apiSession";
import { combineAbortSignals } from "@/lib/inflightRequests";
import { workflowApi, type WorkflowRunDetail } from "@/lib/workflowApi";

export type CatalogExclusion = {
  id: number;
  path: string;
  /** `whole` or `whitelist`; read `catalogExclusionMode` for the normalised value. */
  mode: string;
  action: string;
  note: string;
  enabled: boolean;
  createdAt: string;
};

/**
 * `whole` removes the path and everything below it. `whitelist` is a subtree
 * exception: it keeps a path indexed even when an ancestor is excluded.
 */
export type CatalogExclusionMode = "whole" | "whitelist";

export const CATALOG_EXCLUSION_MODES: readonly CatalogExclusionMode[] = ["whole", "whitelist"];

export type CatalogExclusionInput = {
  path: string;
  mode?: CatalogExclusionMode;
  note?: string;
  /** The server creates rules enabled; the field is part of the wire contract. */
  enabled?: boolean;
};

/** Unknown or empty values from older rows fall back to the default rule. */
export function catalogExclusionMode(value: string | undefined): CatalogExclusionMode {
  return value === "whitelist" ? "whitelist" : "whole";
}

export type CatalogExclusionPathProblem = "empty" | "relative" | "parent" | "root" | null;

/** The server repeats this validation; the form needs the same answer without a round trip. */
export function catalogExclusionPathProblem(value: string): CatalogExclusionPathProblem {
  const trimmed = value.trim();
  if (!trimmed) return "empty";
  if (!trimmed.startsWith("/")) return "relative";
  if (trimmed.split("/").includes("..")) return "parent";
  if (normalizeCatalogExclusionPath(trimmed) === "") return "root";
  return null;
}

export function normalizeCatalogExclusionPath(value: string): string {
  return value.trim().replace(/\/+/g, "/").replace(/\/+$/, "");
}

export const catalogExclusionApi = {
  list: (sourceId: number, signal?: AbortSignal) =>
    getJSON<CatalogExclusion[]>(`/api/remote-sources/${sourceId}/catalog/exclusions`, signal),
  create: (sourceId: number, input: CatalogExclusionInput) =>
    postJSONBody<CatalogExclusion & { runId: number }>(`/api/remote-sources/${sourceId}/catalog/exclusions`, input),
  remove: (sourceId: number, exclusionId: number) =>
    deleteJSON<{ ok: boolean; path: string; mode: string; runId: number }>(
      `/api/remote-sources/${sourceId}/catalog/exclusions/${exclusionId}`,
    ),
};

export type CatalogExclusionTask = {
  runId: number;
  status: "saved" | "queued" | "running" | "succeeded" | "failed" | "cancelled" | "partial" | "unknown";
  canRetry: boolean;
  pauseReason: "low_space" | "source_disabled" | "maintenance" | "";
};

const exclusionWorkflow = "source_catalog_exclusion";

function belongsToSource(run: WorkflowRunDetail, sourceId: number) {
  return (
    run.workflowCode === exclusionWorkflow &&
    run.nodeRuns.some((node) => {
      if (node.nodeType !== exclusionWorkflow) return false;
      try {
        return JSON.parse(node.inputJson)?.sourceId === sourceId;
      } catch {
        return false;
      }
    })
  );
}

function exclusionTask(run: WorkflowRunDetail): CatalogExclusionTask {
  const statuses = ["queued", "running", "succeeded", "failed", "cancelled", "partial"];
  const status = statuses.includes(run.status) ? (run.status as CatalogExclusionTask["status"]) : "unknown";
  let reason: unknown;
  try {
    reason = JSON.parse(run.summaryJson)?.reason;
  } catch {
    /* No public pause reason. */
  }
  return {
    runId: run.id,
    status,
    canRetry: run.canManage === true && ["failed", "cancelled", "partial"].includes(status),
    pauseReason:
      reason === "low_disk_space"
        ? "low_space"
        : reason === "source_disabled" || reason === "maintenance"
          ? reason
          : "",
  };
}

// The Activity list has no source filter. Inspect one bounded page at a time,
// newest first, and match its public node input instead of a title or path.
async function recoverExclusionTask(sourceId: number, signal: AbortSignal) {
  for (let page = 1; ; page++) {
    const result = await workflowApi.listWorkflowRuns(page, 25, "all", "", exclusionWorkflow, signal);
    for (const item of result.runs) {
      const run = await workflowApi.getWorkflowRun(item.id, signal);
      signal.throwIfAborted();
      if (belongsToSource(run, sourceId)) return run;
    }
    if (result.runs.length === 0 || result.page * result.pageSize >= result.total) return null;
    await waitForTaskPoll(signal, 2000);
  }
}

/** Observe the saved operation; only its succeeded status confirms publication. */
export async function watchCatalogExclusionTask(options: {
  sourceId: number;
  runId: number | null;
  signal: AbortSignal;
  onTask: (task: CatalogExclusionTask | null) => void;
  onComplete: () => void;
  onError: () => void;
}) {
  const lifetime = combineAbortSignals(options.signal, apiSessionSignal());
  const { signal } = lifetime;
  let runId = options.runId;
  let failures = 0;
  let paused = false;
  try {
    while (!signal.aborted) {
      try {
        const run = runId
          ? await workflowApi.getWorkflowRun(runId, signal)
          : await recoverExclusionTask(options.sourceId, signal);
        signal.throwIfAborted();
        if (!run) {
          options.onTask(null);
          return;
        }
        if (!belongsToSource(run, options.sourceId)) throw new Error("Task source mismatch");
        const recoveredSuccess = !runId && run.status === "succeeded";
        runId = run.id;
        failures = 0;
        const task = exclusionTask(run);
        paused = Boolean(task.pauseReason);
        options.onTask(recoveredSuccess ? null : task);
        if (run.status === "succeeded") options.onComplete();
        if (run.status !== "queued" && run.status !== "running") return;
      } catch {
        if (signal.aborted) return;
        options.onError();
        if (++failures >= 3) return;
      }
      // Reads are strictly sequential. Hidden pages and deferred work poll less often.
      const hidden = typeof document !== "undefined" && document.hidden;
      await waitForTaskPoll(signal, failures ? Math.min(30000, 2000 * 2 ** failures) : hidden || paused ? 15000 : 2000);
    }
  } catch {
    if (!signal.aborted) options.onError();
  } finally {
    lifetime.dispose();
  }
}

function waitForTaskPoll(signal: AbortSignal, ms: number) {
  return new Promise<void>((resolve, reject) => {
    signal.throwIfAborted();
    const cancel = () => {
      clearTimeout(timer);
      reject(signal.reason);
    };
    const timer = setTimeout(() => {
      signal.removeEventListener("abort", cancel);
      resolve();
    }, ms);
    signal.addEventListener("abort", cancel, { once: true });
  });
}
