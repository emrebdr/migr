// The window a migration acts through, as far as the contract is concerned.
//
// This file declares what one migration.execute carries: which migration is running, which way,
// whether it is a run or a question, and the signal that ends it. The schema builder is M7-T3 and
// the six host callbacks are M7-T4, so a migration written today typechecks and emits nothing.
//
// The AbortSignal rides here rather than in a second parameter, which is where the Go SDK puts its
// context. That is CLAUDE.md §8's rule -- idiomatic syntax per language, identical semantics -- and
// the facts beside it are on the Schema in Go too (ADR 0042).

/** Direction is which way a migration is being run. */
export type Direction = "up" | "down";

/** Mode is whether this call runs the migration or asks it what it would do, per docs/protocol.md §6. */
export type Mode = "apply" | "plan";

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
}
