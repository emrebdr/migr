# 0013. Migration history is an append-only log, keyed by the run rather than by the row

## Status

Accepted

## Date

2026-07-28

## Context

A migration tool's history table is the only durable record of what it did. Everything the CLI
reports about a database — which migrations are applied, which are pending, whether one was edited
after the fact, whether a failed run left the schema in a state nobody can reason about — is read
back out of it. It is also the one table migr itself owns, so its shape is a compatibility surface
in the same way the IR is: changing it later means migrating every installation's history.

Three constraints were fixed before this decision and are not reopened here.

[ADR 0011](0011-driver-contract.md) recorded the accessor as `History() history.Store`, taking no
context and returning no error. A driver therefore builds the store inside `Open` and binds it to
the connection, and the store cannot fail to be constructed. It also recorded that
`internal/history` must never import `internal/driver`, because the contract names `Store` as a
return type and the other direction is an import cycle.

CLAUDE.md invariant #3 puts raw SQL only in driver packages, and states specifically that the
history table is created through driver-compiled IR operations. So the table's shape has to be
expressed as `ir.Operations` somewhere above the drivers.

CLAUDE.md invariant #8 forbids `time.Now`, enforced by `forbidigo` with no path exclusion — it
applies to test files too. No clock abstraction exists anywhere in the repository, and
`internal/config` is the only package that imports `time` at all.

What was open: the table's shape, how a run's outcome is spelled, who owns the schema, what
creates the table, and where the timestamp comes from.

## Decision Drivers

- `migr status` resolves four states — applied, pending, missing, drifted (M4-T3) — and
  `migr history` separately prints an execution log (M5-T3). Both read the same table.
- `migr down` with no arguments rolls back the last *batch*, not the last migration (M4-T8), so
  the order runs were applied in has to survive in the table.
- A driver with no transactional DDL marks a migration **dirty** with the index of the last
  successful statement, and the next `up` refuses to proceed until that is explicitly resolved
  (M4-T9). M9-T6 resumes from that index.
- A migration is recorded before its transaction commits (M4-T5), so on a transactional engine a
  crashed run leaves no row at all.
- Three drivers implement this, and a fourth is a v1 goal. Anything a driver decides for itself is
  something three drivers can decide differently.
- Invariant #6: applied migrations are immutable, and nothing silently rewrites a recorded value.

## Considered Options

### Question A: is the table a log of runs or a state of migrations?

**A1 — One mutable row per migration.** `up` inserts, `down` deletes, a failure updates the row in
place. What most migration tools do.

**Pros**

- `migr status` is a direct read with no folding.
- The table is small and stays small.

**Cons**

- `migr history` has no execution log to print, which is a command the CLI surface already
  promises.
- A rollback erases the evidence that it happened, and re-applying overwrites the record of the
  first attempt — so the table cannot answer "what did this database do last Tuesday", which is
  the question an operator asks after an incident.
- Updating a row in place is the shape invariant #6 exists to discourage, even where the invariant
  is literally about checksums.

**A2 — Append-only log of runs.** Every run appends a row, including rollbacks and failures.
Current state is folded from the log.

**Pros**

- `migr history` *is* the table.
- Nothing is ever updated or deleted, so no write can destroy an earlier fact.
- A rollback and a re-apply are two rows, which is what actually happened.

**Cons**

- `migr status` has to fold rather than read, and the fold is a piece of logic that can be wrong.
- The table grows with every run rather than with every migration.

### Question B: how is a run's outcome spelled?

**B1 — `success bool` plus an `error` column,** as M3-T6's task notes recorded before this
decision.

**Rejected because:** it cannot express dirty, and dirty is the distinction that matters most. A
failed run left the schema unchanged and the next `up` may proceed; a dirty one did not, and the
next `up` must refuse. Adding a second `dirty bool` alongside makes `success = true, dirty = true`
representable, which is the class of thing [ADR 0012](0012-capability-resolution.md) made
unrepresentable in the capability model rather than merely invalid.

