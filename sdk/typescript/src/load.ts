// Reading an imported module as the migration its filename names.
//
// This is the runtime half of the contract, and it exists because the compile-time half cannot
// cover everything. A .js or .mjs migration is loaded by the same runtime and checked by nothing;
// and the named-export style is unconstrainable by any type, since nothing relates one export of a
// module to another. So every rule migration.ts states is stated again here.
//
// It takes a namespace rather than a path: importing files is migr-runtime-node's job (M7-T5), and
// keeping that out means all of this is testable with no import, no process, and no toolchain.

import { MigrationError } from "./error.ts";
import type { Migration } from "./migration.ts";
import { parseName } from "./name.ts";

/** Loaded is one migration this SDK read, and what its filename says about it. */
export interface Loaded {
  /** The UTC timestamp its filename carries, which the engine keys everything by. */
  readonly version: string;
  /** The snake_case name, for a message a human reads. */
  readonly name: string;
  /** The filename it was read from, which migration.discover reports. */
  readonly file: string;
  /** The migration itself. */
  readonly migration: Migration;
}

/** NAMED are the members a module may export one by one, which is the second of the two styles. */
const NAMED = ["up", "down", "validate", "dependencies", "irreversible"] as const;

/** fromModule reads one imported module as the migration its filename names. */
export function fromModule(file: string, namespace: unknown): Loaded {
  const named = parseName(file);

  return {
    version: named.version,
    name: named.name,
    file: named.file,
    migration: migrationOf(file, namespace),
  };
}

/** Registry is every migration one loader read, and everything wrong with the set. */
export class Registry {
  /** loaded is each migration read, keyed by the version its filename carries. */
  readonly #loaded = new Map<string, Loaded>();
  /** problems are the modules that could not be read, in the order they were offered. */
  readonly #problems: Error[] = [];

  /** add reads one imported module, or records the reason it could not be read. */
  add(file: string, namespace: unknown): void {
    try {
      this.#record(fromModule(file, namespace));
    } catch (thrown) {
      this.#problems.push(errorOf(thrown));
    }
  }

