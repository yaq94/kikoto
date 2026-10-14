import {
  AlertTriangle,
  ArrowLeft,
  CheckCircle2,
  ChevronDown,
  ChevronRight,
  Cloud,
  FileAudio,
  FileText,
  FileVideo,
  Folder,
  FolderCog,
  FolderOpen,
  HardDrive,
  Image as ImageIcon,
  Loader2,
} from "lucide-react";
import { useMemo, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogHeader } from "@/components/ui/dialog";
import { ConfirmMediaBatchDeleteDialog } from "@/features/work-detail/dialogs/ConfirmMediaBatchDeleteDialog";
import {
  buildFolderEditTree,
  folderEditExtensions,
  folderEditResourceChanges,
  folderEditSelectedBytes,
  folderEditTreeFiles,
  withFolderEditPaths,
  type FolderEditFile,
  type FolderEditResource,
  type FolderEditResultEntry,
  type FolderEditResultState,
  type FolderEditTarget,
  type FolderEditTreeNode,
} from "@/features/work-detail/folder-edit/folderEditModel";
import {
  useFolderEditWorkspace,
  type FolderEditInitialRemote,
  type FolderEditWorkspace,
} from "@/features/work-detail/folder-edit/useFolderEditWorkspace";
import { formatBytes } from "@/features/work-detail/media/mediaTreeModel";
import { languageLabel, naturalCompare } from "@/features/work-detail/workflows/RemoteFetchWorkspaceTrees";
import type { MediaDeleteTarget } from "@/features/work-detail/workflows/useMediaCleanupWorkflow";
import type { MediaItem, RemoteFetchFileDecision, RemoteFetchResolution, WorkFolderEditResult } from "@/lib/api";
import { cn } from "@/lib/tailwindClassNames";

type EditPane = "sources" | "select" | "result";

const noSelection: ReadonlySet<string> = new Set();

/**
 * Edits one local folder of a work in three columns: where files can come
 * from, the files of the chosen place, and the folder as it will be. The
 * edited folder is the first place; clearing a file there removes it.
 */
