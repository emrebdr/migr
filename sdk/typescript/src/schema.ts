// The window a migration acts through, and the four chains it describes a change with.
//
// This file declares what one migration.execute carries -- which migration is running, which way,
// whether it is a run or a question, and the signal that ends it -- and the six callbacks §5
// defines. It builds and decodes every one of those payloads itself: the session below it only
// frames, numbers, and matches answers, so §5's shapes sit beside the migration that reads them.
//
// The AbortSignal rides here rather than in a second parameter, which is where the Go SDK puts its
// context. That is CLAUDE.md §8's rule -- idiomatic syntax per language, identical semantics -- and
// the facts beside it are on the Schema in Go too (ADR 0042).
//
// The window takes a Host rather than a stream, so the whole builder and every callback is
// exercised against a recording host and no process. The session supplies the real one.
//
// It also seals two holes where Go has one. A chain built and never applied emits nothing, and a
// migration that reports success having changed nothing is the failure docs/ir.md §3.4 names -- so
// the window remembers every chain it handed out. TypeScript adds the second: an apply that was
// sent and never awaited would carry its failure into a promise nobody reads, so the window
// remembers those too and surfaces the first of them when the call ends (ADR 0043).

import type { Builder, NewTable, Statements, TableChain, ViewChain } from "./builder.ts";
import { newBuilder, newTableChain, statementsChain, tableChain, viewChain } from "./builder.ts";
import { checkAll } from "./check.ts";
import { OperationError } from "./error.ts";
import { since } from "./kind.ts";
import type { CreateTable, Operation } from "./operation.ts";
import { documentOf } from "./operation.ts";
import {
  HOST_CAPABILITY_GET,
  HOST_DB_EXEC,
  HOST_DB_QUERY,
  HOST_INFO,
  HOST_LOG,
  HOST_SCHEMA_APPLY,
  IR_VERSION,
} from "./wire/method.ts";
import { ProtocolError, UNSUPPORTED_IR } from "./wire/code.ts";
import type {
  CapabilityGetResult,
  DBExecResult,
  DBQueryResult,
  Direction,
  InfoResult,
  Level,
  Mode,
  SchemaApplyResult,
} from "./wire/payload.ts";

export type { Direction, Level, Mode } from "./wire/payload.ts";

/** Rows is what a read returned: every value the server's own text, and null where it was null. */
export type Rows = DBQueryResult;

/** Outcome is what a migration's own statement changed, and whether it ran at all. */
export type Outcome = DBExecResult;

/** Verdict is what the target engine makes of one operation, on both of §5's axes at once. */
export type Verdict = CapabilityGetResult;

/** Info is the facts about this run a migration may branch on. */
export type Info = InfoResult;

/** Host is what a callback goes through: this side's own numbering, framing, and pending table. */
export interface Host {
  /** Sends one request and waits for its answer, which the signal abandons. */
  call<R>(method: string, params: unknown, signal: AbortSignal): Promise<R>;
  /** Sends one notification, which nothing answers and nothing waits on. */
  notify(method: string, params: unknown): void;
}

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
  /** Runs this migration's own read, which the host holds to being one, and does so in both modes. */
  query(sql: string, ...args: string[]): Promise<Rows>;
  /** Sends one statement the IR cannot express, which plan mode records rather than runs. */
  exec(sql: string, ...args: string[]): Promise<Outcome>;
  /** Asks what the target engine makes of an operation, before emitting it. */
  capability(op: Operation): Promise<Verdict>;
  /** Reports the facts about this run a migration may branch on. */
  info(): Promise<Info>;
  /** Writes one structured line to the engine's own logger, which nothing answers. */
  log(level: Level, message: string, fields?: Record<string, string>): void;
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
  /** The newest irVersion the engine reads, which every apply is held to; absent means this SDK's. */
  readonly irVersion?: number;
}

/** Window is a Schema the session can close, which a migration is never handed. */
export interface Window extends Schema {
  /** Ends the call and reports what it emitted, or what it built and never applied. */
  seal(): Promise<number>;
}

/** carried refuses an operation newer than the engine stated it reads, before anything is sent. */
function carried(ops: readonly Operation[], stated: number): void {
  ops.forEach((op, at) => {
    const needs = operationSince(op);

    if (needs > stated) {
      throw new ProtocolError(
        UNSUPPORTED_IR,
        `operation ${at + 1} is ${describeSince(op)}, which needs irVersion ${needs}, and the ` +
          `engine stated ${stated} -- upgrade migr to run it`,
      );
    }
  });
}

/** NO_TRANSACTION_SINCE is the irVersion that first carries raw's no_transaction. */
const NO_TRANSACTION_SINCE = 3;

/** operationSince returns the oldest irVersion carrying one operation, which a field can raise. */
export function operationSince(op: Operation): number {
  if (op.op === "raw" && op.no_transaction === true) {
    return NO_TRANSACTION_SINCE;
  }

  return since(op.op);
}

/** describeSince names what needs the newer version: the kind, or the kind and the field raising it. */
function describeSince(op: Operation): string {
  return operationSince(op) > since(op.op) ? `${op.op} carrying no_transaction` : op.op;
}

/** newSchema opens the window one migration.execute acts through. */
export function newSchema(call: Call, host: Host): Window {
  const built: Builder[] = [];
  const watched: Promise<void>[] = [];
  const failures: unknown[] = [];

  let emitted = 0;
  let closed = false;

  const open = (method: string): void => {
    if (closed) {
      throw new OperationError(
        `this call has ended, and ${method} is no longer valid (docs/protocol.md §5.1)`,
      );
    }

    call.signal.throwIfAborted();
  };

  const ask = async <R>(method: string, params: unknown): Promise<R> => {
    open(method);

    return host.call<R>(method, params, call.signal);
  };

  const deliver = async (ops: Operation[]): Promise<void> => {
    open(HOST_SCHEMA_APPLY);

    if (ops.length === 0) {
      return;
    }

    carried(ops, call.irVersion ?? IR_VERSION);
    checkAll(ops);

    const recorded = await host.call<SchemaApplyResult>(
      HOST_SCHEMA_APPLY,
      { operations: ops.map(documentOf) },
      call.signal,
    );

    if (recorded.recorded !== ops.length) {
      throw new OperationError(
        `the host recorded ${recorded.recorded} of the ${ops.length} operations sent`,
      );
    }

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
    query: (sql: string, ...args: string[]) => ask<Rows>(HOST_DB_QUERY, { sql, args }),
    exec: (sql: string, ...args: string[]) => ask<Outcome>(HOST_DB_EXEC, { sql, args }),
    capability: (op: Operation) =>
      ask<Verdict>(HOST_CAPABILITY_GET, { operation: documentOf(op) }),
    info: () => ask<Info>(HOST_INFO, {}),
    log: (level: Level, message: string, fields?: Record<string, string>) => {
      host.notify(HOST_LOG, { level, message, fields: fields ?? {} });
    },
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
