# 0028. A validator reports in phases

## Status

Accepted

## Date

2026-08-12

## Context

`internal/validate` has been a `doc.go` and nothing else since M0, stating two properties nothing
had yet had to honor: *"reports every problem it finds rather than stopping at the first"* and
*"runs without a database connection wherever possible."* M5-T5 is where both become code.

It is also where three later tasks arrive. M10-T1 adds destructive-operation detection, **M10-T2
declares a dependency on M5-T5**, M10-T3 adds reference integrity, and M10-T4 makes the rule set
pluggable. So what this task decides is less *which checks run today* than *what a check is*.

Three things about the codebase forced the shape:

**Nothing in it accumulates.** `migration.Discover` returns on the first bad filename,
`checkDistinct` on the first duplicate version, `plan.Compile` on the first refused operation, and
`ir.ReverseAll` on the first operation with no inverse. `migration.GraphError` is the only type in
the repository that reports *"3 problems"*. Every one of those is correct for the caller it was
written for — a run must not perform half a migration — and wrong for a report.

**A driver needs no database to compile.** `drivers.New(name, opts)` takes no DSN and
`plan.Planner.Compile` takes no context. What it cannot answer is the version-dependent refusal
`postgres.checkServer` adds, which reads a server this build detected inside `Open`.

**A `.sql` migration cannot say what every SDK can say.** Invariant #10 lets a `Down` declare itself
`Irreversible()` **with a reason**. A `.sql` file has only "authored a down" or "did not", and
`ir.Raw` has no inverse — so every down-less `.sql` migration is one nothing can roll back, with no
way to distinguish "I forgot" from "it cannot be done". That gap is
[ADR 0025](0025-sql-source-language.md)'s, and reversibility landing here is what surfaces it.

## Decision Drivers

- CLAUDE.md invariant #10: every migration has `Up` and `Down`; a `Down` **may declare itself
  irreversible with a reason**, but may not be silently absent.
- `internal/cli/doc.go`: *the argument is wrong → exit 2, the world is not ready → exit 1.*
- M4-T6 refuses an irreversible rollback with exit 1 because it needs a database to know what is
  applied; TASKS assigns reversibility here precisely because that constraint disappears.
- ADR 0027, E: `migr status` exits 0 having reported drift, so "is this repository safe to deploy"
  had no answer anywhere.

## Considered Options

### A. What "every problem rather than the first" covers

- **A1: every migration, and within one, every check whose inputs exist** ✔. A check that fails
  stops the checks that depend on it *for that migration and nothing else*.
- A2: one flat pass, every check on every migration regardless. **Rejected**: a file that cannot be
  read cannot be compiled or reversed either, and reporting all three is three complaints about one
  defect — the fastest way to teach an operator to skim a report.
- A3: stop at the first migration, as every other caller does. **Rejected**: it is the done-when, and
  it sends someone round the loop once per broken file.

Within a migration, every operation is still reported. `plan.Compile` fails a migration whole and
`ir.ReverseAll` fails a list whole; this package walks the operations itself through the same
primitives — `Planner.Step` (an existing method, exported by this task) and `Operation.Reverse` — so
an operator fixing one refusal sees the other in the same pass.

### B. Whether it opens a database

- **B1: never** ✔. `Options` carries a `plan.Planner`, which holds a `driver.Compiler`, and there is
  nowhere to put a session. It is the one command CI can run on a pull request with no secret.
- B2: connect when it can, adding drift. **Rejected**: a gate that silently skips a check because the
  DSN was wrong reports a pass it did not earn. Drift is already reported exhaustively by
  `migr status` and refused by `migr up` (exit 4).
- B3: a `--drift` flag that fails if it cannot connect. **Rejected on timing, not merit** — it is a
  real second mode, and worth revisiting once M10 has filled in the offline checks.

Opening no database is not the same as needing no configuration: which engine to compile for comes
from `migr.yaml`, and there is no answer without one.

### C. Whether compiling is validation

- **C1: yes** ✔. It needs no connection and it is what makes the gate worth running: an operation
  this engine refuses is found before anyone opens a session.
- C2: leave it to M10-T2. **Rejected**: M10-T2 is *capability preflight* — the `capability.Set`, built
  inside `Open` — and compile-refusal is the free half of a biconditional the driver conformance
  suite already asserts. Deferring it would leave the offline command unable to answer the question
  it is best placed to answer.

A **derived** down is compiled too, not assumed. An inverse can be refused by a driver that accepted
what it inverts, and stopping at "it inverted" would pass a rollback nothing can run.

