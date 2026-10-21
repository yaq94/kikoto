import { Plus, Trash2 } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

import { useAuth } from "@/auth/AuthProvider";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

import {
  catalogExclusionApi,
  catalogExclusionPathProblem,
  normalizeCatalogExclusionPath,
  type CatalogExclusion,
} from "./catalogExclusions";

/**
 * Directory rules that keep a subtree out of the catalog index. Removing a rule
 * only makes the path discoverable again; the next scan decides what is there.
 */
export function SourceCatalogExclusions({ sourceId }: { sourceId: number }) {
  const { t } = useTranslation();
  const auth = useAuth();
  const canWrite = auth.hasPermission("sources:write") && !auth.demoMode;
  const [items, setItems] = useState<CatalogExclusion[] | null>(null);
  const [path, setPath] = useState("");
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [deleting, setDeleting] = useState<number | null>(null);
  const [error, setError] = useState(false);
  const [refresh, setRefresh] = useState(0);

  useEffect(() => {
    if (!canWrite) return;
    const abort = new AbortController();
    setError(false);
    catalogExclusionApi
      .list(sourceId, abort.signal)
      .then((result) => {
        if (!abort.signal.aborted) setItems(result);
      })
      .catch(() => {
        if (!abort.signal.aborted) setError(true);
      });
    return () => abort.abort();
  }, [sourceId, canWrite, refresh]);

  if (!canWrite) return null;

  const add = async () => {
    if (catalogExclusionPathProblem(path)) {
      setError(true);
      return;
    }
    setBusy(true);
    setError(false);
    try {
      await catalogExclusionApi.create(sourceId, {
        path: normalizeCatalogExclusionPath(path),
        note: note.trim(),
      });
      setPath("");
      setNote("");
      setRefresh((value) => value + 1);
    } catch {
      setError(true);
    } finally {
      setBusy(false);
    }
  };

  const remove = async (exclusionId: number) => {
    setDeleting(exclusionId);
    setError(false);
    try {
      await catalogExclusionApi.remove(sourceId, exclusionId);
      setRefresh((value) => value + 1);
    } catch {
      setError(true);
    } finally {
      setDeleting(null);
    }
  };

  return (
    <section aria-label={t("sourceIndex.exclusions.title")} className="w-full space-y-2 border-t border-border pt-3">
      <p className="text-sm font-medium">{t("sourceIndex.exclusions.title")}</p>
      <p className="text-xs text-muted-foreground">{t("sourceIndex.exclusions.hint")}</p>
      <div className="flex flex-wrap items-end gap-2">
        <label className="grid min-w-52 flex-1 gap-1 text-sm">
          <span>{t("sourceIndex.exclusions.path")}</span>
          <Input
            value={path}
            placeholder={t("sourceIndex.exclusions.pathPlaceholder")}
            spellCheck={false}
            onChange={(event) => setPath(event.target.value)}
          />
        </label>
        <label className="grid min-w-40 flex-1 gap-1 text-sm">
          <span>{t("sourceIndex.exclusions.note")}</span>
          <Input value={note} onChange={(event) => setNote(event.target.value)} />
        </label>
        <Button size="sm" disabled={busy} onClick={() => void add()}>
          <Plus className="h-4 w-4" />
          {t(busy ? "sourceIndex.exclusions.adding" : "sourceIndex.exclusions.add")}
        </Button>
      </div>
      {error && (
        <p role="alert" className="text-sm text-destructive">
          {t("sourceIndex.exclusions.failed")}
        </p>
      )}
      {items && items.length === 0 && (
        <p className="text-sm text-muted-foreground">{t("sourceIndex.exclusions.empty")}</p>
      )}
      {items && items.length > 0 && (
        <ul className="divide-y divide-border rounded-[var(--control-radius)] border border-border text-sm">
          {items.map((item) => (
            <li key={item.id} className="flex min-w-0 items-center gap-2 px-3 py-2">
              <span className="min-w-0 flex-1">
                <span className="block truncate font-medium">{item.path}</span>
                {item.note && <span className="block truncate text-xs text-muted-foreground">{item.note}</span>}
              </span>
              <Button
                variant="ghost"
                size="icon"
                className="h-8 w-8 text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
                aria-label={t("sourceIndex.exclusions.remove")}
                title={t("sourceIndex.exclusions.remove")}
                disabled={deleting !== null}
                onClick={() => void remove(item.id)}
              >
                <Trash2 className="h-4 w-4" />
              </Button>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
