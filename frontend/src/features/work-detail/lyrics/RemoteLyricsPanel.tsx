import { useMemo, useState } from "react";
import { CloudDownload, FileText, Search } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { FloatingSelect, type FloatingSelectOption } from "@/components/ui/floating-select";
import { toastFromError, useToast } from "@/components/ui/toast";
import type { LyricsManagerAudio } from "@/features/work-detail/lyrics/lyricsManagerModel";
import { LyricsPreview, loadLocalLyricsText, loadRemoteLyricsText } from "@/features/work-detail/lyrics/LyricsPreview";
import {
  defaultLyricsEditionCode,
  editionLanguageKey,
  folderDisplayName,
  localLyricsEditions,
  localLyricsFiles,
  lyricsTargetFolders,
  remoteLyricsFiles,
  remoteLyricsRequest,
  suggestRemoteLyricsMapping,
  type RemoteLyricsFile,
} from "@/features/work-detail/lyrics/remoteLyricsModel";
import { formatTrackDuration } from "@/features/work-detail/media/mediaTreeModel";
import { api, ApiError, type LibrarySource, type WorkDetail, type WorkTranslation } from "@/lib/api";
import { editionLyricsLanguage, lyricsLanguageFolderName, lyricsLanguages } from "@/lib/lyricsLanguage";

const unassignedValue = "none";
/** The source that copies lyrics from another local edition of the family. */
const librarySourceValue = "library";

/** Where the listed files come from: a remote edition, or a library folder of another edition. */
type LyricsOrigin = { kind: "remote"; sourceId: number; code: string } | { kind: "library"; folderId: number };

type RemoteListing =
  | { status: "idle" | "loading" | "not_found" | "error" }
  | { status: "ready"; origin: LyricsOrigin; files: RemoteLyricsFile[] };

export type RemoteLyricsFetchState = ReturnType<typeof useRemoteLyricsFetch>;

/**
 * State of the "Add lyrics" tab: which source and edition to read, what
 * language the lyrics are in, what to add, and which track each file goes to.
 */
