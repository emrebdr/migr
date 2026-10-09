# 0073. Folding operations onto a schema, writing them back out, and `migr diff`

## Status

Accepted

## Date

2026-10-08

## Context

M11-T5's done-when is *"the command compares the live database to the desired schema and writes a
migration file in the configured language."* It named one blocker — *"the desired schema has no
producer yet … **M10-T3's note is where that decision is recorded** — append an `M11-T8` or scope it
in, whichever task reaches it first"* — and this task reached it first.

It did not name a second one. **Nothing could render operations back into source.** `migr create`'s
templates are empty skeletons, and a command that writes a migration file has to put the change
*in* it. No note in TASKS.md owned that, so it is scoped in here too and the task is corrected to
say so.

So three pieces: `internal/fold` (IR → snapshot), `internal/source` (IR → source), and the command.

### What was measured first

Two probes against all five engines, and one read of the finished pipeline by hand. Nothing from
them ships; all three changed the design.

**A projected column type compares as a fact, and that is usable.** `schema.ColumnType` requires a
native spelling and a projection has never seen an engine, so one is invented: the logical type's own
spelling. Measured, nothing downstream can tell — `ColumnType.Equal` compares two *exact* types on
the logical type alone, so a projected `int32` equals PostgreSQL's `integer` and MySQL's `int` alike.
What it does mean is that meeting an **ambiguous** live type whose best guess differs is a refusal by
design ([ADR 0070](0070-the-snapshot-differ.md), C1): SQLite declares `int16`, `int32` and `float32`
ambiguously and MySQL declares `timestamp` and `timestamptz` ambiguously. SQLite's `int64` and `text`
are ambiguous and guess *correctly*, so those compare.

**A view's body and a check's expression come back rewritten by four of the five engines, so they
cannot be compared at all.** One view asked for as `SELECT id FROM t` reads back as
`" SELECT id\n   FROM t;"` on PostgreSQL and as ``select `migr`.`t`.`id` AS `id` from `migr`.`t` ``
on MySQL; SQLite alone echoes it. One check expression `id > 0` reads back **four ways**:

| SQLite | PostgreSQL | MySQL | MariaDB |
|---|---|---|---|
| `id > 0` | `((id > 0))` | `` (`id` > 0) `` | `` `id` > 0 `` |

Compared as written, every `migr diff` over a database that is exactly up to date would propose
replacing every view and every check it can see. That decided option C.

**The first version of the command found nothing to write and the first renderer wrote a body rather
than a file.** Read by hand, the generated Go did not compile — the renderer produced the statements
and the command wrote them straight out as the file — and the first file it did produce declared
`type Migration struct{}`, which two generated migrations in one package cannot both do. Both are
in option E.

## Decision Drivers

- ADR 0011's rule: no method or field until something can implement it. The projection has three
  waiting consumers, so it is built once and shared rather than three times.
- ADR 0070's stated payoff: *"a migration `migr diff` writes is reversible by derivation"*. A
  renderer that dropped the recorded payloads would spend that for nothing.
- ADR 0025 C: one file format, one writer. A `diff` that wrote directly would be the second spelling.
- ADR 0027: a report takes no lock, a preview does. This is neither.
- Invariant #6: an applied migration is immutable, so a drifted project cannot be folded honestly.
- CLAUDE.md §9's filename grammar, which is what makes a generated identifier derivable at all.

## Considered Options

### A. Where the projection lives

- **A1: a package of its own, `internal/fold`** ✔. `Forward(from, ops) (schema.Snapshot, []Refusal)`
  — pure, deterministic, no error, the shape `diff.Diff` and `engine.Destructive` have.
- A2: a file in `internal/diff`. **Rejected**: ADR 0070 F says the differ's names-only availability
  model "must not grow into one here", and a sibling file in the same package is the shortest route
  to the two becoming one thing.
- A3: a method on `schema.Snapshot`. **Rejected**: ADR 0065 forbids `internal/schema` from importing
  `internal/ir` at all, which is the whole reason the two neutral vocabularies live in `schema` and
  `ir` aliases them.

