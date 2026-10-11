import type { WorkDetail, WorkMetadataVariant } from "@/lib/api";
import { metadataLanguageChoices, metadataVariantLabel } from "../metadataPresentationModel";

export type MetadataLanguageScope = {
  key: string;
  language: string;
  origin: boolean;
  label: string;
  variant: WorkMetadataVariant | null;
};

/**
 * Builds the editor's display scopes from the provider variants actually
 * present on this work. `origin` is an edition marker, not a hard-coded
 * Japanese language assumption.
 */
export function metadataLanguageScopes(
  work: Pick<WorkDetail, "metadataPresentation">,
  selectedKey = "",
): MetadataLanguageScope[] {
  const choices = metadataLanguageChoices(work.metadataPresentation, selectedKey);
  const result: MetadataLanguageScope[] = [{ key: "all", language: "", origin: false, label: "All", variant: null }];
  for (const choice of choices) {
    const variant = choice.variant;
    const label = metadataVariantLabel(variant, {
      original: "Original",
      language: metadataScopeLanguageLabel,
    });
    result.push({
      key: choice.key,
      language: variant.language.trim().toLowerCase(),
      origin: variant.origin,
      label,
      variant,
    });
  }
  return result.length > 2 ? result : result.slice(1);
}

function metadataScopeLanguageLabel(value: string) {
  switch (value.trim().toLowerCase()) {
    case "ja-jp":
      return "Japanese";
    case "en-us":
      return "English";
    case "zh-cn":
      return "Simplified Chinese";
    case "zh-tw":
      return "Traditional Chinese";
    case "ko-kr":
      return "Korean";
    default:
      return value || "Unknown language";
  }
}

export function scopeMatchesLanguage(scope: MetadataLanguageScope, language: string, origin = false) {
  if (scope.key === "all") return true;
  if (scope.origin) return origin;
  return scope.language !== "" && scope.language === language.trim().toLowerCase();
}
