import { useTranslation } from "react-i18next";
import { cn } from "@/lib/tailwindClassNames";
import type { MetadataLanguageScope } from "./metadataLanguageScope";

export function MetadataLanguageScopeTabs({
  scopes,
  selected,
  onSelect,
}: {
  scopes: MetadataLanguageScope[];
  selected: string;
  onSelect: (key: string) => void;
}) {
  const { t } = useTranslation();
  if (scopes.length <= 1) return null;
  return (
    <div className="space-y-1.5 border-b pb-3">
      <p className="text-xs font-medium text-muted-foreground">{t("metadataEditor.languageScope")}</p>
      <div
        role="tablist"
        aria-label={t("metadataEditor.languageScope")}
        className="app-scrollbar flex gap-1 overflow-x-auto"
      >
        {scopes.map((scope) => {
          const active = scope.key === selected;
          return (
            <button
              key={scope.key}
              type="button"
              role="tab"
              aria-selected={active}
              className={cn(
                "min-h-9 shrink-0 rounded-md px-3 text-sm transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                active
                  ? "bg-secondary font-medium text-secondary-foreground"
                  : "text-muted-foreground hover:bg-muted hover:text-foreground",
              )}
              onClick={() => onSelect(scope.key)}
            >
              {scope.key === "all" ? t("metadataEditor.allLanguages") : scope.label}
            </button>
          );
        })}
      </div>
    </div>
  );
}
