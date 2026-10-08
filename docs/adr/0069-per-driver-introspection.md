# 0069. Per-driver introspection, and what a live schema is allowed to refuse

## Status

Accepted

## Date

2026-10-05

## Context

`schema.Snapshot` has existed since M11-T1 and nothing produced one. [ADR 0011](0011-driver-contract.md)
recorded `Conn.Introspect(ctx) (*schema.Snapshot, error)` nine milestones ago and deliberately did
not add it, because *"a method whose only possible implementation returns a plausible empty value is
worse than a missing one"* — three real implementations were M11-T2's own done-when.

Five things were waiting: M11-T3 (the differ), M11-T6 (the neutral round trip), M10-T3 (reference
integrity), and **[ADR 0008](0008-reverse-operations.md)'s `from` trust problem** — *"nothing
verifies that a recorded prior definition matches what was actually there; a producer that fills it
in wrongly produces a reverse that restores the wrong schema."*

### What was measured first

Throwaway probes against PostgreSQL 16 and 17, MySQL 8.0, MariaDB 11.4 and SQLite, each building a
schema through the operations migr compiles and then asking the catalog what it holds. Nothing from
them ships. The measurements that decided code:

**The catalog does not echo the compiler, and the reverse map is keyed on the catalog.** PostgreSQL
rewrites four of eighteen (`varchar(255)` reads back as `character varying(255)`); `pragma_table_xinfo`
uppercases `text`, `integer`, `real` and `blob` and leaves every other spelling verbatim; MariaDB
appends display widths where MySQL 8.0 strips them server-side.

**MariaDB cannot tell an omitted referential action from an explicit `RESTRICT`.** Measured on 11.4:
an omitted clause reads back as `RESTRICT`, an explicit `NO ACTION` reads back as `NO ACTION`, and an
explicit `RESTRICT` reads back as `RESTRICT` — and that variant's own `SHOW CREATE` *drops* an
explicit `RESTRICT` while keeping an explicit `NO ACTION`. The distinction is not in the catalog.
MySQL 8.0 is the mirror image: an omitted clause reads back as `NO ACTION`.

**MariaDB has no `json` type, and the check it attaches is what tells one from a `text`.** A `json`
column is a `longtext` plus a check named after the column whose clause is exactly
``json_valid(`col`)``, which is also the shape a *hand-written* check on that variant takes, because
MariaDB names a column's own check after the column.

**Three catalog columns do not exist on one variant.** `CHECK_CONSTRAINTS.TABLE_NAME` and
`STATISTICS.EXPRESSION` are MySQL's alone; `TABLE_CONSTRAINTS.ENFORCED` likewise. Each is error 1054
on the other.

**Joining `pg_constraint` on `conindid` alone double-counts.** A foreign key's `conindid` names the
index on the *referenced* side, so the parent's primary-key index comes back twice. `generate_subscripts`
over `indkey` is **0-based**, int2vector's lower bound being zero.

**SQLite records several things in exactly one place: the `CREATE TABLE` statement.** A check
expression has no catalog at all; a foreign key's name is not in `pragma_foreign_key_list`;
`DEFERRABLE` is in neither. `pragma_table_info` omits a generated column entirely. And a table-level
`PRIMARY KEY (id)` over an `integer` column creates no index and no `index_list` row, because it is
the rowid.

**Neither of the two gaps M3-T7 and M9-T7 pinned is reachable from rollback time.** This is the
measurement that reshaped the task. A reversed `drop_column` does not bring back the index the server
dropped with the column — but by rollback time that index is gone, and the recorded `from` never held
it. A reversed `add_primary_key` does not undo the `NOT NULL` the key implied — and `ir.AddPrimaryKey`
carries no `from` at all, an add being inverted by name. Comparing a recorded prior state against the
live schema *at rollback time*, which is what ADR 0008's note proposed, catches neither.

## Decision Drivers

