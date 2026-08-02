# 0008. Every operation declares an inverse or a reason it has none

## Status

Accepted

## Date

2026-07-27

## Context

Rolling a migration back means running the inverse of what it did, in reverse order. For that to
be mechanical rather than hand-written, every operation has to answer one question: what undoes
you?

Most answers are obvious. `add_column` is undone by `drop_column`; `rename_table` is undone by
itself with the arguments swapped. Two classes are not.

**Operations that destroy information they do not record.** `drop_column` names a table and a
column, not the column's type, nullability, or default. Nothing in the operation is enough to put
it back.

**Operations with no inverse in the vocabulary.** Working through the mapping surfaced a gap in
the operation set defined by [ADR 0006](0006-ir-operation-shape.md): five of eight subjects had
matching add and drop operations, and three did not.

| Subject | add | drop |
|---|---|---|
| table, column, index, foreign key, view | ✓ | ✓ |
| **unique, check, primary key** | ✓ | **absent** |

The rollback executor halts an entire batch when it meets an irreversible operation, before
running anything. So leaving that gap would mean a migration adding a unique constraint could
never be rolled back — and would block every migration batched with it — for a change that is one
`DROP CONSTRAINT` away from trivial.

## Decision Drivers

- **Totality.** Every operation must answer, and adding a new one must not be able to skip the
  question.
- **A reason beats a category.** "Irreversible" alone tells an operator nothing about whether it
  was avoidable.
- **Never guess.** A reverse that restores the wrong definition is worse than one that refuses,
  because it fails silently and at the moment of recovery.
- **Cost of changing the operation set.** It only grows more expensive: each driver must compile
  every operation, and none exist yet.

## Considered Options

### Question A: The three missing drop operations

#### Option A1: Add `drop_unique`, `drop_check`, `drop_primary_key`

**Pros**

- Symmetric with `drop_index` and `drop_foreign_key`, which already exist; every constraint kind
  then reads the same way.
- Keeps one schema fact to one operation.
- No driver implements `Compile` yet, so the five-part rule for adding an operation costs nothing
  today. It will never be cheaper.

**Cons**

- The v1 set grows from seventeen operations to twenty, and the specification grows with it.

#### Option A2: One generic `drop_constraint`

**Pros**

- A single addition instead of three.

**Cons**

- `drop_foreign_key` already exists, so dropping a foreign key would have two spellings — the
  thing ADR 0006 was written to prevent. Making it consistent would mean removing
  `drop_foreign_key` as well, a larger change than adding three operations.

**Rejected because:** it trades three narrow operations for one broad one plus a contradiction of
the principle the operation set is built on.

#### Option A3: Declare the three irreversible

**Pros**

- No change to the operation set.

**Cons**

- Adding a unique constraint would permanently block rollback for its whole batch.

**Rejected because:** the operation is trivially reversible in every target engine. Declaring it
irreversible would be the framework reporting a limitation of its own vocabulary as a property of
the database, which is exactly the kind of dishonesty an explicit `Unsupported` is supposed to
prevent elsewhere.

### Question B: Operations that destroy information

#### Option B1: An optional `from` payload

Each such operation gains an optional field holding what it replaced. Supplied, the operation
inverts exactly; absent, it is irreversible with a reason naming the missing field.

**Pros**

- A schema differ knows both sides of every change and can always populate it, so generated
  migrations are fully reversible.
- A hand-written migration that cannot know the live schema simply says so, rather than guessing.
- The reason is actionable: it names the field that would fix it.

**Cons**

- Widens ten operations, and the payload can be wrong if a producer fills it in carelessly — a
  reverse that restores a definition that was never there.

#### Option B2: No prior state; these operations are irreversible

**Pros**

- The smallest surface. An operation never claims to know something it was not told.

**Cons**

- Rollback would halt on most real migrations, pushing every `Down` back to hand-written SQL.

**Rejected because:** it gives up automatic rollback for the common case to avoid a risk that
belongs to the producer of the operation, not to its shape. Migrations may still declare
themselves irreversible; this only removes the cases where they need not.

