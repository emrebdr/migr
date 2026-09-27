// Which module system a file belongs to, by Node's own rule.
//
// Node answers this itself for JavaScript and does not answer it at all for TypeScript: a load hook
// is handed a null format for every .ts file that is not inside a "type": "module" package, and
// calling nextLoad to find out throws ERR_UNSUPPORTED_TYPESCRIPT_SYNTAX on the very syntax esbuild
// is here to handle. So the rule is read here, once, and two callers share the answer -- the hook,
// to know what esbuild should emit, and the loader, to know whether an imported namespace is the
// interop one Node builds around module.exports.

import { readFileSync } from "node:fs";
import { dirname, extname, join } from "node:path";

/** System is the two module systems Node runs, which decide how a file is compiled and read. */
export type System = "commonjs" | "module";

/** MANIFEST is the file whose type member decides the system for everything beside it. */
const MANIFEST = "package.json";

/** MODULE are the extensions that are ES modules whatever the package around them declares. */
const MODULE = [".mjs", ".mts"];

/** COMMONJS are the extensions that are CommonJS whatever the package around them declares. */
const COMMONJS = [".cjs", ".cts"];

/** systemOf reports which module system a file belongs to, the way Node itself decides it. */
export function systemOf(path: string): System {
  const extension = extname(path);

  if (MODULE.includes(extension)) {
    return "module";
  }

  if (COMMONJS.includes(extension)) {
    return "commonjs";
  }

  return declared(dirname(path));
}

/** declared walks towards the root for the nearest package.json, which is where Node stops too. */
function declared(from: string): System {
  for (let at = from; ;) {
    const type = typeOf(join(at, MANIFEST));

    if (type !== undefined) {
      return type === "module" ? "module" : "commonjs";
    }

    const up = dirname(at);

    if (up === at) {
      return "commonjs";
    }

    at = up;
  }
}

/** typeOf reads one package.json's type, reporting nothing at all for a file that is not there. */
function typeOf(path: string): string | undefined {
  const source = read(path);

  return source === undefined ? undefined : parsed(path, source);
}

/** read returns a file's contents, and nothing for the one that is not there, which is expected. */
function read(path: string): string | undefined {
  try {
    return readFileSync(path, "utf8");
  } catch {
    return undefined;
  }
}

/** parsed reads the type member, refusing a manifest that is not JSON rather than assuming one. */
function parsed(path: string, source: string): string {
  let held: { type?: unknown };

  try {
    held = JSON.parse(source) as { type?: unknown };
  } catch (thrown) {
    throw new Error(`${path} is not JSON, so nothing can say which module system applies`, {
      cause: thrown,
    });
  }

  return typeof held.type === "string" ? held.type : "";
}