export function FolderEditDialog({
  workId,
  folder,
  currentItems,
  initialRemote,
  onBack,
  onClose,
  onApplied,
}: {
  workId: number;
  folder: { id: number; rootPath: string };
  currentItems: MediaItem[];
  initialRemote?: FolderEditInitialRemote | null;
  /** Returns to the folder's own file list. */
  onBack?: () => void;
  onClose: () => void;
  onApplied: (result: WorkFolderEditResult) => void;
}) {
  const { t } = useTranslation();
  const target = useMemo<FolderEditTarget>(
    () => ({ workId, folderId: folder.id, rootPath: folder.rootPath }),
    [folder.id, folder.rootPath, workId],
  );
  const workspace = useFolderEditWorkspace({ target, currentItems, initialRemote, onApplied });
  const [pane, setPane] = useState<EditPane>(initialRemote ? "select" : "sources");
  const [confirmation, setConfirmation] = useState<{ step: 1 | 2; targets: MediaDeleteTarget[] } | null>(null);
  const removalTargets = useMemo<MediaDeleteTarget[]>(
    () =>
      workspace.removedFiles
        .filter((file) => file.locationId !== null)
        .map((file) => ({
          kind: "local",
          locationId: file.locationId!,
          workId,
          title: file.name,
          path: `${target.rootPath.replace(/\/+$/, "")}/${file.path}`,
          sizeBytes: file.sizeBytes,
        })),
    [target.rootPath, workId, workspace.removedFiles],
  );
  const startApply = () => {
    if (removalTargets.length > 0) setConfirmation({ step: 1, targets: removalTargets });
    else void workspace.apply();
  };
  const paneCounts: Record<EditPane, number> = {
    sources: workspace.resources.length,
    select: workspace.request.additions.length,
    result: workspace.result.filter((entry) => entry.state !== "remove" && entry.state !== "excluded").length,
  };

  return (
    <>
      <Dialog
        onClose={onClose}
        size="full"
        dismissible={!workspace.applying}
        marker="folder-edit"
        className="h-[var(--dialog-max-height)] max-w-7xl bg-background md:h-[min(90dvh,var(--dialog-max-height))]"
      >
        <DialogHeader
          title={t("folderEdit.title")}
          description={t("folderEdit.description")}
          icon={<FolderCog className="h-4 w-4" />}
          onClose={onClose}
          closeLabel={t("content.close")}
        />
        <div className="flex shrink-0 gap-1 border-b bg-background px-3 py-2 lg:hidden">
          <div className="flex min-w-0 flex-1 gap-1 rounded-lg bg-muted p-1">
            {(["sources", "select", "result"] as const).map((value) => (
              <button
                key={value}
                type="button"
                aria-pressed={pane === value}
                onClick={() => setPane(value)}
                className={cn(
                  "inline-flex min-h-8 min-w-0 flex-1 items-center justify-center gap-1.5 rounded-md px-1.5 text-xs font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                  pane === value
                    ? "bg-background text-foreground shadow-sm"
                    : "text-muted-foreground hover:text-foreground",
                )}
              >
                <span className="truncate">{t(`folderEdit.pane.${value}`)}</span>
                <span aria-hidden="true" className="rounded-full bg-background/60 px-1.5 text-3xs tabular-nums">
                  {paneCounts[value]}
                </span>
              </button>
            ))}
          </div>
        </div>
        <div className="flex min-h-0 flex-1 flex-col lg:grid lg:grid-cols-[17rem_minmax(0,1fr)_minmax(0,25rem)]">
          <ResourceColumn
            workspace={workspace}
            active={pane === "sources"}
            onSelect={(resource) => {
              workspace.selectResource(resource);
              setPane("select");
            }}
          />
          <SelectionColumn workspace={workspace} active={pane === "select"} />
          <ResultColumn workspace={workspace} rootPath={target.rootPath} active={pane === "result"} />
        </div>
        <FolderEditFooter
          workspace={workspace}
          onBack={onBack}
          onReviewConflicts={() => setPane("result")}
          onApply={startApply}
        />
      </Dialog>
      {confirmation && (
        <ConfirmMediaBatchDeleteDialog
          targets={confirmation.targets}
          mode="files_only"
          step={confirmation.step}
          deleting={workspace.applying}
          onCancel={() => setConfirmation(null)}
          onContinue={() => setConfirmation({ ...confirmation, step: 2 })}
          onConfirm={() => {
            setConfirmation(null);
            void workspace.apply();
          }}
        />
      )}
    </>
  );
}

function ColumnLabel({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <div className={cn("text-2xs font-medium uppercase tracking-wide text-muted-foreground", className)}>
      {children}
    </div>
  );
}

