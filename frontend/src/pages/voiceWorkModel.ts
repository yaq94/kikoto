import type { CreatorWorkSort } from "@/lib/creatorWorksBrowse";
import type { SortDirection } from "@/lib/libraryApi";
import type {
  CircleSourceStat,
  VoiceKnownWork,
  VoiceRemoteObservation,
  VoiceRemoteSourceSet,
  VoiceRemoteWork,
} from "@/lib/api";

export type VoiceWorkView = (VoiceKnownWork | VoiceRemoteWork) & {
  remoteObservations?: VoiceRemoteObservation[];
};

/**
 * Orders a voice actor's works like a circle catalog: by the chosen fact in
 * either direction, with works that lack it last and the code breaking ties.
 */
export function sortVoiceWorks(
  works: readonly VoiceWorkView[],
  sort: CreatorWorkSort,
  direction: SortDirection,
): VoiceWorkView[] {
  const sign = direction === "asc" ? 1 : -1;
  const byCode = (left: VoiceWorkView, right: VoiceWorkView) =>
    normalizedCode(left.primaryCode).localeCompare(normalizedCode(right.primaryCode));
  // Missing values sort last whichever way the order runs.
  const compareFact = <T>(left: T | null, right: T | null, compare: (a: T, b: T) => number) => {
    if (left === null || right === null) return Number(left === null) - Number(right === null);
    return sign * compare(left, right);
  };
  const number = (value: number | null | undefined) => (typeof value === "number" ? value : null);
  const text = (value: string) => value.trim() || null;
  return [...works].sort((left, right) => {
    const primary =
      sort === "code"
        ? 0
        : sort === "title"
          ? compareFact(text(left.title), text(right.title), (a, b) =>
              a.localeCompare(b, undefined, { sensitivity: "base" }),
            )
          : sort === "rating"
            ? compareFact(number(left.rating), number(right.rating), (a, b) => a - b)
            : sort === "sales"
              ? compareFact(number(left.sales), number(right.sales), (a, b) => a - b)
              : compareFact(text(voiceWorkSortDate(left)), text(voiceWorkSortDate(right)), (a, b) =>
                  a.localeCompare(b),
                );
    return primary || sign * byCode(left, right);
  });
}

export function mergeVoiceWorks(knownWorks: VoiceKnownWork[], sourceSets: VoiceRemoteSourceSet[]): VoiceWorkView[] {
  const remoteWorks = sourceSets
    .filter((source) => source.status === "ok" || source.works.some((work) => work.availability !== undefined))
    .flatMap((source) => source.works);
  const remoteByCanonicalCode = groupRemoteWorks(remoteWorks);
  const consumedCodes = new Set<string>();

  const mergedKnown = knownWorks.map((work): VoiceWorkView => {
    const key = normalizedCode(work.primaryCode);
    const matches = remoteByCanonicalCode.get(key) ?? [];
    const persistedObservations = work.remoteObservations ?? [];
    const preferredCodes = [
      work.remoteCode,
      ...persistedObservations
        .filter((observation) => normalizedAvailability(observation.status) === "available")
        .map((observation) => observation.remoteCode),
      work.primaryCode,
    ];
    const observations = mergeRemoteObservations(
      persistedObservations,
      liveRemoteObservations(matches),
      preferredCodes,
    );
    if (matches.length === 0 && observations.length === 0) return work;
    if (matches.length > 0) consumedCodes.add(key);
    return {
      ...work,
      remoteObservations: observations,
      sourceTags: mergeObservedSourceTags(work.sourceTags, observations),
    };
  });

  const mergedRemote: VoiceWorkView[] = [];
  for (const remoteWork of remoteWorks) {
    const key = normalizedCode(remoteWork.primaryCode);
    if (consumedCodes.has(key)) continue;
    consumedCodes.add(key);
    const matches = remoteByCanonicalCode.get(key) ?? [remoteWork];
    mergedRemote.push({
      ...remoteWork,
      remoteObservations: liveRemoteObservations(matches, [remoteWork.remoteCode, remoteWork.primaryCode]),
    });
  }
  return [...mergedKnown, ...mergedRemote].sort(
    (left, right) =>
      Number(isLocalVoiceWork(right)) - Number(isLocalVoiceWork(left)) ||
      voiceWorkSortDate(right).localeCompare(voiceWorkSortDate(left)) ||
      normalizedCode(left.primaryCode).localeCompare(normalizedCode(right.primaryCode)),
  );
}

