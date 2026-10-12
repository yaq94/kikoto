import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Languages, RotateCcw, X } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { useMetadataEntrySuggestions } from "@/hooks/useMetadataEntrySuggestions";
import { api, type MetadataTag } from "@/lib/api";
import {
  exactMetadataTag,
  metadataTagAlternateNames,
  metadataTagHasName,
  metadataTagNameIn,
  sameMetadataTagName,
} from "@/lib/metadataTagModel";
import { cn } from "@/lib/tailwindClassNames";
import { MetadataEditorField, SuggestionCombobox } from "./MetadataEditorFields";
import { MetadataTagNamesPanel } from "./MetadataTagNamesPanel";
import type { MetadataFieldStatus } from "./metadataEditorModel";
import { metadataLanguageScopeLabel, type MetadataLanguageScope } from "./metadataLanguageScope";
import type { WorkMetadataTagsEditor } from "./useWorkMetadataTagsEditor";

const rowButtonClass =
  "relative grid h-9 w-9 shrink-0 place-items-center rounded-md text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";

/**
 * Edits the tags on a work as a list: one row per tag with its other names,
 * and its names editor opening inside the row. Which tags a work has is the
 * same in every language; a supported language scope shows and names them in
 * that language.
 */
export function WorkMetadataTagsSection({
  editor,
  languageScope,
}: {
  editor: WorkMetadataTagsEditor;
  languageScope?: MetadataLanguageScope;
}) {
  const { t, i18n } = useTranslation();
  const [query, setQuery] = useState("");
  const [namingId, setNamingId] = useState<number | null>(null);
  const suggestions = useMetadataEntrySuggestions("tags", query);
  const [hiddenNames, setHiddenNames] = useState<string[]>([]);
  // A typed list resolves across awaits, so each step reads the latest draft.
  const editorRef = useRef(editor);
  useEffect(() => {
    editorRef.current = editor;
  });
  const language = languageScope?.kind === "supported" ? languageScope.language : "";
  const languageName = languageScope ? metadataLanguageScopeLabel(languageScope, t, i18n.language) : "";
  const shownName = (tag: { id: number; names?: MetadataTag["names"]; displayName: string }) =>
    language ? metadataTagNameIn(tag, language, editor.draftNames(tag.id)) : { name: tag.displayName, own: true };
  const add = (tag: MetadataTag) => {
    if (tag.resolvedHidden) return;
    editor.add(tag);
    setQuery("");
  };
  const existing = exactMetadataTag(suggestions.entries as MetadataTag[], query);
  const alreadyAdded = editor.tags.some((tag) => metadataTagHasName(tag, query));
  const create = () => {
    if (existing) add(existing);
    else if (!alreadyAdded) {
      editor.stage(query);
      setQuery("");
    }
  };
  // Each name in a comma-separated list joins an existing tag when one matches, else becomes a new tag.
  const addNames = async (names: string[]) => {
    const unique = names.filter(
      (name, index) => names.findIndex((other) => sameMetadataTagName(other, name)) === index,
    );
    const hidden: string[] = [];
    for (const name of unique) {
      if (editorRef.current.tags.some((tag) => metadataTagHasName(tag, name))) continue;
      let match: MetadataTag | undefined;
      try {
        const page = await api.listMetadataTags({
          query: name,
          pageSize: 20,
          includeHidden: true,
          resolveMerged: true,
        });
        match = exactMetadataTag(page.tags, name);
      } catch {
        // Saving a new tag with an existing name still joins that tag.
      }
      if (match?.resolvedHidden) hidden.push(name);
      else if (match) editorRef.current.add(match);
      else editorRef.current.stage(name);
    }
    setHiddenNames(hidden);
  };
  const status: MetadataFieldStatus = editor.changed ? "edited" : editor.hasOverrides ? "manual" : "source";
  if (editor.failed)
    return (
      <div role="alert" className="flex flex-wrap items-center gap-2 text-sm text-muted-foreground">
        {t("metadataEntries.loadFailed")}
        <Button variant="outline" size="sm" onClick={editor.retry}>
          {t("metadataEntries.retry")}
        </Button>
      </div>
    );
  if (!editor.ready) return <p className="text-sm text-muted-foreground">{t("common.loading")}</p>;
  const options = suggestions.entries
    .filter((entry) => !editor.tags.some((tag) => tag.id === entry.id))
    .map((entry) => {
      const tag = entry as MetadataTag;
      const name = shownName(tag).name;
      return {
        key: String(tag.id),
        label: tag.resolvedHidden ? `${name} · ${t("metadataEntries.hidden")}` : name,
        disabled: tag.resolvedHidden,
        onSelect: () => add(tag),
      };
    });
  if (query.trim() && !existing && !alreadyAdded)
    options.push({
      key: "create",
      label: `${t("metadataEntries.createTag")}: ${query.trim()}`,
      disabled: false,
      onSelect: create,
    });
  return (
    <div className="space-y-4">
      <div className="space-y-1.5">
        <label htmlFor="metadata-editor-tag" className="block text-sm font-medium">
          {t("metadataEntries.addTag")}
        </label>
        <SuggestionCombobox
          id="metadata-editor-tag"
          value={query}
          placeholder={t("metadataEditor.searchTags")}
          onChange={setQuery}
          onSubmitText={create}
          onSubmitTokens={(names) => void addNames(names)}
          options={options}
          footer={
            (existing?.resolvedHidden || suggestions.failed) && (
              <>
                {existing?.resolvedHidden && (
                  <p role="alert" className="px-2 py-1 text-xs text-muted-foreground">
                    {t("metadataEntries.hiddenNameConflict")}
                  </p>
                )}
                {suggestions.failed && (
                  <p role="alert" className="px-2 py-1 text-xs text-muted-foreground">
                    {t("metadataEntries.loadFailed")}
                  </p>
                )}
              </>
            )
          }
        />
        {hiddenNames.length > 0 && (
          <p role="alert" className="text-xs text-muted-foreground">
            {t("metadataEntries.hiddenNamesSkipped", { names: hiddenNames.join(", ") })}
          </p>
        )}
        <p className="text-xs text-muted-foreground">{t("metadataEditor.tagHint")}</p>
      </div>
      <MetadataEditorField
        label={t("metadataEntries.tags")}
        labelBadges={
          editor.tags.length > 0 && (
            <span className="shrink-0 text-xs tabular-nums text-muted-foreground">{editor.tags.length}</span>
          )
        }
        status={status}
      >
        {languageScope && languageScope.kind !== "all" && (
          <p className="mb-2 text-xs text-muted-foreground">
            {t(language ? "metadataEditor.tagsInLanguage" : "metadataEditor.tagsOtherLanguage", {
              language: languageName,
            })}
          </p>
        )}
        {editor.tags.length ? (
          <ul className="divide-y overflow-hidden rounded-md border bg-card" aria-label={t("metadataEntries.tags")}>
            {editor.tags.map((tag) => {
              const added = editor.isAdded(tag.id);
              const shown = shownName(tag);
              const naming = namingId === tag.id;
              const otherNames = metadataTagAlternateNames(tag, shown.name, editor.draftNames(tag.id));
              return (
                <li key={tag.id} className={cn(added && "bg-primary/5")}>
                  <div className="flex min-h-12 items-center gap-1 py-1 pl-3 pr-1.5">
                    <div className="min-w-0 flex-1 py-0.5">
                      <div className="flex min-w-0 items-center gap-2">
                        <span className={cn("min-w-0 truncate text-sm", !shown.own && "text-muted-foreground")}>
                          {shown.name}
                        </span>
                        {!shown.own && (
                          <Badge variant="outline" className="shrink-0 px-1.5 py-0 text-[11px] font-medium">
                            {t("metadataEditor.tagNameInherited", { language: languageName })}
                          </Badge>
                        )}
                        {added && (
                          <Badge
                            variant="secondary"
                            className="shrink-0 px-1.5 py-0 text-[11px] font-medium"
                            title={t("metadataEditor.tagAdded")}
                          >
                            {t("metadataEditor.tagAddedBadge")}
                          </Badge>
                        )}
                      </div>
                      {otherNames.length > 0 && (
                        <div className="truncate text-xs text-muted-foreground" title={otherNames.join(" · ")}>
                          {otherNames.join(" · ")}
                        </div>
                      )}
                    </div>
                    <button
                      type="button"
                      onClick={() => setNamingId(naming ? null : tag.id)}
                      aria-label={t("metadataEditor.editTagNames", { name: shown.name })}
                      aria-expanded={naming}
                      title={t("metadataEditor.editTagNames", { name: shown.name })}
                      className={cn(rowButtonClass, naming && "bg-muted text-foreground")}
                    >
                      <Languages className="h-4 w-4" />
                      {editor.isRenamed(tag.id) && (
                        <span className="absolute right-1.5 top-1.5 h-1.5 w-1.5 rounded-full bg-primary">
                          <span className="sr-only">{t("metadataEditor.unsavedMarker")}</span>
                        </span>
                      )}
                    </button>
                    <button
                      type="button"
                      onClick={() => {
                        editor.remove(tag.id);
                        if (naming) setNamingId(null);
                      }}
                      aria-label={t("metadataEntries.removeTag", { name: shown.name })}
                      title={t("metadataEntries.removeTag", { name: shown.name })}
                      className={rowButtonClass}
                    >
                      <X className="h-4 w-4" />
                    </button>
                  </div>
                  {naming && (
                    <div className="border-t bg-muted/40 px-3 py-3">
                      <MetadataTagNamesPanel
                        tag={tag}
                        title={shown.name}
                        language={language}
                        editor={editor}
                        onDone={() => setNamingId(null)}
                      />
                    </div>
                  )}
                </li>
              );
            })}
          </ul>
        ) : (
          <p className="rounded-md border border-dashed px-3 py-2 text-sm text-muted-foreground">
            {t("metadataEntries.noTags")}
          </p>
        )}
      </MetadataEditorField>
      {editor.canRestore && (
        <Button variant="ghost" size="sm" className="gap-1 px-2 text-muted-foreground" onClick={editor.restore}>
          <RotateCcw className="h-3.5 w-3.5" />
          {t("metadataEntries.restoreTags")}
        </Button>
      )}
    </div>
  );
}