function ResourceColumn({
  workspace,
  active,
  onSelect,
}: {
  workspace: FolderEditWorkspace;
  active: boolean;
  onSelect: (resource: FolderEditResource) => void;
}) {
  const { t } = useTranslation();
  const local = workspace.resources.filter((resource) => resource.kind !== "remote");
  const remote = workspace.resources.filter((resource) => resource.kind === "remote");
  const groups = [
    { key: "local", label: t("folderEdit.local"), items: local, empty: "" },
    { key: "remote", label: t("folderEdit.remote"), items: remote, empty: t("folderEdit.noRemoteSources") },
  ];
  return (
    <aside
      aria-label={t("folderEdit.pane.sources")}
      className={cn(
        "app-scroll min-h-0 flex-1 overflow-auto bg-muted/30 lg:block lg:flex-none lg:border-r",
        active ? "block" : "hidden",
      )}
    >
      {groups.map((group) => (
        <div key={group.key} className="px-3 py-3 lg:px-4" role="group" aria-label={group.label}>
          <ColumnLabel className="mb-2">{group.label}</ColumnLabel>
          <div className="space-y-1.5">
            {group.items.map((resource) => (
              <ResourceOption
                key={resource.id}
                resource={resource}
                selected={workspace.activeResource.id === resource.id}
                changes={folderEditResourceChanges(
                  resource,
                  workspace.files[resource.id]?.status === "ready" ? workspace.files[resource.id].files : undefined,
                  workspace.selection[resource.id],
                )}
                disabled={workspace.applying}
                onSelect={() => onSelect(resource)}
              />
            ))}
            {group.key === "remote" && workspace.sourcesStatus === "loading" && (
              <div className="flex items-center gap-2 px-1 py-1 text-xs text-muted-foreground" role="status">
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
                {t("folderEdit.loadingSources")}
              </div>
            )}
            {group.key === "remote" && workspace.sourcesStatus === "error" && (
              <div className="space-y-1.5 px-1 text-xs text-muted-foreground" role="alert">
                <div>{t("folderEdit.sourcesFailed")}</div>
                <Button variant="outline" size="sm" onClick={workspace.retrySources}>
                  {t("folderEdit.retry")}
                </Button>
              </div>
            )}
            {group.items.length === 0 && group.empty && workspace.sourcesStatus === "ready" && (
              <div className="px-1 text-xs text-muted-foreground">{group.empty}</div>
            )}
          </div>
        </div>
      ))}
    </aside>
  );
}

function resourceTitle(resource: FolderEditResource, t: (key: string) => string) {
  if (resource.kind === "current") return t("folderEdit.currentFolder");
  return languageLabel(resource.language, t);
}

function ResourceOption({
  resource,
  selected,
  changes,
  disabled,
  onSelect,
}: {
  resource: FolderEditResource;
  selected: boolean;
  changes: number;
  disabled: boolean;
  onSelect: () => void;
}) {
  const { t } = useTranslation();
  const Icon = resource.kind === "remote" ? Cloud : resource.kind === "current" ? FolderOpen : HardDrive;
  const detail = resource.kind === "remote" ? resource.sourceName : resource.rootPath;
  return (
    <button
      type="button"
      aria-pressed={selected}
      disabled={disabled}
      onClick={onSelect}
      title={detail}
      className={cn(
        "flex w-full items-start gap-2.5 rounded-lg border px-3 py-2 text-left transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-70",
        selected ? "border-primary bg-primary/[0.07]" : "bg-background hover:border-foreground/20 hover:bg-muted/50",
      )}
    >
      <Icon className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
      <span className="min-w-0 flex-1 leading-tight">
        <span className="flex items-center gap-1.5">
          <span className="truncate text-sm font-medium">{resourceTitle(resource, t)}</span>
          {resource.kind !== "current" && resource.origin && (
            <span className="shrink-0 text-2xs text-muted-foreground">{t("remoteFetch.origin")}</span>
          )}
        </span>
        <span className="mt-1 block truncate font-mono text-2xs text-muted-foreground">{resource.editionCode}</span>
        <span className="mt-0.5 block truncate text-2xs text-muted-foreground">{detail}</span>
      </span>
      {changes > 0 && (
        <span
          className={cn(
            "shrink-0 rounded-full px-1.5 py-px text-2xs font-medium tabular-nums",
            resource.kind === "current" ? "bg-error-surface text-error-foreground" : "bg-primary/10 text-primary",
          )}
          aria-label={t(resource.kind === "current" ? "folderEdit.removeCount" : "folderEdit.addCount", {
            count: changes,
          })}
        >
          {resource.kind === "current" ? "−" : "+"}
          {changes}
        </span>
      )}
    </button>
  );
}

