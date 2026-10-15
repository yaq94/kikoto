import type { ReactNode } from "react";

import {
  type RemoteSourceAvailability,
  remoteSourceCanLoad,
  type SourceTabInfo,
  type TrackedPresenceOption,
} from "@/features/work-detail/source/sourceContextModel";
import type { DirectoryRoutingRule, RemoteWorkDetail, SourcePresenceItem, WorkDetail } from "@/lib/api";
import {
  formatTreeStats,
  type TreeNode,
  type TreeStats,
  type TreeTrack,
} from "@/features/work-detail/media/mediaTreeModel";
import i18n from "@/i18n";
import { languageLabel } from "@/features/work-detail/workDetailHelpers";
import { DirectorySkeleton } from "@/features/work-detail/WorkDetailLayout";
import type { FilePreviewRequest } from "@/features/work-detail/dialogs/FilePreviewDialog";
import {
  DirectoryLoadErrorPanel,
  DirectoryOperationBanner,
  LocalSourceStatePanel,
  NoSourceDirectoryPanel,
  RemoteSourceStatePanel,
  SourceDirectoryPanel,
  TrackedUnforkedPanel,
} from "@/features/work-detail/directory/SourceDirectoryPanels";

export type PersistedDirectoryPanelProps = {
  activeEdition: WorkDetail | null;
  description: string;
  tabs: SourceTabInfo[];
  activeKey: string;
  trackedPresenceOptions: TrackedPresenceOption[];
  selectedTrackedPresenceKey: string;
  checkingSources: boolean;
  checkedAt: string;
  root: TreeNode;
  directoryStats: TreeStats;
  directoryRoutingRules: DirectoryRoutingRule[];
  currentLocationId: number | null;
  currentPlaybackKey: string | null;
  autoRoutePath?: string[] | null;
  routeStateKey?: string;
  showNoSourceDirectory: boolean;
  selectedRemoteSource: RemoteSourceAvailability | null | undefined;
  selectedSource: SourceTabInfo | null | undefined;
  selectedTrackedForked: boolean;
  selectedTrackedPresence: SourcePresenceItem | null;
  remoteSources: RemoteSourceAvailability[];
  showDirectorySkeleton: boolean;
  directoryMediaError: string;
  cleanupRunId: number | null;
  cleanupRunStatus: string;
  selectedRemoteDetail: RemoteWorkDetail | null;
  selectionModal: ReactNode;
  onActiveKeyChange: (key: string) => void;
  onTrackedPresenceChange: (key: string) => void;
  onCheckSources: () => void;
  onRetry: () => void;
  onSelectRemote: (remote: RemoteSourceAvailability) => void;
  onOpenCleanupRun: () => void;
  onPlayLocal: (tracks: TreeTrack[], locationId: number) => void;
  onPlayRemote: (tracks: TreeTrack[], locationId: number) => void;
  onQueue: (track: TreeTrack, next: boolean) => void;
  onPreview: (request: FilePreviewRequest) => void;
};

function persistedDirectoryEmptyLabel(props: PersistedDirectoryPanelProps) {
  if (props.showNoSourceDirectory) return i18n.t("libraryDetail.noSourceLinked");
  if (props.selectedRemoteSource) return i18n.t("libraryDetail.noRemoteFiles");
  return i18n.t("libraryDetail.noLocalFiles");
}

function persistedDirectoryToolbar(props: PersistedDirectoryPanelProps) {
  if (props.cleanupRunId) {
    return (
      <DirectoryOperationBanner
        runId={props.cleanupRunId}
        status={props.cleanupRunStatus}
        onOpen={props.onOpenCleanupRun}
      />
    );
  }
  return null;
}

function persistedDirectorySourceState(props: PersistedDirectoryPanelProps) {
  if (props.selectedSource?.kind === "local" && props.selectedSource.status !== "available") {
    return (
      <LocalSourceStatePanel
        status={props.selectedSource.status}
        remoteSources={props.remoteSources}
        onSelectRemote={props.onSelectRemote}
      />
    );
  }
  if (props.selectedRemoteSource && !remoteSourceCanLoad(props.selectedRemoteSource.summary)) {
    return <RemoteSourceStatePanel remote={props.selectedRemoteSource} />;
  }
  if (props.selectedSource?.kind === "tracked" && !props.selectedTrackedForked) {
    return <TrackedUnforkedPanel presence={props.selectedTrackedPresence} remoteSources={props.remoteSources} />;
  }
  if (props.showNoSourceDirectory) {
    return (
      <NoSourceDirectoryPanel
        checking={props.checkingSources}
        checkedAt={props.checkedAt}
        remoteSources={props.remoteSources}
        onRefresh={props.onCheckSources}
      />
    );
  }
  return null;
}

function persistedDirectoryEmptyState(props: PersistedDirectoryPanelProps) {
  if (
    props.selectedRemoteDetail &&
    (props.selectedRemoteSource?.treeLoaded || props.selectedRemoteDetail.tracks.length > 0)
  )
    return null;
  if (props.showDirectorySkeleton) return <DirectorySkeleton />;
  if (props.directoryMediaError) {
    return <DirectoryLoadErrorPanel message={props.directoryMediaError} onRetry={props.onRetry} />;
  }
  return persistedDirectorySourceState(props);
}

function persistedDirectoryLoadingMessage(props: PersistedDirectoryPanelProps) {
  if (props.selectedRemoteDetail) return props.directoryMediaError || props.selectedRemoteSource?.summary.error || "";
  if (!props.selectedRemoteSource || props.selectedRemoteDetail || props.selectedRemoteSource.loading) return "";
  return props.selectedRemoteSource.error || i18n.t("libraryDetail.remoteDirectoryNotLoaded");
}

export function PersistedDirectoryPanel(props: PersistedDirectoryPanelProps) {
  const description = props.activeEdition
    ? i18n.t("libraryDetail.showingFilesFrom", {
        code: props.activeEdition.primaryCode,
        language: languageLabel(props.activeEdition.metadataLanguage),
      })
    : props.description;
  const emptyState = persistedDirectoryEmptyState(props);
  return (
    <SourceDirectoryPanel
      title={i18n.t("libraryDetail.directory")}
      description={description}
      statsLabel={formatTreeStats(props.directoryStats)}
      tabs={props.tabs}
      activeKey={props.activeKey}
      onActiveKeyChange={props.onActiveKeyChange}
      trackedPresenceOptions={props.trackedPresenceOptions}
      selectedTrackedPresenceKey={props.selectedTrackedPresenceKey}
      onTrackedPresenceChange={props.onTrackedPresenceChange}
      checkingSources={props.checkingSources}
      checkedAt={props.checkedAt}
      onCheckSources={props.onCheckSources}
      root={props.root}
      directoryRoutingRules={props.directoryRoutingRules}
      currentLocationId={props.currentLocationId}
      currentPlaybackKey={props.currentPlaybackKey}
      autoRoutePath={props.autoRoutePath}
      routeStateKey={props.routeStateKey}
      emptyLabel={persistedDirectoryEmptyLabel(props)}
      toolbar={persistedDirectoryToolbar(props)}
      selectionModal={props.selectionModal}
      emptyState={emptyState}
      loadingMessage={persistedDirectoryLoadingMessage(props)}
      onPlayFolder={props.selectedRemoteDetail ? props.onPlayRemote : props.onPlayLocal}
      onPlayNext={(track) => props.onQueue(track, true)}
      onAppendQueue={(track) => props.onQueue(track, false)}
      onPreview={props.onPreview}
    />
  );
}