### D. How a `.sql` migration declares that nothing can undo it

- **D1: its own marker,** `-- migr:irreversible: <reason>` ✔, read as a prefix with the reason after
  it. Five refusals, matching the four that file already enforces: a blank reason, the marker twice,
  the marker beside a `-- migr:down`, the marker above `-- migr:up` (where every comment is passed
  over, so it would be ignored in silence), and SQL beneath it.
- D2: a suffix on the down marker, `-- migr:down irreversible: …`. **Rejected**: `markerAt` matches a
  marker whole, deliberately — relaxing it to a prefix makes `-- migr:downstream` a marker.
- D3: no declaration; report it and let M10-T1 pick a form. **Rejected**: it makes the gate unusable
  for exactly the migrations it most wants to flag — a project with one legitimately one-way
  migration could never pass.

The declaration reaches the engine as `ErrDeclaredIrreversible`, beside `ErrNoDownAuthored`, because
the executor is what acts on it: `undo` returns it rather than deriving, so the whole rollback halts
before its first statement ([ADR 0022](0022-rollback-executor.md)). A migration that validates clean
therefore cannot fail a rollback for a reason validation could have found.

### E. One severity or two

- **E1: one** ✔. Everything reported is a failure, and the command exits 2.
- E2: errors and warnings, exiting 0 on warnings alone. **Rejected**: TASKS assigns reversibility here
  because *"it is where exit code 2 becomes honest for it"*, and a warning nobody must act on is one
  nobody reads. D1 is what makes a single severity liveable — there is always something to do.

## Decision Outcome

**A1, B1, C1, D1, E1.**

```go
func Run(ctx context.Context, opts Options) (Report, error)
```

`Run` returns a `Report` rather than an error, for the reason `history.Reconcile` does: *"there are
three problems"* is the answer, not a failure to produce one. The `error` return covers only a run
that could not be set up, mirroring `engine.Options.check`.

A `Problem` carries the migration, the `Check` that found it, the direction and one-based operation
it sits at, the operation kind, and a detail that **never repeats the migration it is about** —
`detailOf` strips a leading filename, so a renderer can name the migration once.

`migr validate` composes `resolve` → `discovered` → `source` → `compiler`, two of which are splits
of existing helpers: `discover` divided into ordering and hashing, and `dial` into building the
driver and opening it. The command names the target database because compilation is
engine-specific, and says plainly that it opened nothing.

The remedy line is the command's, not the check's. `ir.Raw`'s reason says to write the undo *"in
Down"* — correct, source-neutral, and language an author of a `.sql` file cannot act on. The CLI
knows `migrations.language` and prints how *this* format answers it; `internal/validate` cannot and
should not.

## Consequences

### Positive

- A migration set can be checked with no database, no DSN and no secret, which is what a
  pull-request gate needs.
- Reversibility is answerable before anyone tries a rollback, and `.sql` can now say what every SDK
  can say.
- M10's four tasks each add a `Check` and a rule, not a second report — the shape they plug into
  exists and is tested.
- `Planner.Step` being exported gives any future caller per-operation compilation without
  duplicating the three guarantees `plan` makes about a compiled operation.

### Negative

- **`migr validate` cannot see a version-dependent refusal.** A migration using a default function
  the connected server lacks passes validation and fails at `migr up`. That is M10-T2, and until it
  lands the gate is weaker than it looks.
- **It cannot validate a Go or TypeScript project**, since `sourceFor` refuses a language whose
  runtime does not exist (M6-T10, M7-T8). The command reports that rather than passing vacuously.
- **Discovery is still fail-fast.** A directory with three misnamed files reports the first, because
  discovery is what enumerates the set every later check walks. The done-when is satisfied for the
  checks; the grammar keeps one definition.
- **`--require-safe` is still unimplemented**, though CLAUDE.md documents it on this command. It
  means *no operation exceeds `safety.max_lock_impact`*, every `.sql` migration is `ir.Raw` →
  `LockUnknown`, and M13-T6 owns it — exactly as `--safety` was left for `migr plan` (M5-T4).

### Neutral

- A `.sql` migration remains destructive by definition (`ir.Raw.Destructive()` is true), so M10-T1
  still owes an answer for `--allow-destructive`. The marker added here is a candidate form for that
  declaration too, and the invariant's *"or the migration declares intent explicitly"* now has one
  precedent to point at.
- `migr validate` exits 2 where `migr status` exits 0 over the same project. They answer different
  questions: one reports state, the other judges the repository.