function SelectionColumn({ workspace, active }: { workspace: FolderEditWorkspace; active: boolean }) {
  const { t } = useTranslation();
  const resource = workspace.activeResource;
  const state = workspace.files[resource.id];
  const files = state?.status === "ready" ? state.files : noFiles;
  const selected = workspace.selection[resource.id] ?? noSelection;
  const tree = useMemo(() => buildFolderEditTree(files), [files]);
  const extensions = useMemo(() => folderEditExtensions(files).slice(0, 5), [files]);
  const allPaths = useMemo(() => files.map((file) => file.path), [files]);
  const selectedCount = files.filter((file) => selected.has(file.path)).length;
  const bytes = folderEditSelectedBytes(files, selected);
  const setIncluded = (paths: readonly string[], included: boolean) =>
    workspace.setSelected(resource.id, withFolderEditPaths(selected, paths, included));
  const hint =
    resource.kind === "current"
      ? t("folderEdit.currentHint")
      : resource.kind === "local"
        ? t("folderEdit.localHint")
        : t("folderEdit.remoteHint", { source: resource.sourceName });
  return (
    <section
      aria-label={t("folderEdit.pane.select")}
      className={cn("min-h-0 flex-1 flex-col bg-card lg:flex lg:border-r", active ? "flex" : "hidden")}
    >
      <div className="flex shrink-0 flex-col gap-2 border-b px-3 py-2.5 lg:px-4">
        <div className="flex items-center justify-between gap-3">
          <div className="min-w-0">
            <div className="truncate text-sm font-medium">
              {resourceTitle(resource, t)}
              {resource.kind !== "current" && (
                <span className="ml-1.5 font-mono text-xs font-normal text-muted-foreground">
                  {resource.editionCode}
                </span>
              )}
            </div>
            <div className="truncate text-xs tabular-nums text-muted-foreground">
              {t("folderEdit.selectionSummary", { selected: selectedCount, total: files.length })}
              {selectedCount > 0 && bytes > 0 ? ` · ${formatBytes(bytes)}` : ""}
            </div>
          </div>
          <div className="-mr-2 flex shrink-0 gap-1">
            <Button
              variant="ghost"
              size="sm"
              disabled={workspace.applying || files.length === 0}
              onClick={() => setIncluded(allPaths, true)}
            >
              {t("remoteFetch.all")}
            </Button>
            <Button
              variant="ghost"
              size="sm"
              disabled={workspace.applying || files.length === 0}
              onClick={() => setIncluded(allPaths, false)}
            >
              {t("remoteFetch.none")}
            </Button>
          </div>
        </div>
        <p className="text-xs text-muted-foreground">{hint}</p>
        {extensions.length > 1 && (
          <div role="group" aria-label={t("remoteFetch.fileTypes")} className="flex flex-wrap items-center gap-1.5">
            {extensions.map(({ extension, paths }) => {
              const count = paths.filter((path) => selected.has(path)).length;
              const checked = count === paths.length;
              return (
                <label
                  key={extension}
                  className={cn(
                    "inline-flex h-8 cursor-pointer items-center gap-1.5 rounded-full border pl-1.5 pr-2.5 text-xs transition-colors",
                    count > 0
                      ? "border-primary/40 bg-primary/[0.07]"
                      : "bg-background text-muted-foreground hover:bg-muted",
                  )}
                >
                  <Checkbox
                    className="h-4 w-4 rounded-full"
                    checked={checked}
                    indeterminate={count > 0 && !checked}
                    disabled={workspace.applying}
                    onCheckedChange={() => setIncluded(paths, !checked)}
                    aria-label={t("remoteFetch.include", { extension: extension.toUpperCase() })}
                  />
                  <span className="font-medium">{extension.toUpperCase()}</span>
                  <span className="tabular-nums text-muted-foreground">{paths.length}</span>
                </label>
              );
            })}
          </div>
        )}
      </div>
      <div className="app-scroll min-h-0 flex-1 overflow-auto overscroll-contain px-2 py-2">
        {!state || state.status === "loading" ? (
          <PaneNote>
            <Loader2 className="h-4 w-4 animate-spin" />
            {t("folderEdit.loadingFiles")}
          </PaneNote>
        ) : state.status === "error" ? (
          <PaneNote>
            {t("folderEdit.filesFailed")}
            <Button variant="outline" size="sm" onClick={() => workspace.retryResource(resource)}>
              {t("folderEdit.retry")}
            </Button>
          </PaneNote>
        ) : files.length === 0 ? (
          <PaneNote>{t("folderEdit.noFiles")}</PaneNote>
        ) : (
          <SelectionNode
            // Folder open state belongs to the resource it was opened in.
            key={resource.id}
            node={tree}
            depth={0}
            selected={selected}
            disabled={workspace.applying}
            onToggle={setIncluded}
            isRoot
          />
        )}
      </div>
    </section>
  );
}

