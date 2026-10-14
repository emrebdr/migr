import assert from "node:assert/strict";
import { test } from "node:test";

import { replaceColumn, ReplacementError, type Replacement } from "./expand.ts";
import { undone, type Migration } from "./migration.ts";
import type { Host } from "./schema.ts";
import { newSchema } from "./schema.ts";
import { t } from "./type.ts";

// The same assertions as sdk/go/expand_test.go, over the same bytes: a phase emits what the Go
// SDK's emits, each unsafe step warns and no safe one does, and a replacement nothing can carry out
// is refused by every phase. The runtime conformance suite holds the two SDKs to each other.

/** Ran is what one phase emitted in one direction, and what it warned. */
interface Ran {
  /** The documents the host was sent, rendered as the bytes the operations array carries. */
  documents: string[];
  /** The messages logged at warn, in order. */
  warnings: string[];
}

/** run runs one direction of a phase against a host that keeps what it was sent. */
async function run(phase: Migration, direction: "up" | "down"): Promise<Ran> {
  const ran: Ran = { documents: [], warnings: [] };
  const host: Host = {
    call: async <R>(_method: string, params: unknown): Promise<R> => {
      const { operations } = params as { operations: unknown[] };

      ran.documents.push(...operations.map((document) => JSON.stringify(document)));

      return { recorded: operations.length } as R;
    },
    notify: (_method, params) => {
      const logged = params as { level: string; message: string };

      if (logged.level === "warn") {
        ran.warnings.push(logged.message);
      }
    },
  };
  const s = newSchema(
    {
      migration: "20260727T153000",
      direction,
      mode: "plan",
      signal: new AbortController().signal,
    },
    host,
  );

  if (direction === "up") {
    await phase.up(s);
  } else {
    assert.ok(phase.down !== undefined, "the phase authors no down");
    await phase.down(s);
  }

  await s.seal();

  return ran;
}

/** email is the replacement the tests share: a NOT NULL string widened, keeping its name. */
function email(): Replacement {
  return replaceColumn("users", "email", t.string(255)).with(t.string(320));
}

test("the keep-name form emits the four phases in both directions", async () => {
  const r = email();
  const cases: [Migration, "up" | "down", string[]][] = [
    [
      r.expand(),
      "up",
      [
        `{"op":"add_column","table":"users","column":{"name":"email_next","type":"string(320)","nullable":true}}`,
      ],
    ],
    [
      r.expand(),
      "down",
      [
        `{"op":"drop_column","table":"users","column":"email_next","from":{"name":"email_next","type":"string(320)","nullable":true}}`,
      ],
    ],
    [
      r.backfill(),
      "up",
      [
        `{"op":"data_transform","table":"users","column":"email_next","type":"string(320)","from":"email","function":"copy"}`,
      ],
    ],
    [r.backfill(), "down", []],
    [
      r.swap(),
      "up",
      [
        `{"op":"alter_column","table":"users","column":"email","nullable":true,"from":{"nullable":false}}`,
        `{"op":"alter_column","table":"users","column":"email_next","nullable":false,"from":{"nullable":true}}`,
        `{"op":"rename_column","table":"users","from":"email","to":"email_old"}`,
        `{"op":"rename_column","table":"users","from":"email_next","to":"email"}`,
      ],
    ],
    [
      r.swap(),
      "down",
      [
        `{"op":"rename_column","table":"users","from":"email","to":"email_next"}`,
        `{"op":"rename_column","table":"users","from":"email_old","to":"email"}`,
        `{"op":"alter_column","table":"users","column":"email_next","nullable":true,"from":{"nullable":false}}`,
        `{"op":"alter_column","table":"users","column":"email","nullable":false,"from":{"nullable":true}}`,
      ],
    ],
    [
      r.contract(),
      "up",
      [
        `{"op":"drop_column","table":"users","column":"email_old","from":{"name":"email_old","type":"string(255)","nullable":true}}`,
      ],
    ],
    [
      r.contract(),
      "down",
      [
        `{"op":"add_column","table":"users","column":{"name":"email_old","type":"string(255)","nullable":true}}`,
        `{"op":"data_transform","table":"users","column":"email_old","type":"string(255)","from":"email","function":"copy"}`,
      ],
    ],
  ];

  for (const [phase, direction, want] of cases) {
    assert.deepEqual((await run(phase, direction)).documents, want);
  }
});

