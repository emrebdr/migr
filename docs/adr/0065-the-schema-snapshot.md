# 0065. The schema snapshot: what a driver-neutral schema can honestly claim

## Status

Accepted

## Date

2026-10-03

## Context

`internal/schema` has held the logical type system since M1-T1, and its own package doc has said
*"snapshot model in M11-T1"* ever since. The signature was decided in
[ADR 0011](0011-driver-contract.md) and recorded rather than stubbed: `Conn.Introspect(ctx)
(*schema.Snapshot, error)`, because *"a method whose only possible implementation returns a
plausible empty value is worse than a missing one."*

Five things are waiting on the type, which is why this task is worth doing properly rather than
minimally:

- **M11-T2**, per-driver introspection, needs the type three drivers produce and a vocabulary
  normalization can land in;
- **M3-T7** and **M9-T7** each left two measured gaps *asserted as gaps* — an index silently
  dropped with its column, and the `NOT NULL` that `add_primary_key` implied — in tests that flip
  when a rollback can compare recorded prior state against the live schema;
- **M4-T6** left the sharpest one ([ADR 0022](0022-rollback-executor.md), G): deriving an inverse
  re-emits a migration's up against a database where it is already applied, and
  [ADR 0009](0009-migration-checksum.md) permits branching on live query results, so the inverse may
  undo something that never ran. The history records only a digest, so nothing can catch it
  afterwards;
- **M11-T3** needs something to diff and **M11-T6** needs something to compare;
- **M10-T3**, reference integrity, needs a state to project pending operations onto.

### What was measured first

Seven throwaway probes against PostgreSQL 16 and 17, MySQL 8.0, MariaDB 11.4 and SQLite, each
building a table through the operations migr actually compiles and then asking the catalog what it
holds. Nothing from them ships; this task adds no catalog SQL at all. Twelve findings, of which
these decided the design:

**The catalog does not echo the driver's own spelling.** PostgreSQL rewrites four of eighteen
(`varchar(255)` reads back as `character varying(255)`, `timestamp` as `timestamp without time
zone`), SQLite uppercases `text`, `integer`, `real` and `blob` while leaving `varchar(255)` and
`timestamptz` verbatim, and MariaDB appends display widths (`int(11)`, `bigint(20)`, `year(4)`). A
reverse map keyed on what the compiler emitted is wrong on every engine.

**No driver's type map is injective.** SQLite declares `int16`, `int32` and `int64` all as
`integer`, both floats as `real`, and `text`, `json` and `enum` all as `text`; MariaDB declares
`json` as `longtext`, which is also what `text` declares as; MySQL and MariaDB both declare
`timestamp` and `timestamptz` as `datetime(6)`. Only PostgreSQL distinguishes all eighteen, and only
because it refuses `enum` outright.

**Three things are engine text.** A view body asked for as `SELECT * FROM t` comes back as an
expanded column list with a trailing semicolon from PostgreSQL, as one line qualified with the
*database's own name* from MySQL, and verbatim from SQLite. A check expression `nullable_one > 0`
comes back parenthesised by MySQL, unparenthesised by MariaDB, parenthesised by PostgreSQL, verbatim
by SQLite. The literal default `x` comes back as `'x'::character varying`, `x`, `'x'` and `'x'`
respectively.

**Two names no two engines agree on.** A primary key is `<table>_pkey` on PostgreSQL, always
`PRIMARY` on MySQL whatever it was called, and nameless on SQLite when it is the rowid.
`pragma_foreign_key_list` reports a foreign key's referenced table, its columns and both actions,
and no name at all.

**Servers add things nobody asked for.** MySQL creates an index for a foreign key over an unindexed
column, named after the constraint. MariaDB attaches `json_valid()` as a CHECK to a `json` column.
This project's own SQLite driver renders an `enum` as an unnamed `CHECK ... IN (...)`. And
`AUTO_INCREMENT UNIQUE` produced a constraint named after its column.

**"Nothing specified" has two spellings.** For a foreign key declaring `ON DELETE CASCADE` only,
PostgreSQL and MySQL omit the update clause entirely and SQLite answers `NO ACTION` outright.

**A generated column hides.** SQLite's `pragma_table_info` omits one completely — it appears only in
`table_xinfo` with `hidden = 2` — and PostgreSQL reports it as an ordinary `integer` column whose
default is `(nullable_one * 2)`.