export function voiceWorkRemoteTarget(work: VoiceWorkView): { sourceId: number; code: string } | null {
  // A known work carries its own source tags rather than a single sourceId. An
  // unknown observation is still a reference the reader can open and check;
  // only a settled negative answer is withheld.
  const observations = work.remoteObservations ?? [];
  const observation =
    observations.find((item) => item.status === "available") ??
    observations.find((item) => item.status === "unknown");
  if (observation) return { sourceId: observation.sourceId, code: observation.remoteCode };
  if ("sourceId" in work && validSourceID(work.sourceId)) {
    // Unknown is a pending answer, not a refusal: opening the source runs the
    // check that resolves it. A negative answer without other evidence blocks
    // the card instead of promising a directory the source no longer lists.
    if (!work.remotePlayable && work.availability !== "available" && work.availability !== "unknown" && !work.hasRemote)
      return null;
    const code = (work.remoteCode || work.primaryCode).trim();
    return code ? { sourceId: work.sourceId, code } : null;
  }
  if (!("sourceTags" in work)) return null;
  const source = work.sourceTags.find(
    (tag) => validSourceID(tag.sourceId) && tag.key !== "cache" && tag.status === "available",
  );
  const code = (work.remoteCode || work.primaryCode).trim();
  return source?.sourceId && code ? { sourceId: source.sourceId, code } : null;
}

export function voiceWorkObservedSourceTags(work: VoiceWorkView) {
  if ("sourceTags" in work) return work.sourceTags;
  return mergeObservedSourceTags([], work.remoteObservations ?? []);
}

/**
 * Whether the card has a source reference it can open and check. A catalog row
 * whose latest answer is unknown still qualifies even though it is not
 * playable, so the Remote filter lists it while Available does not.
 */
export function voiceWorkHasRemoteReference(work: VoiceWorkView) {
  return voiceWorkRemoteTarget(work) !== null;
}

/**
 * Whether a source says the work is playable now. A stored presence row only
 * proves the source held the work at some point, so it cannot answer this
 * question; it still keeps the card visible through voiceWorkRemoteTarget.
 */
export function voiceWorkHasRemoteAvailability(work: VoiceWorkView) {
  if (work.remoteObservations?.some((observation) => observation.status === "available")) return true;
  if ("sourceTags" in work) {
    return work.sourceTags.some(
      (tag) => tag.key !== "cache" && validSourceID(tag.sourceId) && tag.status === "available",
    );
  }
  return "remotePlayable" in work && work.remotePlayable;
}

function groupRemoteWorks(remoteWorks: VoiceRemoteWork[]) {
  const grouped = new Map<string, VoiceRemoteWork[]>();
  for (const work of remoteWorks) {
    const key = normalizedCode(work.primaryCode);
    if (!key) continue;
    const values = grouped.get(key) ?? [];
    values.push(work);
    grouped.set(key, values);
  }
  return grouped;
}

function liveRemoteObservations(works: VoiceRemoteWork[], preferredCodes: string[] = []) {
  const seen = new Set<string>();
  const observations: VoiceRemoteObservation[] = [];
  for (const work of works) {
    const remoteCode = (work.remoteCode || work.primaryCode).trim();
    if (!validSourceID(work.sourceId) || !remoteCode) continue;
    const key = `${work.sourceId}:${normalizedCode(remoteCode)}`;
    if (seen.has(key)) continue;
    seen.add(key);
    observations.push({
      sourceId: work.sourceId,
      sourceCode: work.sourceCode,
      sourceName: work.sourceName,
      remoteCode,
      status: normalizedAvailability(work.availability || (work.remotePlayable ? "available" : "unknown")),
    });
  }
  const preferred = preferredCodes.map(normalizedCode).filter(Boolean);
  return observations
    .map((observation, index) => ({ observation, index }))
    .sort(
      (left, right) =>
        preferredCodeRank(left.observation.remoteCode, preferred) -
          preferredCodeRank(right.observation.remoteCode, preferred) || left.index - right.index,
    )
    .map(({ observation }) => observation);
}

