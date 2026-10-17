/** The name this SDK reports itself as in the handshake, which the engine logs. */
export const SDK_NAME = "@migr/sdk";

/**
 * This package's own version, which the handshake reports as `sdk.version`.
 *
 * It is written here as well as in package.json because a bundle cannot read its own manifest
 * without shipping one; version.test.ts holds the two level rather than a build step doing it.
 */
export const VERSION = "1.0.0-alpha.0";

/** The protocol version this SDK speaks, which docs/protocol.md specifies. */
export const PROTOCOL_VERSION = 1;

/**
 * Every protocol version this SDK can be driven with, ascending.
 *
 * `initialize` offers a set and the runtime picks one, so an SDK that grew support for a second
 * version would list both here and keep working with an engine that speaks only the first.
 */
export const PROTOCOL_VERSIONS: readonly number[] = [PROTOCOL_VERSION];

/** The newest Operation IR version this SDK emits; an engine stating an older one still runs it. */
export const IR_VERSION = 3;

/** What the engine calls this runtime, spelled as migr.yaml spells `migrations.language`. */
export const LANGUAGE = "typescript";

/** Reports the Node runtime this migration is being executed by, for the handshake. */
export function languageRuntime(): string {
  return process.versions.node;
}
