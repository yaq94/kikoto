import { useTranslation } from "react-i18next";
import { openWorkDetail } from "@/app/workDetailNavigation";
import { Button } from "@/components/ui/button";
import type { CatalogEntry } from "@/lib/sourceCatalogApi";

export function CatalogAvailabilityNotice({
  entry,
}: {
  entry: Pick<CatalogEntry, "catalogEntryId" | "title" | "status" | "relocationCandidates">;
}) {
  const { t } = useTranslation();
  const candidates = entry.relocationCandidates ?? [];
  if ((!entry.status || entry.status === "available") && candidates.length === 0) return null;
  return (
    <div className="space-y-2 text-sm text-muted-foreground">
      <p role="status" aria-label={t("sourceLifecycle.label")}>
        {t(`sourceLifecycle.${entry.status ?? "available"}`)} {t("sourceLifecycle.saved")}
      </p>
      {candidates.length > 0 && (
        <div>
          <p>{t("sourceLifecycle.candidates")}</p>
          {candidates.map((candidate) => (
            <Button
              key={candidate.catalogEntryId}
              variant="outline"
              onClick={() =>
                openWorkDetail(
                  { kind: "catalog", catalogEntryId: candidate.catalogEntryId },
                  { returnTo: `/catalog/${entry.catalogEntryId}`, returnLabel: entry.title },
                )
              }
            >
              {candidate.title}
            </Button>
          ))}
        </div>
      )}
    </div>
  );
}
