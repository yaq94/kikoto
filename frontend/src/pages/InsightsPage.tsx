import { ArchiveRestore, ChevronRight } from "lucide-react";
import type { ComponentProps } from "react";
import { useTranslation } from "react-i18next";

import { ListeningHistoryPage, openLinkInPlace } from "@/features/listening-history/ListeningHistoryPage";
import { personalTabPath } from "@/pages/personalTabs";

/**
 * The account's own listening as one page: the listening dashboard, then the
 * way to Your data, where the same account exports and imports it.
 */
export function InsightsPage({
  listening,
  onOpenPath,
}: {
  listening: ComponentProps<typeof ListeningHistoryPage>;
  onOpenPath: (path: string) => void;
}) {
  const { t } = useTranslation();
  const dataPath = personalTabPath("data");
  return (
    <div className="space-y-5">
      <ListeningHistoryPage {...listening} />
      <div className="mx-auto w-full max-w-6xl">
        <a
          href={dataPath}
          onClick={(event) => openLinkInPlace(event, { href: dataPath, open: () => onOpenPath(dataPath) })}
          className="theme-card-surface group flex min-h-14 items-center gap-3 rounded-xl border bg-card px-4 py-3 transition-colors hover:bg-muted/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <ArchiveRestore className="h-5 w-5 shrink-0 text-muted-foreground" aria-hidden="true" />
          <span className="min-w-0 flex-1">
            <span className="block text-sm font-medium group-hover:text-primary">{t("nav.userData")}</span>
            <span className="block text-xs leading-5 text-muted-foreground">{t("personal.insights.dataHint")}</span>
          </span>
          <ChevronRight className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
        </a>
      </div>
    </div>
  );
}