export function useRemoteLyricsFetch({
  work,
  audio,
  sources,
  canAssign,
  onFetched,
}: {
  work: WorkDetail;
  audio: LyricsManagerAudio[];
  sources: LibrarySource[];
  canAssign: boolean;
  onFetched: () => Promise<void>;
}) {
  const { t } = useTranslation();
  const toast = useToast();
  const editions = useMemo(() => familyEditions(work), [work]);
  const libraryEditions = useMemo(() => localLyricsEditions(editions, work.id), [editions, work.id]);
  const targetFolders = useMemo(
    () => lyricsTargetFolders(work.localFolders ?? [], work.mediaItems),
    [work.localFolders, work.mediaItems],
  );
  const [sourceValue, setSourceValue] = useState(() => (sources[0] ? String(sources[0].id) : librarySourceValue));
  const fromLibrary = sourceValue === librarySourceValue;
  const sourceEditions = fromLibrary ? libraryEditions : editions;
  const [editionCode, setEditionCode] = useState(() => defaultLyricsEditionCode(sourceEditions, work.primaryCode));
  const [language, setLanguage] = useState(() => editionLanguage(sourceEditions, editionCode));
  const [folderId, setFolderId] = useState(() => targetFolders[0]?.id ?? 0);
  const [listing, setListing] = useState<RemoteListing>({ status: "idle" });
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [mapping, setMapping] = useState<Map<number, string | null>>(new Map());
  const [fetching, setFetching] = useState(false);
  const files = listing.status === "ready" ? listing.files : [];
  const request = remoteLyricsRequest(selected, mapping, canAssign);

  const resetListing = () => setListing({ status: "idle" });

  /** Selects an edition and takes its declared language as the lyrics' language. */
  const selectEdition = (nextEditions: WorkTranslation[], code: string) => {
    setEditionCode(code);
    setLanguage(editionLanguage(nextEditions, code));
    resetListing();
  };

  const showFiles = (origin: LyricsOrigin, found: RemoteLyricsFile[]) => {
    setListing({ status: "ready", origin, files: found });
    setSelected(new Set(found.map((file) => file.path)));
    setMapping(suggestRemoteLyricsMapping(audio, found));
  };

  const list = async () => {
    if (!editionCode) return;
    setListing({ status: "loading" });
    try {
      if (fromLibrary) {
        const edition = libraryEditions.find((candidate) => candidate.primaryCode === editionCode);
        if (!edition?.workId) return setListing({ status: "not_found" });
        const detail = await api.getWork(edition.workId);
        const folder = (detail.localFolders ?? []).find((candidate) => candidate.state === "active");
        if (!folder) return setListing({ status: "not_found" });
        showFiles({ kind: "library", folderId: folder.id }, localLyricsFiles(detail.mediaItems, folder.rootPath));
        return;
      }
      const sourceId = Number(sourceValue);
      if (!sourceId) return setListing({ status: "idle" });
      const response = await api.getRemoteSourceWorkTracks(sourceId, editionCode);
      showFiles({ kind: "remote", sourceId, code: editionCode }, remoteLyricsFiles(response.tracks));
    } catch (error) {
      setListing({ status: error instanceof ApiError && error.status === 404 ? "not_found" : "error" });
    }
  };

  const download = async () => {
    if (listing.status !== "ready" || request.files.length === 0 || !folderId) return;
    setFetching(true);
    try {
      const origin = listing.origin;
      const result = await api.fetchWorkLyrics(work.id, {
        sourceId: origin.kind === "remote" ? origin.sourceId : 0,
        remoteCode: origin.kind === "remote" ? origin.code : "",
        sourceFolderId: origin.kind === "library" ? origin.folderId : undefined,
        folderId,
        language,
        ...request,
      });
      toast.success(
        result.downloaded > 0
          ? t("lyricsManager.downloaded", { count: result.downloaded, folder: folderDisplayName(result.folder) })
          : t("lyricsManager.alreadyPresent", { folder: folderDisplayName(result.folder) }),
      );
      resetListing();
      await onFetched();
    } catch (error) {
      toast.notify(toastFromError(error, t("lyricsManager.downloadFailed")));
    } finally {
      setFetching(false);
    }
  };

  return {
    editions: sourceEditions,
    sources,
    libraryAvailable: libraryEditions.length > 0,
    targetFolders,
    sourceValue,
    fromLibrary,
    editionCode,
    language,
    folderId,
    listing,
    files,
    selected,
    mapping,
    fetching,
    request,
    canAssign,
    available: (sources.length > 0 || libraryEditions.length > 0) && targetFolders.length > 0,
    setSourceValue: (value: string) => {
      const nextEditions = value === librarySourceValue ? libraryEditions : editions;
      setSourceValue(value);
      selectEdition(nextEditions, defaultLyricsEditionCode(nextEditions, work.primaryCode));
    },
    setEditionCode: (value: string) => selectEdition(sourceEditions, value),
    setLanguage,
    setFolderId,
    toggleFile: (path: string, checked: boolean) =>
      setSelected((current) => {
        const next = new Set(current);
        if (checked) next.add(path);
        else next.delete(path);
        return next;
      }),
    toggleAll: (checked: boolean) => setSelected(new Set(checked ? files.map((file) => file.path) : [])),
    assign: (audioID: number, path: string | null) => setMapping((current) => new Map(current).set(audioID, path)),
    list,
    download,
  };
}