const noFiles: FolderEditFile[] = [];

function PaneNote({ children }: { children: ReactNode }) {
  return (
    <div
      className="grid min-h-32 place-items-center gap-2 rounded-md border border-dashed p-4 text-center text-xs text-muted-foreground"
      role="status"
    >
      {children}
    </div>
  );
}

function sortedFolders<T>(node: FolderEditTreeNode<T>) {
  return Array.from(node.children.values()).sort((left, right) => naturalCompare(left.name, right.name));
}

function SelectionNode({
  node,
  depth,
  selected,
  disabled,
  isRoot = false,
  onToggle,
}: {
  node: FolderEditTreeNode<FolderEditFile>;
  depth: number;
  selected: ReadonlySet<string>;
  disabled: boolean;
  isRoot?: boolean;
  onToggle: (paths: readonly string[], included: boolean) => void;
}) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(depth <= 1);
  const paths = useMemo(() => folderEditTreeFiles(node).map((file) => file.path), [node]);
  const count = paths.filter((path) => selected.has(path)).length;
  const checked = paths.length > 0 && count === paths.length;
  const files = [...node.files].sort((left, right) => naturalCompare(left.name, right.name));
  return (
    <div className="space-y-px">
      {!isRoot && (
        <div
          className="flex min-h-9 items-center gap-2 rounded-md px-2 text-sm hover:bg-muted/70"
          style={{ paddingLeft: (depth - 1) * 14 + 8 }}
        >
          <button
            type="button"
            className="rounded p-0.5 text-muted-foreground hover:bg-background"
            onClick={() => setOpen((value) => !value)}
            aria-expanded={open}
            aria-label={
              open
                ? t("libraryDetail.collapseFolder", { name: node.name })
                : t("libraryDetail.expandFolder", { name: node.name })
            }
          >
            {open ? <ChevronDown className="h-3.5 w-3.5" /> : <ChevronRight className="h-3.5 w-3.5" />}
          </button>
          <Checkbox
            checked={checked}
            indeterminate={count > 0 && !checked}
            disabled={disabled || paths.length === 0}
            onCheckedChange={() => onToggle(paths, !checked)}
            aria-label={t("libraryDetail.selectFolder", { name: node.name })}
          />
          <Folder className="h-4 w-4 shrink-0 text-primary" aria-hidden="true" />
          <button
            type="button"
            className="min-w-0 flex-1 truncate text-left font-medium"
            onClick={() => setOpen((value) => !value)}
          >
            {node.name}
          </button>
          <span className="shrink-0 text-xs tabular-nums text-muted-foreground">
            {count}/{paths.length}
          </span>
        </div>
      )}
      {(isRoot || open) &&
        sortedFolders(node).map((child) => (
          <SelectionNode
            key={child.path}
            node={child}
            depth={depth + 1}
            selected={selected}
            disabled={disabled}
            onToggle={onToggle}
          />
        ))}
      {(isRoot || open) &&
        files.map((file) => {
          const included = selected.has(file.path);
          return (
            <label
              key={file.path}
              className={cn(
                "flex min-h-9 cursor-pointer items-center gap-2 rounded-md px-2 text-sm",
                included ? "hover:bg-muted/70" : "text-muted-foreground hover:bg-muted/70",
              )}
              style={{ paddingLeft: depth * 14 + 8 }}
              title={file.path}
            >
              <span className="w-[18px] shrink-0" aria-hidden="true" />
              <Checkbox
                checked={included}
                disabled={disabled}
                onCheckedChange={(next) => onToggle([file.path], next)}
                aria-label={t("libraryDetail.selectFile", { name: file.name })}
              />
              <KindIcon kind={file.kind} />
              <span className="min-w-0 flex-1 truncate">{file.name}</span>
              <span className="w-16 shrink-0 text-right text-xs tabular-nums text-muted-foreground">
                {file.sizeBytes === null ? "" : formatBytes(file.sizeBytes)}
              </span>
            </label>
          );
        })}
    </div>
  );
}

