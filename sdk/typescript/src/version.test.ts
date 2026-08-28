import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

import {
  IR_VERSION,
  LANGUAGE,
  PROTOCOL_VERSION,
  PROTOCOL_VERSIONS,
  SDK_NAME,
  VERSION,
  languageRuntime,
} from "./version.ts";

const here = dirname(fileURLToPath(import.meta.url));

/** Reads this package's manifest, which is the version npm publishes under. */
function manifest(): { name: string; version: string } {
  return JSON.parse(readFileSync(join(here, "..", "package.json"), "utf8"));
}

/** Reads the protocol version the specification itself states in its title. */
function specifiedProtocolVersion(): number {
  const spec = readFileSync(join(here, "..", "..", "..", "docs", "protocol.md"), "utf8");
  const stated = /^#\s+migr Runtime Protocol.*`protocolVersion`\s+(\d+)/m.exec(spec);

  assert.ok(stated, "docs/protocol.md does not state a protocolVersion in its title");

  return Number(stated[1]);
}

/** Returns the major of a semantic version, ignoring any prerelease or build suffix. */
function major(version: string): number {
  const parsed = /^(\d+)\./.exec(version);

  assert.ok(parsed, `${version} does not begin with a major version`);

  return Number(parsed[1]);
}

test("the version this SDK reports is the one npm publishes", () => {
  assert.equal(VERSION, manifest().version);
  assert.equal(SDK_NAME, manifest().name);
});

test("the package's major version is the protocol version it speaks", () => {
  assert.equal(
    major(VERSION),
    PROTOCOL_VERSION,
    "@migr/sdk encodes the protocol version in its major: 1.x.y speaks protocol 1 (ADR 0041)",
  );
});

test("the protocol version is the one the specification states", () => {
  assert.equal(
    PROTOCOL_VERSION,
    specifiedProtocolVersion(),
    "docs/protocol.md is normative, and this SDK is held to it rather than to the Go engine",
  );
});

test("the versions offered at initialize include the one this SDK speaks", () => {
  assert.ok(PROTOCOL_VERSIONS.includes(PROTOCOL_VERSION));
  assert.deepEqual(
    [...PROTOCOL_VERSIONS].sort((a, b) => a - b),
    [...PROTOCOL_VERSIONS],
  );
});

test("the IR version is stated rather than negotiated", () => {
  assert.equal(typeof IR_VERSION, "number");
  assert.ok(IR_VERSION >= 1);
});

test("the language is spelled as migr.yaml spells migrations.language", () => {
  assert.equal(LANGUAGE, "typescript");
});

test("the language runtime reported is the Node executing this migration", () => {
  assert.equal(languageRuntime(), process.versions.node);
});
