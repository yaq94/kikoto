import { LoaderCircle, Save } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

import { SettingsRow, SettingsSection } from "@/components/settings/SettingsSection";
import { Button } from "@/components/ui/button";
import { toastFromError, useToast } from "@/components/ui/toast";
import { api } from "@/lib/api";
import { deviceTimeZone, setListeningTimeZone } from "@/lib/timeZone";

import { TimeZoneSelect } from "./TimeZoneSelect";

/** The id a Settings link scrolls to. */
export const ACCOUNT_TIME_ZONE_SECTION_ID = "settings-section-time-zone";

/**
 * The account's time zone: automatic, which follows the device in use, or one
 * IANA zone for every device. The zone decides where a day starts in the
 * account's listening report, and a saved change reaches this tab's player at
 * once.
 */
export function AccountTimeZoneSection({ readOnly }: { readOnly: boolean }) {
  const { t } = useTranslation();
  const toast = useToast();
  // The saved zone; an empty string is automatic, and null is not loaded yet.
  const [saved, setSaved] = useState<string | null>(null);
  const [draft, setDraft] = useState("");
  const [failed, setFailed] = useState(false);
  const [saving, setSaving] = useState(false);
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    const controller = new AbortController();
    setFailed(false);
    api
      .getUserPreferences(controller.signal)
      .then((preferences) => {
        const next = preferences.timeZone ?? "";
        setSaved(next);
        setDraft(next);
      })
      .catch(() => {
        if (!controller.signal.aborted) setFailed(true);
      });
    return () => controller.abort();
  }, [attempt]);

  const save = async () => {
    if (readOnly || saving || saved === null) return;
    setSaving(true);
    try {
      const preferences = await api.updateUserPreferences({ timeZone: draft || null });
      const next = preferences.timeZone ?? "";
      setSaved(next);
      setDraft(next);
      setListeningTimeZone(preferences.timeZone ?? null);
      toast.success(t("timeZone.saved"));
    } catch (error) {
      toast.notify(toastFromError(error, t("timeZone.saveFailed")));
    } finally {
      setSaving(false);
    }
  };

  const device = deviceTimeZone() ?? "UTC";
  return (
    <SettingsSection
      id={ACCOUNT_TIME_ZONE_SECTION_ID}
      className="scroll-mt-[calc(var(--header-height)+var(--safe-area-top)+1rem)]"
      title={t("timeZone.title")}
      footer={
        <Button
          type="button"
          size="sm"
          disabled={readOnly || saving || saved === null || draft === saved}
          onClick={() => void save()}
        >
          {saving ? <LoaderCircle className="h-4 w-4 animate-spin" /> : <Save className="h-4 w-4" />}
          {t("timeZone.save")}
        </Button>
      }
    >
      <SettingsRow htmlFor="account-time-zone" title={t("timeZone.title")} description={t("timeZone.description")}>
        {failed ? (
          <span role="alert" className="flex items-center gap-2 text-sm text-muted-foreground">
            {t("timeZone.loadFailed")}
            <Button type="button" variant="outline" size="sm" onClick={() => setAttempt((value) => value + 1)}>
              {t("common.retry")}
            </Button>
          </span>
        ) : (
          <TimeZoneSelect
            id="account-time-zone"
            value={draft}
            followLabel={t("timeZone.auto", { zone: device })}
            inUse={[saved, device]}
            disabled={readOnly || saving || saved === null}
            ariaLabel={t("timeZone.title")}
            onChange={setDraft}
          />
        )}
      </SettingsRow>
    </SettingsSection>
  );
}