function KindIcon({ kind }: { kind: string }) {
  const className = "h-4 w-4 shrink-0 text-muted-foreground";
  if (kind === "audio") return <FileAudio className={className} aria-hidden="true" />;
  if (kind === "video") return <FileVideo className={className} aria-hidden="true" />;
  if (kind === "image") return <ImageIcon className={className} aria-hidden="true" />;
  return <FileText className={className} aria-hidden="true" />;
}

function ResultColumn({
  workspace,
  rootPath,
  active,
}: {
  workspace: FolderEditWorkspace;
  rootPath: string;
  active: boolean;
}) {
  const { t } = useTranslation();
  const tree = useMemo(() => buildFolderEditTree(workspace.result), [workspace.result]);
  const added = workspace.result.filter((entry) => entry.state === "add_local" || entry.state === "add_remote").length;
  const removed = workspace.result.filter((entry) => entry.state === "remove").length;
  return (
    <section
      aria-label={t("folderEdit.pane.result")}
      className={cn("min-h-0 flex-1 flex-col bg-card lg:flex", active ? "flex" : "hidden")}
    >
      <div className="shrink-0 space-y-1 border-b px-3 py-2.5 lg:px-4">
        <div className="flex items-center justify-between gap-2">
          <div className="text-sm font-medium">{t("folderEdit.pane.result")}</div>
          <div className="flex shrink-0 items-center gap-1.5 text-xs tabular-nums">
            {added > 0 && (
              <span className="rounded-full bg-primary/10 px-1.5 py-px font-medium text-primary">
                {t("folderEdit.addCount", { count: added })}
              </span>
            )}
            {removed > 0 && (
              <span className="rounded-full bg-error-surface px-1.5 py-px font-medium text-error-foreground">
                {t("folderEdit.removeCount", { count: removed })}
              </span>
            )}
          </div>
        </div>
        <div className="truncate font-mono text-xs text-muted-foreground" title={rootPath}>
          {rootPath}
        </div>
      </div>
      <div className="app-scroll min-h-0 flex-1 overflow-auto overscroll-contain px-2 py-2">
        {workspace.result.length === 0 ? (
          <PaneNote>{t("folderEdit.emptyResult")}</PaneNote>
        ) : (
          <ResultNode
            node={tree}
            depth={0}
            decisions={workspace.decisions}
            disabled={workspace.applying}
            onDecisionChange={workspace.setDecision}
            isRoot
          />
        )}
      </div>
    </section>
  );
}

const resultBadge: Record<
  FolderEditResultState,
  { key: string; variant: "outline" | "success" | "error" | "warning" }
> = {
  keep: { key: "folderEdit.state.keep", variant: "outline" },
  same: { key: "folderEdit.state.same", variant: "outline" },
  remove: { key: "folderEdit.state.remove", variant: "error" },
  replaced: { key: "folderEdit.state.replaced", variant: "warning" },
  add_local: { key: "folderEdit.state.copy", variant: "success" },
  add_remote: { key: "folderEdit.state.download", variant: "success" },
  conflict: { key: "remoteFetch.actionConflict", variant: "error" },
  excluded: { key: "remoteFetch.actionExcluded", variant: "outline" },
};