export function RemoteLyricsPanel({ state, audio }: { state: RemoteLyricsFetchState; audio: LyricsManagerAudio[] }) {
  const { t } = useTranslation();
  const [previewAudioID, setPreviewAudioID] = useState<number | null>(null);
  const sourceOptions: FloatingSelectOption[] = [
    ...state.sources.map((source) => ({ value: String(source.id), label: source.displayName })),
    ...(state.libraryAvailable ? [{ value: librarySourceValue, label: t("lyricsManager.librarySource") }] : []),
  ];
  const editionOptions: FloatingSelectOption[] = state.editions.map((edition) => {
    const key = editionLanguageKey(edition.metadataLanguage);
    const language = key ? t(key) : edition.editionLabel || edition.metadataLanguage;
    const origin = edition.origin ? ` · ${t("lyricsManager.originEdition")}` : "";
    return { value: edition.primaryCode, label: `${edition.primaryCode} · ${language || "—"}${origin}` };
  });
  const languageOptions: FloatingSelectOption[] = lyricsLanguages.map(([language, labelKey]) => ({
    value: language || allLanguagesValue,
    label: t(labelKey),
  }));
  const folderOptions: FloatingSelectOption[] = state.targetFolders.map((folder) => ({
    value: String(folder.id),
    label: folder.rootPath,
  }));
  const busy = state.fetching || state.listing.status === "loading";
  const selectedCount = state.selected.size;
  const allSelected = state.files.length > 0 && selectedCount === state.files.length;
  const selectedFiles = state.files.filter((file) => state.selected.has(file.path));
  const listing = state.listing;

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-end gap-2">
        <label className="min-w-0 flex-1 basis-48">
          <span className="mb-1 block text-xs font-medium text-muted-foreground">
            {t("lyricsManager.lyricsSource")}
          </span>
          <FloatingSelect
            value={state.sourceValue}
            options={sourceOptions}
            onValueChange={state.setSourceValue}
            ariaLabel={t("lyricsManager.lyricsSource")}
            disabled={busy}
          />
        </label>
        <label className="min-w-0 flex-1 basis-56">
          <span className="mb-1 block text-xs font-medium text-muted-foreground">{t("lyricsManager.edition")}</span>
          <FloatingSelect
            value={state.editionCode}
            options={editionOptions}
            onValueChange={state.setEditionCode}
            ariaLabel={t("lyricsManager.edition")}
            disabled={busy}
          />
        </label>
        <Button variant="outline" onClick={() => void state.list()} disabled={busy || !state.editionCode}>
          <Search className="h-4 w-4" />
          {listing.status === "loading" ? t("lyricsManager.listing") : t("lyricsManager.listLyrics")}
        </Button>
      </div>
      <p className="text-xs text-muted-foreground">
        {state.fromLibrary ? t("lyricsManager.libraryHint") : t("lyricsManager.remoteHint")}
      </p>

      {listing.status === "not_found" && (
        <p role="status" className="text-sm text-muted-foreground">
          {t("lyricsManager.editionNotFound")}
        </p>
      )}
      {listing.status === "error" && (
        <p role="status" className="text-sm text-muted-foreground">
          {t("lyricsManager.remoteLoadFailed")}
        </p>
      )}
      {listing.status === "ready" && state.files.length === 0 && (
        <p role="status" className="text-sm text-muted-foreground">
          {t("lyricsManager.noRemoteLyrics")}
        </p>
      )}

      {listing.status === "ready" && state.files.length > 0 && (
        <>
          <section aria-label={t("lyricsManager.remoteFiles")}>
            <div className="mb-1 flex items-center gap-2 text-xs font-medium text-muted-foreground">
              <Checkbox
                checked={allSelected}
                indeterminate={selectedCount > 0 && !allSelected}
                onCheckedChange={state.toggleAll}
                aria-label={t("lyricsManager.selectAllRemote")}
                disabled={busy}
              />
              <span>{t("lyricsManager.remoteFiles")}</span>
              <span className="ml-auto tabular-nums">
                {t("lyricsManager.remoteSelected", { selected: selectedCount, total: state.files.length })}
              </span>
            </div>
            <ul className="app-scrollbar max-h-48 divide-y overflow-y-auto rounded-lg border">
              {state.files.map((file) => (
                <li key={file.path} className="flex items-center gap-2 px-3 py-1.5 text-sm">
                  <Checkbox
                    checked={state.selected.has(file.path)}
                    onCheckedChange={(checked) => state.toggleFile(file.path, checked)}
                    aria-label={file.path}
                    disabled={busy}
                  />
                  <span className="min-w-0 flex-1 truncate" title={file.path}>
                    {file.title}
                  </span>
                  {file.folder && (
                    <span className="max-w-[40%] truncate text-xs text-muted-foreground">{file.folder}</span>
                  )}
                </li>
              ))}
            </ul>
          </section>

          <section aria-label={t("lyricsManager.lyricsLanguage")} className="space-y-1">
            <h4 className="text-xs font-medium text-muted-foreground">{t("lyricsManager.lyricsLanguage")}</h4>
            <FloatingSelect
              value={state.language || allLanguagesValue}
              options={languageOptions}
              onValueChange={(value) => state.setLanguage(value === allLanguagesValue ? "" : value)}
              ariaLabel={t("lyricsManager.lyricsLanguage")}
              disabled={busy}
            />
            <p className="text-xs text-muted-foreground">{t("lyricsManager.lyricsLanguageHint")}</p>
          </section>

          <section aria-label={t("lyricsManager.mapTracks")}>
            <h4 className="mb-1 text-xs font-medium text-muted-foreground">{t("lyricsManager.mapTracks")}</h4>
            <p className="mb-2 text-xs text-muted-foreground">
              {state.canAssign ? t("lyricsManager.mapHint") : t("lyricsManager.assignNeedsWrite")}
            </p>
            <ul className="divide-y rounded-lg border">
              {audio.map((entry) => (
                <RemoteMappingRow
                  key={entry.mediaItemId}
                  entry={entry}
                  value={state.mapping.get(entry.mediaItemId) ?? null}
                  files={selectedFiles}
                  disabled={busy || !state.canAssign}
                  previewOpen={previewAudioID === entry.mediaItemId}
                  origin={listing.origin}
                  onAssign={(path) => state.assign(entry.mediaItemId, path)}
                  onTogglePreview={() =>
                    setPreviewAudioID((current) => (current === entry.mediaItemId ? null : entry.mediaItemId))
                  }
                />
              ))}
            </ul>
          </section>

          <section aria-label={t("lyricsManager.targetFolder")} className="space-y-1">
            <h4 className="text-xs font-medium text-muted-foreground">{t("lyricsManager.targetFolder")}</h4>
            {folderOptions.length > 1 ? (
              <FloatingSelect
                value={String(state.folderId)}
                options={folderOptions}
                onValueChange={(value) => state.setFolderId(Number(value))}
                ariaLabel={t("lyricsManager.targetFolder")}
                disabled={busy}
              />
            ) : (
              <p className="truncate text-sm" title={state.targetFolders[0]?.rootPath}>
                {state.targetFolders[0]?.rootPath}
              </p>
            )}
            <p className="text-xs text-muted-foreground">
              {t("lyricsManager.targetHint", { folder: lyricsLanguageFolderName(state.language) })}
            </p>
          </section>
        </>
      )}
    </div>
  );
}