- M11-T2's done-when: *"PostgreSQL, SQLite, and MySQL each produce a snapshot, and normalization
  makes equivalent schemas compare equal across engines despite differing native type names."*
- ADR 0065, A1: a guess never equals a fact, and a caller that must act asks `Exact()` and refuses.
- ADR 0065, B: a definition the operations cannot express is recorded, marked, and never acted on.
- CLAUDE.md §11: a database difference belongs in the capability system, not in an
  `if driver == "mysql"` branch in the engine.
- Invariant #8: planning paths are deterministic.
- CLAUDE.md §11: *"the framework's credibility is rollback correctness… favor the safer path: fail
  early, hold the lock, refuse the ambiguous operation."*

## Considered Options

### A. How a unique thing is reported

- **A1: every unique index or constraint is a `schema.Unique`, and no driver ever sets
  `Index.Unique`** ✔. One rule on three engines, so the cross-engine claim holds by construction —
  and it is the only vocabulary the operations have: `ir.AddIndex` has no `Unique` field, so a differ
  could not act on anything else. The index behind a primary key or a unique constraint is not
  reported as an index of its own, nor is the one MySQL creates for a foreign key over an unindexed
  column.
- A2: faithful where the engine can tell — PostgreSQL distinguishes a `UNIQUE` constraint from a bare
  `CREATE UNIQUE INDEX`, so report the latter as `Index{Unique: true}` and mark it `Beyond`.
  **Rejected**: it buys a distinction no operation can express, at the cost of making one engine's
  snapshot a different shape from the other two — which is the thing this task exists to prevent.

### B. A constraint the server attached by itself

- **B1: absorb it into the element that caused it, matched against this driver's own rendering** ✔.
  SQLite's unnamed `CHECK ("c" IN (…))` resolves that `text` column to `enum(…)`; MariaDB's
  ``json_valid(`c`)``, named after the column, resolves a `longtext` to `json`. Neither is reported as
  a check. The second is also what makes a `json` column compare equal between the two MySQL variants
  — a `longtext` *without* the check is plain `text`.
- B2: suppress the artifact and leave the column as the engine spelled it. **Rejected on the
  done-when**: an `enum` or a `json` column would then never compare equal across engines, and
  M11-T6's empty diff would be unreachable for either.
- B3: report it as the constraint it is. **Rejected**, and the task's own text says why: a naive
  snapshot reports it as real and a differ then proposes dropping it.

**What bounds B1 is a measurement rather than a judgment.** A check byte-identical to what
`labelCheck` renders is indistinguishable from an enum — measured, `CHECK ("d" IN ('a', 'b'))`
hand-written is absorbed and `CHECK (c IN ('a','b'))` is not. So the absorbed type stays **ambiguous**
and nothing acts on it without asking `Exact()` first.

### C. Normalizing a default, and what stays engine text

- **C1: normalize into the vocabulary where the vocabulary can hold it, and keep engine text where it
  cannot** ✔. A literal's quotes and PostgreSQL's trailing cast are stripped, so `'x'::character
  varying`, `'x'`, `x` and `'x'` all read back as the literal `x`; each engine's spelling of the four
  neutral functions maps back to the function; `nextval(…)` and a generated expression are
  `ColumnDefault.Expression`, which compares as the text it is. Display widths are stripped to MySQL
  8.0's own spelling, which is what keeps an `int32` column comparable between the two variants.
- **MariaDB's `RESTRICT` reads as `no_action`, and MySQL's reads as `restrict`.** That is the one
  variant-gated reading in the three drivers, and it is what lets a key with no actions declared
  compare equal across all four servers. The cost is that an explicitly declared `RESTRICT` on
  MariaDB reads as `no_action` — a distinction that server does not record, and the two are
  behaviourally identical on both engines.
- C2: compare a check expression and a view body too. **Rejected on measurement**: ADR 0065 already
  found both are rewritten by the server, so comparing either would refuse every correct rollback on
  PostgreSQL and MySQL. They are carried and never compared across engines.

