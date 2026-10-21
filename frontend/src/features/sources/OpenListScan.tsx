import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useAuth } from "@/auth/AuthProvider";
import { Button } from "@/components/ui/button";
import { api } from "@/lib/api";
import { FullSourceIndex } from "./FullSourceIndex";
import { SourceCatalogExclusions } from "./SourceCatalogExclusions";

export function OpenListScan({ sourceId, onDone }: { sourceId: number; onDone: () => void }) {
  const { t } = useTranslation();
  const auth = useAuth();
  const abort = useRef<AbortController | null>(null);
  const [status, setStatus] = useState("");
  useEffect(() => () => abort.current?.abort(), []);
  if (!auth.hasPermission("sources:write") || auth.demoMode) return null;
  return (
    <div className="flex flex-wrap items-center gap-2">
      <Button
        variant="outline"
        disabled={status === "running"}
        onClick={() => {
          abort.current = new AbortController();
          setStatus("running");
          void api
            .scanSourceCatalog(sourceId, abort.current.signal)
            .then((result) => {
              setStatus(result.status);
              onDone();
            })
            .catch(() => setStatus("failed"));
        }}
      >
        {t("openlist.scan")}
      </Button>
      {status === "running" && (
        <Button variant="outline" onClick={() => abort.current?.abort()}>
          {t("common.cancel")}
        </Button>
      )}
      {status && (
        <p role="status" aria-label={t("openlist.scanStatus")}>
          {t(`openlist.${status}`)}
        </p>
      )}
      <FullSourceIndex key={sourceId} sourceId={sourceId} onDone={onDone} />
      <SourceCatalogExclusions key={"exclusions-" + sourceId} sourceId={sourceId} />
    </div>
  );
}
