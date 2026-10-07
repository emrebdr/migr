# 0070. The snapshot differ, and what it declines to propose

## Status

Accepted

## Date

2026-10-07

## Context

`internal/diff` has held nothing but a `doc.go` since M0. M11-T1 built the snapshot model and M11-T2
made all three drivers produce one; this is the other direction — **snapshot → IR** — and M11-T4
(rename detection), M11-T5 (`migr diff`) and M11-T6 (the neutral round trip) all wait on it.

M11-T3's done-when is *"diffing two snapshots emits a correct, ordered IR op list; the ordering
respects dependencies (create a table before its foreign keys)"*, with the task text naming the two
things that must be refused on: `schema.Unrepresentable`, because *"a differ that ignored the marker
would propose recreating a partial index as a plain one"*, and `ColumnType.Exact()`, because *"an
`alter_column` emitted off an ambiguous type is a rewrite proposed from a guess"*.

Nothing can fold IR *onto* a snapshot — [ADR 0065](0065-the-schema-snapshot.md)'s notes record that
projection as owned by no task — so there is no pure round trip available, and the only thing that
can judge *correct* is an engine. That is what decided where the proof lives (option J).

### What was measured first

Three throwaway probes. Nothing from them ships.

**A minimal delta is refused by both MySQL variants.** An `alter_column` changing only nullability on
a column that carries a default is refused by `internal/driver/mysql`, on 8.0 and 11.4 alike:
*"it carries a default this operation does not replace, and a redefinition drops every attribute it
does not restate"*. Restating the same default on the operation is accepted and preserves the value —
measured by reading the column back. That decided option D.

**A diff-generated list is not always a list `migr up` can run today.** `plan.Planner.Compile`
compiles every operation — `Expander.Expand` included — before any of them executes, and an emulated
operation is compiled against the database ([ADR 0053](0053-compiling-against-the-database.md)). So
on SQLite an `add_foreign_key` over a table an earlier `create_table` in the same list creates is
expanded against a catalog with no such table: *"cannot rebuild this table "extra": this database has
no such table, so there is no definition to rebuild it from"*. **This task's own done-when example
cannot run through `migr up` on SQLite until M11-T7**, which is recorded there rather than designed
around here — the operations and their order are right.

**The refusals read badly when the marker's own words are framed.** Driven against a real PostgreSQL
schema holding a `serial` column, a generated column, a per-column collation, a partial index, an
expression index and a `tsvector`, the first wording produced *"the schema holds it carries index
"legacy_lower_idx", whose key covers something that is not a column, which nothing here can
represent, which no operation can express"* — broken grammar and the same clause twice, every
driver's detail already ending in one. The detail is written to follow a colon, as
`schema.Finding.Describe` renders it, so it is passed through verbatim.

## Decision Drivers

- The marker and the certainty are the two things in the snapshot model whose whole purpose is to be
  refused on, and this is the first thing that reads either.
- Invariant #10: both directions, always. A generated migration that cannot be undone is one the
  project's own `migr validate` rejects.
- Invariant #8: planning paths are deterministic. A driver reports a schema in whatever order its
  catalog answers.
- CLAUDE.md §11: a database difference belongs in the capability system, not in a branch on a driver
  name. This package cannot name an engine.
- M11-T6's *empty diff* has to be a statement about the schema rather than about what the snapshot
  declined to look at.

## Considered Options

### A. What is matched, and how

- **A1: tables, columns and views by name; the five constraint kinds as set differences under the
  model's own `Equal`** ✔. A matched pair can be *changed* — `add_column`, `drop_column`,
  `alter_column` and `create_view … or_replace` all exist — while there is no `alter_index`, so a
  changed index is a drop and an add and so is a renamed one. One rule for five kinds, and no
  pairing heuristic to get wrong. It also inherits the model's two deliberate blind spots: `Equal`
  ignores a primary key's name and a foreign key's, because no two engines agree on one (ADR 0065),
  so a key whose name moved is no change at all — which is what stops a diff between two engines
  proposing to rebuild every key it can see.
- A2: match constraints by name and compare their fields. **Rejected**: it makes a renamed index a
  *modification* with no operation to express it, and it would propose rebuilding every foreign key
  whose name one engine assigned differently, which is the case ADR 0065 measured.
- A3: detect renames here. **Rejected**: that is M11-T4, and the task says it must be *confirmed
  interactively rather than guessed*. Until then a renamed table is a drop and a create, and the net
  is invariant #7 — `drop_table` and `drop_column` are destructive, so `migr up` refuses a generated
  migration that destroys what the project has not permitted.

### B. What a difference the operations cannot express gets