function isLocalVoiceWork(work: VoiceWorkView) {
  return "local" in work ? work.local : work.hasLocal;
}

function voiceWorkSortDate(work: VoiceWorkView) {
  return ("releaseDate" in work ? work.releaseDate || "" : "").trim();
}

function mergeRemoteObservations(
  left: VoiceRemoteObservation[],
  right: VoiceRemoteObservation[],
  preferredCodes: string[],
) {
  const byKey = new Map<string, VoiceRemoteObservation>();
  for (const observation of [...left, ...right]) {
    const remoteCode = observation.remoteCode.trim();
    if (!validSourceID(observation.sourceId) || !remoteCode) continue;
    const normalized: VoiceRemoteObservation = {
      ...observation,
      remoteCode,
      status: normalizedAvailability(observation.status),
    };
    const key = `${observation.sourceId}:${normalizedCode(remoteCode)}`;
    const existing = byKey.get(key);
    if (!existing || availabilityPriority(normalized.status) >= availabilityPriority(existing.status)) {
      byKey.set(key, normalized);
    }
  }
  const preferred = preferredCodes.map(normalizedCode).filter(Boolean);
  return Array.from(byKey.values())
    .map((observation, index) => ({ observation, index }))
    .sort(
      (leftItem, rightItem) =>
        preferredCodeRank(leftItem.observation.remoteCode, preferred) -
          preferredCodeRank(rightItem.observation.remoteCode, preferred) || leftItem.index - rightItem.index,
    )
    .map(({ observation }) => observation);
}

function preferredCodeRank(code: string, preferredCodes: string[]) {
  const index = preferredCodes.indexOf(normalizedCode(code));
  return index === -1 ? preferredCodes.length : index;
}

function mergeObservedSourceTags(sourceTags: CircleSourceStat[], observations: VoiceRemoteObservation[]) {
  const byKey = new Map<string, CircleSourceStat>();
  for (const tag of sourceTags) byKey.set(sourceTagKey(tag), tag);
  for (const observation of observations) {
    const status = normalizedAvailability(observation.status);
    const tag: CircleSourceStat = {
      key: `source:${observation.sourceId}`,
      sourceId: observation.sourceId,
      displayName: observation.sourceName || observation.sourceCode || "Remote source",
      status,
      count: status === "available" ? 1 : 0,
    };
    const key = sourceTagKey(tag);
    const existing = byKey.get(key);
    if (!existing || availabilityPriority(tag.status) > availabilityPriority(existing.status)) byKey.set(key, tag);
  }
  return Array.from(byKey.values());
}

function sourceTagKey(tag: CircleSourceStat) {
  return tag.sourceId ? `source:${tag.sourceId}` : tag.key;
}

function normalizedCode(value: string) {
  return value.trim().toUpperCase();
}

function validSourceID(value: number | null | undefined): value is number {
  return typeof value === "number" && Number.isInteger(value) && value > 0;
}

function normalizedAvailability(value: string): VoiceRemoteObservation["status"] {
  const normalized = value.trim().toLowerCase();
  switch (normalized) {
    case "available":
    case "disabled":
    case "error":
    case "unavailable":
    case "not_found":
    case "unknown":
      return normalized;
    case "missing":
      return "not_found";
    default:
      return "unknown";
  }
}

function availabilityPriority(status: string) {
  switch (status) {
    case "available":
      return 6;
    case "disabled":
      return 5;
    case "error":
      return 4;
    case "unavailable":
      return 3;
    case "not_found":
      return 2;
    case "unknown":
      return 1;
    default:
      return 0;
  }
}
