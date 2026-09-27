// The program @migr/sdk installs as migr-runtime-node.
//
// docs/protocol.md §9 specifies no command-line contract and says a runtime must not depend on argv,
// so nothing here reads any. What it is told arrives in initialize: migrations.dir, absolute, and a
// working directory that is the project root rather than that directory.
//
// Source maps are enabled before the first migration is loaded, because every .ts file reaches Node
// through esbuild from here on and a stack trace pointing into transpiled output names a line the
// author never wrote.

import { setSourceMapsSupport } from "node:module";

import { fromDirectory } from "./directory.ts";
import type { Registry } from "./load.ts";
import { run } from "./session.ts";
import type { InitializeParams } from "./wire/payload.ts";

/** main serves this process as a migr runtime, and returns the status it should exit with. */
export async function main(): Promise<number> {
  setSourceMapsSupport(true);

  return run({ load: opened });
}

/** opened reads the project the handshake described, which is one directory of migration files. */
function opened(project: InitializeParams): Promise<Registry> {
  return fromDirectory(project.migrations.dir);
}