function ResultNode({
  node,
  depth,
  decisions,
  disabled,
  isRoot = false,
  onDecisionChange,
}: {
  node: FolderEditTreeNode<FolderEditResultEntry>;
  depth: number;
  decisions: Readonly<Record<string, RemoteFetchFileDecision>>;
  disabled: boolean;
  isRoot?: boolean;
  onDecisionChange: (decision: RemoteFetchFileDecision) => void;
}) {
  const [open, setOpen] = useState(true);
  const entries = [...node.files].sort((left, right) => naturalCompare(left.name, right.name));
  return (
    <div className="space-y-px">
      {!isRoot && (
        <button
          type="button"
          className="flex min-h-8 w-full items-center gap-2 rounded-md px-2 text-left text-sm hover:bg-muted/70"
          style={{ paddingLeft: (depth - 1) * 14 + 8 }}
          aria-expanded={open}
          onClick={() => setOpen((value) => !value)}
        >
          {open ? (
            <ChevronDown className="h-3.5 w-3.5 text-muted-foreground" />
          ) : (
            <ChevronRight className="h-3.5 w-3.5 text-muted-foreground" />
          )}
          <Folder className="h-4 w-4 text-muted-foreground" aria-hidden="true" />
          <span className="truncate font-medium">{node.name}</span>
        </button>
      )}
      {(isRoot || open) &&
        sortedFolders(node).map((child) => (
          <ResultNode
            key={child.path}
            node={child}
            depth={depth + 1}
            decisions={decisions}
            disabled={disabled}
            onDecisionChange={onDecisionChange}
          />
        ))}
      {(isRoot || open) &&
        entries.map((entry) => (
          <ResultRow
            key={entry.key}
            entry={entry}
            depth={depth}
            decision={entry.item ? decisions[entry.item.itemKey] : undefined}
            disabled={disabled}
            onDecisionChange={onDecisionChange}
          />
        ))}
    </div>
  );
}

function ResultRow({
  entry,
  depth,
  decision,
  disabled,
  onDecisionChange,
}: {
  entry: FolderEditResultEntry;
  depth: number;
  decision: RemoteFetchFileDecision | undefined;
  disabled: boolean;
  onDecisionChange: (decision: RemoteFetchFileDecision) => void;
}) {
  const { t } = useTranslation();
  const badge = resultBadge[entry.state];
  const leaving = entry.state === "remove" || entry.state === "replaced";
  const item = entry.item;
  const current: RemoteFetchFileDecision = decision ?? {
    itemKey: item?.itemKey ?? "",
    sourceId: 0,
    resolution: (item?.resolution || "auto") as RemoteFetchResolution,
    targetPath: entry.path,
  };
  const showEditor = Boolean(item && (item.targetConflict || current.resolution !== "auto"));
  const source = item
    ? item.sourceKind === "remote"
      ? t("folderEdit.fromSource", { source: `${item.remoteSourceName} · ${item.remoteCode ?? ""}` })
      : t("folderEdit.fromSource", { source: item.localSourcePath })
    : "";
  return (
    <div
      className={cn("rounded-md", entry.state === "conflict" ? "bg-error-surface/50" : "hover:bg-muted/60")}
      style={{ marginLeft: depth * 14 }}
      title={source || entry.path}
    >
      <div className="flex min-h-8 items-center gap-2 px-2 text-xs">
        <KindIcon kind={entry.kind} />
        <span className={cn("min-w-0 flex-1 truncate", leaving && "text-muted-foreground line-through")}>
          {entry.name}
        </span>
        {entry.sizeBytes !== null && (
          <span className="hidden shrink-0 tabular-nums text-muted-foreground sm:inline">
            {formatBytes(entry.sizeBytes)}
          </span>
        )}
        <Badge variant={badge.variant} className="shrink-0 px-2 py-0 text-2xs">
          {t(badge.key)}
        </Badge>
      </div>
      {showEditor && item && (
        <div className="grid gap-2 border-t px-2 py-2 text-xs">
          <label className="space-y-1">
            <span className="text-muted-foreground">{t("remoteFetch.conflictAction")}</span>
            <select
              className="h-8 w-full rounded-md border bg-background px-2"
              value={current.resolution}
              disabled={disabled}
              onChange={(event) =>
                onDecisionChange({ ...current, resolution: event.target.value as RemoteFetchResolution })
              }
            >
              <option value="auto">{t("remoteFetch.unresolved")}</option>
              <option value="keep_local">{t("folderEdit.keepExisting")}</option>
              <option value="replace">{t("folderEdit.replaceExisting")}</option>
              <option value="keep_both">{t("remoteFetch.keepBoth")}</option>
              <option value="rename">{t("remoteFetch.renameIncoming")}</option>
            </select>
          </label>
          {current.resolution === "rename" && (
            <label className="space-y-1">
              <span className="text-muted-foreground">{t("folderEdit.targetPath")}</span>
              <input
                className="h-8 w-full rounded-md border bg-background px-2"
                value={current.targetPath}
                disabled={disabled}
                onChange={(event) => onDecisionChange({ ...current, targetPath: event.target.value })}
              />
            </label>
          )}
          {item.targetConflictReason && <div className="text-destructive">{item.targetConflictReason}</div>}
        </div>
      )}
    </div>
  );
}