- **B1: declined in both directions, and reported** ✔. Emit nothing for that element, either way.
  The task's objection to recreating a partial index as a plain one applies just as well to
  *dropping* one: a `drop_index` carrying `from.columns` for a partial index recreates it plain at the
  next `migr down`, which is the same degradation arriving a milestone later. What B1 buys is a
  property worth stating — **every operation the differ emits carries a complete `from` payload**, so
  a migration `migr diff` writes is reversible by derivation and passes the project's own
  `migr validate`.
- B2: decline creates, emit drops with no `from`. **Rejected on merit rather than on taste**: it is
  honest about irreversibility where B1 is silent about the gap, but it writes a migration that fails
  `migr validate`'s `reverse` check — exit 2 — the moment it is generated. A tool whose output fails
  the project's own CI gate is worse than one that says what it left out.
- B3: emit the degraded form and warn. **Rejected**: that is the silent degradation
  [ADR 0065](0065-the-schema-snapshot.md) B1 exists to prevent, with a warning attached.
- A marker is read as a **lookup, not a report**: an element that carries one and is identical in both
  schemas produces no operation, so it produces no refusal either. That is what makes a database
  diffed against itself empty however much of it migr could not have created, and it keeps M11-T6's
  claim about the schema. A caller wanting the whole picture calls `schema.Unrepresentable`, which is
  exported for that.

### C. A type one side only guesses at

- **C1: compare the logical answer; refuse a change, allow a creation** ✔. Two guesses that agree
  propose nothing, the best answer available being that the column is unchanged. Two that disagree
  are refused, because the operation would carry a target type nothing verified and record a prior
  type that is itself a guess — ADR 0008's trust problem, written by migr rather than by an author.
  A *new* column is emitted off the logical answer however certain it is: there is nothing to compare
  it against, the guess is the only answer there is, and no data is at risk. The task's own wording
  draws that line, naming `alter_column` specifically. A type naming no logical type at all is the
  floor in both directions.
- C2: use `schema.ColumnType.Equal`. **Rejected on measurement**: that method is deliberate that a
  guess never equals a fact, so an exact `int64` and an ambiguous `int64 (integer)` are a difference —
  and on SQLite every integer and float column reads back ambiguous, so every one of them would be
  refused whenever the other side is a desired schema. `migr diff` would be useless on that engine.
  The transform is the differ's own, exactly as it is the snapshot conformance suite's own and for the
  same reason.
- C3: refuse a creation off a guess as well. **Rejected**: it refuses `migr diff` on an existing
  SQLite database outright, which is the only kind of database that command exists for.

### D. What an `alter_column` states

- **D1: the column's whole expressible state except its type, with the three treated differently** ✔.
  *Nullability* only when it changes, because restating an unchanged `NOT NULL` costs PostgreSQL a
  full scan to re-validate — invariant #12's concern rather than tidiness. *The default* whenever the
  target column has one, changed or not, because a driver that changes a column by restating its
  whole definition drops every attribute it was not told about
  ([ADR 0058](0058-compiling-mysql.md)) — measured as a refusal on both MySQL variants. *The type*
  only when it changes, because `ir.AlterColumn.Destructive()` keys on its presence, so restating an
  unchanged one would make every column change need permitting. `From` records the prior value of
  exactly the fields set, which is `AlterColumn.currentState()`'s own rule read backwards.
- D2: a minimal delta throughout. **Rejected on measurement**: both MySQL variants refuse a
  nullability change on a defaulted column, so the generated migration would not run on a third of
  the engines.
- D3: restate everything including the type. **Rejected**: it makes every column change destructive.
- The asymmetry is stated as a property of a generator that knows both states, not as a branch on an
  engine, which is what keeps §11 intact. One hole stays open and is written down: where the column's
  default is engine SQL the differ cannot restate it, so a nullability-only change on such a column
  is refused by that driver at run time. That is ADR 0058's own hole, not this one's.

### E. The order

- **E1: one fixed order, and exactly palindromic** ✔. No operation carries a foreign key inline —
  `ir.CreateTable` holds columns and a primary key — so every real dependency between two operations
  is a boundary between two kinds, and a topological sort would be machinery for a table. Drops come
  before creates because an index name is namespace-scoped on PostgreSQL, so a name moving between
  tables must be freed before it is claimed. And every pair sits at opposite ends with
  `alter_column` — which inverts to itself — in the middle, so **reversing a list in this order gives
  a list in this order**: that is what makes a derived down of a generated migration runnable rather
  than merely well formed. A test holds all seventeen to their mirrors.
