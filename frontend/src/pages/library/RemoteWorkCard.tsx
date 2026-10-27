import { GitFork, HardDriveDownload, Info, ListMusic } from "lucide-react";

import { useAuth } from "@/auth/AuthProvider";
import { useToast } from "@/components/ui/toast";
import type { SourceVisibilityMode } from "@/components/source-visibility/sourceVisibility";
import { CompactWorkCardShell, type CompactWorkCardLayout } from "@/components/work-card/CompactWorkCard";
import { showsRecommendationBadge } from "@/components/work-card/recommendationBadge";
import {
  dlsiteTagBadges,
  WorkCardActionButton,
  WorkCardDLsiteAction,
  WorkCardFooter,
  WorkCardListButton,
  WorkCardQuickMarkButton,
  WorkCardSelection,
  WorkCardShell,
  type WorkCardViewModel,
} from "@/components/work-card/WorkCardShell";
import { dlsiteWorkURL } from "@/features/work-detail/workDetailShared";
import i18n from "@/i18n";
import type { LibrarySource, ListeningStatus, RemoteWork } from "@/lib/api";

import { libraryCoverSourceBadges } from "./libraryCoverSources";
import { remoteSourceBadges, sourceBadgesNeedLabel } from "./remoteWorkBadges";

export function RemoteWorkCard({
  work,
  recommendationScored,
  recommendationBaseline = 0,
  revealDelay,
  source,
  coverSourceMode,
  selected,
  selectable,
  selectionActive,
  isBusy,
  onSelectedChange,
  onOpen,
  onFork,
  onTagOpen,
  onMark,
  canMark,
  onSave,
  onEnsureWork,
  onListSaved,
  onQueue,
  isQueueing,
  layout,
}: {
  work: RemoteWork;
  recommendationScored: boolean;
  /** Affinity base of the snapshot that produced this score. */
  recommendationBaseline?: number;
  revealDelay: number;
  source: LibrarySource;
  coverSourceMode: SourceVisibilityMode;
  selected: boolean;
  selectable: boolean;
  selectionActive: boolean;
  isBusy: boolean;
  onSelectedChange: (checked: boolean) => void;
  onOpen: () => void;
  onFork: () => void;
  onTagOpen: (tag: string) => void;
  onMark: (status: ListeningStatus) => void;
  /** Checked before the mark menu opens. */
  canMark: () => boolean;
  onSave: () => void;
  onEnsureWork: () => Promise<number | null>;
  onListSaved: (workId: number, favorite: boolean) => void;
  /** Catalog rows have no files to download, but they can still be queued. */
  onQueue: () => void;
  isQueueing: boolean;
  layout: CompactWorkCardLayout | null;
}) {
  const Shell = layout ? CompactWorkCardShell : WorkCardShell;
  const toast = useToast();
  const baseView = remoteWorkCardView(
    work,
    source,
    recommendationScored,
    useAuth().recommendationThreshold,
    recommendationBaseline,
  );
  const sourceBadges = libraryCoverSourceBadges(
    baseView.sourceBadges,
    { kind: "remote", sourceId: source.id },
    coverSourceMode,
  );
  const view: WorkCardViewModel = {
    ...baseView,
    ...sourceBadges,
    // A verdict is the reason to look at the card; an icon plus a tooltip hides
    // it on touch and in a screenshot. Healthy marks keep the compact style.
    sourceBadgeStyle: sourceBadgesNeedLabel(sourceBadges.sourceBadges) ? "label" : "icon",
    recommendationRevealDelay: revealDelay,
  };

  return (
    <Shell
      layout={layout ?? "row"}
      work={view}
      selection={
        selectionActive ? (
          <WorkCardSelection checked={selected} disabled={!selectable} onChange={onSelectedChange} />
        ) : undefined
      }
      onOpen={onOpen}
      onTagOpen={onTagOpen}
      canOpen={Boolean(work.primaryCode || work.catalogEntryId || work.seriesId)}
      footer={
        <WorkCardFooter
          className={layout === "row" ? "h-10 border-border/40" : undefined}
          left={work.primaryCode ? <WorkCardDLsiteAction href={dlsiteWorkURL(work.primaryCode)} /> : null}
          right={
            <>
              {work.catalogEntryId && (
                <WorkCardActionButton
                  title={i18n.t("library.catalogQueue")}
                  disabled={isBusy || isQueueing}
                  onClick={(event) => {
                    event.stopPropagation();
                    onQueue();
                  }}
                >
                  <ListMusic className="h-4 w-4" />
                </WorkCardActionButton>
              )}
              {work.catalogEntryId ? (
                // A folder source has nothing to fork or download. One enabled
                // control explains that, instead of two disabled buttons a
                // keyboard reader cannot reach.
                <WorkCardActionButton
                  title={i18n.t("library.catalogStreamOnly")}
                  disabled={isBusy}
                  onClick={(event) => {
                    event.stopPropagation();
                    toast.info(i18n.t("library.catalogStreamOnly"));
                  }}
                >
                  <Info className="h-4 w-4" />
                </WorkCardActionButton>
              ) : (
                <>
                  <WorkCardActionButton
                    title={i18n.t("detailActions.fork")}
                    disabled={isBusy || !work.primaryCode}
                    onClick={(event) => {
                      event.stopPropagation();
                      onFork();
                    }}
                  >
                    <GitFork className="h-4 w-4" />
                  </WorkCardActionButton>
                  <WorkCardActionButton
                    title={i18n.t("detailActions.fetch")}
                    disabled={isBusy || !work.primaryCode}
                    onClick={(event) => {
                      event.stopPropagation();
                      onSave();
                    }}
                  >
                    <HardDriveDownload className="h-4 w-4" />
                  </WorkCardActionButton>
                </>
              )}
              <WorkCardListButton
                workId={work.workId}
                active={work.favorite}
                disabled={isBusy || (!work.primaryCode && !work.catalogEntryId)}
                ensureWorkId={onEnsureWork}
                onSaved={(favorite, workId) => onListSaved(workId, favorite)}
              />
              <WorkCardQuickMarkButton
                value={work.listeningStatus}
                disabled={isBusy || (!work.primaryCode && !work.catalogEntryId)}
                canOpen={canMark}
                onChange={onMark}
              />
            </>
          }
        />
      }
    />
  );
}

