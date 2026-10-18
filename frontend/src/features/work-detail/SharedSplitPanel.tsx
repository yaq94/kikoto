import { useEffect, useId, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useAuth } from "@/auth/AuthProvider";
import { Button } from "@/components/ui/button";
import { NativeSelect } from "@/components/ui/input";
import { api, type SharedSplit, type SplitAllocation } from "@/lib/api";

export function SharedSplitPanel({
  associationId,
  alreadySplit,
  onCompleted,
}: {
  associationId: number;
  alreadySplit: boolean;
  onCompleted?: () => void;
}) {
  const auth = useAuth();
  const { t } = useTranslation();
  const id = useId();
  const request = useRef<AbortController | null>(null);
  const [items, setItems] = useState<Array<Pick<SharedSplit, "id" | "status">>>([]);
  const [split, setSplit] = useState<SharedSplit | null>(null);
  const [allocation, setAllocation] = useState<SplitAllocation | "">("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(false);
  useEffect(() => {
    const controller = new AbortController();
    request.current = controller;
    setSplit(null);
    setItems([]);
    setAllocation("");
    setBusy(false);
    setError(false);
    void api
      .listSharedSplits(associationId, controller.signal)
      .then((value) => {
        if (!controller.signal.aborted) setItems(value);
      })
      .catch(() => {
        if (!controller.signal.aborted) setError(true);
      });
    return () => controller.abort();
  }, [associationId, auth.user?.id]);
  const run = async (action: () => Promise<SharedSplit>) => {
    if (busy) return;
    const controller = request.current;
    setBusy(true);
    setError(false);
    try {
      const value = await action();
      if (!controller?.signal.aborted) {
        setSplit(value);
        setAllocation(value.myAllocation);
        setItems((current) => [
          { id: value.id, status: value.status },
          ...current.filter((item) => item.id !== value.id),
        ]);
        if (value.status === "completed") onCompleted?.();
      }
    } catch {
      if (!controller?.signal.aborted) setError(true);
    } finally {
      if (!controller?.signal.aborted) setBusy(false);
    }
  };
  return (
    <section aria-label={t("sharedSplit.title")} className="space-y-3 border-t pt-3">
      <h3 className="font-semibold">{t("sharedSplit.title")}</h3>
      <p className="text-sm text-muted-foreground">{t("sharedSplit.hint")}</p>
      {auth.user && auth.hasPermission("library:write") && !alreadySplit && (
        <Button variant="outline" disabled={busy} onClick={() => void run(() => api.previewSharedSplit(associationId))}>
          {t("sharedSplit.preview")}
        </Button>
      )}
      {items.length > 0 && (
        <ul aria-label={t("sharedSplit.operations")}>
          {items.map((item) => (
            <li key={item.id}>
              <Button variant="outline" disabled={busy} onClick={() => void run(() => api.getSharedSplit(item.id))}>
                {t("sharedSplit.open", { id: item.id })} · {t(`sharedSplit.status.${item.status}`)}
              </Button>
            </li>
          ))}
        </ul>
      )}
      {split && (
        <div className="space-y-3">
          <p role="status">{t(`sharedSplit.status.${split.status}`)}</p>
          <div className="grid gap-3 sm:grid-cols-2">
            {(["left", "right"] as const).map((side) => {
              const object = split[side];
              const review = side === "left" ? split.myBeforeLeft : split.myBeforeRight;
              return (
                <section key={side} aria-label={t(`sharedSplit.before.${side}`)}>
                  <h4 className="font-medium">
                    {t(`sharedSplit.before.${side}`)}: {object.title}
                  </h4>
                  <ul>
                    {object.sources.map((source, index) => (
                      <li key={`${source.catalogEntryId}:${index}`}>
                        {source.sourceName} · {source.title}
                      </li>
                    ))}
                  </ul>
                  {auth.user && (
                    <p>
                      {t("personal.review.rating")}: {review.rating ?? t("personal.review.unrated")}
                      <br />
                      {t("personal.review.note")}: {review.note}
                    </p>
                  )}
                </section>
              );
            })}
          </div>
          {auth.user && (
            <section aria-label={t("sharedSplit.current")}>
              <h4 className="font-medium">{t("sharedSplit.current")}</h4>
              <p>{t(`sharedSplit.change.${split.myRecordChange}`)}</p>
              <p>
                {t("personal.review.rating")}: {split.myCurrentReview.rating ?? t("personal.review.unrated")}
                <br />
                {t("personal.review.note")}: {split.myCurrentReview.note}
              </p>
            </section>
          )}
          {split.status === "preview" && auth.hasPermission("library:write") && (
            <Button disabled={busy} onClick={() => void run(() => api.startSharedSplit(split.id, split.version))}>
              {t("sharedSplit.start")}
            </Button>
          )}
          {split.status === "preview" && !auth.hasPermission("library:write") && (
            <p>{t("sharedAssociation.editorRequired")}</p>
          )}
          {split.status === "pending" && split.myRecordChange !== "unchanged" && auth.user && (
            <div className="space-y-2">
              <label htmlFor={`${id}-allocation`}>{t("sharedSplit.allocation")}</label>
              <NativeSelect
                id={`${id}-allocation`}
                value={allocation}
                disabled={busy}
                onChange={(event) => setAllocation(event.target.value as SplitAllocation | "")}
              >
                <option value="">{t("sharedSplit.choose")}</option>
                <option value="left">{t("sharedSplit.left", { title: split.left.title })}</option>
                <option value="right">{t("sharedSplit.right", { title: split.right.title })}</option>
                <option value="both">{t("sharedSplit.both")}</option>
              </NativeSelect>
              <Button
                disabled={busy || !allocation}
                onClick={() => {
                  if (allocation) void run(() => api.decideSharedSplit(split.id, split.version, allocation));
                }}
              >
                {t("sharedSplit.confirm")}
              </Button>
              {split.myAllocation && <p>{t("sharedSplit.saved")}</p>}
            </div>
          )}
          <Button variant="outline" disabled={busy} onClick={() => void run(() => api.getSharedSplit(split.id))}>
            {t("sharedSplit.refresh")}
          </Button>
          {split.status === "stale" && !alreadySplit && auth.user && (
            <Button disabled={busy} onClick={() => void run(() => api.previewSharedSplit(associationId))}>
              {t("sharedAssociation.newPreview")}
            </Button>
          )}
        </div>
      )}
      {error && <p role="alert">{t("sharedSplit.error")}</p>}
    </section>
  );
}
