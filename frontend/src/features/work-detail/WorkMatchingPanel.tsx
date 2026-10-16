import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useAuth } from "@/auth/AuthProvider";
import { Button } from "@/components/ui/button";
import { api, type AssociationSubject, type WorkMatchResult } from "@/lib/api";

export function WorkMatchingPanel({
  subject,
  onCandidate,
  onOperation,
}: {
  subject: AssociationSubject;
  onCandidate: (candidate: AssociationSubject) => void;
  onOperation: (id: number) => void;
}) {
  const { t } = useTranslation();
  const auth = useAuth();
  const [result, setResult] = useState<WorkMatchResult | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(false);
  const request = useRef<AbortController | null>(null);
  useEffect(() => {
    const controller = new AbortController();
    request.current = controller;
    setResult(null);
    setBusy(false);
    setError(false);
    void api
      .getWorkMatching({ kind: subject.kind, id: subject.id }, controller.signal)
      .then((value) => {
        if (!controller.signal.aborted) setResult(value);
      })
      .catch(() => {
        if (!controller.signal.aborted) setError(true);
      });
    return () => controller.abort();
  }, [subject.kind, subject.id, auth.user?.id]);
  const check = async () => {
    if (busy) return;
    setBusy(true);
    setError(false);
    const controller = request.current;
    try {
      const value = auth.hasPermission("library:write")
        ? await api.runWorkMatching(subject, controller?.signal)
        : await api.getWorkMatching(subject, controller?.signal);
      if (!controller?.signal.aborted) setResult(value);
    } catch {
      if (!controller?.signal.aborted) setError(true);
    } finally {
      if (!controller?.signal.aborted) setBusy(false);
    }
  };
  const hasMore = result !== null && result.candidateCount > result.candidates.length;
  return (
    <section aria-label={t("workMatching.title")} className="space-y-2">
      <h3 className="font-medium">{t("workMatching.title")}</h3>
      <p className="text-sm text-muted-foreground">{t("workMatching.hint")}</p>
      <Button variant="outline" disabled={busy} onClick={() => void check()}>
        {t(auth.hasPermission("library:write") ? "workMatching.check" : "workMatching.refresh")}
      </Button>
      {result && (
        <>
          <p>
            {t(`workMatching.status.${result.status}`)} ·{" "}
            {t(`workMatching.rule.${result.rule}`, { defaultValue: t("workMatching.rule.insufficient_evidence") })}
          </p>
          {result.operationId && (
            <Button variant="outline" onClick={() => onOperation(result.operationId!)}>
              {t("sharedAssociation.open", { id: result.operationId })}
            </Button>
          )}
          <ul aria-label={t("workMatching.candidates")} className="space-y-2">
            {result.candidates.map((candidate) => (
              <li key={`${candidate.subject.kind}:${candidate.subject.id}`}>
                <p>
                  {candidate.title} ·{" "}
                  {t(`workMatching.rule.${candidate.rule}`, {
                    defaultValue: t("workMatching.rule.insufficient_evidence"),
                  })}
                </p>
                {auth.user && (
                  <Button variant="outline" onClick={() => onCandidate(candidate.subject)}>
                    {t("workMatching.review", { title: candidate.title })}
                  </Button>
                )}
              </li>
            ))}
          </ul>
          {hasMore && <p>{t("workMatching.more")}</p>}
        </>
      )}
      {error && <p>{t("workMatching.error")}</p>}
    </section>
  );
}