function remoteWorkCardView(
  work: RemoteWork,
  source: LibrarySource,
  scored: boolean,
  threshold: number,
  baseline: number,
): WorkCardViewModel {
  const sourceLabel = source.displayName || source.code || i18n.t("workCard.remoteSource");
  return {
    translationResource: work.seriesId
      ? { seriesId: work.seriesId }
      : work.catalogEntryId
        ? { catalogEntryId: work.catalogEntryId }
        : { sourceId: source.id, code: work.remoteCode || work.primaryCode || work.remoteId },
    code: work.seriesId
      ? i18n.t("catalogSeries.series")
      : work.primaryCode || (work.catalogEntryId ? i18n.t("openlist.numberless") : work.remoteId),
    title: work.title,
    circle: work.circle || sourceLabel || i18n.t("workCard.unknownCircle"),
    ageRating: work.ageRating,
    voiceActors: work.voiceActors,
    coverUrl: work.coverUrl,
    rating: work.rating,
    ratingCount: work.ratingCount,
    sales: work.sales,
    price: work.price,
    priceCurrency: "JPY",
    series: null,
    hasLyrics: work.hasLyrics,
    dlsiteTags: dlsiteTagBadges(work.tags),
    userTags: [],
    recommended: showsRecommendationBadge(scored, work.recommendScore, baseline),
    recommendationHighlighted: work.recommendScore >= threshold,
    recommendationScore: work.recommendScore,
    sourceBadges: remoteSourceBadges(work, source.id, sourceLabel),
  };
}
