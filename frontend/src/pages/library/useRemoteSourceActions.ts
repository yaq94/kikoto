import type { TFunction } from "i18next";
import { useState } from "react";

import {
  REMOTE_BULK_FETCH_PERMISSIONS,
  REMOTE_BULK_TRACK_PERMISSIONS,
  REMOTE_TRACK_PERMISSIONS,
  usePermissionGate,
} from "@/auth/usePermissionGate";
import { toastFromError, type useToast } from "@/components/ui/toast";
import { useRemoteFetchWorkspace } from "@/features/work-detail/workflows/useRemoteFetchWorkspace";
import { catalogCardQueue } from "@/features/work-detail/catalogCardQueue";
import { usePlayer } from "@/player/PlayerProvider";
import { api, type LibrarySource, type ListeningStatus, type RemoteWork } from "@/lib/api";
import { libraryBulkCopy, useRemoteWorkActions } from "@/pages/useRemoteWorkActions";

import { remoteWorkActionCode, remoteWorkRowKey } from "./remoteSourceBrowseModel";

export function useRemoteSourceActions({
  source,
  selectedSyncable,
  selectedSaveable,
  toast,
  t,
  onWorkStateChanged,
  onSynced,
}: {
  source: LibrarySource;
  selectedSyncable: RemoteWork[];
  selectedSaveable: RemoteWork[];
  toast: ReturnType<typeof useToast>;
  t: TFunction;
  /** Patches one card by its row key; a numberless card has no code to match on. */
  onWorkStateChanged: (
    rowKey: string,
    patch: Partial<Pick<RemoteWork, "workId" | "favorite" | "listeningStatus">>,
  ) => void;
  onSynced: (workID: number, options?: { openTracked?: boolean }) => Promise<void>;
}) {
  const requireBulkFetch = usePermissionGate(REMOTE_BULK_FETCH_PERMISSIONS);
  const requireBulkTrack = usePermissionGate(REMOTE_BULK_TRACK_PERMISSIONS, { deferDemo: true });
  const requireTrack = usePermissionGate(REMOTE_TRACK_PERMISSIONS, { deferDemo: true });
  const [syncingWorkKey, setSyncingWorkKey] = useState<string | null>(null);
  const [queueingCatalogId, setQueueingCatalogId] = useState<number | null>(null);
  const player = usePlayer();
  const [isBulkBusy, setIsBulkBusy] = useState(false);
  const [saveConfirm, setSaveConfirm] = useState<{ codes: string[]; run: () => Promise<void> } | null>(null);
  const fetchWorkspace = useRemoteFetchWorkspace({ onWorksChanged: () => onSynced(0) });
  const remoteWorkActions = useRemoteWorkActions();
  const remoteTargets = (works: readonly RemoteWork[]) =>
    works.map((work) => ({ sourceId: source.id, code: remoteWorkActionCode(work) }));

  const forkWork = async (work: RemoteWork, reason: string) => {
    if (!work.primaryCode) {
      toast.warning(t("library.remoteWorkNoCode"));
      return;
    }
    if (!requireTrack()) return;
    setSyncingWorkKey(remoteWorkRowKey(work));
    try {
      await remoteWorkActions.queueFork({ sourceId: source.id, code: remoteWorkActionCode(work) }, reason);
    } finally {
      setSyncingWorkKey(null);
    }
  };

  const runBulkSaveSelected = async () => {
    if (!requireBulkFetch()) return;
    setIsBulkBusy(true);
    try {
      await remoteWorkActions.recordBulkRuns("fetch", remoteTargets(selectedSaveable), libraryBulkCopy, () =>
        onSynced(0),
      );
    } finally {
      setIsBulkBusy(false);
      setSaveConfirm(null);
    }
  };

  const bulkForkSelected = async () => {
    if (selectedSyncable.length === 0 || !requireBulkTrack()) return;
    setIsBulkBusy(true);
    try {
      await remoteWorkActions.recordBulkRuns("track", remoteTargets(selectedSyncable), libraryBulkCopy, () =>
        onSynced(0),
      );
    } finally {
      setIsBulkBusy(false);
    }
  };

  const bulkSaveSelected = async () => {
    if (selectedSaveable.length === 0 || !requireBulkFetch()) return;
    setSaveConfirm({ codes: selectedSaveable.map((work) => work.primaryCode), run: runBulkSaveSelected });
  };

  // A catalog row has no work until someone keeps a record of it. Registering
  // it is the same step a rating takes, without writing either field.
  const ensureCatalogWork = async (work: RemoteWork): Promise<number | null> => {
    if (!work.catalogEntryId) return null;
    setSyncingWorkKey(remoteWorkRowKey(work));
    try {
      const result = await api.registerCatalogWork(work.catalogEntryId);
      onWorkStateChanged(remoteWorkRowKey(work), { workId: result.workId });
      return result.workId;
    } catch (error) {
      toast.notify(toastFromError(error, t("errors.unavailable")));
      return null;
    } finally {
      setSyncingWorkKey(null);
    }
  };

  /** Queues every published track of a catalog entry, which has no work of its own. */
  const queueCatalogWork = async (work: RemoteWork) => {
    if (!work.catalogEntryId) return;
    setQueueingCatalogId(work.catalogEntryId);
    try {
      const queue = await catalogCardQueue({
        catalogEntryId: work.catalogEntryId,
        workId: work.workId,
        sourceId: source.id,
        sourceName: source.displayName || source.code,
        primaryCode: work.primaryCode,
        title: work.title,
      });
      if (!queue) {
        toast.info(t("library.catalogEmpty"));
        return;
      }
      player.appendQueue(queue.tracks);
      toast.success(t("library.catalogQueued", { count: queue.tracks.length }));
    } catch (error) {
      toast.notify(toastFromError(error, t("errors.unavailable")));
    } finally {
      setQueueingCatalogId(null);
    }
  };

  const ensureRemoteWorkForState = async (work: RemoteWork, reason: string) => {
    const result = await api.syncRemoteSourceWork(source.id, remoteWorkActionCode(work), reason);
    onWorkStateChanged(remoteWorkRowKey(work), { workId: result.workId });
    await onSynced(result.workId);
    return result.workId;
  };

  /** A mark or list on a work not yet in the Library adds it there first, which is tracking. */
  const canStateRemoteWork = (work: RemoteWork) => Boolean(work.workId) || requireTrack();

  const markRemoteWork = async (work: RemoteWork, status: ListeningStatus) => {
    // A catalog card may have no printed code at all; it needs a work id, not a
    // code, so the guard asks for identity rather than a label.
    if (!work.workId && !work.primaryCode && !work.catalogEntryId) return;
    if (!canStateRemoteWork(work)) return;
    setSyncingWorkKey(remoteWorkRowKey(work));
    try {
      const workId =
        work.workId ??
        (work.catalogEntryId ? await ensureCatalogWork(work) : await ensureRemoteWorkForState(work, "mark_interest"));
      if (!workId) return;
      await api.updateWorkUserState(workId, { listeningStatus: status });
      onWorkStateChanged(remoteWorkRowKey(work), { workId, listeningStatus: status });
      toast.success(t("library.savedAndMarked", { code: work.primaryCode || work.title }));
      await onSynced(workId);
    } catch (error) {
      toast.notify(toastFromError(error, t("errors.unavailable")));
    } finally {
      setSyncingWorkKey(null);
    }
  };

  const ensureRemoteWorkForList = async (work: RemoteWork) => {
    if (work.workId) return work.workId;
    if (work.catalogEntryId) return ensureCatalogWork(work);
    if (!work.primaryCode || !canStateRemoteWork(work)) return null;
    setSyncingWorkKey(remoteWorkRowKey(work));
    try {
      const result = await api.syncRemoteSourceWork(source.id, remoteWorkActionCode(work), "list_remote");
      toast.success(t("library.savedForList", { code: result.primaryCode }));
      return result.workId;
    } catch (error) {
      toast.notify(toastFromError(error, t("errors.unavailable")));
      return null;
    } finally {
      setSyncingWorkKey(null);
    }
  };

  return {
    fetchWorkspace,
    syncingWorkKey,
    isBulkBusy,
    saveConfirm,
    clearSaveConfirm: () => setSaveConfirm(null),
    forkWork,
    bulkForkSelected,
    bulkSaveSelected,
    runBulkSaveSelected,
    markRemoteWork,
    canStateRemoteWork,
    ensureRemoteWorkForList,
    queueCatalogWork,
    queueingCatalogId,
  };
}
