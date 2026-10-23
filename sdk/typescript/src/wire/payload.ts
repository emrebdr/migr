// The parameters and results docs/protocol.md §3, §4, and §5 define.
//
// A payload is open where the envelope is closed: an unknown member inside params or result is
// ignored, which is §10.2's compatibility rule and the reason nothing here refuses a member it does
// not know. What it does refuse is a payload of the wrong shape, or one missing something required,
// which §7.2 makes -32602.
//
// The three closed sets are listed as data as well as declared as types, because protocol.test.ts
// reads the document and compares the two.

import { INVALID_PARAMS, ProtocolError } from "./code.ts";

/** Direction is which way a migration is being run, which §4 closes migration.execute to. */
export type Direction = "up" | "down";

/** DIRECTIONS are both directions every migration has. */
export const DIRECTIONS = ["up", "down"] as const satisfies readonly Direction[];

/** Mode is whether a migration.execute is a run or a preview, which §6 says changes one callback. */
export type Mode = "apply" | "plan";

/** MODES are the two modes §4 closes migration.execute to. */
export const MODES = ["apply", "plan"] as const satisfies readonly Mode[];

/** Level is a log line's severity, which §5 closes to four values. */
export type Level = "debug" | "info" | "warn" | "error";

/** LEVELS are the four levels host.log carries, which the engine maps onto its own logger. */
export const LEVELS = ["debug", "info", "warn", "error"] as const satisfies readonly Level[];

/** Support is what a driver makes of an operation on the first of §5's two axes. */
export type Support = "native" | "emulated" | "unsupported";

/** Lock is what an operation costs a live database, which is the second axis. */
export type Lock =
  "lock_free" | "brief_exclusive" | "long_exclusive" | "table_rewrite" | "unknown";

/** Component names one half of what answered the handshake: the language runtime, or the SDK. */
export interface Component {
  /** What it calls itself. */
  name: string;
  /** Its version, which the engine logs and never branches on. */
  version: string;
}

/** InitializeParams is the handshake: every version the engine speaks, and the project it opened. */
export interface InitializeParams {
  /** Every version of this protocol the engine speaks, ascending. Never empty. */
  protocolVersions: number[];
  /** The Operation IR version the engine will accept. Stated, not negotiated. */
  irVersion: number;
  /** The engine's own name and version, for the runtime's diagnostics. */
  client: Component;
  /** Where the migrations are, and which language they were written in. */
  migrations: { dir: string; language: string };
  /** The target a migration may branch on, which carries no DSN, ever. */
  database: { name: string; driver: string };
}

/** InitializeResult is the version this SDK picked, and what implements it. */
export interface InitializeResult {
  /** The one version this SDK chose from what the engine offered, which binds both sides. */
  protocolVersion: number;
  /** The language runtime and its version. */
  runtime: Component;
  /** The library implementing this protocol. */
  sdk: Component;
}

/** Discovered is one migration this SDK loaded, named by the version its filename carries. */
export interface Discovered {
  /** The UTC timestamp its filename carries, which the engine keys everything by. */
  version: string;
  /** The file it was loaded from. */
  file: string;
  /** The versions this migration declares it must follow, read and never computed. */
  dependencies: string[];
  /** The step of a change across deploys this migration declares itself, absent where it declares none. */
  phase?: WirePhase;
  /** How this migration runs against a live table, absent where it runs as a plain ALTER. */
  online?: string;
}

/** WirePhase is one step of a change across deploys, as migration.discover carries it. */
export interface WirePhase {
  /** The change the step belongs to. */
  change: string;
  /** The step. */
  name: string;
  /** The step it waits on, absent where it waits on none. */
  after?: string;
}

/** DiscoverResult is every migration this SDK loaded, with what each one declares. */
export interface DiscoverResult {
  /** What was loaded, in no meaningful order: the engine sorts and overlays the graph itself. */
  migrations: Discovered[];
}

/** ExecuteParams names one migration, one direction, and whether this call runs it or asks it. */
export interface ExecuteParams {
  /** The migration's version, as migration.discover reported it. */
  id: string;
  /** Which way to run it. */
  direction: Direction;
  /** Whether this call runs the migration or asks it what it would do. */
  mode: Mode;
}

/** ExecuteResult reports how many operations were emitted, which the engine checks its count against. */
export interface ExecuteResult {
  /** How many operations this call emitted through host.schema.apply. */
  operations: number;
}

/** ValidateParams names the migration whose own check is being run. */
export interface ValidateParams {
  /** The migration's version. */
  id: string;
}

/** CancelParams names a request the sender made and is still waiting on. */
export interface CancelParams {
  /** The id of that request, in the sender's own sequence. */
  id: number;
}

/** SchemaApplyParams carries the operations a migration performs, as the IR array alone. */
export interface SchemaApplyParams {
  /** An Operation IR operations array, without the envelope: irVersion was agreed at initialize. */
  operations: unknown[];
}

/** SchemaApplyResult reports how many operations this call added to the migration's list. */
export interface SchemaApplyResult {
  /** What the host recorded, which the caller checks against what it sent. */
  recorded: number;
}

/** DBQueryParams is a migration's own read: its statement and its bound parameters. */
export interface DBQueryParams {
  /** The statement, in the target engine's placeholder style. */
  sql: string;
  /** The bound parameters, positional and every one of them a string. */
  args: string[];
}

/** DBQueryResult is what a read returned, every value text or null for the reason §5 gives. */
export interface DBQueryResult {
  /** The columns, in the order the statement selected them. */
  columns: string[];
  /** One entry per row, each as wide as columns, every value the server's own text or null. */
  rows: (string | null)[][];
}