**It folds and it does not validate.** An operation naming something that is not there is a
`Refusal` rather than a panic or a silent create, which is what makes the same fold readable as
M10-T3's reference-integrity check — there, these refusals *are* the findings.

**What it does that no payload records is what every engine does.** A primary key makes its columns
`NOT NULL`, and a dropped column takes its index, its unique, its foreign key and its place in the
key with it. Both are measured gaps (M3-T7, M9-T7) that `engine.Unrestorable`'s implied half exists
to refuse, and a projection that left them out would describe a schema no server produces — and
would then have `migr diff` propose tightening columns the engine had already tightened. Neither is
reachable from the differ's own direction, since it refuses to propose a key over a nullable column
at all, so both are tested directly.

### B. `raw`, and what a partial fold would mean

- **B1: `raw` is a refusal, and a caller refuses the project whole** ✔. Engine SQL has no readable
  effect, so a list carrying one leaves every schema after it unknowable. A desired schema missing
  whatever a statement built is one whose diff proposes **dropping it**.
- B2: fold `raw` as a no-op and diff against what is left. **Rejected**: that is the silent
  degradation, and the thing it degrades is a `DROP TABLE` proposal.
- B3: refuse only the operations after the raw one. **Rejected**: the schema is unknowable from that
  point on, so there is no honest "after".

**So `migr diff` is unavailable to the `.sql` format**, by the same property that already costs that
format its derived down, its capability checks and its lint rules: every statement in one is
`ir.Raw`. The refusal says both halves — that nothing writes a generated migration in `sql`, and
that there would be nothing to write about either.

### C. Comparing a projection against a live schema

- **C1: an engine-text body is compared for presence, not for content** ✔. `fold.Comparable(live,
  desired)` carries the live text across for every view and every check both schemas hold under one
  name, so only a genuine arrival or departure is left to propose. An element only one side holds
  keeps its own text, which is what lets the operation that creates it carry the real body.
- C2: compare them as written. **Rejected on the measurement**: four spellings of one expression.
- C3: normalize the engine's spelling back. **Rejected**: that is a SQL parser per engine, and ADR
  0065 is deliberate that these three fields do not port.
- C4: drop views and checks from both sides entirely. **Rejected**: it loses the case that is
  actually useful — a view the project declares that the database does not have.

It lives in `internal/fold` rather than in the command because the test that judges the projection
needs the same rule, and a policy spelled twice is a policy with two definitions.

### D. What the renderer renders

- **D1: one neutral chain, a writer per language, and every recorded payload written out** ✔.
  `internal/source` turns operations into `Chain`s of `Call`s with typed `Arg`s;
  `internal/source/golang` and `internal/source/typescript` spell them. The two SDKs are the same
  shape with different spelling — methods recase, type constructors carry a prefix rather than a
  receiver, a payload is a struct literal or an object — so a third language is a writer and an
  eighteen-row name table, not a second renderer.
- D2: a renderer per language. **Rejected**: nineteen operations each, twice, with the chain
  structure duplicated and free to drift.
- D3: render the inverse as an authored `Down` instead of writing the `from` payloads.
  **Rejected**: it contradicts the ADR that made the derivation work. ADR 0070 declines to emit a
  drop whose prior definition it cannot record precisely so that the derived down is exact, and a
  renderer that dropped `.Was(…)` would write migrations this project's own `migr validate` rejects.

**There is no `sql` writer**, and that is B1's consequence rather than an omission: a project in that
format is refused before anything reaches a renderer, so a backend for it would be unreachable code.

Two per-language details that are not symmetry for its own sake. The Go writer's output is held to
being **byte-exactly what gofmt writes**, because a generated file people read and edit has to arrive
formatted; the TypeScript writer's is deliberately *not* held to prettier, whose line breaking is
width-driven, so matching it means reproducing it — what is asserted there is that it typechecks
against the published package by self-reference. And **a Go file's identifier carries the version**,
not the name: every Go migration in a project shares one package and the default name is the same for
every diff, so `Migration20261008T153059` is unique by construction where `Diff` is a project that
does not build. TypeScript needs none of this, each migration being a module with a default export —
ADR 0042's "the module system replaces the registry", one more time.

