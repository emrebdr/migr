// A throwaway database, and one place the config cannot be put.
//
// The config has to live inside the project rather than in a temporary directory somewhere, and that
// is measured rather than tidy: docs/protocol.md §9 makes the directory holding migr.yaml the
// runtime's working directory, and both adapters probe from it -- the Node one asks Node to resolve
// @migr/sdk from there, the Go one runs go list -m. A config in /tmp therefore fails before a
// migration is ever loaded.
//
// So it goes under <project>/.migr/, which is the directory migr already owns inside a project and
// which ignores itself from the inside with a .gitignore holding "*". The database does
// not: it goes in a temporary directory of its own, and remove() takes both away.
//
// Every command is spawnSync, so nothing is left running for a test to clean up after and
// an assertion reads like one. The documents are read from --json rather than scraped from the
// tables, since those shapes are ones internal/cli specifies and tests.

import { spawnSync } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";

import { LANGUAGE } from "../index.ts";

import { found } from "./binary.ts";

/** CACHE_DIR is the directory migr already owns inside a project, which ignores itself. */
export const CACHE_DIR = ".migr";

/** LOCK_TIMEOUT keeps a wedged run failing in seconds rather than waiting out the 30s default. */
const LOCK_TIMEOUT = "5s";

/** EXIT_VALIDATION is what migr validate leaves when it found problems, which is a report. */
const EXIT_VALIDATION = 2;

/** Options is the throwaway a migration is run against: a database removed with the project. */
export interface Options {
  /** The migrations directory, which the config names absolutely whatever this says. */
  dir: string;
  /** What migrations.language holds, and this SDK's own language by default. */
  language?: string;
  /** The engine to run against, and sqlite over a file of this project's own by default. */
  driver?: string;
  /** How that engine is reached, which must be given whenever driver is. */
  dsn?: string;
  /** The table the runs are recorded in, and one named for this project by default. */
  history?: string;
  /** The operation kinds this project permits, which migr up refuses otherwise. */
  destructive?: string[];
  /** The whole migr.yaml to write instead, for a project the fields above cannot describe. */
  config?: string;
  /** The migr to run, and whatever found() resolves by default. */
  binary?: string;
}

/** Applied is one migration an invocation performed, and what it cost. */
export interface Applied {
  /** Its position within the batch, counting from one. */
  ordinal: number;
  /** The migration's UTC timestamp. */
  version: string;
  /** The filename it was found in. */
  file: string;
  /** How many statements it executed. */
  statements: number;
}

/** Skipped is one migration an invocation did not act on, and the state it was left in. */
export interface Skipped {
  /** The migration's UTC timestamp. */
  version: string;
  /** The filename it was found in, absent when only the history names it. */
  file?: string;
  /** What reconciliation resolved it to. */
  state: string;
}

/** Run is what one migr up or migr down did. */
export interface Run {
  /** The name migr.yaml holds the target under. */
  database: string;
  /** Which way the invocation ran. */
  direction: string;
  /** What every run in this invocation was recorded under, absent when nothing ran. */
  batch?: number;
  /** One entry per migration performed, in the order they were performed. */
  runs: Applied[];
  /** One entry per migration this invocation did not perform. */
  skipped?: Skipped[];
}

/** Finding is one problem or warning a validation run reported. */
export interface Finding {
  /** The migration's UTC timestamp. */
  version: string;
  /** The filename it was found in. */
  file: string;
  /** What found the problem. */
  check: string;
  /** The lint rule that found it, absent for a check that is not a rule. */
  rule?: string;
  /** Which of the migration's two halves the problem is in. */
  direction: string;
  /** Its place in that half, counting from one, absent when it is not about one operation. */
  operation?: number;
  /** The operation at fault, absent when the problem is not about one. */
  kind?: string;
  /** What is wrong, in terms its author can act on. */
  detail: string;
}

/** Unchecked is one half of one migration a run could not judge, and what stopped it. */
export interface Unchecked {
  /** The migration's UTC timestamp. */
  version: string;
  /** The filename it was found in. */
  file: string;
  /** Which half went unchecked. */
  direction: string;
  /** The one operation this names, absent for a whole half. */
  operation?: number;
  /** That operation, absent where a whole half went unchecked. */
  kind?: string;
  /** What stopped the check. */
  reason: string;
}

/** Report is what migr validate found, which is its product rather than its failure. */
export interface Report {
  /** The engine every migration was compiled for. */
  driver: string;
  /** Whether the checks needing the target database were made at all. */
  connected: boolean;
  /** What this project's migrations are written in. */
  language: string;
  /** How many migrations were looked at. */
  migrations: number;
  /** Everything found wrong, which exits 2. */
  problems: Finding[];
  /** Everything a rule set to warn found, which is judged and fails nothing. */
  warnings?: Finding[];
  /** Every half nothing could judge, which is not the same as one that passed. */
  unchecked?: Unchecked[];
}

