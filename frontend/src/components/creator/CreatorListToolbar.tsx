import { Search, X } from "lucide-react";
import { useTranslation } from "react-i18next";

import { CollectionFilterChips } from "@/components/collection/CollectionFilterChips";
import type { CollectionFilterOption } from "@/components/collection/CollectionFilterPicker";
import { PageSizePicker } from "@/components/collection/PageSizePicker";
import { dismissKeyboardOnEnter } from "@/lib/keyboard";

export type CreatorListToolbarFilterOption<Value extends string> = CollectionFilterOption<Value>;

/**
 * Search, view chips, and page size for a creator list. A phone keeps the
 * search field on its own row above a sideways-scrolling chip strip; wider
 * layouts put search, chips, and page size on one wrapping row.
 */
export function CreatorListToolbar<FilterValue extends string>({
  label,
  query,
  placeholder,
  filter,
  filterOptions,
  pageSize,
  pageSizeOptions,
  onQueryChange,
  onFilterChange,
  onPageSizeChange,
}: {
  label: string;
  query: string;
  placeholder: string;
  filter: FilterValue;
  filterOptions: readonly CreatorListToolbarFilterOption<FilterValue>[];
  pageSize: number;
  pageSizeOptions: readonly number[];
  onQueryChange: (value: string) => void;
  onFilterChange: (value: FilterValue) => void;
  onPageSizeChange: (value: number) => void;
}) {
  const { t } = useTranslation();

  return (
    <section className="flex flex-wrap items-center gap-x-3 gap-y-2.5" data-toast-avoid>
      <div className="search-field order-1 flex min-h-10 min-w-0 flex-1 items-center gap-2 rounded-lg border bg-card px-3 text-sm lg:w-72 lg:flex-none xl:w-80">
        <Search className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
        <input
          className="min-w-0 flex-1 bg-transparent outline-none placeholder:text-muted-foreground"
          value={query}
          onKeyDown={dismissKeyboardOnEnter}
          onChange={(event) => onQueryChange(event.target.value)}
          placeholder={placeholder}
          aria-label={placeholder}
        />
        {query.trim() && (
          <button
            type="button"
            className="rounded-sm text-muted-foreground hover:text-foreground"
            onClick={() => onQueryChange("")}
            aria-label={t("collection.clearSearch")}
            title={t("collection.clearSearch")}
          >
            <X className="h-4 w-4" />
          </button>
        )}
      </div>
      <div className="order-2 shrink-0 lg:order-3 lg:ml-auto">
        <PageSizePicker value={pageSize} options={pageSizeOptions} onChange={onPageSizeChange} />
      </div>
      <CollectionFilterChips
        label={t("collection.filters", { label })}
        value={filter}
        options={filterOptions}
        onChange={onFilterChange}
        className="order-3 -mx-4 w-[calc(100%+2rem)] px-4 lg:order-2 lg:mx-0 lg:w-auto lg:min-w-0 lg:flex-1 lg:px-0"
      />
    </section>
  );
}
