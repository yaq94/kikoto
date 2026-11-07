import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { changeApiSession } from "@/lib/apiSession";
import { watchCatalogExclusionTask, type CatalogExclusionTask } from "./catalogExclusions";

const fetchMock = vi.fn<typeof fetch>();
const controllers: AbortController[] = [];

function run(status: string, sourceId = 7, id = 41) {
  return {
    id,
    workflowCode: "source_catalog_exclusion",
    status,
    canManage: true,
    summaryJson: "{}",
    nodeRuns: [{ nodeType: "source_catalog_exclusion", inputJson: JSON.stringify({ sourceId }) }],
  };
}

function watch(runId: number | null = 41) {
  const controller = new AbortController();
  controllers.push(controller);
  const tasks: (CatalogExclusionTask | null)[] = [];
  const onComplete = vi.fn();
  const onError = vi.fn();
  const done = watchCatalogExclusionTask({
    sourceId: 7,
    runId,
    signal: controller.signal,
    onTask: (task) => tasks.push(task),
    onComplete,
    onError,
  });
  return { controller, tasks, onComplete, onError, done };
}

beforeEach(() => {
  vi.useFakeTimers();
  fetchMock.mockReset();
  vi.stubGlobal("fetch", fetchMock);
  vi.stubGlobal("localStorage", { getItem: () => null });
  changeApiSession();
});

afterEach(async () => {
  controllers.splice(0).forEach((controller) => controller.abort());
  await vi.runAllTimersAsync();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("catalog exclusion task observation", () => {
  it("keeps a saved change processing until the public run reports success", async () => {
    fetchMock
      .mockResolvedValueOnce(Response.json(run("queued")))
      .mockResolvedValueOnce(Response.json(run("running")))
      .mockResolvedValueOnce(Response.json(run("succeeded")));
    const observed = watch();
    await vi.advanceTimersByTimeAsync(0);
    expect(observed.tasks[observed.tasks.length - 1]).toMatchObject({ runId: 41, status: "queued" });
    expect(observed.onComplete).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(2000);
    expect(observed.tasks[observed.tasks.length - 1]).toMatchObject({ status: "running" });
    expect(observed.onComplete).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(2000);
    await observed.done;
    expect(observed.tasks[observed.tasks.length - 1]).toMatchObject({ status: "succeeded" });
    expect(observed.onComplete).toHaveBeenCalledOnce();
    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual(Array(3).fill("/api/workflow-runs/41"));
  });

  it("recovers the source's saved task across pages without creating work", async () => {
    fetchMock
      .mockResolvedValueOnce(Response.json({ runs: [{ id: 50 }], total: 2, page: 1, pageSize: 1 }))
      .mockResolvedValueOnce(Response.json(run("running", 8, 50)))
      .mockResolvedValueOnce(Response.json({ runs: [{ id: 41 }], total: 2, page: 2, pageSize: 1 }))
      .mockResolvedValueOnce(Response.json(run("cancelled")));
    const observed = watch(null);
    await vi.advanceTimersByTimeAsync(2000);
    await observed.done;
    expect(observed.tasks[observed.tasks.length - 1]).toEqual({
      runId: 41,
      status: "cancelled",
      canRetry: true,
      pauseReason: "",
    });
    expect(observed.onComplete).not.toHaveBeenCalled();
    expect(fetchMock.mock.calls.every(([, init]) => !init?.method || init.method === "GET")).toBe(true);
  });

  it("keeps failures recoverable but respects the run owner's permissions", async () => {
    fetchMock.mockResolvedValueOnce(Response.json({ ...run("failed"), canManage: false }));
    const observed = watch();
    await observed.done;
    expect(observed.tasks[observed.tasks.length - 1]).toMatchObject({ status: "failed", canRetry: false });
    expect(observed.onComplete).not.toHaveBeenCalled();
  });

  it("reports an allowlisted disk pause without exposing private details", async () => {
    fetchMock.mockImplementation(async () =>
      Response.json({ ...run("queued"), summaryJson: '{"reason":"low_disk_space"}' }),
    );
    const observed = watch();
    await vi.advanceTimersByTimeAsync(0);
    expect(observed.tasks[0]).toMatchObject({ status: "queued", pauseReason: "low_space" });
    expect(observed.onComplete).not.toHaveBeenCalled();
  });

  it("backs off status failures and retries reads without completing or creating work", async () => {
    fetchMock
      .mockResolvedValueOnce(Response.json({ error: "private upstream detail" }, { status: 503 }))
      .mockResolvedValueOnce(Response.json(run("succeeded")));
    const observed = watch();
    await vi.advanceTimersByTimeAsync(0);
    expect(observed.onError).toHaveBeenCalledWith();
    expect(observed.onComplete).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(4000);
    await observed.done;
    expect(observed.onComplete).toHaveBeenCalledOnce();
  });

  it("stops after three failed reads until the caller explicitly refreshes", async () => {
    fetchMock.mockImplementation(async () => Response.json({ error: "private upstream detail" }, { status: 503 }));
    const observed = watch();
    await vi.advanceTimersByTimeAsync(60000);
    await observed.done;
    expect(fetchMock).toHaveBeenCalledTimes(3);
    expect(observed.onComplete).not.toHaveBeenCalled();
    expect(observed.tasks).toEqual([]);
  });

  it("does not accept success from a task belonging to another source", async () => {
    fetchMock.mockImplementation(async () => Response.json(run("succeeded", 8)));
    const observed = watch();
    await vi.advanceTimersByTimeAsync(60000);
    await observed.done;
    expect(observed.tasks).toEqual([]);
    expect(observed.onComplete).not.toHaveBeenCalled();
    expect(observed.onError).toHaveBeenCalled();
  });

  it("keeps an unfamiliar terminal status unconfirmed and hides unrecognized diagnostics", async () => {
    fetchMock.mockResolvedValueOnce(
      Response.json({ ...run("skipped"), summaryJson: '{"reason":"private upstream detail"}' }),
    );
    const observed = watch();
    await observed.done;
    expect(observed.tasks).toEqual([{ runId: 41, status: "unknown", canRetry: false, pauseReason: "" }]);
    expect(observed.onComplete).not.toHaveBeenCalled();
  });

  it("cancels an in-flight poll and ignores its late response after leaving the source", async () => {
    let settle!: (response: Response) => void;
    fetchMock.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          settle = resolve;
        }),
    );
    const observed = watch();
    await vi.advanceTimersByTimeAsync(0);
    await vi.advanceTimersByTimeAsync(60000);
    expect(fetchMock).toHaveBeenCalledOnce();
    observed.controller.abort();
    expect(fetchMock.mock.calls[0][1]?.signal?.aborted).toBe(true);
    settle(Response.json(run("succeeded")));
    await observed.done;
    expect(observed.tasks).toEqual([]);
    expect(observed.onComplete).not.toHaveBeenCalled();
    expect(observed.onError).not.toHaveBeenCalled();
  });

  it("stops old polling when a session changes, even without a component rerender", async () => {
    fetchMock.mockResolvedValue(Response.json(run("running")));
    const observed = watch();
    await vi.advanceTimersByTimeAsync(0);
    changeApiSession();
    await vi.advanceTimersByTimeAsync(60000);
    await observed.done;
    expect(fetchMock).toHaveBeenCalledOnce();
    expect(observed.onError).not.toHaveBeenCalled();
  });
});
