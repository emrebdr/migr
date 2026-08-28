// The window a migration acts through, and the four chains it describes a change with.
//
// This file declares what one migration.execute carries: which migration is running, which way,
// whether it is a run or a question, and the signal that ends it. The six host callbacks are
// M7-T4, so a migration can describe a change and cannot yet ask the database anything.
//
// The AbortSignal rides here rather than in a second parameter, which is where the Go SDK puts its
// context. That is CLAUDE.md §8's rule -- idiomatic syntax per language, identical semantics -- and
// the facts beside it are on the Schema in Go too (ADR 0042).
//
// The window takes an emit sink rather than a host, so the whole builder is exercised with a
// recording sink and no process. M7-T4 supplies the sink that reaches host.schema.apply.
//
// It also seals two holes where Go has one. A chain built and never applied emits nothing, and a
// migration that reports success having changed nothing is the failure docs/ir.md §3.4 names -- so
// the window remembers every chain it handed out. TypeScript adds the second: an apply that was
// sent and never awaited would carry its failure into a promise nobody reads, so the window
// remembers those too and surfaces the first of them when the call ends (ADR 0043).

import type { Builder, NewTable, Sink, Statements, TableChain, ViewChain } from "./builder.ts";
import { newBuilder, newTableChain, statementsChain, tableChain, viewChain } from "./builder.ts";
import { checkAll } from "./check.ts";
import { OperationError } from "./error.ts";
import type { CreateTable, Operation } from "./operation.ts";

/** Direction is which way a migration is being run. */
export type Direction = "up" | "down";

/** Mode is whether this call runs the migration or asks it what it would do, per docs/protocol.md §6. */
export type Mode = "apply" | "plan";

/** Schema is the window a migration acts through, and every callback it may make. */
export interface Schema {
  /** The version this call is running, which is what every message names it by. */
  readonly migration: string;
  /** Which way this call is running the migration. */
  readonly direction: Direction;
  /** Whether this call runs the migration or asks it, read from this call and no other. */
  readonly mode: Mode;
  /** Whether this call is a question, which no invocation turns into a run. */
  readonly planning: boolean;
  /** Aborted when the engine abandons this request, which every callback refuses after. */
  readonly signal: AbortSignal;
  /** Opens a change to an existing table, which its own apply emits. */
  table(name: string): TableChain;
  /** Opens a table being created, which states its key inline (docs/ir.md §7.2). */
  createTable(name: string): NewTable;
  /** Opens a change to a view, which its own apply emits. */
  view(name: string): ViewChain;
  /** Opens raw statements addressed to one engine by name, which no other engine will run. */
  raw(driver: string, sql: string): Statements;
  /** Emits operations, which the engine records in both modes and compiles for its own driver. */
  apply(...ops: Operation[]): Promise<void>;
}

/** Call is what one migration.execute says about itself, which the window reports unchanged. */
export interface Call {
  /** The version being run. */
  readonly migration: string;
  /** Which way it is being run. */
  readonly direction: Direction;
  /** Whether this call runs the migration or asks it, read from this call and no other. */
  readonly mode: Mode;
  /** Aborted when the engine abandons this request. */
  readonly signal: AbortSignal;
}

/** Window is a Schema the session can close, which a migration is never handed. */
export interface Window extends Schema {
  /** Ends the call and reports what it emitted, or what it built and never applied. */
  seal(): Promise<number>;
}

/** newSchema opens the window one migration.execute acts through. */
export function newSchema(call: Call, emit: Sink): Window {
  const built: Builder[] = [];
  const watched: Promise<void>[] = [];
  const failures: unknown[] = [];

  let emitted = 0;
  let closed = false;

  const deliver = async (ops: Operation[]): Promise<void> => {
    if (closed) {
      throw new OperationError("this call has ended, and its callbacks are no longer valid");
    }

    if (ops.length === 0) {
      return;
    }

    checkAll(ops);
    await emit(ops);

    emitted += ops.length;
  };

  const apply = (...ops: Operation[]): Promise<void> => {
    const sent = deliver(ops);

    watched.push(
      sent.then(
        () => undefined,
        (failure: unknown) => {
          failures.push(failure);
        },
      ),
    );

    return sent;
  };

  const opened = (subject: string, name: string): Builder => {
    const build = newBuilder((ops) => apply(...ops), subject, name);
    built.push(build);

    return build;
  };

  return {
    migration: call.migration,
    direction: call.direction,
    mode: call.mode,
    planning: call.mode === "plan",
    signal: call.signal,
    apply,
    table: (name: string) => tableChain(opened(`table ${JSON.stringify(name)}`, name)),
    createTable: (name: string) => {
      const op: CreateTable = { op: "create_table", table: name, columns: [] };
      const build = opened(`table ${JSON.stringify(name)}`, name);

      build.add(op);

      return newTableChain(build, op);
    },
    view: (name: string) => viewChain(opened(`view ${JSON.stringify(name)}`, name)),
    raw: (driver: string, sql: string) =>
      statementsChain(opened("raw statements", "")).raw(driver, sql),
    seal: async (): Promise<number> => {
      closed = true;

      await Promise.all(watched);

      const failure = failures[0];

      if (failure !== undefined) {
        throw failure;
      }

      unapplied(built);

      return emitted;
    },
  };
}

/** unapplied reports every change built and never applied, which is a migration that emits nothing. */
function unapplied(built: readonly Builder[]): void {
  const forgotten = built
    .filter((build) => build.held() > 0)
    .map((build) => `${build.held()} to ${build.subject}`);

  if (forgotten.length > 0) {
    throw new OperationError(`changes were built and never applied: ${forgotten.join(", ")}`);
  }
}
