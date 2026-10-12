import { Plus, Search, X } from "lucide-react";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { quickMarkMeta } from "@/components/work-card/WorkCardShell";
import { listeningStatusOptions } from "@/features/work-detail/workDetailShared";
import { numberFormat } from "@/i18n/format";
import { useLocale } from "@/i18n/LocaleProvider";
import type { ListeningStatus } from "@/lib/api";
import { dismissKeyboardOnEnter } from "@/lib/keyboard";
import { cn } from "@/lib/tailwindClassNames";

import { LibrarySortButton, type LibrarySortProps } from "./LibrarySortButton";

/**
 * The phone Library header: an always-visible search field, the source rail,
 * sort and view controls, quick-mark filter chips, and a result caption. It
 * replaces the desktop toolbar's row of icon buttons, which on a phone hid
 * search behind a toggle and filters behind an unlabeled popover.
 */
export function MobileLibraryToolbar({
  sourceTabs,
  searchQuery,
  onSearchChange,
  onAddClause,
  addClauseOpen,
  resultCount,
  resultPage,
  sort,
  actions,
  statusFilter,
  onStatusFilterChange,
  children,
}: {
  sourceTabs: ReactNode;
  searchQuery: string;
  onSearchChange: (value: string) => void;
  onAddClause: (anchor: HTMLElement) => void;
  addClauseOpen: boolean;
  resultCount: number | null;
  /** Shown beside the count when the results span more than one page. */
  resultPage: { page: number; totalPages: number };
  sort: LibrarySortProps;
  actions: ReactNode;
  /** Null when the active view cannot filter by quick mark. */
  statusFilter: ListeningStatus | "all" | null;
  onStatusFilterChange: (value: ListeningStatus | "all") => void;
  /** Rendered between the source rail and the result line. */
  children?: ReactNode;
}) {
  const { t } = useTranslation();
  const { resolvedLocale } = useLocale();
  return (
    <div className="space-y-3" data-toast-avoid>
      <div className="search-field flex h-11 min-w-0 items-center gap-2 rounded-full border bg-card pl-4 pr-1.5 text-sm shadow-sm transition-colors focus-within:border-primary/50">
        <Search className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
        <input
          className="min-w-0 flex-1 bg-transparent outline-none placeholder:text-muted-foreground"
          type="search"
          enterKeyHint="search"
          value={searchQuery}
          onKeyDown={dismissKeyboardOnEnter}
          onChange={(event) => onSearchChange(event.target.value)}
          placeholder={t("library.mobileSearchPlaceholder")}
          aria-label={t("library.searchLibrary")}
        />
        {searchQuery.trim() && (
          <button
            type="button"
            className="grid h-8 w-8 shrink-0 place-items-center rounded-full text-muted-foreground hover:bg-muted hover:text-foreground"
            onClick={() => onSearchChange("")}
            aria-label={t("library.clearSearch")}
          >
            <X className="h-4 w-4" />
          </button>
        )}
        <button
          type="button"
          className={cn(
            "grid h-8 w-8 shrink-0 place-items-center rounded-full bg-muted text-muted-foreground hover:text-foreground",
            addClauseOpen && "bg-primary/10 text-primary",
          )}
          onClick={(event) => onAddClause(event.currentTarget)}
          aria-label={t("library.addSearchCondition")}
          aria-haspopup="dialog"
          aria-expanded={addClauseOpen}
        >
          <Plus className="h-4 w-4" />
        </button>
      </div>
      <div className="-mx-4 overflow-x-auto px-4 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
        {sourceTabs}
      </div>
      {children}
      <div className="flex min-h-9 items-center gap-2">
        <LibrarySortButton {...sort} />
        <div className="ml-auto flex shrink-0 items-center gap-2">{actions}</div>
      </div>
      {statusFilter !== null && <StatusChips value={statusFilter} onChange={onStatusFilterChange} />}
      <p className="flex min-h-5 min-w-0 items-center gap-2 text-xs text-muted-foreground">
        {resultCount !== null && (
          <>
            <span className="truncate font-semibold text-foreground">
              {t("library.resultCount", { value: numberFormat(resolvedLocale).format(resultCount) })}
            </span>
            {resultPage.totalPages > 1 && (
              <>
                <span className="h-3 w-px shrink-0 bg-border" aria-hidden="true" />
                <span className="shrink-0 tabular-nums">
                  {t("library.resultPage", {
                    page: numberFormat(resolvedLocale).format(resultPage.page),
                    totalPages: numberFormat(resolvedLocale).format(resultPage.totalPages),
                  })}
                </span>
              </>
            )}
          </>
        )}
      </p>
    </div>
  );
}

function StatusChips({
  value,
  onChange,
}: {
  value: ListeningStatus | "all";
  onChange: (value: ListeningStatus | "all") => void;
}) {
  const { t } = useTranslation();
  const chip = (active: boolean) =>
    cn(
      "inline-flex h-8 shrink-0 items-center gap-1.5 whitespace-nowrap rounded-full border px-3 text-xs font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
      active
        ? "border-primary/30 bg-primary/10 text-primary"
        : "border-border bg-card text-muted-foreground hover:text-foreground",
    );
  return (
    <div
      className="-mx-4 flex gap-1.5 overflow-x-auto px-4 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden"
      role="group"
      aria-label={t("library.markFilter")}
    >
      <button
        type="button"
        className={chip(value === "all")}
        aria-pressed={value === "all"}
        onClick={() => onChange("all")}
      >
        {t("library.allStatuses")}
      </button>
      {listeningStatusOptions.map((option) => {
        const meta = quickMarkMeta(option.value);
        const active = value === option.value;
        return (
          <button
            key={option.value}
            type="button"
            className={chip(active)}
            aria-pressed={active}
            onClick={() => onChange(active ? "all" : option.value)}
          >
            <meta.icon className={cn("h-3.5 w-3.5", !active && meta.className)} aria-hidden="true" />
            {t(`library.status.${option.value}`, { defaultValue: option.label })}
          </button>
        );
      })}
    </div>
  );
}