/** DBExecParams is a migration's own statement, the escape hatch the Operation IR cannot express. */
export interface DBExecParams {
  /** The statement, in the target engine's own dialect. */
  sql: string;
  /** The bound parameters, positional and every one of them a string. */
  args: string[];
}

/** DBExecResult reports what the statement changed, and whether it ran at all. */
export interface DBExecResult {
  /** How many rows it changed, as text for the reason a query's values are text. */
  rowsAffected: string;
  /** False under plan, which is how a migration can tell it is being previewed. */
  executed: boolean;
}

/** CapabilityGetParams asks about a whole operation, since a driver refuses payloads and not kinds. */
export interface CapabilityGetParams {
  /** The operation itself, because MySQL's answer depends on the column's type. */
  operation: unknown;
}

/** CapabilityGetResult is what the target engine makes of one operation, on both axes at once. */
export interface CapabilityGetResult {
  /** Whether the engine supports it directly, achieves it another way, or refuses it. */
  support: Support;
  /** How it is achieved, present only for an emulated operation. */
  strategy?: string;
  /** Why it is refused, present only for an unsupported one. */
  reason?: string;
  /** What it costs a live database. */
  lock: Lock;
}

/** InfoResult reports the facts about this run a migration may branch on. */
export interface InfoResult {
  /** The key migr.yaml holds the target under. */
  database: string;
  /** The engine that will compile the operations. */
  driver: string;
  /** The version string the driver detected at Open, verbatim. */
  server: string;
  /** Whether the engine runs DDL inside a transaction. */
  transactionalDDL: boolean;
  /** The mode of the call this callback arrived inside, not a property of the session. */
  mode: Mode;
}

/** LogParams is one structured log line, addressed to the engine's own logger. */
export interface LogParams {
  /** One of the four levels, to which the engine applies its own -v threshold. */
  level: Level;
  /** The line itself. */
  message: string;
  /** Whatever else there is to say, every value a string. */
  fields: Record<string, string>;
}

/** readInitialize reads the handshake, refusing a payload that could not open a session. */
export function readInitialize(params: unknown): InitializeParams {
  const held = objectOf(params);
  const versions = held["protocolVersions"];

  if (!Array.isArray(versions) || versions.length === 0) {
    throw new ProtocolError(INVALID_PARAMS, "protocolVersions must list at least one version");
  }

  return {
    protocolVersions: versions.map((version) => numberOf(version, "protocolVersions")),
    irVersion: numberOf(held["irVersion"], "irVersion"),
    client: componentOf(held["client"], "client"),
    migrations: {
      dir: stringOf(held["migrations"], "dir", "migrations.dir"),
      language: stringOf(held["migrations"], "language", "migrations.language"),
    },
    database: {
      name: stringOf(held["database"], "name", "database.name"),
      driver: stringOf(held["database"], "driver", "database.driver"),
    },
  };
}

/** readExecute reads one migration.execute, refusing a call that could not name what it asks for. */
export function readExecute(params: unknown): ExecuteParams {
  const held = objectOf(params);
  const direction = held["direction"];
  const mode = held["mode"];

  if (!(DIRECTIONS as readonly unknown[]).includes(direction)) {
    throw new ProtocolError(
      INVALID_PARAMS,
      `direction must be one of ${DIRECTIONS.join(", ")}, and this call carries ` +
        JSON.stringify(direction),
    );
  }

  if (!(MODES as readonly unknown[]).includes(mode)) {
    throw new ProtocolError(
      INVALID_PARAMS,
      `mode must be one of ${MODES.join(", ")}, and this call carries ${JSON.stringify(mode)}`,
    );
  }

  return {
    id: required(held["id"], "id"),
    direction: direction as Direction,
    mode: mode as Mode,
  };
}

/** readValidate reads one migration.validate, which names the migration to check and nothing else. */
export function readValidate(params: unknown): ValidateParams {
  return { id: required(objectOf(params)["id"], "id") };
}

/** readCancel reads one $/cancel, or nothing, since a notification has no answer to refuse with. */
export function readCancel(params: unknown): CancelParams | undefined {
  if (typeof params !== "object" || params === null) {
    return undefined;
  }

  const id = (params as Record<string, unknown>)["id"];

  return typeof id === "number" && Number.isInteger(id) ? { id } : undefined;
}

/** objectOf refuses parameters that are not the object every method's are. */
function objectOf(params: unknown): Record<string, unknown> {
  if (params === undefined || params === null) {
    return {};
  }

  if (typeof params !== "object" || Array.isArray(params)) {
    throw new ProtocolError(INVALID_PARAMS, "params must be an object");
  }

  return params as Record<string, unknown>;
}

/** required reads a member that must be a non-empty string. */
function required(held: unknown, named: string): string {
  if (typeof held !== "string" || held === "") {
    throw new ProtocolError(INVALID_PARAMS, `${named} is required`);
  }

  return held;
}

/** numberOf reads a member that must be a number, which the two version lines are. */
function numberOf(held: unknown, named: string): number {
  if (typeof held !== "number" || !Number.isInteger(held)) {
    throw new ProtocolError(INVALID_PARAMS, `${named} must be a whole number`);
  }

  return held;
}

/** stringOf reads one member of a nested object, naming the path when it is not there. */
function stringOf(held: unknown, member: string, path: string): string {
  return required(objectOf(held)[member], path);
}

/** componentOf reads a name and a version, which is what both halves of the handshake carry. */
function componentOf(held: unknown, path: string): Component {
  const nested = objectOf(held);

  return {
    name: required(nested["name"], `${path}.name`),
    version: required(nested["version"], `${path}.version`),
  };
}