/** A select value cannot be empty, so every language has its own. */
const allLanguagesValue = "all";

function RemoteMappingRow({
  entry,
  value,
  files,
  disabled,
  previewOpen,
  origin,
  onAssign,
  onTogglePreview,
}: {
  entry: LyricsManagerAudio;
  value: string | null;
  files: RemoteLyricsFile[];
  disabled: boolean;
  previewOpen: boolean;
  origin: LyricsOrigin;
  onAssign: (path: string | null) => void;
  onTogglePreview: () => void;
}) {
  const { t } = useTranslation();
  const selectedFile = value ? (files.find((file) => file.path === value) ?? null) : null;
  const options: FloatingSelectOption[] = [
    { value: unassignedValue, label: t("lyricsManager.noAssignment") },
    ...files.map((file) => ({
      value: file.path,
      label: (
        <span className="flex min-w-0 flex-col" title={file.path}>
          <span className="truncate">{file.title}</span>
          {file.folder && <span className="truncate text-xs text-muted-foreground">{file.folder}</span>}
        </span>
      ),
    })),
  ];
  const duration = formatTrackDuration(entry.durationSeconds);
  return (
    <li className="px-3 py-2.5">
      <div className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,20rem)_auto] sm:items-center">
        <div className="min-w-0">
          <div className="truncate text-sm font-medium" title={entry.path}>
            {entry.title}
          </div>
          {duration && <div className="mt-0.5 text-xs tabular-nums text-muted-foreground">{duration}</div>}
        </div>
        <FloatingSelect
          value={selectedFile?.path ?? unassignedValue}
          options={options}
          onValueChange={(next) => onAssign(next === unassignedValue ? null : next)}
          ariaLabel={t("lyricsManager.lyricsFor", { title: entry.title })}
          disabled={disabled}
          contentClassName="max-w-[min(32rem,calc(100vw-1.5rem))]"
        />
        <Button
          variant="ghost"
          size="sm"
          className="justify-self-start sm:justify-self-auto"
          aria-expanded={previewOpen}
          aria-label={t("lyricsManager.previewOf", { title: entry.title })}
          disabled={!selectedFile}
          onClick={onTogglePreview}
        >
          <FileText className="h-4 w-4" />
          {t("lyricsManager.preview")}
        </Button>
      </div>
      {previewOpen && selectedFile && (
        <LyricsPreview
          label={selectedFile.path}
          loadKey={lyricsPreviewKey(origin, selectedFile)}
          load={() => loadLyricsPreview(origin, selectedFile)}
          durationSeconds={entry.durationSeconds}
        />
      )}
    </li>
  );
}

