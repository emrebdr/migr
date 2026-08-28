// The migration contract: one required member, and each optional part on its own.
//
// A migration is undone in exactly one of the three ways docs/protocol.md §7.4 names -- it writes a
// down, it declares it has none and says why, or it has neither and the engine derives the inverse.
// Those are three shapes here rather than three fields, so a migration that both writes a down and
// declares it has none does not compile. Go answers that at registration, because Go has no way to
// say it in a type (ADR 0037, A).
//
// Behaviour is a method and a fact is a property. `dependencies` is a readonly array rather than a
// method for one reason: ADR 0018 requires a declaration and forbids a computation, and a method
// invites the graph that would let migr plan and migr up order the same migrations differently.
//
// A .js or .mjs migration is loaded by the same runtime and checked by no compiler at all, so every
// rule stated here has a runtime twin in load.ts. The named-export style needs one regardless: no
// type constrains a module's exports against each other.

import type { Schema } from "./schema.ts";

/** Up performs the change, emitting operations through the Schema it is given. */
export type Up = (s: Schema) => void | Promise<void>;

/** Down undoes what up did, emitting the operations that put the schema back. */
export type Down = (s: Schema) => void | Promise<void>;

/** Validate reports what is wrong with this migration, reaching no database. */
export type Validate = () => void | Promise<void>;

/** Change is what every migration has, however it is undone. */
interface Change {
  /** up performs the change, and is the only member a migration must have. */
  up: Up;
  /** validate is the migration's own check, which migr validate runs and which emits nothing. */
  validate?: Validate;
  /** dependencies are the versions this migration must follow, declared and never computed. */
  dependencies?: readonly string[];
}

/** Undo is the three honest answers to how a migration is undone, as three shapes. */
type Undo =
  | { down: Down; irreversible?: never }
  | { irreversible: string; down?: never }
  | { down?: never; irreversible?: never };

/** Migration is one schema change, and up is the only member a migration must have. */
export type Migration = Change & Undo;

/** defineMigration returns the migration it is given, typed so every part is checked and inferred. */
export function defineMigration<const T extends Migration>(m: T): T {
  return m;
}

/** authored returns the down a migration writes itself, which no derived one replaces. */
export function authored(m: Migration): Down | undefined {
  return m.down;
}

/** declared returns the reason a migration gives for having no undo, if it gives one. */
export function declared(m: Migration): string | undefined {
  return m.irreversible;
}

/** dependencies returns what a migration declares it must follow, which is nothing by default. */
export function dependencies(m: Migration): readonly string[] {
  return m.dependencies ?? [];
}

/** check runs a migration's own validation, which a migration that has none passes. */
export async function check(m: Migration): Promise<void> {
  await m.validate?.();
}
