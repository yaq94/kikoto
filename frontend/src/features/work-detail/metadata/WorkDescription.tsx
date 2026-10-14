import { useTranslation } from "react-i18next";
import { TranslatedText } from "@/components/content-translation/ContentTranslation";

// Introductions are stored as plain text. React escapes markup-like characters
// without parsing away authored angle brackets, ampersands or line breaks.
export function WorkDescription({ description }: { description?: string }) {
  const { t } = useTranslation();
  if (!description?.trim()) return null;
  return (
    <section aria-label={t("workTitles.description")} className="rounded-lg border bg-card p-4">
      <h2 className="mb-2 text-sm font-semibold">{t("workTitles.description")}</h2>
      <p className="whitespace-pre-wrap break-words text-sm text-muted-foreground">
        <TranslatedText text={description} field="description" />
      </p>
    </section>
  );
}
