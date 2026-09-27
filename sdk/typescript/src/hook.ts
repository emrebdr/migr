// Making a TypeScript migration something Node can run, with no build step of the author's own.
//
// Two hooks, and both of them answer something Node will not. load transpiles through esbuild rather
// than leaving it to Node's own type stripping, which refuses an enum -- ERR_UNSUPPORTED_TYPESCRIPT_SYNTAX
// -- along with every other construct that is not erasable. resolve retries a relative .js specifier
// as the .ts file beside it, because tsc forbids writing ./x.ts unless allowImportingTsExtensions is
// on: .js is the only spelling a TypeScript project can use for its own helper, and it is the one
// Node cannot resolve. Without that retry, "no build step" holds only for a migration that imports
// nothing.
//
// They are registered by install and never at import. Reaching into the process of a program that
// merely imported this package is worse than the hole it would close (ADR 0037, E) -- and esbuild is
// imported dynamically for the same reason, so writing a migration does not load a transpiler.

import { readFileSync } from "node:fs";
import { registerHooks } from "node:module";
import type {
  LoadFnOutput,
  LoadHookContext,
  ResolveFnOutput,
  ResolveHookContext,
} from "node:module";
import { extname } from "node:path";
import { fileURLToPath } from "node:url";

import type { transformSync } from "esbuild";

import type { System } from "./system.ts";
import { systemOf } from "./system.ts";

/** TRANSPILED are the extensions Node cannot run itself, which are the TypeScript ones. */
const TRANSPILED = [".ts", ".mts", ".cts"];

/** RETRIED maps the specifier tsc makes a project write onto the file that is beside it. */
const RETRIED = [
  [".js", ".ts"],
  [".mjs", ".mts"],
  [".cjs", ".cts"],
] as const;

/** TARGET is what the output must run on, which is this package's own engines requirement. */
const TARGET = "node24";

/** NOT_FOUND is Node's code for a specifier that resolved to nothing, and the only one retried. */
const NOT_FOUND = "ERR_MODULE_NOT_FOUND";

/** installing is the one installation, kept so that a second call waits on the first. */
let installing: Promise<void> | undefined;

/** transpile is what turns TypeScript into JavaScript, held because a load hook is synchronous. */
let transpile: typeof transformSync | undefined;

/** install puts this process's module hooks in place, once, and never at import. */
export async function install(): Promise<void> {
  installing ??= registered();

  await installing;
}

/** missing reports an installation with no transpiler, which is the one way install fails. */
export function missing(cause: unknown): Error {
  return new Error(
    "@migr/sdk could not load esbuild, which is what it transpiles TypeScript migrations with: " +
      "reinstall this package's dependencies",
    { cause },
  );
}

/** registered loads the transpiler and puts both hooks in place, which happens exactly once. */
async function registered(): Promise<void> {
  transpile = await loaded();

  registerHooks({ load, resolve });
}

/** loaded imports esbuild, which is dynamic so that only a process loading TypeScript pays for it. */
async function loaded(): Promise<typeof transformSync> {
  try {
    return (await import("esbuild")).transformSync;
  } catch (thrown) {
    throw missing(thrown);
  }
}

/** load hands Node JavaScript for every TypeScript file, and leaves everything else to Node. */
function load(
  url: string,
  context: LoadHookContext,
  nextLoad: (url: string, context?: Partial<LoadHookContext>) => LoadFnOutput,
): LoadFnOutput {
  if (!transpiles(url)) {
    return nextLoad(url, context);
  }

  const path = fileURLToPath(url);
  const system = systemOf(path);

  return { format: system, source: javascript(path, system), shortCircuit: true };
}

/** transpiles reports whether a URL names a TypeScript file on this filesystem. */
function transpiles(url: string): boolean {
  return url.startsWith("file:") && TRANSPILED.includes(extname(new URL(url).pathname));
}

/** javascript runs one file through esbuild, emitting the system Node will evaluate it as. */
function javascript(path: string, system: System): string {
  if (transpile === undefined) {
    throw missing(new Error("the hooks were installed without one"));
  }

  return transpile(readFileSync(path, "utf8"), {
    loader: "ts",
    format: system === "commonjs" ? "cjs" : "esm",
    target: TARGET,
    sourcefile: path,
    sourcemap: "inline",
  }).code;
}

/** resolve answers what Node will not: the .ts file a project's own .js specifier was written for. */
function resolve(
  specifier: string,
  context: ResolveHookContext,
  nextResolve: (specifier: string, context?: Partial<ResolveHookContext>) => ResolveFnOutput,
): ResolveFnOutput {
  try {
    return nextResolve(specifier, context);
  } catch (thrown) {
    return beside(specifier, context, nextResolve, thrown);
  }
}

/** beside resolves the TypeScript sibling of a relative specifier, or reports Node's own refusal. */
function beside(
  specifier: string,
  context: ResolveHookContext,
  nextResolve: (specifier: string, context?: Partial<ResolveHookContext>) => ResolveFnOutput,
  thrown: unknown,
): ResolveFnOutput {
  const sibling = written(specifier);

  if (sibling === undefined || !relative(specifier) || !absent(thrown)) {
    throw thrown;
  }

  try {
    return nextResolve(sibling, context);
  } catch {
    throw thrown;
  }
}

/** written returns the TypeScript file a JavaScript specifier would have been compiled from. */
function written(specifier: string): string | undefined {
  for (const [spelled, compiled] of RETRIED) {
    if (specifier.endsWith(spelled)) {
      return `${specifier.slice(0, specifier.length - spelled.length)}${compiled}`;
    }
  }

  return undefined;
}

/** relative reports whether a specifier names a file beside the one importing it. */
function relative(specifier: string): boolean {
  return specifier.startsWith("./") || specifier.startsWith("../");
}

/** absent reports whether Node refused a specifier because there is no such file. */
function absent(thrown: unknown): boolean {
  return (thrown as { code?: unknown } | null)?.code === NOT_FOUND;
}