  /** all returns what was read, in version order, reporting every problem rather than the first. */
  all(): Loaded[] {
    if (this.#problems.length > 0) {
      throw joined(this.#problems);
    }

    return [...this.#loaded.values()].sort(byVersion);
  }

  /** lookup returns the migration read under one version, and undefined when there is none. */
  lookup(version: string): Loaded | undefined {
    return this.#loaded.get(version);
  }

  /** record keeps one migration, refusing a version another file already claims. */
  #record(entry: Loaded): void {
    const taken = this.#loaded.get(entry.version);

    if (taken !== undefined) {
      throw new MigrationError(
        "duplicate",
        entry.file,
        `${taken.file} and ${entry.file} both register version ${entry.version}`,
      );
    }

    this.#loaded.set(entry.version, entry);
  }
}

/** migrationOf reads a module's exports as a migration, in whichever of the two styles it uses. */
function migrationOf(file: string, namespace: unknown): Migration {
  const exported = recordOf(file, namespace);
  const named = NAMED.filter((member) => exported[member] !== undefined);
  const fallback = exported["default"];

  if (fallback !== undefined) {
    if (named.length > 0) {
      throw new MigrationError(
        "ambiguous_module",
        file,
        `${file} default-exports a migration and also exports ${listed(named)}, ` +
          "and a module uses one style or the other",
      );
    }

    return checked(file, objectOf(file, fallback));
  }

  if (named.length === 0) {
    throw new MigrationError(
      "no_migration",
      file,
      `${file} exports no migration: default-export one, or export up on its own ` +
        `(with any of ${listed(NAMED.slice(1))})`,
    );
  }

  return checked(file, exported);
}

/** checked holds a module's exports to every rule the contract states, and is the one cast here. */
function checked(file: string, source: Record<string, unknown>): Migration {
  checkUp(file, source["up"]);
  checkOptionalFunction(file, source["down"], "down");
  checkOptionalFunction(file, source["validate"], "validate");
  checkDependencies(file, source["dependencies"]);
  checkIrreversible(file, source["irreversible"]);
  checkDirections(file, source["down"], source["irreversible"]);

  return source as unknown as Migration;
}

/** checkUp refuses a module whose up is missing or is not something that can be run. */
function checkUp(file: string, value: unknown): void {
  if (value === undefined) {
    throw new MigrationError(
      "bad_member",
      file,
      `${file} exports no up, and up is the one member a migration must have`,
    );
  }

  if (typeof value !== "function") {
    throw new MigrationError(
      "bad_member",
      file,
      `${file} exports up as ${describeType(value)}, and up is a function`,
    );
  }
}

/** checkOptionalFunction refuses an optional member that is present as something uncallable. */
function checkOptionalFunction(file: string, value: unknown, member: string): void {
  if (value !== undefined && typeof value !== "function") {
    throw new MigrationError(
      "bad_member",
      file,
      `${file} exports ${member} as ${describeType(value)}, and ${member} is a function`,
    );
  }
}

/** checkDependencies refuses anything but a list of versions, a computation of one included. */
function checkDependencies(file: string, value: unknown): void {
  if (value === undefined) {
    return;
  }

  if (!Array.isArray(value) || value.some((version) => typeof version !== "string")) {
    throw new MigrationError(
      "bad_member",
      file,
      `${file} exports dependencies as ${describeType(value)}, and dependencies is a list of ` +
        "versions this migration declares, never one it computes",
    );
  }
}

/** checkIrreversible refuses a declaration that is not a reason, a blank one included. */
function checkIrreversible(file: string, value: unknown): void {
  if (value === undefined) {
    return;
  }

  if (typeof value !== "string") {
    throw new MigrationError(
      "bad_member",
      file,
      `${file} exports irreversible as ${describeType(value)}, and irreversible is the reason a ` +
        "rollback halts with",
    );
  }

  if (value.trim() === "") {
    throw new MigrationError(
      "no_reason",
      file,
      `${file} declares itself irreversible and gives no reason`,
    );
  }
}

/** checkDirections refuses a migration that both authors an undo and declares it has none. */
function checkDirections(file: string, down: unknown, irreversible: unknown): void {
  if (down !== undefined && irreversible !== undefined) {
    throw new MigrationError(
      "both_directions",
      file,
      `${file} exports a down and also declares itself irreversible, ` +
        "and a migration is one or the other",
    );
  }
}

/** recordOf refuses a namespace that is not a module's, which nothing could be exported from. */
function recordOf(file: string, namespace: unknown): Record<string, unknown> {
  if (typeof namespace !== "object" || namespace === null) {
    throw new MigrationError(
      "no_migration",
      file,
      `${file} loaded as ${describeType(namespace)} rather than as a module`,
    );
  }

  return namespace as Record<string, unknown>;
}

/** objectOf refuses a default export that is not the object a migration is. */
function objectOf(file: string, value: unknown): Record<string, unknown> {
  if (typeof value === "function") {
    throw defaultIsNotAnInstance(file, value);
  }

  if (typeof value !== "object" || value === null) {
    throw new MigrationError(
      "no_migration",
      file,
      `${file} default-exports ${describeType(value)}, and a migration is an object with an up`,
    );
  }

  return value as Record<string, unknown>;
}

/** defaultIsNotAnInstance tells a class from a plain function, since the advice differs. */
function defaultIsNotAnInstance(file: string, value: unknown): MigrationError {
  const named = value as { name?: unknown; prototype?: unknown };
  const shape = named.prototype;
  const carries =
    typeof shape === "object" &&
    shape !== null &&
    typeof (shape as Record<string, unknown>)["up"] === "function";

  if (!carries) {
    return new MigrationError(
      "no_migration",
      file,
      `${file} default-exports a function, and a migration is an object with an up`,
    );
  }

  return new MigrationError(
    "no_migration",
    file,
    `${file} default-exports a class, and a migration is an instance of one: ` +
      `export default new ${classNameOf(named.name)}()`,
  );
}

/** classNameOf names the class an author should instantiate, falling back when it is anonymous. */
function classNameOf(name: unknown): string {
  return typeof name === "string" && name !== "" ? name : "Migration";
}

/** byVersion orders what was read the way the engine sorts it, which is by the timestamp. */
function byVersion(a: Loaded, b: Loaded): number {
  if (a.version === b.version) {
    return 0;
  }

  return a.version < b.version ? -1 : 1;
}

/** joined reports every problem found, since one complaint would hide the rest. */
function joined(problems: readonly Error[]): Error {
  const first = problems[0];

  if (problems.length === 1 && first !== undefined) {
    return first;
  }

  return new AggregateError(problems, problems.map((problem) => problem.message).join("; "));
}

/** errorOf renders whatever was thrown as an error, since JavaScript permits throwing anything. */
function errorOf(thrown: unknown): Error {
  return thrown instanceof Error ? thrown : new Error(String(thrown));
}

/** describeType names what a value is, for a message about what it should have been. */
function describeType(value: unknown): string {
  if (value === null) {
    return "null";
  }

  if (value === undefined) {
    return "nothing";
  }

  if (Array.isArray(value)) {
    return "an array";
  }

  return typeof value === "object" ? "an object" : `a ${typeof value}`;
}

/** listed renders a set of member names the way a sentence carries them. */
function listed(members: readonly string[]): string {
  if (members.length < 2) {
    return members.join("");
  }

  return `${members.slice(0, -1).join(", ")} and ${members[members.length - 1]}`;
}