## Decision Drivers

- M11-T1's done-when: *"`internal/schema` represents tables, columns, indexes, constraints, and
  views in a driver-neutral, comparable, serializable form."*
- M11-T2's done-when: *"normalization makes equivalent schemas compare equal across engines despite
  differing native type names."*
- CLAUDE.md §11: *"Prefer an honest `Unsupported` over a clever emulation. A clear failure at
  validation time is worth more than a surprising table rebuild in production."*
- Invariant #8: planning paths are deterministic — no map-iteration order, sorted keys.
- Invariant #5: the IR and the protocol are versioned because they cross a boundary.
- ADR 0011: a decided signature is recorded rather than stubbed.

## Considered Options

### A. A column type the engine's own spelling cannot resolve

- **A1: three states — exact, ambiguous, unknown** ✔. `ColumnType` records the engine's own
  spelling always, the logical type where there is one, and which of the three answers it is. `Equal`
  compares two exact types on the logical type alone — which is what lets a PostgreSQL `integer`
  equal a MySQL `int` — and anything inexact on the certainty, the guess and the native spelling
  together, so **a guess never equals a fact**. A caller that must act asks `Exact()` and refuses.
  That is `capability.LockUnknown` ranking pessimistically and `validate`'s `Unchecked`, one
  subsystem over.
- A2: a logical type and nothing else, with introspection failing where it cannot resolve one.
  **Rejected on measurement rather than on taste**: MySQL could then not snapshot a `datetime(6)` at
  all, and SQLite could not snapshot an integer column, so two of the three drivers M11-T2 must
  deliver would be unimplementable. One engine-specific column anywhere would also make `migr diff`
  refuse the database rather than the column.
- A3: the native spelling alone, with comparison defined per engine. **Rejected**: it abandons the
  cross-engine half of M11-T2's done-when and the words *driver-neutral* in this task's own.
- A4: carry the full ambiguity set — `integer` is one of {int16, int32, int64}. **Rejected**:
  equality over overlapping sets is not transitive, so `Equal` would stop being an equivalence
  relation, and no caller needs more than *can I act on this*.

### B. A definition the v1 operation set cannot express

- **B1: record it, mark it, never act on it** ✔. Every element carries an optional `Unrepresented`,
  which holds text and nothing a caller could mistake for a payload. `Unrepresentable(Snapshot)
  []Finding` lists every one in canonical order, so the rule is reachable rather than remembered —
  the shape `engine.Destructive` and `engine.Unsupported` already have, one policy read by a report
  and a run alike. The marker counts in equality, so a round trip notices one appearing or vanishing.
- B2: omit what migr could not have created. **Rejected**: M11-T6's *"empty diff"* would then be
  partly a statement about what the snapshot declined to look at, and a differ would propose
  dropping and recreating a partial index as a plain one. The generated-column measurement makes it
  concrete: unmarked, a differ either re-adds a column that exists or tries to alter one no engine
  will let it change.
- B3: refuse to snapshot a database holding anything inexpressible. **Rejected** for A2's reason, one
  level up: it refuses real production databases, which are the only ones `migr diff` exists for.

### C. The model's shape, in a package that already has a stricter precedent

- **C1: validated structs for the aggregate, constructor-guarded values for the small invariants** ✔.
  `Snapshot`, `Table`, `Column` and the five constraint types have exported fields and a
  `Validate`, the shape `ir.TableDef`, `ir.ColumnsDef` and `ir.ForeignKeyDef` already take — three
  drivers build these row by row from a catalog in M11-T2 and every fixture constructs one by hand.
  `ColumnType` keeps `schema.Type`'s own shape, because its three states are an invariant and its
  zero value is not one of them.
- C2: unexported fields and constructors throughout, matching `schema.Type`. **Rejected**: an
  aggregate this size turns into a builder API that three drivers and every fixture must go through,
  which is why `ir`'s own payloads did not take that shape. What C2 buys — no defensive
  canonicalization in `Equal` — is bought instead by `Equal` canonicalizing, which costs one call.

### D. Where the shared vocabulary lives