/** Counts is how many migrations are in each of the five states. */
export interface Counts {
  /** How many have run and are recorded as applied. */
  applied: number;
  /** How many a stopped run left between states. */
  dirty: number;
  /** How many no longer hash to what was recorded. */
  drifted: number;
  /** How many the history names and the directory does not hold. */
  missing: number;
  /** How many have not run. */
  pending: number;
}

/** Entry is one migration migr status printed a row for. */
export interface Entry {
  /** The migration's UTC timestamp. */
  version: string;
  /** The filename it was found in, absent when only the history names it. */
  file?: string;
  /** What reconciliation resolved it to. */
  state: string;
}

/** Status is what migr status reported, which takes no migration lock and promises no action. */
export interface Status {
  /** Whether this database has a migration history table at all. */
  history_table: boolean;
  /** How many migrations the directory holds. */
  files: number;
  /** How the migrations were sequenced, which a report names rather than resolves. */
  order: string;
  /** One row per migration, in the order they were printed. */
  statuses: Entry[];
  /** How many migrations are in each state. */
  counts: Counts;
}

/** Failure is what migr reported when a command failed, which --json makes one document. */
export interface Failure {
  /** The exit code, which CLAUDE.md §5 gives five meanings. */
  code: number;
  /** What went wrong, as the one line a failing command writes. */
  error: string;
}

/** Project is a staged project with a database of its own, and the commands run against it. */
export interface Project {
  /** The migr.yaml this project is driven through, for a test that reads or extends it. */
  readonly config: string;
  /** How the throwaway database is reached, for a test that inspects it with its own driver. */
  readonly dsn: string;
  /** Applies this project's migrations, throwing with what migr reported if it could not. */
  up(...args: string[]): Run;
  /** Rolls them back, throwing with what migr reported if it could not. */
  down(...args: string[]): Run;
  /** Reports what the checks found, problems being this command's product and not a failure. */
  validate(...args: string[]): Report;
  /** Reports what this database holds. */
  status(...args: string[]): Status;
  /** Runs any command and returns the document it printed, for one this helper does not type. */
  output(...args: string[]): string;
  /** Runs a command expecting it to fail, and reports the failure migr wrote on stderr. */
  refused(...args: string[]): Failure;
  /** Applies and rolls back twice, which is what says a rollback left nothing behind. */
  roundTrip(): void;
  /** Removes the config and the database, which a test calls in an after hook or a finally. */
  remove(): void;
}

/** project stages a project over a database of its own, and returns what runs against it. */
export function project(options: Options): Project {
  const dir = absolute(options.dir);
  const home = inside(dir);
  const scratch = mkdtempSync(join(tmpdir(), "migr-test-"));
  const dsn = target(options, scratch);
  const binary = options.binary ?? found();
  const config = join(home, "migr.yaml");

  writeFileSync(config, render(options, dir, dsn, table(options, home)));

  const ran = (args: string[]): Run => into<Run>(binary, config, args);

  return {
    config,
    dsn,
    up: (...args: string[]) => ran(["up", ...args]),
    down: (...args: string[]) => ran(["down", ...args]),
    status: (...args: string[]) => into<Status>(binary, config, ["status", ...args]),
    validate: (...args: string[]) => validated(binary, config, ["validate", ...args]),
    output: (...args: string[]) => printed(binary, config, args),
    refused: (...args: string[]) => refusal(binary, config, args),
    roundTrip: () => {
      const first = ran(["up"]);

      ran(["down"]);

      const again = ran(["up"]);

      ran(["down"]);

      if (again.runs.length !== first.runs.length) {
        throw new Error(
          `the first up applied ${first.runs.length} migrations and the second applied ` +
            `${again.runs.length}, so the rollback between them did not leave the database ` +
            `where the first up found it`,
        );
      }

      const left = into<Status>(binary, config, ["status"]).counts.applied;

      if (left !== 0) {
        throw new Error(`${left} migrations are still applied after the second rollback`);
      }
    },
    remove: () => {
      rmSync(home, { recursive: true, force: true });
      rmSync(scratch, { recursive: true, force: true });
    },
  };
}

/** into runs one command, throws if migr did, and reads the document it printed. */
function into<T>(binary: string, config: string, args: string[]): T {
  return JSON.parse(printed(binary, config, args)) as T;
}

/** printed runs one command and returns its document, throwing with both streams if it failed. */
function printed(binary: string, config: string, args: string[]): string {
  const { out, stderr, code } = run(binary, config, args);

  return document(args, out, stderr, code, 0);
}

/** validated runs the checks, tolerating the one exit code that means problems were found. */
function validated(binary: string, config: string, args: string[]): Report {
  const { out, stderr, code } = run(binary, config, args);

  return JSON.parse(document(args, out, stderr, code, EXIT_VALIDATION)) as Report;
}

