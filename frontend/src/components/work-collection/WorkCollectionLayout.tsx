import { Columns3Cog, LayoutGrid, List as ListIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import type { TFunction } from "i18next";
import { useEffect, useId, useRef, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { sourceVisibilityModes, type SourceVisibilityMode } from "@/components/source-visibility/sourceVisibility";
import { AnchoredPopover } from "@/components/ui/anchored-popover";
import { Switch } from "@/components/ui/switch";
import { segmentedItemClassName, segmentedListClassName } from "@/components/ui/segmented";
import i18n from "@/i18n";
import { cn } from "@/lib/tailwindClassNames";
import {
  isWorkCollectionDesktopColumnSetting,
  isWorkCollectionMobileColumnSetting,
  workCollectionDesktopColumnOptions,
  workCollectionMobileColumnOptions,
  type WorkCollectionColumnSetting,
} from "@/components/work-collection/workCollectionLayoutModel";

export {
  workCollectionClassName,
  workCollectionColumnOptions,
  workCollectionStyle,
} from "@/components/work-collection/workCollectionLayoutModel";
export type {
  WorkCollectionColumnCount,
  WorkCollectionColumnSetting,
} from "@/components/work-collection/workCollectionLayoutModel";

const layoutStorageKey = "kikoto:work-collection-layout";
const layoutChangeEvent = "kikoto:work-collection-layout-change";

export type WorkCollectionViewMode = "grid" | "list";

type StoredWorkCollectionLayout = {
  mobileColumns: WorkCollectionColumnSetting;
  desktopColumns: WorkCollectionColumnSetting;
  /** Phone collections that support it use the compact card layouts. */
  mobileCompact: boolean;
};

type WorkCollectionLayoutDefaults = Omit<StoredWorkCollectionLayout, "mobileCompact"> & { mobileCompact?: boolean };

export function useWorkCollectionLayout(
  initial: WorkCollectionLayoutDefaults = { mobileColumns: "auto", desktopColumns: "auto" },
) {
  const [layout, setLayout] = useState<StoredWorkCollectionLayout>(() => readStoredLayout(initial));
  // Callers may pass a fresh default object each render; subscribe on its values.
  const {
    mobileColumns: initialMobileColumns,
    desktopColumns: initialDesktopColumns,
    mobileCompact: initialMobileCompact,
  } = initial;
  useEffect(() => {
    const sync = () =>
      setLayout(
        readStoredLayout({
          mobileColumns: initialMobileColumns,
          desktopColumns: initialDesktopColumns,
          mobileCompact: initialMobileCompact,
        }),
      );
    window.addEventListener("storage", sync);
    window.addEventListener(layoutChangeEvent, sync);
    return () => {
      window.removeEventListener("storage", sync);
      window.removeEventListener(layoutChangeEvent, sync);
    };
  }, [initialDesktopColumns, initialMobileColumns, initialMobileCompact]);
  const update = (patch: Partial<StoredWorkCollectionLayout>) => {
    setLayout((current) => {
      const next = { ...current, ...patch };
      localStorage.setItem(layoutStorageKey, JSON.stringify(next));
      window.dispatchEvent(new Event(layoutChangeEvent));
      return next;
    });
  };
  return {
    ...layout,
    setMobileColumns: (mobileColumns: WorkCollectionColumnSetting) => update({ mobileColumns }),
    setDesktopColumns: (desktopColumns: WorkCollectionColumnSetting) => update({ desktopColumns }),
    setMobileCompact: (mobileCompact: boolean) => update({ mobileCompact }),
  };
}

/**
 * One toolbar control for how a work collection is displayed: the grid or list
 * view, grid columns, and items per page share a popover so the toolbar spends
 * a single icon on them. Any group may be omitted (a list view has no columns;
 * some collections have one view or are not paged). The popover stays open
 * after a choice so several can be adjusted.
 */
export function WorkCollectionDisplayPicker({
  mobileColumns,
  desktopColumns,
  onMobileColumnsChange,
  onDesktopColumnsChange,
  showColumns = true,
  view,
  mobileCompact,
  onMobileCompactChange,
  pageSize,
  pageSizeOptions,
  onPageSizeChange,
  coverSources,
}: {
  mobileColumns: WorkCollectionColumnSetting;
  desktopColumns: WorkCollectionColumnSetting;
  onMobileColumnsChange: (value: WorkCollectionColumnSetting) => void;
  onDesktopColumnsChange: (value: WorkCollectionColumnSetting) => void;
  showColumns?: boolean;
  /** Passing it adds the grid or list choice for collections that draw both. */
  view?: { mode: WorkCollectionViewMode; onChange: (mode: WorkCollectionViewMode) => void };
  /** Passing the handler adds the compact card switch, shown on narrow screens only. */
  mobileCompact?: boolean;
  onMobileCompactChange?: (value: boolean) => void;
  pageSize?: number;
  pageSizeOptions?: readonly number[];
  onPageSizeChange?: (value: number) => void;
  /** Whether cover source marks show automatically (exceptions only), always, or never. */
  coverSources?: { mode: SourceVisibilityMode; onChange: (mode: SourceVisibilityMode) => void };
}) {
  const { t } = useTranslation("translation", { i18n });
  const [open, setOpen] = useState(false);
  const isWide = useIsWideLayout();
  const anchorRef = useRef<HTMLDivElement | null>(null);
  const currentColumns = isWide ? desktopColumns : mobileColumns;
  const columnOptions: readonly WorkCollectionColumnSetting[] = [
    "auto",
    ...(isWide ? workCollectionDesktopColumnOptions : workCollectionMobileColumnOptions),
  ];
  const setColumns = (value: WorkCollectionColumnSetting) => {
    if (isWide) onDesktopColumnsChange(value);
    else onMobileColumnsChange(value);
  };
  const paged = pageSize !== undefined && pageSizeOptions !== undefined && onPageSizeChange !== undefined;
  const viewOptions = [
    { value: "grid" as const, label: t("collection.gridView"), icon: LayoutGrid },
    { value: "list" as const, label: t("collection.listView"), icon: ListIcon },
  ];
  const summary = [
    view ? viewOptions.find((option) => option.value === view.mode)?.label : null,
    showColumns ? t("collection.columns", { label: columnSettingLabel(currentColumns, t) }) : null,
    paged ? t("collection.pageSize", { value: pageSize }) : null,
  ]
    .filter(Boolean)
    .join(" · ");
  const buttonLabel = t("collection.displayOptionsValue", { value: summary });

  return (
    <div className="relative" ref={anchorRef}>
      <Button
        variant="toolbar"
        size="icon-sm"
        aria-haspopup="dialog"
        aria-expanded={open}
        className="relative"
        title={buttonLabel}
        aria-label={buttonLabel}
        type="button"
        onClick={() => setOpen((current) => !current)}
      >
        <Columns3Cog className="h-4 w-4" />
      </Button>
      <AnchoredPopover
        open={open}
        anchorRef={anchorRef}
        onOpenChange={setOpen}
        ariaLabel={t("collection.displayOptions")}
        className="flex w-[min(18rem,calc(100vw-1.5rem))] flex-col gap-3 p-3 text-sm"
      >
        {view && (
          <DisplayOptionGroup label={t("collection.viewMode")}>
            {viewOptions.map((option) => (
              <DisplayOptionButton
                key={option.value}
                checked={view.mode === option.value}
                label={option.label}
                className="gap-1.5"
                onSelect={() => view.onChange(option.value)}
              >
                <option.icon className="h-4 w-4" aria-hidden="true" />
                {t(`collection.viewModes.${option.value}`)}
              </DisplayOptionButton>
            ))}
          </DisplayOptionGroup>
        )}
        {showColumns && (
          <DisplayOptionGroup label={t("collection.columnsHeading")}>
            {columnOptions.map((option) => (
              <DisplayOptionButton
                key={option}
                checked={currentColumns === option}
                label={columnOptionLabel(option, t)}
                className={option === "auto" ? "flex-[1.6]" : undefined}
                onSelect={() => setColumns(option)}
              >
                {columnSettingLabel(option, t)}
              </DisplayOptionButton>
            ))}
          </DisplayOptionGroup>
        )}
        {!isWide && onMobileCompactChange && (
          <label className="flex min-h-11 cursor-pointer items-center justify-between gap-3 px-0.5">
            <span className="min-w-0">
              <span className="block text-xs font-medium text-muted-foreground">{t("collection.compactCards")}</span>
              <span className="block text-2xs text-muted-foreground/80">{t("collection.compactCardsHint")}</span>
            </span>
            <Switch
              checked={mobileCompact ?? false}
              onCheckedChange={onMobileCompactChange}
              aria-label={t("collection.compactCards")}
            />
          </label>
        )}
        {paged && (
          <DisplayOptionGroup label={t("collection.itemsPerPage")}>
            {pageSizeOptions.map((option) => (
              <DisplayOptionButton
                key={option}
                checked={pageSize === option}
                label={t("collection.perPageOption", { value: option })}
                onSelect={() => onPageSizeChange(option)}
              >
                {option}
              </DisplayOptionButton>
            ))}
          </DisplayOptionGroup>
        )}
        {coverSources && (
          <DisplayOptionGroup label={t("collection.coverSources")}>
            {sourceVisibilityModes.map((mode) => (
              <DisplayOptionButton
                key={mode}
                checked={coverSources.mode === mode}
                label={t(`collection.coverSourceModes.${mode}`)}
                onSelect={() => coverSources.onChange(mode)}
              >
                {t(`library.sourceVisibility.modes.${mode}`)}
              </DisplayOptionButton>
            ))}
          </DisplayOptionGroup>
        )}
      </AnchoredPopover>
    </div>
  );
}

function DisplayOptionGroup({ label, children }: { label: string; children: ReactNode }) {
  const labelId = useId();
  return (
    <div className="flex flex-col gap-1.5">
      <div id={labelId} className="px-0.5 text-xs font-medium text-muted-foreground">
        {label}
      </div>
      <div role="radiogroup" aria-labelledby={labelId} className={segmentedListClassName("w-full")}>
        {children}
      </div>
    </div>
  );
}

function DisplayOptionButton({
  checked,
  label,
  className,
  onSelect,
  children,
}: {
  checked: boolean;
  label: string;
  className?: string;
  onSelect: () => void;
  children: ReactNode;
}) {
  return (
    <button
      type="button"
      role="radio"
      aria-checked={checked}
      title={label}
      aria-label={label}
      className={segmentedItemClassName(checked, cn("min-w-0 flex-1 justify-center px-0 tabular-nums", className))}
      onClick={onSelect}
    >
      {children}
    </button>
  );
}

function useIsWideLayout() {
  const [wide, setWide] = useState(() => window.matchMedia("(min-width: 1024px)").matches);
  useEffect(() => {
    const media = window.matchMedia("(min-width: 1024px)");
    const update = () => setWide(media.matches);
    media.addEventListener("change", update);
    return () => media.removeEventListener("change", update);
  }, []);
  return wide;
}

function columnSettingLabel(setting: WorkCollectionColumnSetting, t: TFunction<"translation">) {
  return setting === "auto" ? t("collection.auto") : String(setting);
}

function columnOptionLabel(setting: WorkCollectionColumnSetting, t: TFunction<"translation">) {
  if (setting === "auto") return t("collection.automaticColumns");
  return t("collection.column", { count: setting });
}

function readStoredLayout(defaults: WorkCollectionLayoutDefaults): StoredWorkCollectionLayout {
  const fallback: StoredWorkCollectionLayout = { ...defaults, mobileCompact: defaults.mobileCompact ?? false };
  try {
    const value = JSON.parse(localStorage.getItem(layoutStorageKey) ?? "{}") as Partial<StoredWorkCollectionLayout>;
    return {
      mobileColumns: isWorkCollectionMobileColumnSetting(value.mobileColumns)
        ? value.mobileColumns
        : fallback.mobileColumns,
      // A stored desktop column choice outside the supported options reverts to the default.
      desktopColumns: isWorkCollectionDesktopColumnSetting(value.desktopColumns)
        ? value.desktopColumns
        : isWorkCollectionDesktopColumnSetting(fallback.desktopColumns)
          ? fallback.desktopColumns
          : "auto",
      mobileCompact: typeof value.mobileCompact === "boolean" ? value.mobileCompact : fallback.mobileCompact,
    };
  } catch {
    return fallback;
  }
}
