import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import type { CatalogIndexProgress } from "@/lib/api";

/**
 * The diagnostic detail of one index run. It starts collapsed: the toolbar
 * above already carries the actions, and the summary line keeps the live counts
 * readable while the tables and directory lists stay folded away.
 */
export function SourceIndexProgressPanel({
  progress,
  page,
  pages,
  onPageChange,
}: {
  progress: CatalogIndexProgress;
  page: number;
  pages: number;
  onPageChange: (page: number) => void;
}) {
  const { t } = useTranslation();
  return (
    <details className="rounded-[var(--control-radius)] border border-border">
      <summary className="cursor-pointer px-2 py-1 text-sm">
        {t(`sourceIndex.state.${progress.status}`)}
        <span className="text-muted-foreground">
          {" · "}
          {t("sourceIndex.summary", {
            completed: progress.completedDirectories,
            pending: progress.pendingDirectories,
            failed: progress.failedDirectories,
            empty: progress.unconfirmedEmptyDirectories,
          })}
        </span>
      </summary>
      <div className="space-y-2 border-t border-border/60 p-2">
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
          {t(progress.finishedAt ? "sourceIndex.timesFinished" : "sourceIndex.times", {
            started: progress.startedAt,
            ...(progress.finishedAt ? { finished: progress.finishedAt } : {}),
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
            <Button variant="ghost" disabled={page <= 1} onClick={() => onPageChange(page - 1)}>
              {t("sourceIndex.previous")}
            </Button>
            <span>
              {page} / {pages}
            </span>
            <Button variant="ghost" disabled={page >= pages} onClick={() => onPageChange(page + 1)}>
              {t("sourceIndex.next")}
            </Button>
          </div>
        )}
      </div>
    </details>
  );
}
