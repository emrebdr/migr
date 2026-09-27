import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import {
  copyFileSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, extname, join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

// The runtime conformance fixtures, typechecked against the published contract.
//
// esbuild transpiles a migration and does not typecheck it, and these files sit outside this package
// so tsc --noEmit never reaches them -- where Go's expression of the same fixtures gets the compiler
// for free from `go build`. Without this, a fixture could be wrong in a way that only showed up as a
// failure under the conformance tag, on a machine that has Node.
//
// It compiles in a directory inside node_modules so that `@migr/sdk` resolves to this package by
// self-reference, through the same exports map a consumer resolves -- template.test.ts's trick, for
// the same reason: the check runs against what is published rather than against the sources.

const here = dirname(fileURLToPath(import.meta.url));
const root = join(here, "..");

/** FIXTURES is where the neutral fixture set keeps this language's expression of every migration. */
const FIXTURES = join(
  root,
  "..",
  "..",
  "test",
  "conformance",
  "runtime",
  "testdata",
  "sources",
  "typescript",
);

/** ERASABLE is the setting these files are exempt from, and the enum in one of them is why.
 *
 * The package itself must stay erasable so `node --test` can run it with no transpiler. A migration
 * need not: the runtime compiles it with esbuild, and one fixture carries an enum precisely so a run
 * with the transpiler switched off cannot pass.
 */
const ERASABLE = { erasableSyntaxOnly: false };

/** sources lists every fixture this language expresses, refusing a tree that has moved. */
function sources(from: string = FIXTURES): string[] {
  const found = readdirSync(from);

  assert.ok(found.length > 0, `${from} holds no fixtures, so this check would prove nothing`);

  for (const name of found) {
    assert.equal(extname(name), ".ts", `${name} is not a TypeScript source`);
  }

  return found;
}

/** typechecks one directory, failing with what tsc said rather than with an exit status. */
function typecheck(directory: string): void {
  try {
    execFileSync(
      process.execPath,
      [join(root, "node_modules", "typescript", "bin", "tsc"), "--project", directory],
      { stdio: "pipe" },
    );
  } catch (thrown) {
    assert.fail(`the conformance fixtures do not typecheck:\n${said(thrown)}`);
  }
}

/** said renders what a failed command printed, which is where tsc puts its diagnostics. */
function said(thrown: unknown): string {
  const failure = thrown as { stdout?: Buffer; stderr?: Buffer };

  return `${failure.stdout?.toString() ?? ""}${failure.stderr?.toString() ?? ""}`;
}

test("every conformance fixture typechecks against the published contract", () => {
  const written = mkdtempSync(join(root, "node_modules", ".migr-fixtures-"));

  try {
    for (const name of sources()) {
      copyFileSync(join(FIXTURES, name), join(written, name));
    }

    writeFileSync(
      join(written, "tsconfig.json"),
      JSON.stringify({
        extends: "../../tsconfig.json",
        compilerOptions: ERASABLE,
        include: ["*.ts"],
      }),
    );

    typecheck(written);
  } finally {
    rmSync(written, { recursive: true, force: true });
  }
});

test("the fixtures import this package by the name it is published under", () => {
  for (const name of sources()) {
    const source = readFileSync(join(FIXTURES, name), "utf8");

    assert.match(source, /from "@migr\/sdk"/, `${name} does not import @migr/sdk`);
  }
});

test("a fixture tree that has moved or gained a stray file fails rather than checking nothing", () => {
  const empty = mkdtempSync(join(tmpdir(), "migr-fixtures-"));

  try {
    assert.throws(() => sources(empty), /holds no fixtures/);

    writeFileSync(join(empty, "README.md"), "these are the fixtures\n");

    assert.throws(() => sources(empty), /is not a TypeScript source/);
  } finally {
    rmSync(empty, { recursive: true, force: true });
  }
});

test("some fixture carries syntax Node's own type stripping refuses", () => {
  const carrying = sources().filter((name) =>
    /^enum \w+ \{$/m.test(readFileSync(join(FIXTURES, name), "utf8")),
  );

  assert.ok(
    carrying.length > 0,
    "no fixture declares an enum, so the whole set is erasable syntax and the conformance run " +
      "would pass identically with the transpiler switched off. Tidying the last enum away " +
      "disarms that guard silently, which is what this test is here to stop.",
  );
});