test("the rename form swaps no names and contract drops the original", async () => {
  const r = replaceColumn("users", "email", t.string(255)).renameTo("email_address");

  assert.match((await run(r.expand(), "up")).documents.join(), /"name":"email_address"/);

  const swap = (await run(r.swap(), "up")).documents;
  assert.equal(swap.length, 2);
  assert.doesNotMatch(swap.join(), /rename_column/);

  assert.match((await run(r.contract(), "up")).documents.join(), /"column":"email","from"/);
  assert.match(
    (await run(r.contract(), "down")).documents.join(),
    /"column":"email","type":"string\(255\)","from":"email_address"/,
  );
});

test("a contract after a lossy backfill is irreversible and says why", () => {
  const lossy = undone(email().through("lower").contract());

  assert.equal(lossy.undo, "declared");
  assert.match(lossy.reason ?? "", /through lower/);
  assert.equal(undone(email().contract()).undo, "authored");
});

test("only the unsafe steps warn, and each names what it breaks", async () => {
  const r = email();
  const cases: [Migration, "up" | "down", string | undefined][] = [
    [r.expand(), "up", undefined],
    [r.expand(), "down", `unsafe step: expand down: drops "users"."email_next"`],
    [r.backfill(), "up", undefined],
    [r.backfill(), "down", undefined],
    [r.swap(), "up", `unsafe step: swap: "users"."email" now names the new column`],
    [
      r.swap(),
      "down",
      `unsafe step: swap down: "users"."email" is the column an app reads again`,
    ],
    [r.contract(), "up", `unsafe step: contract: drops "users"."email_old" and the values in it`],
    [r.contract(), "down", undefined],
  ];

  for (const [phase, direction, want] of cases) {
    const { warnings } = await run(phase, direction);

    if (want === undefined) {
      assert.deepEqual(warnings, [], `${direction} warned and it is safe`);
    } else {
      assert.equal(warnings.length, 1);
      assert.ok(warnings[0]?.startsWith(want), `${warnings[0]} does not start ${want}`);
    }
  }
});

test("a replacement that cannot be carried out is refused by every phase", async () => {
  const refused: [Replacement, RegExp][] = [
    [replaceColumn("", "email", t.string(255)).with(t.text()), /table is required/],
    [replaceColumn("users", "", t.string(255)).with(t.text()), /column is required/],
    [replaceColumn("users", "email", t.string(255)), /nothing to replace/],
    [replaceColumn("users", "email", t.string(255)).with(t.string(255)), /nothing to replace/],
    [email().via("email"), /is the column being replaced/],
    [email().retire("email"), /is the column being replaced/],
    [email().via("spare").retire("spare"), /cannot name both/],
    [
      replaceColumn("users", "email", t.string(255)).renameTo("email"),
      /names the column it renames/,
    ],
    [email().renameTo("address").via("spare"), /via and retire name nothing/],
    [email().through("reverse" as never), /unknown function/],
  ];

  for (const [r, why] of refused) {
    assert.throws(
      () => r.validate(),
      (err: unknown) => err instanceof ReplacementError && why.test(err.message),
    );

    for (const phase of [r.expand(), r.backfill(), r.swap(), r.contract()]) {
      await assert.rejects(run(phase, "up"), ReplacementError);
      assert.throws(() => phase.validate?.(), ReplacementError);
    }
  }
});

test("a replacement is a value, so deriving one leaves the original alone", async () => {
  const base = email();
  base.renameTo("email_address").through("lower").null();

  assert.match((await run(base.expand(), "up")).documents.join(), /"name":"email_next"/);
});

test("a nullable column is not relaxed and a nullable result is not tightened", async () => {
  const r = replaceColumn("users", "email", t.string(255)).wasNull().with(t.string(320)).null();
  const swap = (await run(r.swap(), "up")).documents;

  assert.equal(swap.length, 2);
  assert.doesNotMatch(swap.join(), /alter_column/);
});
