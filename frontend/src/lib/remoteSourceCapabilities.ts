import type { FileSource, LibrarySource } from "@/lib/api";

/** Capability a remote source declares when its API can describe works. */
export const METADATA_CAPABILITY = "metadata";

const METADATA_SOURCE_TYPES = new Set(["kikoeru_compatible", "kikoeru_compatible_number178"]);

/** The public source list reports capability without exposing its endpoint or configuration. */
export function librarySourceProvidesMetadata(source: LibrarySource): boolean {
  return METADATA_SOURCE_TYPES.has(source.sourceType) && source.metadataCapable !== false;
}

/**
 * Recommendation scores need a Kikoeru-style work list with tags, cast and
 * circle. A catalog source such as OpenList has no such list, and its score
 * endpoint answers "source unavailable"; asking anyway turns a missing
 * capability into a fake outage of the source itself.
 */
export function librarySourceSupportsRecommendations(source: LibrarySource): boolean {
  return librarySourceProvidesMetadata(source);
}

/**
 * Whether a source declares the metadata capability. A remote source without
 * a declared list keeps its type's default; an explicit list is authoritative.
 */
export function sourceProvidesMetadata(source: Pick<FileSource, "sourceType" | "config">): boolean {
  if (!METADATA_SOURCE_TYPES.has(source.sourceType)) return false;
  const capabilities = source.config.capabilities;
  return capabilities === undefined || capabilities.includes(METADATA_CAPABILITY);
}

/** Returns config with the metadata capability declared on or off. */
export function withMetadataCapability(config: FileSource["config"], enabled: boolean): FileSource["config"] {
  return { ...config, capabilities: enabled ? [METADATA_CAPABILITY] : [] };
}