- E2: a topological sort over the objects. **Rejected**: nothing to sort. A view's body is engine text
  and a check's expression is too, so the graph cannot be read from the snapshot at all — which is
  exactly why every view is dropped first and created last.
- E3: a `phase` enum switched over per operation. **Rejected**: it duplicates the order in a switch a
  test would have to restate. `order` is one `[]ir.Kind` literal, `emit` files an operation under its
  own kind, and so an operation cannot be put in the wrong place — the ordering is structural and what
  holds the literal is the named cases plus the engines.
- **A dropped table's own indexes, uniques, checks and foreign keys are dropped by name first**, even
  though every engine takes them with the table. A recorded table definition holds columns and a
  primary key and nothing else, so a bare `drop_table` is a migration whose inverse rebuilds a
  different table — which is what `internal/engine`'s implied gate refuses, and rightly.

### F. Keeping the list coherent when something is refused

- **F1: a names-only model of the target state** ✔. An index over a column that was refused would
  name something that is never going to be there, so the differ tracks `to`'s tables and columns
  minus everything refused and consults it before every constraint it emits. A refused `create_table`
  takes that table's indexes and keys with it, said once in the table's own refusal rather than five
  times; a foreign key naming a table the diff is not creating is refused rather than handed to a
  server to reject.
- F2: fold the emitted operations onto the snapshot. **Rejected, and deliberately**: that is the
  IR → snapshot projection M10-T3 needs and no task in M11 owns. Names are enough for coherence and
  must not grow into one here.
- A referenced table the target schema never names is **left alone** rather than refused, because the
  model says outright that a foreign key may point at a table the snapshot does not hold — a snapshot
  being one namespace and a cross-schema reference being real.

### G. A key moves as two halves, and either can be impossible

- **G1: one half refused holds the other back, and says so** ✔. There is no `alter_primary_key`, so a
  key change is a drop and an add; a drop alone leaves the table without the key the target asked for
  and an add alone names one the table already has. So when either half cannot be proposed both are
  reported, the second saying which half held it back.
- G2: emit the half that can be proposed. **Rejected**: it produces a statement the server refuses,
  from a tool whose output is reviewed before it runs.
- The differ also refuses a key the target schema states over a column it calls nullable. No engine
  holds both at once, so such a target contradicts itself — and emitting it would be refused by
  `engine.Unrestorable`'s implied sense anyway, which is the gate a generated migration has to pass.

### H. The shape of the refusal

- **H1: `Kind`, `Subject`, `Reason`, and a `Describe`** ✔. `engine.Disagreement`'s shape minus the
  fields only a run has, so a refusal reads as the line it is: `add_index on "orders_total_idx": …`.
  Free-text reasons, following `validate.Unchecked`.
- H2: a reason enum. **Rejected**: nothing branches on the category — `engine.Disagreement` carries
  `Sense` because the two senses have *different gates*, and these have none. Adding one later is
  additive.
- H3: reuse `schema.Finding`. **Rejected**: a finding is about one snapshot, and a refusal is about a
  pair and about the operation that was not emitted, which is what `Kind` carries.