### D. What a snapshot does not hold

- **D1: not migr's own history table, and not the engine's own objects** ✔. The driver knows the
  table from `Options.Namespace()`, so it excludes it and everything attached to it; SQLite also
  excludes `sqlite_sequence` and `sqlite_stat*`. Including it would have every `migr diff` propose
  dropping the migration history, and would make M11-T6's empty diff unreachable.
- D2: take an exclusion as a parameter, the way `pgtest.Digest` did. **Rejected**: ADR 0011 settled
  the signature, and a caller that could forget is a caller that will. M11-T6 found that exact
  caller: `cmd/migr`'s PostgreSQL digest excluded nothing and got away with it only because the
  history table was in both compared moments (ADR 0074, B).

### E. What closes ADR 0008's trust problem

- **E1: one pure function, two senses, read before anything compiles** ✔. `engine.Unrestorable(ops,
  live)` compares what the IR records against what the snapshot holds and reports **stated**
  disagreements — a recorded `from` the live object contradicts, or an operation naming something that
  is not in the state it needs — and **implied** ones, which are what the live schema shows an
  operation will change that no payload records at all.
- E2: compare at rollback time, which is what ADR 0008's note proposed. **Rejected on measurement**:
  neither pinned case is reachable from there, as the Context records. The stated half is read in both
  directions; the implied half can only be read before the forward operation runs.
- E3: widen the `from` payloads. **Rejected**, and the task text says why: `ir.Column` has no field
  for an index and `ir.TableDef` none for a constraint, so this would be an IR change under
  invariant #5 — and `ir.AddPrimaryKey` has no payload at all.

**The implied half is three operations, each measured.** A dropped column takes every index,
constraint and reference over it; a dropped table takes everything `ir.TableDef` cannot hold; and a
primary key over a nullable column implies a `NOT NULL` that dropping the key does not undo. Each
detail names the remedy, and each remedy is an operation the author adds rather than a flag — drop the
index explicitly, or set the column `NOT NULL` with an explicit `alter_column`. Both then round-trip
on every engine, which `cmd/migr` asserts end to end.

**MySQL's own residue is deliberately not here.** The index it creates for a foreign key over an
unindexed column is something a server adds rather than something the operation set cannot record, so
refusing it would put an engine difference inside the engine. Introspection suppresses it instead, and
M9-T7's assertion about it stays pinned.

### F. What permits an implied disagreement

- **F1: an authored down, asked for lazily** ✔. migr guarantees what *it* derives, and an authored
  down is the author's own instruction — which is §7's reason for judging only the up half of what a
  run destroys, and the reason a rollback is judged on its stated half alone. The source is asked what
  the migration undoes only once a finding appears, so a migration with none pays no extra round trip.
- F2: refuse always, with no opt-out. **Rejected**: an authored down that recreates the index is
  refused anyway, for a loss it already handles.
- F3: a new `validation.allow_implicit` list, ADR 0063's shape. **Rejected**: an authored down is
  already the human statement that list would be, and it is one migr can ask for rather than one a
  project has to maintain.
- F4: a `warn`-level lint rule. **Rejected on M10-T4's own argument**: a warning that can be ignored
  is not a safety mechanism, and `migr up` never reads a rule.

### G. Where the check is read, and where it is not

