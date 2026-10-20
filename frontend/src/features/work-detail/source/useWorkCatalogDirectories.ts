import { useEffect, useRef, useState } from "react";
import { api, type CatalogDirectory } from "@/lib/api";
import { catalogDirectoryNeedsCheck } from "./sourceContextModel";

export function useWorkCatalogDirectories(workId: number | null) {
  const [directories, setDirectories] = useState<CatalogDirectory[]>([]);
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  const followUps = useRef(0);
  useEffect(() => {
    followUps.current = 0;
  }, [workId]);
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
  // The server checks due tabs after responding. A bounded follow-up makes the
  // new status visible without turning a slow source into a polling loop.
  const needsCheck = directories.some((entry) => catalogDirectoryNeedsCheck(entry));
  useEffect(() => {
    if (!workId || !needsCheck || followUps.current >= 2) return;
    const timer = window.setTimeout(() => {
      followUps.current += 1;
      setAttempt((value) => value + 1);
    }, 3000);
    return () => window.clearTimeout(timer);
  }, [workId, needsCheck, directories]);
  return { directories, error, retry: () => setAttempt((value) => value + 1) };
}
