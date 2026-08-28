import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

// The template `migr create --lang ts` writes is held by the CLI, in Go, and typechecking it is the
// only way to know it is what it claims to be. The Go side asserts what the template says; this
// asserts that what it says compiles, which is the TypeScript half of "what create writes is what
// the rest of migr reads".
//
// It compiles in a directory inside node_modules so that `@migr/sdk` resolves to this package by
// self-reference, through the same exports map a consumer resolves -- which means the check runs
// against what is published rather than against the sources.

const here = dirname(fileURLToPath(import.meta.url));
const root = join(here, "..");
const declaring = join(root, "..", "..", "internal", "source", "typescript", "template.go");

/** Reads the template migr create writes, which the CLI holds as a Go constant. */
function template(): string {
  const source = readFileSync(declaring, "utf8");
  const sdk = /const SDK = "([^"]+)"/.exec(source);
  const open = source.indexOf("`", source.indexOf("const Template ="));
  const close = source.lastIndexOf("`");

  assert.ok(sdk?.[1], `${declaring} declares no SDK constant`);
  assert.ok(open > 0 && close > open, `${declaring} declares no Template constant`);

  return source.slice(open + 1, close).replace("` + SDK + `", sdk[1]);
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
    assert.fail(`the template migr create writes does not typecheck:\n${said(thrown)}`);
  }
}

/** said renders what a failed command printed, which is where tsc puts its diagnostics. */
function said(thrown: unknown): string {
  const failure = thrown as { stdout?: Buffer; stderr?: Buffer };

  return `${failure.stdout?.toString() ?? ""}${failure.stderr?.toString() ?? ""}`;
}

test("the template migr create writes typechecks against the published contract", () => {
  const written = mkdtempSync(join(root, "node_modules", ".migr-template-"));

  try {
    writeFileSync(join(written, "20260727T153000_add_users.ts"), template());
    writeFileSync(
      join(written, "tsconfig.json"),
      JSON.stringify({ extends: "../../tsconfig.json", include: ["*.ts"] }),
    );

    typecheck(written);
  } finally {
    rmSync(written, { recursive: true, force: true });
  }
});

test("the template imports this package by the name it is published under", () => {
  assert.match(template(), /from "@migr\/sdk"/);
});
