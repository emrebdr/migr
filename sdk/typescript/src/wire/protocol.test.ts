import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

import { CODES, slugOf } from "./code.ts";
import { decode, encode, JSONRPC } from "./message.ts";
import { METHODS, PROTOCOL_VERSION } from "./method.ts";
import { DIRECTIONS, LEVELS, MODES } from "./payload.ts";

// The second copy of the wire, held to the document rather than to the first.
//
// docs/protocol.md is normative, and M6-T2 turned its checked examples into byte-exact round-trip
// vectors: every one of them decodes into this copy's envelope and re-encodes to the same bytes.
// That is what catches an implementation written from the other side's code rather than from the
// document -- member order and spelling are specified here rather than incidental.
//
// The count is read from the document rather than written down, so a forty-fourth example is
// covered on the day it is added.

const here = dirname(fileURLToPath(import.meta.url));
const specPath = join(here, "..", "..", "..", "..", "docs", "protocol.md");

/** Checked is one checked JSON block, with the line it starts on for failure messages. */
interface Checked {
  readonly line: number;
  readonly body: string;
}

/** lines reads the specification, refusing to skip when it is missing. */
function lines(): string[] {
  return readFileSync(specPath, "utf8").replaceAll("\r\n", "\n").split("\n");
}

/** checked collects every checked JSON block in the document, in the order it carries them. */
function checked(): Checked[] {
  const found: Checked[] = [];
  let fence: string | undefined;
  let block: string[] = [];
  let opened = 0;

  lines().forEach((line, at) => {
    if (!line.startsWith("```")) {
      if (fence !== undefined) {
        block.push(line);
      }

      return;
    }

    if (fence === undefined) {
      fence = line.slice(3).trim();
      block = [];
      opened = at + 1;

      return;
    }

    if (fence === "json") {
      found.push({ line: opened, body: block.join("\n") });
    }

    fence = undefined;
  });

  assert.equal(
    fence,
    undefined,
    `${specPath}: a fenced block opened at line ${opened} never closes`,
  );

  return found;
}

/** methods collects the level-three headings that name a method and nothing else. */
function methods(): string[] {
  return lines()
    .filter((line) => line.startsWith("### "))
    .map((line) => backticked(line.slice(4).trim()))
    .filter((named): named is string => named !== undefined);
}

/** tabulated collects the number and slug every code table row assigns. */
function tabulated(): Map<number, string> {
  const found = new Map<number, string>();

  for (const line of lines()) {
    const cells = cellsOf(line);
    const number = Number(backticked(cells[0] ?? ""));
    const slug = backticked(cells[1] ?? "");

    if (cells.length >= 3 && Number.isInteger(number) && slug !== undefined) {
      found.set(number, slug);
    }
  }

  return found;
}

/** cellsOf splits one markdown table row into its cells, and reports none for any other line. */
function cellsOf(line: string): string[] {
  const trimmed = line.trim();

  if (!trimmed.startsWith("|") || !trimmed.endsWith("|")) {
    return [];
  }

  return trimmed
    .slice(1, -1)
    .split("|")
    .map((cell) => cell.trim());
}

/** backticked reads the one backticked value a cell or heading holds, and nothing else. */
function backticked(text: string): string | undefined {
  const written = /^`([^`]+)`$/.exec(text);

  return written?.[1];
}

/** valuesFor collects the values a parameter table's row offers for one member. */
function valuesFor(member: string): string[] {
  for (const line of lines()) {
    const cells = cellsOf(line);

    if (backticked(cells[0] ?? "") === member) {
      return quoted(cells[1] ?? "");
    }
  }

  return assert.fail(`${specPath} tabulates no row for ${JSON.stringify(member)}`);
}

/** quoted collects every backticked, double-quoted value one cell offers. */
function quoted(cell: string): string[] {
  return [...cell.matchAll(/`"([^"]+)"`/g)].map((one) => one[1] as string);
}

/** statedAfter collects every backticked name on the line that opens with a marker. */
function statedAfter(marker: string): string[] {
  const line = lines().find((one) => one.startsWith(marker));

  assert.ok(line, `${specPath} no longer carries a line opening ${JSON.stringify(marker)}`);

  const stated = line.split(", and ")[0] as string;

  return [...stated.matchAll(/`([^`]+)`/g)].map((one) => one[1] as string).slice(1);
}

test("every documented method exists here, and every method here is documented", () => {
  const documented = methods();

  assert.ok(documented.length > 0, `${specPath} documents no method at all`);
  assert.deepEqual([...documented].sort(), [...METHODS].sort());
});

test("every tabulated code and slug is the one this SDK knows, in both directions", () => {
  const stated = tabulated();

  assert.ok(stated.size > 0, `${specPath} tabulates no error code at all`);

  for (const [code, slug] of stated) {
    assert.equal(
      slugOf(code),
      slug,
      `${specPath} calls code ${code} ${JSON.stringify(slug)}, and this SDK calls it ` +
        JSON.stringify(slugOf(code)),
    );
  }

  for (const code of CODES) {
    assert.ok(
      stated.has(code),
      `this SDK knows code ${code}, which ${specPath} tabulates no row for`,
    );
  }
});

test("every checked block round-trips through this codec byte for byte", () => {
  const found = checked();

  assert.ok(found.length > 0, `${specPath} carries no checked example at all`);

  for (const vector of found) {
    const where = `${specPath}:${vector.line}`;
    const m = decode(vector.body);

    assert.equal(
      encode(m),
      vector.body,
      `${where}: the example re-encodes differently, and member order is specified here`,
    );
  }
});

test("every checked block is a whole message, and states the one version there is", () => {
  for (const vector of checked()) {
    const parsed: unknown = JSON.parse(vector.body);

    assert.equal(typeof parsed, "object");
    assert.equal(
      (parsed as Record<string, unknown>)["jsonrpc"],
      JSONRPC,
      `${specPath}:${vector.line}: the example carries another jsonrpc`,
    );
  }
});

test("the protocol version this SDK speaks is the one the document describes", () => {
  const want = `### 10.1 What \`protocolVersion\` ${PROTOCOL_VERSION} covers`;

  assert.ok(
    lines().includes(want),
    `${specPath} has no section ${JSON.stringify(want)}, so this SDK speaks a version it does not describe`,
  );
});

test("the three closed sets are the ones the document closes them to", () => {
  assert.deepEqual(valuesFor("direction").sort(), [...DIRECTIONS].sort());
  assert.deepEqual(valuesFor("mode").sort(), [...MODES].sort());
  assert.deepEqual(statedAfter("`level` is ").sort(), [...LEVELS].sort());
});
