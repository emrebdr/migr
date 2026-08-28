import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

import { check } from "./check.ts";
import { ACTIONS, FUNCTIONS } from "./column.ts";
import type { Kind } from "./kind.ts";
import { isKind, kinds } from "./kind.ts";
import type { Member, Shape } from "./operation.ts";
import { decode, encode, SHAPES, STRUCTURES } from "./operation.ts";
import { TYPE_KINDS } from "./type.ts";

// The third copy of the IR, held to the document rather than to either of the other two.
//
// Decoding is the half that matters. A test that only constructed operations could not miss a
// member this copy forgot to declare, because it would never have written one -- so every checked
// vector is read into this copy's shapes, which refuse a member they do not define, and then
// re-encoded byte for byte. Member order, omitted optionals, escaping, and type spelling all have
// to match exactly, because docs/ir.md §4.2 makes those bytes a compatibility surface.

const here = dirname(fileURLToPath(import.meta.url));
const specPath = join(here, "..", "..", "..", "docs", "ir.md");

/** Example is one checked JSON block, with the line it starts on for failure messages. */
interface Example {
  readonly line: number;
  readonly body: string;
}

/** Field is one row of an operation's field table. */
interface Field {
  readonly name: string;
  readonly required: boolean;
  readonly line: number;
}

/** Section is one operation's entry in the specification. */
interface Section {
  readonly kind: string;
  readonly line: number;
  readonly examples: Example[];
  readonly fields: Field[];
}

/** lines reads the specification, refusing to skip when it is missing. */
function lines(): string[] {
  return readFileSync(specPath, "utf8").replaceAll("\r\n", "\n").split("\n");
}

/** sections walks the document, collecting each operation heading and what is under it. */
function sections(): Section[] {
  const found: Section[] = [];
  let current: Section | undefined;
  let fence: string | undefined;
  let block: string[] = [];
  let opened = 0;

  lines().forEach((line, at) => {
    const number = at + 1;
    const tag = fenceTag(line);

    if (tag !== undefined) {
      if (fence === undefined) {
        fence = tag;
        opened = number;
        block = [];
      } else {
        if (current !== undefined && fence === "json") {
          current.examples.push({ line: opened, body: block.join("\n") });
        }

        fence = undefined;
      }

      return;
    }

    if (fence !== undefined) {
      block.push(line);

      return;
    }

    const heading = operationHeading(line);

    if (heading !== undefined) {
      current = { kind: heading, line: number, examples: [], fields: [] };
      found.push(current);

      return;
    }

    if (line.startsWith("## ")) {
      current = undefined;

      return;
    }

    const field = fieldRow(line, number);

    if (field !== undefined && current !== undefined) {
      current.fields.push(field);
    }
  });

  assert.equal(
    fence,
    undefined,
    `${specPath}: a fenced block opened at line ${opened} never closes`,
  );

  return found;
}

/** fenceTag reports a fenced block's language tag when a line opens or closes a fence. */
function fenceTag(line: string): string | undefined {
  return line.startsWith("```") ? line.slice(3).trim() : undefined;
}

/** operationHeading reports the operation a level-three heading names, when it names one. */
function operationHeading(line: string): string | undefined {
  return line.startsWith("### ") ? backticked(line.slice(4).trim()) : undefined;
}

/** fieldRow reports the member a field table names, when a line names one. */
function fieldRow(line: string, number: number): Field | undefined {
  const cells = cellsOf(line);
  const required = cells[2]?.replaceAll("*", "").trim();

  if (required !== "yes" && required !== "no") {
    return undefined;
  }

  const name = backticked(cells[0] ?? "");

  return name === undefined ? undefined : { name, required: required === "yes", line: number };
}

/** cellsOf splits one markdown table row into its cells. */
function cellsOf(line: string): string[] {
  const trimmed = line.trim();

  if (!trimmed.startsWith("|")) {
    return [];
  }

  return trimmed
    .replace(/^\|/, "")
    .replace(/\|$/, "")
    .split("|")
    .map((cell) => cell.trim());
}

/** backticked unwraps the first value written between backticks, with or without a link label. */
function backticked(text: string): string | undefined {
  const written = /^\[?`([^`]+)`/.exec(text);

  return written?.[1];
}

/** allBackticked unwraps every value written between backticks on one line. */
function allBackticked(line: string): string[] {
  return [...line.matchAll(/`([^`]+)`/g)].map((written) => written[1] as string);
}

/** runAfter returns the first unbroken run of non-empty lines following a marker line. */
function runAfter(marker: string): string[] {
  const all = lines();
  let at = all.findIndex((line) => line.trim().startsWith(marker));

  assert.ok(at >= 0, `${specPath} no longer carries ${JSON.stringify(marker)}`);

  for (at += 1; at < all.length && all[at]?.trim() === ""; at += 1);

  const start = at;

  while (at < all.length && all[at]?.trim() !== "") {
    at += 1;
  }

  return all.slice(start, at);
}

