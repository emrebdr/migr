import assert from "node:assert/strict";
import { test } from "node:test";

import { MigrationError } from "./error.ts";
import { EXTENSIONS, parseName } from "./name.ts";

/** Reads a filename the grammar is expected to refuse, returning what it refused it with. */
function refusal(file: string): MigrationError {
  try {
    parseName(file);
  } catch (thrown) {
    assert.ok(thrown instanceof MigrationError, `${file} was refused with ${String(thrown)}`);

    return thrown;
  }

  assert.fail(`${file} was accepted as a migration filename`);
}

test("a migration filename is a version, a name, and an extension this SDK loads", () => {
  for (const extension of EXTENSIONS) {
    assert.deepEqual(parseName(`20260727T153000_add_users${extension}`), {
      version: "20260727T153000",
      name: "add_users",
      file: `20260727T153000_add_users${extension}`,
    });
  }
});

test("which runtime executes a file is the extension's question, and this SDK answers for three", () => {
  for (const file of ["20260727T153000_add_users.go", "20260727T153000_add_users.sql"]) {
    assert.equal(refusal(file).code, "malformed_name");
  }

  assert.match(refusal("20260727T153000_add_users").message, /no extension at all/);
});

test("a version is a real UTC instant, so a day the calendar does not have is refused", () => {
  assert.match(refusal("20260230T000000_add_users.ts").message, /not a UTC instant/);
  assert.match(refusal("20260227T250000_add_users.ts").message, /not a UTC instant/);
  assert.match(refusal("20261327T153000_add_users.ts").message, /not a UTC instant/);

  assert.deepEqual(parseName("20240229T235959_add_users.ts").version, "20240229T235959");
});

test("a version that is not the shape at all is refused as the shape", () => {
  assert.match(refusal("2026-07-27_add_users.ts").message, /YYYYMMDDThhmmss/);
  assert.match(refusal("v1_add_users.ts").message, /YYYYMMDDThhmmss/);
  assert.match(refusal("_add_users.ts").message, /no version before its first _/);
});

test("a name is lowercase words joined by single underscores", () => {
  assert.match(refusal("20260727T153000_addUsers.ts").message, /"U"/);
  assert.match(refusal("20260727T153000_add-users.ts").message, /"-"/);
  assert.match(
    refusal("20260727T153000_add__users.ts").message,
    /leading, trailing, or doubled _/,
  );
  assert.match(
    refusal("20260727T153000_add_users_.ts").message,
    /leading, trailing, or doubled _/,
  );
  assert.match(refusal("20260727T153000.ts").message, /no _ separating a version from a name/);
  assert.match(refusal("20260727T153000_.ts").message, /no name after its version/);
});

test("a declaration file is not a migration, which falls out of the name rule", () => {
  assert.match(refusal("20260727T153000_add_users.d.ts").message, /"\."/);
});

test("every refusal names the grammar it held the file to", () => {
  const refused = refusal("20260727T153000_addUsers.ts");

  assert.match(refused.message, /<YYYYMMDDThhmmss>_<snake_case name>\(\.ts\|\.js\|\.mjs\)/);
  assert.equal(refused.file, "20260727T153000_addUsers.ts");
});