#### Option B3: Mandatory prior state

**Pros**

- Total reversibility; nothing but `raw` would be irreversible.

**Cons**

- An SDK writing a migration by hand does not know the live schema and cannot fill it in
  honestly. A required field gets filled with a guess.

**Rejected because:** a guessed prior definition is worse than a missing one. It converts a loud
failure at rollback planning into a silent restoration of the wrong schema.

### Question C: Where the inverse is declared

#### Option C1: A `Reverse` method on the sealed operation interface

**Pros**

- A new operation cannot compile without answering, so totality is structural rather than
  something a test hopes to catch.
- Sits beside the destructiveness verdict, which is decided the same way for the same reason.

**Cons**

- The mapping is spread across twenty methods rather than readable as one table.

#### Option C2: A package-level function with a type switch

**Pros**

- The whole mapping reads on one screen.

**Cons**

- A type switch is not checked for exhaustiveness, so a new operation falls through to a default
  case instead of failing to build.

**Rejected because:** the one property that matters here is totality, and this is the option that
does not enforce it.

## Decision Outcome

**A1, B1, and C1.** The operation set gains three drops. Operations that destroy information
carry an optional `from`. `Reverse` is a method on the sealed interface, returning either an
operation or a typed error carrying the reason.

What that commits to:

- **Reversing "set a default" restores "no default" as an explicit drop**, not as an omitted
  field. Leaving the field out means "do not change the default", which would leave the new
  default in place after a rollback — a silent partial restoration.
- **`create_view` with replacement enabled does not assume the view was new.** It may have
  overwritten an existing definition, so it inverts to a restoration when the prior query was
  recorded, and is irreversible otherwise. Dropping the view would destroy something the
  migration never created.
- **`raw` is always irreversible.** Its effect is opaque to the framework by construction.
- **Reversing a list fails whole.** The list is inverted and re-ordered only if every operation in
  it can be inverted, so the rollback executor refuses before acting rather than stopping halfway.

## Consequences

### Positive

- Rollback is mechanical for every operation a schema differ can generate.
- An irreversible verdict names the field that would fix it, so it reads as a gap in the recorded
  data rather than a limitation of the tool.
- Every add/create operation now has a matching drop, which also makes the operation set easier to
  reason about than the asymmetric one it replaces.

### Negative

- **The `from` payload is trust, not proof.** Nothing verifies that a recorded prior definition
  matches what was actually there; a producer that fills it in wrongly produces a reverse that
  restores the wrong schema. Introspection can close this later by comparing against the live
  database before a rollback runs.
- Twenty operations rather than seventeen: more to compile in every driver, more golden files,
  more specification.
- Ten operations carry an optional field that most hand-written migrations will leave empty, so
  the common hand-written case remains irreversible.

### Neutral

- `add`→`drop` inversions need no prior state, because the drop identifies its target by name and
  the add's own payload is what re-creates it.
- The column-list definition is shared by index, unique, and primary key. All three are literally
  a list of columns; that is type reuse, not two spellings of one fact.

## Notes

Revisit the trust problem in `from` once introspection exists: a rollback could verify the
recorded prior definition against the live schema and refuse when they disagree, turning a silent
wrong restoration into a loud one.

M3-T7 measured that problem rather than only recording it. Every operation but `raw` now
round-trips against PostgreSQL 16 and 17 — applied, reversed, and compared against a catalog
digest — so a `from` payload that describes the wrong thing is caught rather than trusted. Two
gaps survive that no wider payload closes, and both are the same one this note is about:

- `drop_column` records a column, and PostgreSQL silently destroys any index on it. The reversal
  brings the column back without the index.
- `add_primary_key` inverts by name, and a name says nothing about the `NOT NULL` the constraint
  implied on a nullable column. `DROP CONSTRAINT` leaves it set.

Both are pinned by a test that asserts the gap, so closing either arrives as a failing assertion
rather than as a discovery.