### E. The lock covers reading, not asking

- **E1: acquire, read history, introspect, release — then compute, prompt, and write** ✔.
- E2: hold the lock for the whole command. **Rejected**: a rename prompt waits on a person, and
  holding the migration lock across that blocks every other `migr` run on the database for as long
  as they take to answer.
- E3: take no lock at all. **Rejected**: ADR 0027 draws the line at whether a command promises
  anything. A report answers *what is*; this one persists its answer into a file, so the answer has
  to be about a database nobody is changing.

The file is also **named and claimed before anything connects**, so a name the grammar refuses and a
version another migration already holds are both caught before a database is opened and before
anybody is asked anything — rather than after an operator has spent time at a prompt.

### F. It requires a settled, fully-migrated database

**F1** ✔: every state but `applied` is refused, each with the remedy that settles it — `migr up` for
pending, `migr resolve` for dirty, the file for missing, invariant #6 for drifted. Exit **1**
throughout, because the same command line succeeds once `migr up` has run (ADR 0029).

A desired schema folded from every migration is the schema the database should have **once they have
all run**, so a diff over a half-migrated project would propose everything the pending ones do and
then duplicate them. Folding only the *applied* migrations was rejected: the answer would then change
after every `migr up`, and M11-T6's done-when is "apply-all → introspect → diff-against-desired", so
this command's own success case is that assertion.

### G. `--json` does not prompt

**G1** ✔: a document on stdout and a prompt on stdout cannot both be stdout, and `--json` puts JSON
logs on stderr too, so there is nowhere to ask. It runs and confirms nothing: every candidate is
reported in the document as unconfirmed, so a rename arrives as the drop and the create invariant #7
then gates — the safe side, and what `ConfirmRenames`'s own default already is (ADR 0072, F).

Refusing outright with `ErrNotConfirmable` was rejected: it would make `migr diff` unusable from the
one place a JSON document is for. That sentinel stays what it is — the error for a prompt with no
streams, which this path never builds.

### H. Rejected outright

- **A shadow database for the desired schema** — create a scratch namespace, run every migration into
  it, and introspect that. It needs no projection and handles `raw`, which is a real argument. But it
  needs privileges to create a namespace, it is slow, it runs migrations that may branch on live
  data, and it is a per-driver namespace concept none of the three drivers has. It is a milestone,
  not a task, and B1's refusal is the honest answer until one exists.
- **Folding onto the live snapshot** rather than onto an empty one. It gives the live schema back.
- **Exporting the projection's rename application** so a test could build its own target with it.
  The thing under test must not write its own expectation.

## Consequences

`internal/fold` and `internal/source` are new. `internal/cli` gains `diff.go` and an eleventh
command, which the exit-code matrix now carries — and that matrix gained a per-row flag, because
`migr diff`'s rows are Go projects a runtime has to answer for while *"a runtime that will not
start"* is about an adapter that exists and does not.

**M10-T3 is unblocked and M11-T6 is half done.** That task's "the projection has no task" paragraph
is answered; its reference-integrity check is `fold.Forward` over the introspected snapshot, reading
the refusals as findings. It turned out to need **two** starting points rather than one — the
introspected snapshot and an empty one, reporting only what both refuse — because a migration this
command writes refuses a replay from nothing, drift correction being what it is
([ADR 0075](0075-reference-integrity.md)). M11-T6's pure half — `Forward(a, Diff(a, b))` is `b` — is asserted over
every one of `internal/diff`'s own fixtures, and its per-engine half is a fifth leg of the diff
conformance suite: the fixture folded forward compares equal to what each of the five engines read it
back as. What is left there is the two per-driver catalog digests.

What is written down rather than designed around: `migr diff` cannot serve a `.sql` project; a
column whose live type the catalog can only guess at is a refusal rather than a comparison, which
per engine is SQLite's `int16`/`int32`/`float32` and MySQL's `timestamp`/`timestamptz`; and a
generated migration is a proposal, so the refusals printed beside it are the parts of the gap it
does not close.