- **D1: `Function` and `ReferentialAction` move to `internal/schema`, and `internal/ir` aliases
  them** ✔. `internal/ir` imports `internal/schema` for `schema.Type`, so this package can never
  import `ir`; the snapshot needs both vocabularies, and they name nothing engine-specific. Type
  aliases and constant re-exports keep all 108 existing `ir.Func*` and `ir.Action*` call sites
  compiling untouched, with one definition of each name.
- D2: a second set of constants in `schema`, held level with `ir`'s by a test — the shape
  `protocol.Mode` has against `engine.Mode`. **Rejected**: that pair is duplicated because it
  crosses a wire, and here there is no boundary to cross. A test holding two definitions level is
  worth paying when one of them must exist anyway.
- **D3: `ColumnDefault` is *not* `ir.Default`** ✔, which is the opposite call on a neighbouring
  question. `ir.Default` carries a literal or a neutral function and nothing else, because the IR
  never carries engine SQL. A catalog holds `nextval('users_id_seq')`, so the snapshot's default has
  a third state — an engine expression — and widening `ir.Default` to fit would have weakened an IR
  invariant to serve introspection.

### E. What comparison is defined over

- **E1: sets by name, sequences where the operation set can express order** ✔. Tables, views,
  columns, indexes and constraints are sets, sorted by name; the column lists inside a key, an index
  or a foreign key keep their order, because that order is part of the object. The line is the IR's
  own: M9-T7 measured that a reversed `drop_column` appends the column and no v1 operation can put
  it back where it was, MySQL's `AFTER` having no field in the operation set. A column's position is
  a fact nothing could act on.
- E2: compare column position too, since two engines report it. **Rejected**: it would make every
  reversed `drop_column` a difference, which is the gap M9-T7 documented rather than a change
  anything can emit.
- **E3: the two names no two engines agree on are recorded and not compared** ✔. A primary key
  compares by its columns and a foreign key by its shape; both names are still carried, because
  `drop_primary_key` and `drop_foreign_key` need one. `ir.AddPrimaryKey` had already made `Name`
  optional for the MySQL half of this.
- **E4: a view body, a check expression and a default expression compare as the text they are** ✔,
  and never across kinds — an expression default never equals a literal or a function one. Nothing
  here can know that `curdate()` and `CURRENT_DATE` agree, and claiming otherwise is what a snapshot
  must not do.
- **E5: an unnamed referential action resolves to `no_action`** ✔. That is a spelling rather than a
  fact, and two spellings of one default would make every foreign key read from SQLite differ from
  the same key read from PostgreSQL.

### F. Serializable, and whether that means versioned

- **F1: a strict JSON codec and no version field** ✔. `Marshal` emits the canonical order and
  refuses an invalid snapshot; `Unmarshal` refuses a member this build does not know, the strictness
  `ir.Unmarshal` has. No `schemaVersion`: invariant #5 governs formats that cross a boundary, and a
  snapshot is produced by a driver and read by the engine in one process, hashed by nothing,
  persisted nowhere. What would force one is named in the package doc — a snapshot on the runtime
  protocol, or one stored in history as a baseline.
- F2: version it now, cheaply. **Rejected**: it is a version nothing reads, and adding the member
  later to a document nothing persists is not a breaking change.
- F3: copy `ir.Marshal`'s U+2028/U+2029 rewriting (M7-T9). **Rejected**: that exists so two SDKs in
  two languages emit identical bytes, and this is produced by Go and read by Go.

### G. What this task does not add

- **G1: the type only; no `Conn.Introspect`** ✔. Three real implementations *are* M11-T2's done-when,
  and ADR 0011's no-stub rule forbids the alternative. `internal/driver/doc.go` and CLAUDE.md §4.2
  both tagged the method M11-T1; both are corrected to say the type is M11-T1 and the method M11-T2.
- G2: add the method now, returning a not-implemented error from all three drivers. **Rejected**:
  that is the plausible empty value ADR 0011 names, and the driver conformance suite's *"no third
  possibility"* rule exists to forbid exactly it.

## Decision Outcome

`internal/schema` gains `Snapshot`, `Table`, `View`, `Column`, `ColumnType`, `ColumnDefault`,
`PrimaryKey`, `Index`, `Unique`, `Check`, `ForeignKey`, `Unrepresented`, `Finding`,
`Unrepresentable`, `Marshal` and `Unmarshal`, plus the two vocabularies `internal/ir` now aliases.
`Canonical` defines the order, `Equal` defines comparison, and `Validate` refuses a snapshot no
engine could have produced.