/**
 * document reports the one document a command printed, throwing where migr printed none.
 *
 * A blank answer is never a report, and `migr validate` is why this is a check rather than an
 * assumption: exit 2 is both "the checks found problems", whose report is on stdout, and "this
 * command was given something it refuses" -- a filename the grammar will not take, say -- whose
 * message is on stderr. Which of the two happened is decided by whether a document was printed.
 */
function document(
  args: string[],
  out: string,
  stderr: string,
  code: number,
  tolerated: number,
): string {
  if (code !== 0 && code !== tolerated) {
    throw new Error(`migr ${args.join(" ")} exited ${code}: ${stderr.trim()}`);
  }

  if (out.trim() === "") {
    throw new Error(
      `migr ${args.join(" ")} exited ${code} and printed no document: ${stderr.trim()}`,
    );
  }

  return out;
}

/** refusal runs a command expecting it to fail, and reads the document it wrote on stderr. */
function refusal(binary: string, config: string, args: string[]): Failure {
  const { stderr, code } = run(binary, config, args);

  if (code === 0) {
    throw new Error(`migr ${args.join(" ")} succeeded, and this case is about how it fails`);
  }

  const lines = stderr.trim().split("\n");

  try {
    return JSON.parse(lines[lines.length - 1] ?? "") as Failure;
  } catch {
    throw new Error(
      `migr ${args.join(" ")} exited ${code} and wrote no failure document: ${stderr.trim()}`,
    );
  }
}

/** run spawns one command against this project, and reports both streams and the status it left. */
function run(
  binary: string,
  config: string,
  args: string[],
): { out: string; stderr: string; code: number } {
  const spawned = spawnSync(binary, ["--config", config, "--json", ...args], {
    encoding: "utf8",
  });

  if (spawned.error !== undefined) {
    throw new Error(`running ${binary}: ${String(spawned.error)}`);
  }

  return { out: spawned.stdout, stderr: spawned.stderr, code: spawned.status ?? -1 };
}

/** render builds the whole migr.yaml, or returns the one a project supplied instead of the fields. */
export function render(options: Options, dir: string, dsn: string, history: string): string {
  if (options.config !== undefined) {
    return options.config;
  }

  const body =
    `version: 1\n` +
    `migrations:\n` +
    `  dir: ${JSON.stringify(dir)}\n` +
    `  language: ${JSON.stringify(options.language ?? LANGUAGE)}\n` +
    `databases:\n` +
    `  default:\n` +
    `    driver: ${JSON.stringify(options.driver ?? "sqlite")}\n` +
    `    dsn: ${JSON.stringify(dsn)}\n` +
    `history:\n` +
    `  table: ${JSON.stringify(history)}\n` +
    `lock:\n` +
    `  timeout: ${LOCK_TIMEOUT}\n`;

  return body + permitted(options.destructive ?? []);
}

/** permitted renders which kinds this project lets a migration destroy, which migr up reads. */
function permitted(kinds: string[]): string {
  if (kinds.length === 0) {
    return "";
  }

  return `validation:\n  allow_destructive: [${kinds.map((k) => JSON.stringify(k)).join(", ")}]\n`;
}

/** target reports how the database is reached, which is a file of this project's own by default. */
function target(options: Options, scratch: string): string {
  if ((options.dsn === undefined) !== (options.driver === undefined)) {
    throw new Error(
      "a project naming its own driver names its own dsn too, and the other way round",
    );
  }

  return options.dsn ?? join(scratch, "throwaway.db");
}

/** table reports which history table the runs go in, one per project rather than per file. */
export function table(options: Options, home: string): string {
  return options.history ?? `migr_${identifier(home.slice(home.lastIndexOf("test-")))}`;
}

/** identifier renders something every engine accepts as a table name. */
function identifier(name: string): string {
  return name.toLowerCase().replace(/[^a-z0-9]/g, "_");
}

/** absolute resolves the migrations directory, which the config names absolutely for §9's reason. */
function absolute(dir: string): string {
  if (dir === "") {
    throw new Error("a project names the directory its migrations are in");
  }

  const resolved = resolve(dir);

  if (!existsSync(resolved)) {
    throw new Error(
      `${JSON.stringify(resolved)} is not a directory migrations can be staged from`,
    );
  }

  return resolved;
}

/** inside makes the directory the throwaway config lives in, which has to be within the project. */
function inside(dir: string): string {
  const base = join(dirname(dir), CACHE_DIR);

  mkdirSync(base, { recursive: true });
  ignore(base);

  return mkdtempSync(join(base, "test-"));
}

/** ignore writes the self-ignore migr's own cache directory carries, where there is not one already. */
function ignore(base: string): void {
  const path = join(base, ".gitignore");

  if (!existsSync(path)) {
    writeFileSync(path, "*\n");
  }
}
