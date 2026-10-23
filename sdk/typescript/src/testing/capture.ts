// What one half of a migration did, and what undoes the migration it is half of.
//
// A capture runs the half against a recording host, in this process, reaching nothing. It holds the
// operations as the documents the engine would have received, and `is` renders what you expected
// through the same encoder and compares them -- the method the runtime conformance suite uses, since
// member order is part of the IR and deepEqual does not see it.
//
// What reversal does not do is invert anything. All twenty inverses live in the engine, so two
// SDKs could not reach two answers for one migration; a copy here would be a third, and the day it
// drifted it would pass a down the engine refuses. Whether every operation can be inverted is the
// engine's answer, through project().validate().

import assert from "node:assert/strict";

import type { Kind, Migration, Mode, Operation, Rows, Schema, Undo } from "../index.ts";
import { documentOf, newSchema, undone } from "../index.ts";

import type { Document, Line, Stage, Statement } from "./host.ts";
import { recording } from "./host.ts";

export type { Document, Line, Stage, Statement } from "./host.ts";
export { UnstagedError } from "./host.ts";

/** PLACEHOLDER is the version a capture runs under, a migration handed over as a value having no file. */
export const PLACEHOLDER = "20060102T150405";

/** Capture is what one half of a migration did: the IR it emitted, and every callback it made. */
export interface Capture {
  /** The IR documents the engine would have received, in the order they were sent. */
  readonly operations: readonly Document[];
  /** The statements the migration read with, in order. */
  readonly reads: readonly Statement[];
  /** The statements the migration sent itself, which no rollback undoes. */
  readonly statements: readonly Statement[];
  /** The lines it filed on the engine's logger. */
  readonly logs: readonly Line[];
  /** Holds this capture to having emitted exactly these operations, comparing the documents. */
  is(...want: Operation[]): void;
  /** Names each operation, for an assertion about shape rather than about the whole document. */
  kinds(): Kind[];
}

/** Reversed is how a migration is undone, and what each half it has emitted. */
export interface Reversed {
  /** Which of the three answers docs/protocol.md §7.4 names this migration gives. */
  readonly undo: Undo;
  /** The author's reason a rollback halts with, present only where one is declared. */
  readonly reason?: string;
  /** What the up half emitted, which is what a derived inverse would be derived from. */
  readonly up: Capture;
  /** What an authored down emitted, and empty unless undo is "authored". */
  readonly down: Capture;
}

/** capture reports what a migration's up emits, answering whatever the stage answers. */
export function capture(m: Migration, stage: Stage = {}): Promise<Capture> {
  return half(m, stage, "up", (s) => m.up(s));
}

/** captureDown reports what a migration's own down emits, refusing one that authors none. */
export function captureDown(m: Migration, stage: Stage = {}): Promise<Capture> {
  const { down } = m;

  if (down === undefined) {
    throw new Error(`this migration authors no down of its own: ${undoneBy(m)}`);
  }

  return half(m, stage, "down", (s) => down(s));
}

/** reversal reports how a migration is undone, capturing every half it has. */
export async function reversal(m: Migration, stage: Stage = {}): Promise<Reversed> {
  const { undo, reason } = undone(m);

  const up = await capture(m, stage);
  const down = undo === "authored" ? await captureDown(m, stage) : empty();
  const problem = underivable(undo, up.statements);

  if (problem !== undefined) {
    assert.fail(problem);
  }

  return reason === undefined ? { undo, up, down } : { undo, reason, up, down };
}

/** documents renders operations as the documents the engine receives, through the SDK's own encoder. */
export function documents(...ops: Operation[]): Document[] {
  return ops.map((op) => documentOf(op) as Document);
}

/** answer builds what a staged read returns, every value being the server's own text. */
export function answer(columns: string[], ...rows: (string | null)[][]): Rows {
  return { columns, rows };
}

/** half runs one direction against a recording host, and reports everything that host was asked. */
async function half(
  m: Migration,
  stage: Stage,
  direction: "up" | "down",
  run: (s: Schema) => void | Promise<void>,
): Promise<Capture> {
  const mode: Mode = stage.mode === "plan" ? "plan" : "apply";
  const host = recording(stage, mode);
  const controller = new AbortController();

  const window = newSchema(
    { migration: stage.migration ?? PLACEHOLDER, direction, mode, signal: controller.signal },
    host,
  );

  let raisedBy: unknown;

  try {
    await run(window);
  } catch (thrown) {
    raisedBy = thrown;
  }

  try {
    await window.seal();
  } catch (thrown) {
    raisedBy ??= thrown;
  }

  raisedBy ??= host.refusal();

  if (raisedBy !== undefined) {
    throw raisedBy;
  }

  return captured(host.operations, host.reads, host.statements, host.logs);
}

/** captured builds the value a test asserts against, with the two comparisons it may use. */
function captured(
  operations: Document[],
  reads: Statement[],
  statements: Statement[],
  logs: Line[],
): Capture {
  return {
    operations,
    reads,
    statements,
    logs,
    is: (...want: Operation[]): void => {
      assert.deepStrictEqual(
        operations.map((document) => JSON.stringify(document)),
        documents(...want).map((document) => JSON.stringify(document)),
      );
    },
    kinds: (): Kind[] => operations.map((document) => document["op"] as Kind),
  };
}

/** empty is the half a migration does not have, which is a capture of nothing. */
function empty(): Capture {
  return captured([], [], [], []);
}

/** underivable reports the one rollback an SDK can see is impossible, and nothing otherwise. */
function underivable(undo: Undo, held: readonly Statement[]): string | undefined {
  if (undo !== "derived" || held.length === 0) {
    return undefined;
  }

  return (
    `this migration authors no down, so the engine derives its inverse -- but its up sends ` +
    `${held.length} statement(s) of its own, the first being ${JSON.stringify(held[0]?.sql)}, ` +
    `and a statement has no inverse to derive. An exec is committed on its own, before ` +
    `the transaction the operations run in, and a later failure does not undo it, so the engine ` +
    `refuses to derive a down over one -- write a down, or declare the migration irreversible.`
  );
}

/** undoneBy says what undoes a migration that writes no down, which is what to assert instead. */
function undoneBy(m: Migration): string {
  const { undo, reason } = undone(m);

  if (undo === "declared") {
    return `it declares that it cannot be undone (${reason}), so there is no down to capture`;
  }

  return "the engine derives its inverse from whatever its up emits, which is reversal's to report";
}
