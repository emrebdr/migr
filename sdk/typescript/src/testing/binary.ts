// Where migr is, and why finding it is a failure rather than a skip.
//
// Nothing in this SDK can compile an operation into SQL -- the drivers are the engine's, and will
// stay there -- so running a migration against a real database means running the real binary. A
// helper that quietly skipped itself when the binary was missing would report a pass it did not
// earn, which migr validate refuses to do too; so this throws and names both rungs.
// found() is exported for a suite that would rather decide for itself.
//
// MIGR_BIN set to something that is not an executable is an error and not a fall-through to PATH:
// whoever set it meant it, and silently running a different binary is how a suite starts measuring
// something nobody chose.

import { accessSync, constants, statSync } from "node:fs";
import { delimiter, join } from "node:path";

/** BINARY_VAR is the environment variable naming which migr to run. */
export const BINARY_VAR = "MIGR_BIN";

/** BINARY_NAME is what the second rung looks for on PATH. */
export const BINARY_NAME = "migr";

/** NoBinaryError is a machine with no migr to run, which no project helper can work without. */
export class NoBinaryError extends Error {
  /** name is what a suite matches on when it would rather skip than fail. */
  override readonly name = "NoBinaryError";

  /** Builds the refusal, which names what was looked at rather than only that nothing was found. */
  constructor(message: string) {
    super(`no migr binary: ${message}`);
  }
}

/** Environment is what the ladder reads, passed in so a test stages it without setting it. */
export interface Environment {
  /** The variable naming a binary, absent when nothing set it. */
  named?: string | undefined;
  /** The directories to look in, which is PATH split the way this platform splits it. */
  path: string[];
}

/** found reports where migr is, or throws saying why this machine has none, starting nothing. */
export function found(environment: Environment = ambient()): string {
  if (environment.named !== undefined) {
    return stated(environment.named);
  }

  for (const directory of environment.path) {
    const candidate = join(directory, BINARY_NAME);

    if (runnable(candidate)) {
      return candidate;
    }
  }

  throw new NoBinaryError(
    `${BINARY_VAR} names none, and ${JSON.stringify(BINARY_NAME)} is not on PATH`,
  );
}

/** ambient reads the real environment, which is what found uses when a caller stages nothing. */
export function ambient(): Environment {
  return {
    named: process.env[BINARY_VAR],
    path: (process.env["PATH"] ?? "").split(delimiter).filter((entry) => entry !== ""),
  };
}

/** stated reports the binary the variable names, a failure rather than a fallback when it is wrong. */
function stated(named: string): string {
  if (named === "") {
    throw new NoBinaryError(`${BINARY_VAR} is set to nothing`);
  }

  let directory = false;

  try {
    directory = statSync(named).isDirectory();
  } catch (thrown) {
    throw new NoBinaryError(
      `${BINARY_VAR} names ${JSON.stringify(named)}, which is not there (${String(thrown)})`,
    );
  }

  if (directory || !runnable(named)) {
    throw new NoBinaryError(
      `${BINARY_VAR} names ${JSON.stringify(named)}, which is not an executable`,
    );
  }

  return named;
}

/** runnable reports whether a path is something this process may execute. */
function runnable(path: string): boolean {
  try {
    accessSync(path, constants.X_OK);

    return statSync(path).isFile();
  } catch {
    return false;
  }
}