**B2 — A three-value enum: applied, failed, dirty.** One column, one meaning, no combination that
means nothing.

**Pros**

- The operationally important difference is in the type rather than in a convention.
- A fourth outcome is a new constant, not a new column.

**Cons**

- It contradicts a column list already written down in M3-T6, which has to be corrected.
- "applied" now names both an outcome and one of M4-T3's four reconciled states.

### Question C: who owns the table's schema?

**C1 — Each driver writes its own `ir.CreateTable`.** The contract ships only the interface and
the record.

**Rejected because:** three drivers would produce three history tables with nothing keeping them
the same, and the table is the one thing a `migr` binary must be able to read regardless of which
driver wrote it. It also leaves M12-T1's second table, for seeds, with no shared definition.

**C2 — `internal/history` exports `Schema(table string) ir.Operations`.** Neutral operations,
compiled by whichever driver is connected.

**Pros**

- One definition, three compilers — which is the framework's own claim applied to itself.
- Satisfies invariant #3 exactly: the shape is neutral, the SQL is the driver's.
- The table name stays a parameter, so `history.table` remains configurable and seeds get a second
  table of the same shape for free.

**Cons**

- The history table is now bounded by what v1 IR can express (see Question D).

### Question D: what is the row's key?

**D1 — A surrogate auto-increment `id`,** as M3-T6's notes recorded.

**Rejected because it cannot be built.** `ir.Column` is name, type, nullability, and a default,
where a default is a literal or one of four neutral functions. There is no identity, serial, or
auto-increment concept in v1 IR. Adding one would be a wire-format change requiring its own ADR
and a version bump, and migr's own history table is a poor reason to reshape the IR.

**D2 — `applied_at` decides order.**

**Rejected because:** two rows can share a millisecond, and `migr down` reverses the last batch —
so a tie reverses two migrations in the wrong order, which is precisely the failure the rollback
path exists to prevent.

**D3 — `(batch, ordinal)`, both supplied by the executor.** A batch is one `migr` invocation; an
ordinal is the position within it.

**Pros**

- Needs no clock, no randomness, and no engine feature — it is expressible in v1 IR today.
- It is exactly the order a rollback reverses, stated directly rather than inferred from a
  timestamp.
- Both number from one, so the zero value is not a valid position and an unset field fails
  validation.

**Cons**

- The executor must carry a counter, where a database could have kept one.
- Two processes running concurrently could pick the same batch number — which the migration lock
  already prevents, but the table no longer prevents on its own.

### Question E: where does the timestamp come from?

**E1 — A `Clock` interface in `internal/history`,** injected into the store.

**Rejected because:** it would be the repository's first clock abstraction, built for exactly one
consumer, and the store is not where the instant originates. The executor already owns the run.

**E2 — `AppliedAt` and `Duration` are fields the caller fills in.** This package imports `time`
for the field types and never reads a clock.

**Pros**

- `internal/history` stays a pure contract, with nothing to inject and nothing to fake.
- `forbidigo`'s repo-wide ban needs no exemption, and tests use fixed instants — which invariant
  #8 would have required regardless.

**Cons**

- A caller can supply a wrong or absent timestamp; validation catches absent, not wrong.

### Question F: what creates the table?

**F1 — The engine creates it before the first store call.** `Store` keeps exactly the five methods
the task named.

**Rejected because:** the store would be usable only after a caller performed a setup step the
interface never declares, and every caller would have to know that independently.

**F2 — A sixth method, `Ensure(ctx) error`.** The store owns its own table.

`ir.CreateTable` has no `IfNotExists`, so a driver cannot make this idempotent through IR alone.
It checks existence with its own internal query — which the driver contract already permits for a
driver's own queries — and compiles `Schema(table)` only when the table is absent.

## Decision Outcome

