// Reading a directory of migration files as the registry a session serves from.
//
// This is the half load.ts deliberately does not have. It imports paths, which makes it the one
// module here that cannot be exercised without a filesystem -- and keeping it separate is what lets
// every rule about a module's exports be tested with no import, no process, and no toolchain.
//
// The rules it applies to the directory are the engine's own (CLAUDE.md §9): discovery is flat, a
// dotfile is the one exemption, and a file the grammar refuses fails rather than being skipped. A
// migration that was written, committed, and quietly ignored is the expensive mistake; a README
// failing loudly is by a wide margin the cheap one.
//
// Every refusal joins the registry instead of ending the walk, so migration.discover reports
// everything wrong with a directory at once rather than the first thing.

import type { Dirent } from "node:fs";
import { readdir } from "node:fs/promises";
import { join, resolve } from "node:path";
import { pathToFileURL } from "node:url";

import { MigrationError } from "./error.ts";
import { install } from "./hook.ts";
import { Registry } from "./load.ts";
import { parseName } from "./name.ts";
import { systemOf } from "./system.ts";

/** DOT opens the name of a file discovery ignores, which is the one exemption the grammar has. */
const DOT = ".";

/** fromDirectory reads one migrations directory, importing every file that is a migration. */
export async function fromDirectory(dir: string): Promise<Registry> {
  await install();

  const at = resolve(dir);
  const registry = new Registry();

  for (const entry of await entries(at)) {
    await considered(registry, at, entry);
  }

  return registry;
}

/** entries reads the directory once, in name order, so a report never depends on the filesystem. */
async function entries(at: string): Promise<Dirent[]> {
  let found: Dirent[];

  try {
    found = await readdir(at, { withFileTypes: true });
  } catch (thrown) {
    throw new Error(`the migrations directory ${at} could not be read`, { cause: thrown });
  }

  return found.sort(byName);
}

/** considered records what one directory entry is, or records why it is not a migration at all. */
async function considered(registry: Registry, at: string, entry: Dirent): Promise<void> {
  if (entry.name.startsWith(DOT)) {
    return;
  }

  if (entry.isDirectory()) {
    registry.refuse(
      new MigrationError(
        "malformed_name",
        entry.name,
        `${entry.name} is a directory, and a migrations directory holds migrations and nothing else`,
      ),
    );

    return;
  }

  if (!named(registry, entry.name)) {
    return;
  }

  await imported(registry, join(at, entry.name), entry.name);
}

/** named reports whether a filename is one the grammar accepts, recording the reason when it is not. */
function named(registry: Registry, file: string): boolean {
  try {
    parseName(file);
  } catch (thrown) {
    registry.refuse(errorOf(thrown));

    return false;
  }

  return true;
}

/** imported loads one migration file and hands the registry what it exported. */
async function imported(registry: Registry, path: string, file: string): Promise<void> {
  let namespace: unknown;

  try {
    namespace = await import(pathToFileURL(path).href);
  } catch (thrown) {
    registry.refuse(
      new MigrationError(
        "not_loadable",
        file,
        `${file} could not be loaded: ${describe(thrown)}`,
        {
          cause: thrown,
        },
      ),
    );

    return;
  }

  registry.add(file, exportsOf(path, namespace));
}

/** exportsOf unwraps what a CommonJS module reaches an import as, which is not what it exported. */
function exportsOf(path: string, namespace: unknown): unknown {
  if (systemOf(path) !== "commonjs") {
    return namespace;
  }

  return (namespace as { default?: unknown }).default;
}

/** byName orders the directory the way a human reads it, which for migrations is version order. */
function byName(a: Dirent, b: Dirent): number {
  if (a.name === b.name) {
    return 0;
  }

  return a.name < b.name ? -1 : 1;
}

/** errorOf renders whatever was thrown as an error, since JavaScript permits throwing anything. */
function errorOf(thrown: unknown): Error {
  return thrown instanceof Error ? thrown : new Error(String(thrown));
}

/** describe renders what was thrown as the sentence a refusal carries. */
function describe(thrown: unknown): string {
  return thrown instanceof Error ? thrown.message : String(thrown);
}
