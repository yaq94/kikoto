import { useEffect, useState, type ReactNode } from "react";

import { assetURL } from "@/lib/api";

/**
 * Identity panel for a circle or voice actor: the latest cover as a square
 * portrait over a cover-tinted surface, the name and personal state, the
 * action cluster, and an optional footer strip such as catalog statistics.
 */
export function CreatorDetailHeader({
  label,
  name,
  coverUrl,
  eyebrow,
  aliases = [],
  meta,
  actions,
  children,
}: {
  label?: string;
  name: string;
  coverUrl?: string | null;
  eyebrow?: ReactNode;
  aliases?: string[];
  meta?: ReactNode;
  actions?: ReactNode;
  children?: ReactNode;
}) {
  const [imageFailed, setImageFailed] = useState(false);
  useEffect(() => setImageFailed(false), [coverUrl]);
  const cover = coverUrl && !imageFailed ? assetURL(coverUrl) : "";
  const visibleAliases = aliases.filter((alias) => alias && alias !== name).slice(0, 4);
  return (
    <section aria-label={label}>
      <div className="relative isolate overflow-clip rounded-xl border bg-card">
        <div className="flex flex-col gap-4 p-4 lg:flex-row lg:items-end lg:justify-between lg:gap-6 lg:p-5">
          <div className="flex min-w-0 items-start gap-4 lg:items-center lg:gap-5">
            <CreatorPortrait name={name} cover={cover} onError={() => setImageFailed(true)} />
            <div className="min-w-0 flex-1">
              {eyebrow && (
                <div className="flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-1 text-xs text-muted-foreground">
                  {eyebrow}
                </div>
              )}
              <h2 className="mt-1 line-clamp-2 text-2xl font-semibold leading-tight tracking-tight lg:text-3xl">
                {name}
              </h2>
              {visibleAliases.length > 0 && (
                <p className="mt-0.5 truncate text-sm text-muted-foreground" title={visibleAliases.join(", ")}>
                  {visibleAliases.join(" / ")}
                </p>
              )}
              {meta}
            </div>
          </div>
          {actions}
        </div>
        {children && <div className="border-t bg-background/40 px-4 py-3 lg:px-5">{children}</div>}
        {cover && (
          <div className="pointer-events-none absolute inset-0 -z-10" aria-hidden="true">
            <img src={cover} alt="" className="h-full w-full scale-125 object-cover opacity-25 blur-3xl saturate-150" />
            <div className="absolute inset-0 bg-gradient-to-r from-card/40 via-card/75 to-card" />
          </div>
        )}
      </div>
    </section>
  );
}

function CreatorPortrait({ name, cover, onError }: { name: string; cover: string; onError: () => void }) {
  const initial = Array.from(name.trim())[0] ?? "?";
  return (
    <div
      className="relative h-20 w-20 shrink-0 overflow-hidden rounded-xl bg-secondary ring-1 ring-border lg:h-28 lg:w-28"
      aria-hidden="true"
    >
      {cover ? (
        <img src={cover} alt="" className="h-full w-full object-cover" onError={onError} />
      ) : (
        <span className="grid h-full w-full place-items-center text-2xl font-semibold text-secondary-foreground lg:text-4xl">
          {initial}
        </span>
      )}
    </div>
  );
}