A table with no columns is **valid** here, where `ir.TableDef` requires at least one: PostgreSQL
permits a relation with zero columns and `drop_column` can reach that state, which is why a driver
lists relations as well as describing them — measured at M11-T6, where `Introspect` reports an
emptied table as a `Table` with no columns, so `Equal` tells it from a dropped one. A snapshot
reports what the engine holds.

Coverage is mechanical and derived from `ir.Kinds()`. One test declares, for every one of the twenty
operations, the element it changes and a before/after pair of snapshots, and asserts the model can
tell them apart — so an operation whose effect a snapshot could not see fails the suite, and a
twenty-first operation fails it the day it is added. `raw` is the one kind with no pair, and the
test asserts that it is the only one: its effect is opaque to the framework, which is also why it
has no inverse.

## Consequences

### Positive

- The signature ADR 0011 recorded four milestones ago now has a type behind it, and three drivers can
  be written against it independently in M11-T2.
- A verdict a snapshot cannot support is unspellable rather than merely discouraged: an inexact type
  cannot compare equal to an exact one, and an element beyond the operation set cannot be acted on
  without reading past a marker that `Unrepresentable` will hand you.
- One definition of the neutral default functions and referential actions, for the first time, with
  no call-site churn.
- The twelve measurements are written down in the package doc where M11-T2 will read them, including
  the three that are a driver's problem rather than the model's.

### Negative

- **The unique-constraint / unique-index distinction is not expressible as inexact.** `Index` and
  `Unique` are separate elements, as the operations are, and two engines report one object: MySQL
  implements a unique constraint as a `UNIQUE KEY`, and this project's SQLite driver compiles
  `add_unique` to `CREATE UNIQUE INDEX` with `origin = 'c'`. So introspection there must choose
  which element to report and the model cannot mark that choice as uncertain the way it can mark a
  type. Collapsing them into one element was the alternative and is worse: a differ must emit one of
  two operations, and it would then be guessing at diff time, where nothing knows, instead of at
  introspection time, where an engine sometimes does. M11-T2 owes a per-driver answer.
- **Two fields are recorded and ignored by equality** — a primary key's name and a foreign key's.
  That is the shape a hazard takes: two snapshots can compare equal while differing in a field a
  reader can see. The measurement is what justifies it, and it is stated in the package doc beside
  each field rather than left to be discovered.
- **An ambiguous type's equality is weaker than it looks.** Two MySQL snapshots of one schema agree,
  because the native spelling is the same; a MySQL snapshot and a MariaDB one disagree about any
  `int32` column, because one says `int` and the other `int(11)`. That is honest and it is also a
  cross-engine comparison that fails for a reason the operator cannot act on. M11-T2 can narrow it by
  normalizing display widths before recording the spelling, and that is the right place for it.
- `Equal` canonicalizes defensively, so comparing two large snapshots allocates two sorted copies.
  Nothing on a hot path compares schemas.

### Neutral

- No new normative document. `docs/ir.md` and `docs/protocol.md` are normative because the IR and
  the protocol cross a boundary; this crosses none, so the ADR and the package doc carry it. F1 names
  what would change that.
- The projection this model will be folded forward by — IR applied to a snapshot, which M10-T3 needs
  and which is the opposite direction from M11-T3's differ — **has no task**. Recorded in TASKS.md
  under M10-T3 rather than invented here. **M11-T5 reached it first and scoped it in**: it is
  `internal/fold`, and [ADR 0073](0073-the-schema-projection.md) records what it does that no
  payload records, and why a projected column type is always exact.
- `internal/driver/sqlite`'s `tableDefinition` splitter (ADR 0054) is untouched. It preserves the
  *wording* of a `CREATE TABLE` in order to rebuild it, which is the opposite of what a snapshot
  does, so M11-T2 will not replace it with introspection.

## Notes

The probes are deleted. Their twelve findings live in this ADR's Context and in
`internal/schema/doc.go`; the three that belong to a driver are repeated in TASKS.md under M11-T2,
because that is where someone will be standing when they need them.