**A2, B2, C2, D3, E2, and F2.**

What that commits to:

- **The history table is an append-only log of runs.** Nothing updates and nothing deletes. Current
  state is folded from the log by M4-T3, and `migr history` prints the log itself.
- **A run ends in exactly one of three outcomes,** and the three write methods each own one:
  `Record` writes applied, `MarkFailed` writes failed, `MarkDirty` writes dirty and takes the
  statement index that outcome requires. A caller cannot record a failure marked applied, and
  cannot mark a run dirty without saying where it stopped.
- **`LastStatement` is 1-based**, so zero unambiguously means no statement succeeded — and a run
  where none succeeded left the schema unchanged and is failed rather than dirty. Validation
  rejects the contradiction in both directions.
- **The row's key is `(batch, ordinal)`,** and both number from one.
- **The schema is `ir.Operations` owned by `internal/history`,** with the table name as a
  parameter. `direction` and `outcome` are string columns rather than enums, so a fourth outcome
  never requires an `ALTER` on migr's own history — the same reasoning that sizes `checksum` at
  `string(255)` rather than the 135 characters `migr1` happens to need.
- **This package reads no clock.** The instant and the duration are the caller's, and the two
  version fields are the build's, filled by `Stamp` and required by `Validate`.
- **A corrupt row is not drift.** A recorded value this build cannot read back — a checksum that
  will not parse being the case [ADR 0009](0009-migration-checksum.md) anticipated — is
  `CorruptError`, which unwraps to both its own sentinel and the underlying cause. Drift is exit
  code 4 and means someone edited a migration; an unreadable row means this table needs manual
  repair, and reporting the second as the first is how operators learn to ignore exit code 4.

## Consequences

### Positive

- Three drivers implement one table rather than three, and a fourth inherits it.
- The difference between a failure that changed nothing and one that changed something unknown is
  carried by the type, so M4-T9 and M9-T6 have something to read rather than a convention to
  honor.
- `migr history` and `migr status` read the same rows without either command constraining the
  other.
- Nothing in the write path can destroy a previously recorded fact, which is invariant #6's
  posture applied to the table rather than only to the checksum it holds.

### Negative

- **M3-T6's recorded column list is now wrong** and is corrected by this decision: `success`
  becomes `outcome`, `id` becomes `(batch, ordinal)`, and `version` and `last_statement` are added.
  Thirteen columns become fifteen.
- **Status is derived rather than read.** The fold from log to four states is real logic in M4-T3,
  and a bug there misreports a database rather than failing loudly.
- **The table grows without bound.** Nothing here prunes it, and nothing here decides whether
  pruning is ever allowed — which invariant #6 makes a genuinely open question rather than an
  oversight.
- **The executor carries the batch counter.** Allocating one is now engine-neutral work that M4-T5
  has to get right, including deciding what a batch number means across two databases configured
  in one `migr.yaml`.

### Neutral

- Adding `History()` completes `Conn`. Two deferred methods remain, `Introspect` (M11-T1) and
  `ServerVersion` (M3-T1), so ADR 0011's note that the driver package cannot be reviewed as
  finished still holds, for a shorter list.
- The record is a struct rather than an interface, so M13-T7's expand/contract gate and M12-T1's
  seed scoping are fields added later rather than a re-signing of every driver — the same argument
  ADR 0012 made for `capability.Set`.
- **The import rule ADR 0011 recorded as review-enforced is now mechanical, and adding nothing is
  what made it so.** Because `internal/driver` imports this package, importing it back is a cycle
  the compiler refuses; every package under `internal/driver/` reaches the contract, so every path
  into that subtree is the same cycle, and depguard's prefix deny covers the subtree besides. A
  test that parsed this package's own imports was written and then deleted: it could not fail.

## Notes

The store has no implementation in this decision. M3-T6 writes the first one, M8 and M9 the
others, and whether the contract is right is not known until at least two of them exist.