function FolderEditFooter({
  workspace,
  onBack,
  onReviewConflicts,
  onApply,
}: {
  workspace: FolderEditWorkspace;
  onBack?: () => void;
  onReviewConflicts: () => void;
  onApply: () => void;
}) {
  const { t } = useTranslation();
  const { readiness, plan, request } = workspace;
  const add = plan?.summary.add ?? 0;
  const remove = request.removeLocationIds.length;
  return (
    <div className="flex shrink-0 flex-wrap items-center gap-x-3 gap-y-2 border-t bg-muted/35 px-3 py-2.5 sm:px-5">
      <div
        aria-live="polite"
        className="flex min-h-8 min-w-0 basis-full items-center gap-2 text-sm sm:basis-0 sm:flex-1"
      >
        {workspace.planFailed ? (
          <>
            <AlertTriangle className="h-4 w-4 shrink-0 text-error" />
            <span className="truncate text-error-foreground">{t("folderEdit.planFailed")}</span>
            <Button variant="ghost" size="sm" className="shrink-0 px-2" onClick={workspace.retryPlan}>
              {t("folderEdit.retry")}
            </Button>
          </>
        ) : readiness === "refreshing" || workspace.applying ? (
          <>
            <Loader2 className="h-4 w-4 shrink-0 animate-spin text-muted-foreground" />
            <span className="truncate text-muted-foreground">
              {workspace.applying ? t("folderEdit.applying") : t("remoteFetch.refreshingPreview")}
            </span>
          </>
        ) : readiness === "blocked" ? (
          <>
            <AlertTriangle className="h-4 w-4 shrink-0 text-error" />
            <span className="truncate text-error-foreground">
              {t("folderEdit.statusBlocked", { count: plan?.summary.conflict ?? 0 })}
            </span>
            <Button variant="ghost" size="sm" className="shrink-0 px-2 lg:hidden" onClick={onReviewConflicts}>
              {t("remoteFetch.review")}
            </Button>
          </>
        ) : readiness === "ready" ? (
          <>
            <CheckCircle2 className="h-4 w-4 shrink-0 text-success" />
            <span className="truncate">{t("folderEdit.statusReady", { add, remove })}</span>
          </>
        ) : (
          <span className="truncate text-muted-foreground">{t("folderEdit.statusIdle")}</span>
        )}
      </div>
      <div className="ml-auto flex gap-2">
        {onBack && (
          <Button variant="outline" onClick={onBack} disabled={workspace.applying}>
            <ArrowLeft className="h-4 w-4" />
            {t("folderEdit.back")}
          </Button>
        )}
        <Button onClick={onApply} disabled={readiness !== "ready" || workspace.applying || workspace.planFailed}>
          <CheckCircle2 className="h-4 w-4" />
          {t("folderEdit.apply")}
        </Button>
      </div>
    </div>
  );
}
