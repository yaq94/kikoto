import { useEffect, useId, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useAuth } from "@/auth/AuthProvider";
import { Button } from "@/components/ui/button";
import { Input, NativeSelect, Textarea } from "@/components/ui/input";
import { api, type AssociationSubject, type SharedAssociation } from "@/lib/api";
import { openWorkDetail } from "@/app/workDetailNavigation";
import { WORK_CODE_PATH_PATTERN } from "@/lib/workCode";

export function SharedAssociationPanel({
  subject,
  onCompleted,
}: {
  subject: AssociationSubject;
  onCompleted?: () => void;
}) {
  const auth = useAuth();
  const { t } = useTranslation();
  const id = useId();
  const [target, setTarget] = useState("");
  const [items, setItems] = useState<Array<Pick<SharedAssociation, "id" | "status">>>([]);
  const [operation, setOperation] = useState<SharedAssociation | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(false);
  const [rating, setRating] = useState<number | null>(null);
  const [note, setNote] = useState("");
  const request = useRef<AbortController | null>(null);
  useEffect(() => {
    const controller = new AbortController();
    request.current = controller;
    setBusy(false);
    setOperation(null);
    setItems([]);
    setError(false);
    void api
      .listSharedAssociations({ kind: subject.kind, id: subject.id }, controller.signal)
      .then((value) => {
        if (!controller.signal.aborted) setItems(value);
      })
      .catch(() => {
        if (!controller.signal.aborted) setError(true);
      });
    return () => controller.abort();
  }, [subject.kind, subject.id, auth.user?.id]);
  const show = (value: SharedAssociation) => {
    setOperation(value);
    const review = value.myDecision ?? {
      rating: value.myLeftReview.rating ?? value.myRightReview.rating,
      note: value.myLeftReview.note || value.myRightReview.note,
    };
    setRating(review.rating);
    setNote(review.note);
    setItems((current) => [{ id: value.id, status: value.status }, ...current.filter((item) => item.id !== value.id)]);
  };
  const run = async (action: () => Promise<SharedAssociation>) => {
    if (busy) return;
    setBusy(true);
    setError(false);
    const controller = request.current;
    try {
      const value = await action();
      if (!controller?.signal.aborted) {
        show(value);
        if (value.status === "completed") onCompleted?.();
      }
    } catch {
      if (!controller?.signal.aborted) setError(true);
    } finally {
      if (!controller?.signal.aborted) setBusy(false);
    }
  };
  const preview = async () => {
    const url = new URL(target, window.location.origin);
    const match = /^\/(catalog|catalog-series|work|works)\/([^/?]+)\/?$/.exec(url.pathname);
    const code = WORK_CODE_PATH_PATTERN.exec(url.pathname);
    if (!match && !code) throw new Error("invalid object link");
    const kind = match?.[1] === "catalog-series" ? "series" : match?.[1] === "catalog" ? "catalog" : "work";
    const reference = match?.[2] ?? code![1];
    let targetID = Number(reference);
    if (kind === "work" && !Number.isSafeInteger(targetID)) targetID = (await api.resolveWorkCode(reference)).workId;
    if (!Number.isSafeInteger(targetID) || targetID <= 0) throw new Error("invalid object link");
    return api.previewSharedAssociation(subject, { kind, id: targetID });
  };
  if (auth.demoMode) return null;
  return (
    <section aria-label={t("sharedAssociation.title")} className="space-y-3 rounded-xl border bg-card p-4">
      <h2 className="font-semibold">{t("sharedAssociation.title")}</h2>
      <p className="text-sm text-muted-foreground">{t("sharedAssociation.hint")}</p>
      {auth.user && (
        <div className="space-y-2">
          <label htmlFor={`${id}-target`}>{t("sharedAssociation.target")}</label>
          <Input id={`${id}-target`} value={target} onChange={(event) => setTarget(event.target.value)} />
          <Button variant="outline" disabled={busy || !target.trim()} onClick={() => void run(preview)}>
            {t("sharedAssociation.preview")}
          </Button>
        </div>
      )}
      {items.length > 0 && (
        <ul aria-label={t("sharedAssociation.operations")} className="space-y-2">
          {items.map((item) => (
            <li key={item.id}>
              <Button
                variant="outline"
                disabled={busy}
                onClick={() => void run(() => api.getSharedAssociation(item.id))}
              >
                {t("sharedAssociation.open", { id: item.id })} · {t(`sharedAssociation.status.${item.status}`)}
              </Button>
            </li>
          ))}
        </ul>
      )}
      {operation && (
        <div className="space-y-3">
          <p role="status">{t(`sharedAssociation.status.${operation.status}`)}</p>
          <div className="grid gap-3 sm:grid-cols-2">
            {(["left", "right"] as const).map((side) => {
              const object = operation[side];
              const review = side === "left" ? operation.myLeftReview : operation.myRightReview;
              return (
                <section key={side} aria-label={t(`sharedAssociation.${side}`)} className="space-y-1">
                  <h3 className="font-medium">
                    {t(`sharedAssociation.${side}`)}: {object.title}
                  </h3>
                  <p>{t(`sharedAssociation.level.${object.level}`)}</p>
                  <ul>
                    {object.sources.map((source, index) => (
                      <li key={`${source.catalogEntryId}:${index}`}>
                        <Button
                          variant="outline"
                          onClick={() => {
                            const options = {
                              returnTo: window.location.pathname + window.location.search,
                              returnLabel: t("sharedAssociation.title"),
                            };
                            if (source.catalogEntryId > 0)
                              openWorkDetail({ kind: "catalog", catalogEntryId: source.catalogEntryId }, options);
                            else if (source.workId)
                              openWorkDetail({ kind: "known", canonicalCode: String(source.workId) }, options);
                          }}
                        >
                          {source.sourceName} · {source.title}
                        </Button>
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
          {operation.status === "preview" && auth.hasPermission("library:write") && (
            <Button
              disabled={busy}
              onClick={() => void run(() => api.startSharedAssociation(operation.id, operation.version))}
            >
              {t("sharedAssociation.start")}
            </Button>
          )}
          {operation.status === "preview" && !auth.hasPermission("library:write") && (
            <p>{t("sharedAssociation.editorRequired")}</p>
          )}
          {operation.status === "pending" && operation.myConflictFields.length > 0 && (
            <div className="space-y-2">
              <p>{t("sharedAssociation.ownDecision")}</p>
              <label htmlFor={`${id}-rating`}>{t("sharedAssociation.finalRating")}</label>
              <NativeSelect
                id={`${id}-rating`}
                value={rating ?? ""}
                onChange={(event) => setRating(event.target.value === "" ? null : Number(event.target.value))}
                disabled={busy}
              >
                <option value="">{t("personal.review.unrated")}</option>
                {[0, 1, 2, 3, 4, 5].map((value) => (
                  <option key={value} value={value}>
                    {value}
                  </option>
                ))}
              </NativeSelect>
              <label htmlFor={`${id}-note`}>{t("sharedAssociation.finalNote")}</label>
              <Textarea
                id={`${id}-note`}
                value={note}
                onChange={(event) => setNote(event.target.value)}
                disabled={busy}
              />
              <Button
                disabled={busy}
                onClick={() =>
                  void run(() => api.decideSharedAssociation(operation.id, operation.version, { rating, note }))
                }
              >
                {t("sharedAssociation.decide")}
              </Button>
              {operation.myDecision && <p>{t("sharedAssociation.choiceSaved")}</p>}
            </div>
          )}
          <Button
            variant="outline"
            disabled={busy}
            onClick={() => void run(() => api.getSharedAssociation(operation.id))}
          >
            {t("sharedAssociation.refresh")}
          </Button>
          {operation.status === "stale" && auth.user && (
            <Button
              disabled={busy}
              onClick={() =>
                void run(() => api.previewSharedAssociation(operation.left.subject, operation.right.subject))
              }
            >
              {t("sharedAssociation.newPreview")}
            </Button>
          )}
        </div>
      )}
      {error && <p role="alert">{t("sharedAssociation.error")}</p>}
    </section>
  );
}