- `Diff` returns `(ir.Operations, []Refusal)` and **no error**: a malformed snapshot is
  `Snapshot.Validate`'s question and a caller's to ask, and everything else it meets is a refusal
  rather than a failure. A plain function rather than a struct, so M11-T4's confirmed renames arrive
  as a parameter when something can supply them (ADR 0011's rule).

### I. Emitting `raw` to close what the operations cannot express

- **Rejected outright.** It would close every gap in this document at the cost of both claims a
  generated migration exists to keep: `ir.Raw` names a driver and has no inverse. `rename_table` and
  `rename_column` are not emitted either, and all three are named in `neverEmitted` with the reason,
  so coverage is derived from `ir.Kinds()` and a twenty-first operation fails the suite until somebody
  decides which half it is in.

### J. Where *correct and ordered* is proven

- **J1: pure tests plus a third per-driver conformance suite** ✔. `test/conformance/driver`'s `Diff`
  suite introspects the schema the snapshot fixture builds, mutates it, asks the differ to close the
  gap, runs what it emits through `Compile`/`Exec`, and reads the result back — in both directions,
  plus the self diff. It composes no SQL. A wrong order fails **at the server**: measured, moving
  `add_foreign_key` ahead of `create_table` fails on SQLite with *"no such table"* and on PostgreSQL
  too. SQLite runs it untagged, so one third of the claim is inside `make check`; PostgreSQL and MySQL
  run it under `integration` against all four servers.
- J2: pure tests alone. **Rejected**: *"the ordering respects dependencies"* would stay a claim about
  a slice compared to a hand-written expectation, and M11-T6 would be the first thing to find out.
- **Both sides of every comparison come from one engine, so nothing is normalized.** The mutation
  copies each new column's `ColumnType` from a column already there, which is what makes a raw
  comparison possible — native spellings and certainties included, where a hand-written expectation
  could predict neither. A view and a check are absent for the reason the snapshot fixture leaves them
  out: both are engine text, so no target the suite could write down would read back as itself.
- The suite applies **one operation at a time**, which is what `Snapshot.apply` already did and what
  M11-T7 will make the engine do. So what it proves is the differ's list rather than the queue's
  compilation.

## Decision Outcome

`diff.Diff(from, to) (ir.Operations, []Refusal)`: pure, deterministic, no error. Tables, columns and
views matched by name; constraints as set differences under the model's own `Equal`. Every emitted
drop carries a complete `from`, and anything whose prior definition cannot be recorded is declined in
both directions. A type is compared on the logical answer, a change off a guess refused and a creation
off one allowed. One fixed, palindromic order in `order.go`. A names-only model of the target state
keeps the list coherent. Proven pure in `internal/diff` and executed by every driver in
`test/conformance/driver`.

## Consequences

### Positive

- **The two things in the snapshot model that exist to be refused on now have a reader**, and
  `schema.Unrepresentable`'s documented second consumer is real.
- **Every migration `migr diff` will write is reversible by derivation**, asserted over every fixture
  through `ir.ReverseAll`, and none of them is a list `engine.Unrestorable` refuses — asserted in both
  directions through the gate the engine itself reads.
- **A schema diffed against itself is empty**, including one holding a partial index, a generated
  column, a `serial` default and a `tsvector` — measured against a real PostgreSQL schema. That is
  most of M11-T6's assertion, delivered early.
- **The order's mirror property is mechanical**, so a derived down of a generated migration is
  ordered as well as well formed.
- M11-T4 and M11-T5 have what they need: a function of two snapshots, and a refusal list to print.

### Negative

- **A diff across two different engines proposes replacing every view and every check**, because both
  are engine text and nothing here can know that an expanded column list and a `SELECT *` are the
  same query. That is the model's honesty rather than this differ's choice, and it is written down in
  `internal/diff/doc.go`.
- **On SQLite every integer and float column against a desired schema that disagrees is a refusal
  rather than an operation**, since the catalog cannot tell the widths apart. M11-T6's *empty diff*
  therefore means an empty *operation list*, which is noted under that task.
- **A list this differ emits is not always one `migr up` can run**, measured above: M11-T7 is the
  prerequisite for a generated migration that creates a table and gives it a foreign key on SQLite.
- **A table whose expression index puts a marker on the table itself becomes untouchable**, which is
  ADR 0069's choice read one layer up: the whole table is refused rather than recreated without that
  index. Correct, and worth knowing before someone points `migr diff` at a mature database.
- `tableProblem` reports the **first** thing wrong with a table rather than all of them, so a table
  with five unexpressible columns produces one refusal. `schema.Unrepresentable` is where the full
  list lives.

### Neutral

- **`internal/engine`'s ledger gained a correction this task found.** `ir.AddForeignKey` already named
  both sides of a key; `ir.DropForeignKey` named only its own — and both implied findings read the
  referencing keys of the object they are about, so dropping a recorded key moves exactly the state a
  later drop of the referenced table or column is judged against. Without it a correct diff that
  dropped the referencing key first was refused by the gate, which is the false positive ADR 0069
  calls strictly worse than a missed finding. An unrecorded drop names nothing extra, there being
  nothing to restore it from either.
- No new normative document and no contract change: this emits the IR rather than extending it, so
  invariant #4 is not in play and `docs/ir.md` is unchanged. §7.2's convention that a new table states
  its key inline is followed and asserted.
- `test/conformance/driver`'s `Snapshot.apply` was split so its runner takes an operation list, which
  is what the `Diff` suite reuses. The note about why it compiles and runs one operation at a time is
  now written down where it is load-bearing.

## Notes

The probes are deleted. Their three findings live in this document's Context; the SQLite queue one is
repeated in TASKS.md under M11-T7, because that is where someone will be standing when they need it.

Two mutation survivors are recorded rather than closed. Removing `checkSelf` or `checkBackward` from
the conformance suite's `Run` survives, because driving `Run` needs a fake engine that applies
operations and reflects them — a second implementation of every driver. Both claims are asserted in
`internal/diff`, so what survives is a stage's presence in that suite rather than the claim it makes.
And `referencedIdentities` returning a non-empty identity for an unrecorded key is equivalent: no
valid operation carries an empty table name, so nothing can collide with it.