function lyricsPreviewKey(origin: LyricsOrigin, file: RemoteLyricsFile) {
  return origin.kind === "remote"
    ? `${origin.sourceId}:${origin.code}:${file.path}`
    : `library:${origin.folderId}:${file.path}`;
}

function loadLyricsPreview(origin: LyricsOrigin, file: RemoteLyricsFile) {
  if (origin.kind === "remote") return loadRemoteLyricsText(origin.sourceId, origin.code, file.path);
  return file.locationId
    ? loadLocalLyricsText(file.locationId)
    : Promise.reject(new Error("The library lyrics file has no local location."));
}

export function RemoteLyricsDownloadButton({ state }: { state: RemoteLyricsFetchState }) {
  const { t } = useTranslation();
  const ready = state.listing.status === "ready" && state.request.files.length > 0 && state.folderId > 0;
  return (
    <Button onClick={() => void state.download()} disabled={!ready || state.fetching}>
      <CloudDownload className="h-4 w-4" />
      {state.fetching
        ? t("lyricsManager.downloading")
        : t("lyricsManager.download", { count: state.request.files.length })}
    </Button>
  );
}

/** The lyrics language an edition of the list declares. */
function editionLanguage(editions: WorkTranslation[], code: string) {
  const edition = editions.find((candidate) => candidate.primaryCode === code);
  return editionLyricsLanguage(edition?.metadataLanguage ?? "");
}

function familyEditions(work: WorkDetail): WorkTranslation[] {
  const editions = work.translations ?? [];
  if (editions.some((edition) => edition.primaryCode.toUpperCase() === work.primaryCode.toUpperCase())) return editions;
  return [
    {
      workId: work.id,
      primaryCode: work.primaryCode,
      title: work.title,
      metadataLanguage: work.metadataLanguage,
      editionLabel: "",
      origin: editions.length === 0,
      official: false,
      translationKind: "unknown",
      current: true,
      hasMedia: true,
      mediaState: "indexed_available",
      localAvailable: true,
    },
    ...editions,
  ];
}