/** compact strips the whitespace an example is pretty-printed with, leaving its bytes otherwise. */
function compact(body: string): string {
  let inside = false;
  let escaped = false;
  let built = "";

  for (const character of body) {
    if (inside) {
      built += character;
      escaped = !escaped && character === "\\";
      inside = escaped || character !== '"';

      continue;
    }

    if (character === '"') {
      inside = true;
      built += character;

      continue;
    }

    if (!" \t\n\r".includes(character)) {
      built += character;
    }
  }

  return built;
}

test("the specification documents every operation this SDK emits, and no others", () => {
  const documented = new Map<string, number>();

  for (const section of sections()) {
    const first = documented.get(section.kind);

    assert.equal(
      first,
      undefined,
      `${specPath}:${section.line}: ${section.kind} is documented twice, also at line ${first}`,
    );

    documented.set(section.kind, section.line);

    assert.ok(
      isKind(section.kind),
      `${specPath}:${section.line}: a section names ${section.kind}, which this SDK cannot emit`,
    );
  }

  for (const kind of kinds()) {
    assert.ok(documented.has(kind), `${kind} has no section in ${specPath}`);
  }
});

test("every documented example decodes into this copy and re-encodes byte for byte", () => {
  let checked = 0;

  for (const section of sections()) {
    assert.ok(section.examples.length > 0, `${specPath}:${section.line}: no checked example`);

    for (const example of section.examples) {
      const where = `${specPath}:${example.line}`;
      const op = decode(JSON.parse(example.body));

      assert.equal(
        op.op,
        section.kind,
        `${where}: an example under ${section.kind} decoded as ${op.op}`,
      );

      check(op);

      assert.equal(
        encode(op),
        compact(example.body),
        `${where}: the example is not the document this SDK emits`,
      );

      checked += 1;
    }
  }

  assert.ok(checked >= kinds().length, `only ${checked} vectors were checked`);
});

test("an example carrying a member this copy does not declare is refused rather than skipped", () => {
  assert.throws(
    () =>
      decode({ op: "add_index", table: "users", name: "i", columns: ["email"], unique: true }),
    /"unique"/,
    "decoding accepted a member the operation does not define",
  );
});

test("the field tables are the members this copy declares, required column included", () => {
  for (const section of sections()) {
    assert.ok(isKind(section.kind));
    assert.ok(
      section.fields.length > 0,
      `${specPath}:${section.line}: ${section.kind} tabulates no fields`,
    );

    assertShapeMatches(section.kind, SHAPES[section.kind as Kind], section.fields);
  }
});

test("the shared Column structure is the one docs/ir.md §6.1 tabulates", () => {
  const documented = runAfter("### 6.1 Column")
    .map((line, at) => fieldRow(line, at))
    .filter((field): field is Field => field !== undefined);

  assert.ok(documented.length > 0, `${specPath} §6.1 tabulates no fields`);

  assertShapeMatches("Column", STRUCTURES.COLUMN, documented);
});

/** assertShapeMatches reports whether a shape declares exactly what one table tabulates. */
function assertShapeMatches(subject: string, shape: Shape, documented: readonly Field[]): void {
  const declared = new Map<string, Member>(shape.map((member) => [member.name, member]));

  for (const field of documented) {
    const member = declared.get(field.name);

    assert.ok(
      member,
      `${specPath}:${field.line}: ${subject} documents ${field.name}, undeclared here`,
    );
    assert.equal(
      member.required,
      field.required,
      `${specPath}:${field.line}: ${subject}.${field.name} is documented required=${field.required}`,
    );

    declared.delete(field.name);
  }

  assert.deepEqual(
    [...declared.keys()],
    [],
    `this SDK declares members on ${subject} that the document tabulates nowhere`,
  );
}

test("the logical type vocabulary is the one the document defines", () => {
  const standalone = runAfter("Fourteen stand alone:").flatMap(allBackticked);
  const parameterized = runAfter("Four take parameters:")
    .map((line) => backticked(cellsOf(line)[0] ?? ""))
    .filter((form): form is string => form !== undefined)
    .map((form) => form.split("(")[0] as string);

  assert.deepEqual(
    [...standalone, ...parameterized].sort(),
    [...TYPE_KINDS].sort(),
    "the logical types documented and the ones this SDK spells are not one set",
  );
});

test("the two closed sets are the ones the document closes them to", () => {
  assert.deepEqual(
    runAfter("### 6.3 Default functions").flatMap(allBackticked).sort(),
    [...FUNCTIONS].sort(),
  );

  assert.deepEqual(
    runAfter("### 6.4 Referential actions").flatMap(allBackticked).sort(),
    [...ACTIONS].sort(),
  );
});
