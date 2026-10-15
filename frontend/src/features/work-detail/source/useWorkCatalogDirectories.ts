import { useEffect, useState } from "react";
import { api, type CatalogDirectory } from "@/lib/api";

export function useWorkCatalogDirectories(workId: number | null) {
  const [directories, setDirectories] = useState<CatalogDirectory[]>([]);
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    setDirectories([]);
    setError("");
    if (!workId) return;
    const controller = new AbortController();
    void api
      .getWorkCatalogDirectories(workId, controller.signal)
      .then((result) => {
        if (!controller.signal.aborted) setDirectories(result.directories);
      })
      .catch((error) => {
        if (!controller.signal.aborted) setError(error.message);
      });
    return () => controller.abort();
  }, [workId, attempt]);
  return { directories, error, retry: () => setAttempt((value) => value + 1) };
}
