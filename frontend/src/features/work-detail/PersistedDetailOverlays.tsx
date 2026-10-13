import type { ReactNode } from "react";

import type { ReforkTarget, RemoteSourceAvailability } from "@/features/work-detail/source/sourceContextModel";
import {
  api,
  type LibrarySource,
  type SourcePresenceItem,
  type WorkDetail,
  type WorkMetadataSyncRunResult,
} from "@/lib/api";
import type { TreeNode } from "@/features/work-detail/media/mediaTreeModel";
import i18n from "@/i18n";
import { FilePreviewDialog, type FilePreviewRequest } from "@/features/work-detail/dialogs/FilePreviewDialog";
import { toastFromError, type useToast } from "@/components/ui/toast";
import type { MediaCleanupMode, MediaDeleteTarget } from "@/features/work-detail/workflows/useMediaCleanupWorkflow";
import { DirectoryManagerDialog } from "@/features/work-detail/dialogs/DirectoryManagerDialog";
import { LyricsManagerDialog } from "@/features/work-detail/lyrics/LyricsManagerDialog";
import { WorkMetadataEditorModal, type MetadataEditorSection } from "@/features/work-detail/metadata";
import { ReforkConfirmDialog } from "@/features/work-detail/dialogs/ReforkConfirmDialog";
import { useAuth } from "@/auth/AuthProvider";

export function PersistedFilePreviewOverlay({
  preview,
  work,
  toast,
  onClose,
  onMetadataSaved,
}: {
  preview: FilePreviewRequest | null;
  work: WorkDetail | null;
  toast: ReturnType<typeof useToast>;
  onClose: () => void;
  onMetadataSaved: () => Promise<void>;
}) {
  if (!preview) return null;
  const onSetCover = work
    ? async (locationId: number) => {
        try {
          await api.setWorkCoverOverride(work.id, locationId);
          toast.success(i18n.t("libraryDetail.coverOverrideSaved"));
          onClose();
          await onMetadataSaved();
        } catch (error) {
          toast.notify(toastFromError(error, i18n.t("libraryDetail.coverOverrideSaveFailed")));
        }
      }
    : undefined;
  return <FilePreviewDialog request={preview} onClose={onClose} onSetCover={onSetCover} />;
}

export function PersistedDirectoryManagerOverlay({
  open,
  root,
  selectedTrackedPresence,
  showNoSourceDirectory,
  selectedRemoteSource,
  deleting,
  onDeleteTargets,
  workID,
  canForgetWork,
  localRoot,
  localFolders = [],
  onSelectLocalFolder,
  onClose,
}: {
  open: boolean;
  root: TreeNode;
  selectedTrackedPresence: SourcePresenceItem | null;
  showNoSourceDirectory: boolean;
  selectedRemoteSource: RemoteSourceAvailability | null | undefined;
  deleting: boolean;
  onDeleteTargets: (targets: MediaDeleteTarget[], mode: MediaCleanupMode) => void;
  workID: number;
  canForgetWork: boolean;
  localRoot: { folderId: number; path: string } | null;
  /** The work's local folders when it has several; file management then shows one at a time. */
  localFolders?: { id: number; rootPath: string }[];
  onSelectLocalFolder?: (folderId: number) => void;
  onClose: () => void;
}) {
  const { demoMode, hasPermission } = useAuth();
  if (!open) return null;
  // Comparing files reads the library, which the server allows only to users who manage its files.
  const canCheckDuplicates = !demoMode && hasPermission("downloads:manage");
  const title = selectedTrackedPresence ? i18n.t("libraryDetail.manageCache") : i18n.t("libraryDetail.manageFiles");
  const description = selectedTrackedPresence
    ? i18n.t("libraryDetail.reviewCachedFiles")
    : i18n.t("libraryDetail.reviewFileOperations");
  const emptyLabel = selectedTrackedPresence
    ? i18n.t("libraryDetail.noCachedFiles")
    : showNoSourceDirectory
      ? i18n.t("libraryDetail.noSourceLinked")
      : selectedRemoteSource
        ? i18n.t("libraryDetail.noRemoteFiles")
        : i18n.t("libraryDetail.noLocalFiles");
  return (
    <DirectoryManagerDialog
      root={root}
      title={title}
      description={description}
      emptyLabel={emptyLabel}
      onClose={onClose}
      deleting={deleting}
      onDeleteTargets={onDeleteTargets}
      workId={workID}
      canForgetWork={canForgetWork}
      allowCacheDelete={!selectedRemoteSource}
      allowLocalDelete={!selectedRemoteSource && !selectedTrackedPresence}
      duplicateWorkId={canCheckDuplicates ? workID : 0}
      localRoot={localRoot}
      localFolders={localFolders}
      onSelectLocalFolder={onSelectLocalFolder}
      showCachedFilter={Boolean(selectedTrackedPresence)}
    />
  );
}

export function PersistedLyricsManagerOverlay({
  open,
  work,
  remoteSources,
  onClose,
  onSaved,
}: {
  open: boolean;
  work: WorkDetail | null;
  remoteSources: RemoteSourceAvailability[];
  onClose: () => void;
  onSaved: () => Promise<void>;
}) {
  const { demoMode, hasPermission } = useAuth();
  if (!open || !work) return null;
  return (
    <LyricsManagerDialog
      work={work}
      readOnly={demoMode || !hasPermission("library:write")}
      remoteSources={remoteSources.map((remote) => remote.source).filter((source) => source.enabled)}
      canDownload={!demoMode && hasPermission("downloads:manage")}
      onClose={onClose}
      onSaved={onSaved}
    />
  );
}

export function PersistedMetadataEditorOverlay({
  section,
  work,
  sources = [],
  selectedMetadataVariantKey,
  canSyncMetadata = false,
  refreshActions,
  onClose,
  onSaved,
  onLinkChanged,
}: {
  section: MetadataEditorSection | null;
  work: WorkDetail | null;
  sources?: LibrarySource[];
  selectedMetadataVariantKey: string;
  canSyncMetadata?: boolean;
  refreshActions?: ReactNode;
  onClose: () => void;
  onSaved: () => void;
  onLinkChanged: (result: { sync?: WorkMetadataSyncRunResult }) => void;
}) {
  const { demoMode, hasPermission } = useAuth();
  if (!section || !work) return null;
  return (
    <WorkMetadataEditorModal
      work={work}
      sources={sources}
      selectedMetadataVariantKey={selectedMetadataVariantKey}
      canSyncMetadata={canSyncMetadata}
      readOnly={demoMode || !hasPermission("library:write")}
      initialSection={section}
      refreshActions={refreshActions}
      onClose={onClose}
      onSaved={onSaved}
      onLinkChanged={onLinkChanged}
    />
  );
}

export function PersistedReforkOverlay({
  target,
  busy,
  onClose,
  onConfirm,
}: {
  target: ReforkTarget | null;
  busy: boolean;
  onClose: () => void;
  onConfirm: (remote: RemoteSourceAvailability) => void;
}) {
  if (!target) return null;
  return (
    <ReforkConfirmDialog
      currentName={target.current?.source.displayName ?? i18n.t("libraryDetail.currentFork")}
      nextName={target.next.source.displayName}
      busy={busy}
      onClose={onClose}
      onConfirm={() => onConfirm(target.next)}
    />
  );
}