- **G1: the run and the preview, and not `migr validate`** ✔. `migr up` reads it per migration,
  immediately before that migration is compiled, which is the only moment the live schema is the one
  it will run against; `migr down` reads the stated half immediately before each inverse runs, for the
  same reason. `migr plan` marks rather than refuses (invariant #7) and judges **the first queued
  migration only**, since a preview applies nothing and every later migration would be judged against
  a schema its predecessors will have moved.
- G2: a seventh `migr validate --connect` check. **Rejected on [ADR 0064](0064-capability-preflight.md),
  C2**: a check reading the live catalog calls a correct migration broken whenever the migration that
  creates its table has not run yet, and the projection that would fix that has no task. The run has
  no such problem, because by then the predecessors have run.
- **Exit 1, and no new code.** ADR 0029's rule decides it: the same command line succeeds against a
  database that does not contradict the migration, so the world is what is not ready.

### H. What is judged, and what is deliberately not

- **H1: an operation is judged only where nothing earlier in the same list touched what it names, and
  nothing at all is judged after a raw statement** ✔. Every judgment compares against the live schema
  and an earlier operation has already moved it, so a migration that creates a table and then indexes
  it would otherwise be refused for indexing a table that "does not exist". A raw statement can move
  anything, so nothing after one can be judged at all.
- H2: fold the earlier operations onto the snapshot and judge everything. **Rejected as another
  task's**: that fold is IR → snapshot, which M10-T3 already recorded as owned by nothing in M11.
  Missing a disagreement is strictly better than refusing a correct migration.
- **A column's type is compared only where the catalog answered exactly**, which is ADR 0065 A1
  applied: SQLite's `integer` is a guess, and a guess must not refuse anything.

### I. Making a driver's type map injective instead of carrying ambiguity

- **I1: leave the three ambiguities where ADR 0065 put them** ✔.
- I2: change what SQLite's compiler declares, so its map becomes injective. **Rejected on
  measurement**: `json` and `enum` as declared type names give those columns NUMERIC affinity, which
  silently coerces `'123'` to an integer; and `smallint`/`int`/`bigint` for the three integer widths
  would stop `INTEGER PRIMARY KEY` being a rowid alias. It trades a storage-behaviour change for a
  comparison convenience.

### J. Where the cross-engine claim is asserted

- **J1: one shared suite, one expected snapshot, every driver held to it** ✔.
  `test/conformance/driver`'s `Snapshot` applies a fixture through `Compile`/`Exec` — composing no SQL
  of its own — reads it back through `Introspect`, and compares. It is stronger than comparing engines
  pairwise, and it needs no second connection, so SQLite runs it **untagged** (ADR 0050) while
  PostgreSQL and MySQL run it under `integration` against all four servers.
- J2: one cross-engine test in `cmd/migr` opening three drivers at once. **Rejected**: only
  `cmd/migr` may name a concrete driver, so SQLite's third of the claim would then need Docker, which
  ADR 0050 says it must not.
- **Column types are compared on the logical type alone**, by a transform the suite owns rather than
  the model: ADR 0065 is deliberate that a guess never equals a fact, and what is being compared here
  is the logical type each engine resolved.
- **Coverage is derived, not listed.** For every `schema.Kind`, the fixture either carries it or
  `inexactTypes` states which catalog cannot answer the question — so a nineteenth logical type fails
  the suite until somebody decides which half it is in. Seven are stated: the two narrower integers
  and `float32` (SQLite's best answer is the wider one), `json` (SQLite declares it as `text`),
  `timestamptz` (MySQL declares it as `datetime(6)`), `enum` (PostgreSQL refuses it) and `array`
  (MySQL and SQLite refuse it).
- **The comparison is scoped to the suite's own prefix**, because one of the three engines gives every
  test in a package the same database and a table some other case left behind would otherwise fail
  this suite for a reason that says nothing about introspection.

## Decision Outcome

| Question | Decision |
|---|---|
| A | every unique element is a `schema.Unique`; no driver sets `Index.Unique` |
| B | a server's own artifact is absorbed into the element that caused it, matched against this driver's rendering |
| C | defaults and actions normalize into the vocabulary; a check, a view body and an expression default stay engine text |
| D | a snapshot never holds migr's history table or the engine's own objects |
| E | `engine.Unrestorable` reads a snapshot before anything compiles, in two senses |
| F | the implied half is permitted by an authored down, asked for lazily |
| G | the run refuses, the preview marks the first migration, `migr validate` says nothing; exit 1 |
| H | an operation is judged only against state nothing earlier in its list moved |
| I | the three ambiguities stay; no compiler's native map changes |
| J | one shared suite and one expected snapshot, SQLite untagged |

## Consequences

### Positive

- `Conn.Introspect` exists on the contract with three real implementations, which is what M11-T3,
  M11-T6 and M10-T3 were each waiting for.
- Equivalent schemas compare equal across four servers, asserted against one expected value rather
  than pairwise, with one third of that claim inside `make check`.
- ADR 0008's trust problem is closed for the nine operations that carry a `from` describing a live
  object, and ADR 0022's sharpest hazard — a derived inverse of an up that emitted differently the
  second time — now lands as an operation naming something already present or already gone.
- Both gaps M3-T7 and M9-T7 pinned are closed by refusal rather than by restoration, and both
  remedies are asserted to round-trip against a real database.
- `migr plan` says what `migr up` will refuse and exits 0, which is invariant #7's rule applied
  without exception.

### Negative

- **A snapshot of a large schema is five queries and no cache.** Nothing streams, and `migr up` reads
  one per migration. That is cheap beside the DDL it precedes and is not free.
- **An explicitly declared `ON UPDATE RESTRICT` on MariaDB reads back as `no_action`**, which a
  round trip of that key reports as a difference. The server does not record the distinction.
- **A view's body round-trips on no engine**, PostgreSQL and MySQL both re-rendering it, so an
  empty diff has to treat a view's query as engine text or compare it another way. M11-T5 did the
  latter, in `fold.Comparable` (ADR 0073); M11-T6 then found the same rewriting one level down, in
  SQLite's rebuild re-wording a whole `CREATE TABLE` (ADR 0074, C).
- **The implied half refuses a migration an authored down may have handled** whenever that down
  cannot be read — which is every migration whose source will not start. The refusal names the
  remedy, and the remedy is correct on every engine, so the cost is an operation rather than a flag.
- **`migr down` of a multi-migration rollback is judged per migration rather than up front**, so a
  stated disagreement in the third inverse surfaces after the first two have run. ADR 0022's up-front
  rule governs refusals knowable in advance, and this one is not: the schema each inverse runs
  against is the one its predecessors leave. What it leaves is a recorded partial rollback rather than
  a database between states.

### Neutral

- Every driver's `Introspect` reads through its own session runner rather than through `Conn.Query`,
  so a snapshot can be taken inside an open transaction as well as outside one — asserted on all
  three engines, since the gate reads one before a transaction and a resume reads one inside.
- `driver.Offline.Introspect` refuses like every other call needing a server, which is what
  `migr validate` would meet if anything ever asked it.
- Eight test doubles gained a one-line refusal. Two of them answer **no snapshot at all**, and
  `Unrestorable` judges nothing against one: those fixtures stage index names against a table they
  never create, so no single snapshot could satisfy both an apply that needs the name free and the
  rollback of that same apply that needs it taken.

## Notes

**A defect this task's own end-to-end suite found, and did not fix.** On SQLite, an emulated operation
is expanded from the catalog as it was before *any* operation in the same compilation ran — because
ADR 0022 compiles everything before running anything and [ADR 0053](0053-expanding-an-operation-against-a-session.md)
compiles an emulated operation against the database. Measured twice: a migration whose up is
`alter_column … NOT NULL` then `add_primary_key` loses the `NOT NULL`, because the second rebuild
re-renders the table from the pre-alter catalog; and `migr down` over two emulated migrations
re-creates what the newer inverse removed. Neither is reachable with one emulated operation per
migration, which is why M8-T6's fixtures never met it. The fix is to expand immediately before each
operation runs, which changes what ADR 0022 says about compiling a queue in advance — an ADR's worth
of decision, so a task of its own.

**What would force a snapshot to carry a version** has not changed: ADR 0065 names it, and nothing
here crosses a boundary. A snapshot is still produced by a driver, read by the engine in one process,
hashed by nothing and persisted nowhere.
