import {
  mediaDeleteTargetKey,
  mediaDeleteTargetsForFile,
  sortedFilesDeep,
} from "@/features/work-detail/dialogs/mediaDeleteTargets";
import type { TreeNode } from "@/features/work-detail/media/mediaTreeModel";
import type { DuplicateFile, DuplicateFileGroup } from "@/lib/api";
import type { MediaDeleteTarget } from "@/features/work-detail/workflows/useMediaCleanupWorkflow";

export type DirectoryManagerOptions = { allowCacheDelete?: boolean; allowLocalDelete?: boolean };

/** A one-click selection shortcut: every deletable copy that shares a format or location. */
export type DirectoryManagerSelectionGroup = {
  key: string;
  files: number;
  sizeBytes: number;
  targetKeys: string[];
};

export type DirectoryManagerSelectionState = {
  selected: number;
  total: number;
  checked: boolean;
  indeterminate: boolean;
};

export function fileExtension(name: string) {
  const baseName = name.replace(/\\/g, "/").split("/").pop() ?? "";
  const dot = baseName.lastIndexOf(".");
  return dot > 0 && dot < baseName.length - 1 ? baseName.slice(dot + 1).toLowerCase() : "";
}

/** Groups deletable files by extension, largest group first, so formats can be removed together. */
export function directoryManagerFormatGroups(root: TreeNode, options: DirectoryManagerOptions) {
  const groups = new Map<string, DirectoryManagerSelectionGroup>();
  for (const file of sortedFilesDeep(root)) {
    const targets = mediaDeleteTargetsForFile(file, options);
    const extension = fileExtension(file.sourcePath || file.title);
    if (targets.length === 0 || !extension) continue;
    const group = groups.get(extension) ?? { key: extension, files: 0, sizeBytes: 0, targetKeys: [] };
    group.files += 1;
    group.sizeBytes += file.sizeBytes ?? 0;
    group.targetKeys.push(...targets.map(mediaDeleteTargetKey));
    groups.set(extension, group);
  }
  return [...groups.values()].sort((a, b) => b.sizeBytes - a.sizeBytes || a.key.localeCompare(b.key));
}

/** Groups file copies by where they are stored; the work root folder is not a file copy. */
export function directoryManagerLocationGroups(targets: MediaDeleteTarget[]) {
  return (["local", "cache"] as const)
    .map((kind): DirectoryManagerSelectionGroup => {
      const matching = targets.filter((target) => target.kind === kind);
      return {
        key: kind,
        files: matching.length,
        sizeBytes: selectedBytes(matching),
        targetKeys: matching.map(mediaDeleteTargetKey),
      };
    })
    .filter((group) => group.files > 0);
}

/** What makes a listed local file redundant: another file with the same content. */
export type DuplicateFileMark = {
  /** A file with the same content, from outside the listed folder when there is one. */
  counterpart: DuplicateFile;
  /** The counterpart is in another folder or edition than the one listed. */
  elsewhere: boolean;
};

export type DirectoryManagerDuplicates = {
  /** A mark per local location of the listed folder that has an identical file. */
  marks: Map<number, DuplicateFileMark>;
  /** Listed files that also exist, with the same content, outside the listed folder. */
  elsewhere: DirectoryManagerSelectionGroup;
  /** Files repeated inside the listed folder: every copy except the first of each set. */
  repeated: DirectoryManagerSelectionGroup;
};

/**
 * Relates identical-file groups to the listed folder. A set that also has a
 * copy outside the folder makes every listed copy redundant; a set that exists
 * only inside it keeps its first file.
 */
export function directoryManagerDuplicates(
  groups: DuplicateFileGroup[],
  fileTargets: MediaDeleteTarget[],
): DirectoryManagerDuplicates {
  const listed = new Map(
    fileTargets.filter((target) => target.kind === "local").map((target) => [target.locationId, target]),
  );
  const marks = new Map<number, DuplicateFileMark>();
  const elsewhere: DirectoryManagerSelectionGroup = { key: "elsewhere", files: 0, sizeBytes: 0, targetKeys: [] };
  const repeated: DirectoryManagerSelectionGroup = { key: "repeated", files: 0, sizeBytes: 0, targetKeys: [] };
  const add = (group: DirectoryManagerSelectionGroup, target: MediaDeleteTarget, sizeBytes: number) => {
    group.files += 1;
    group.sizeBytes += sizeBytes;
    group.targetKeys.push(mediaDeleteTargetKey(target));
  };
  for (const group of groups) {
    const inside = group.files.filter((file) => listed.has(file.locationId));
    const outside = group.files.filter((file) => !listed.has(file.locationId));
    if (inside.length === 0 || inside.length + outside.length < 2) continue;
    inside.forEach((file, index) => {
      const target = listed.get(file.locationId);
      if (!target) return;
      const counterpart = outside[0] ?? inside.find((other) => other.locationId !== file.locationId);
      if (!counterpart) return;
      marks.set(file.locationId, { counterpart, elsewhere: outside.length > 0 });
      if (outside.length > 0) add(elsewhere, target, group.sizeBytes);
      else if (index > 0) add(repeated, target, group.sizeBytes);
    });
  }
  return { marks, elsewhere, repeated };
}

/** The last two segments of a library path, enough to recognize a file without its full location. */
export function compactLibraryPath(path: string) {
  return path.replace(/\\/g, "/").split("/").filter(Boolean).slice(-2).join("/");
}

export function directoryManagerSelectionState(
  targetKeys: string[],
  selectedKeys: Set<string>,
): DirectoryManagerSelectionState {
  const selected = targetKeys.filter((key) => selectedKeys.has(key)).length;
  return {
    selected,
    total: targetKeys.length,
    checked: targetKeys.length > 0 && selected === targetKeys.length,
    indeterminate: selected > 0 && selected < targetKeys.length,
  };
}

export function withTargetKeys(selectedKeys: Set<string>, targetKeys: string[], included: boolean) {
  const next = new Set(selectedKeys);
  for (const key of targetKeys) {
    if (included) next.add(key);
    else next.delete(key);
  }
  return next;
}

export function selectedBytes(targets: MediaDeleteTarget[]) {
  return targets.reduce((sum, target) => sum + (target.sizeBytes ?? 0), 0);
}
