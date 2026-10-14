import { useEffect, useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { useAuth } from "@/auth/AuthProvider";
import { Button } from "@/components/ui/button";
import { NativeSelect, Textarea } from "@/components/ui/input";
import type { PersonalReview, PersonalReviewPatch } from "@/lib/workApi";

export function PersonalReviewEditor({
  value,
  needsRegistration = false,
  onSave,
}: {
  value: PersonalReview;
  needsRegistration?: boolean;
  onSave: (patch: PersonalReviewPatch) => Promise<PersonalReview>;
}) {
  const { t } = useTranslation();
  const auth = useAuth();
  const id = useId();
  const [saved, setSaved] = useState(value);
  const [rating, setRating] = useState(value.personalRating);
  const [note, setNote] = useState(value.personalNote);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(false);
  const [complete, setComplete] = useState(false);
  useEffect(() => {
    setSaved({ personalRating: value.personalRating, personalNote: value.personalNote });
    setRating(value.personalRating);
    setNote(value.personalNote);
    setComplete(false);
  }, [value.personalRating, value.personalNote]);
  const canEdit =
    !auth.demoMode &&
    auth.hasPermission("favorites:write") &&
    (!needsRegistration || auth.hasPermission("remote:track"));
  const changed = rating !== saved.personalRating || note !== saved.personalNote;
  const save = async (patch: PersonalReviewPatch) => {
    if (!canEdit || busy) return;
    setBusy(true);
    setError(false);
    setComplete(false);
    try {
      const result = await onSave(patch);
      setSaved(result);
      // Clearing one field must not discard an unsaved edit to the other.
      if ("rating" in patch) setRating(result.personalRating);
      if ("note" in patch) setNote(result.personalNote);
      setComplete(true);
    } catch {
      setError(true);
    } finally {
      setBusy(false);
    }
  };
  return (
    <section aria-label={t("personal.review.title")} className="space-y-3 rounded-xl border bg-card p-4">
      <h2 id={`${id}-title`} className="font-semibold">
        {t("personal.review.title")}
      </h2>
      <p className="text-sm text-muted-foreground">{t("personal.review.privateHint")}</p>
      {needsRegistration && <p className="text-sm text-muted-foreground">{t("personal.review.registrationHint")}</p>}
      {!canEdit && <p className="text-sm text-muted-foreground">{t("personal.readOnlyAccount")}</p>}
      <div className="flex flex-wrap items-end gap-2">
        <div className="grid gap-1">
          <label htmlFor={`${id}-rating`}>{t("personal.review.rating")}</label>
          <NativeSelect
            id={`${id}-rating`}
            value={rating ?? ""}
            disabled={!canEdit || busy}
            onChange={(event) => {
              setRating(event.target.value === "" ? null : Number(event.target.value));
              setComplete(false);
            }}
          >
            <option value="">{t("personal.review.unrated")}</option>
            {[0, 1, 2, 3, 4, 5].map((score) => (
              <option key={score} value={score}>
                {score}
              </option>
            ))}
          </NativeSelect>
        </div>
        <Button
          variant="outline"
          disabled={!canEdit || busy || saved.personalRating === null}
          onClick={() => void save({ rating: null })}
        >
          {t("personal.review.clearRating")}
        </Button>
      </div>
      <div className="grid gap-1">
        <label htmlFor={`${id}-note`}>{t("personal.review.note")}</label>
        <Textarea
          id={`${id}-note`}
          value={note}
          disabled={!canEdit || busy}
          onChange={(event) => {
            setNote(event.target.value);
            setComplete(false);
          }}
        />
      </div>
      <div className="flex flex-wrap gap-2">
        <Button
          disabled={!canEdit || busy || !changed}
          onClick={() =>
            void save({
              ...(rating !== saved.personalRating ? { rating } : {}),
              ...(note !== saved.personalNote ? { note } : {}),
            })
          }
        >
          {t("personal.review.save")}
        </Button>
        <Button
          variant="outline"
          disabled={!canEdit || busy || saved.personalNote === ""}
          onClick={() => void save({ note: "" })}
        >
          {t("personal.review.clearNote")}
        </Button>
      </div>
      {error && (
        <p role="alert" className="text-sm text-destructive">
          {t("personal.review.failed")}
        </p>
      )}
      {complete && (
        <p role="status" className="text-sm text-muted-foreground">
          {t("personal.review.saved")}
        </p>
      )}
    </section>
  );
}
