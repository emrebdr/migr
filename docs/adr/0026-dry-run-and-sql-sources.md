# 0026. A dry run reads what an apply ensures

## Status

Accepted

## Date

2026-08-12

## Context

M5-T2 is where the framework first runs. Four milestones of contracts — the IR, the driver
contract, the plan compiler, the apply and rollback executors — had never been connected end to
end, because `cmd/migr` registered no driver and `engine.Source` had no implementation.

Most of the task is wiring. Two holes are not, and one existing code path turned out to be in the
way.

**`--dry-run` cannot reuse the apply pipeline as it stands.** `executor.reconcile` calls
`store.Ensure`, which **creates** the history table. A command that reports what it would do and
creates a table while doing so is not a dry run. And a preview that skips `Ensure` immediately hits
[ADR 0019](0019-history-reconciliation.md)'s sentinel: `resolved` treats a report over a database
with no history table as a bug — correctly, for an apply, which has just ensured one.

**A `.sql` file has to become operations, and how many is a decision.** migr counts statements,
records a position within them ([ADR 0013](0013-migration-history.md)), resumes from that position
in M9-T6, and classifies lock impact per statement. Whether any of that means anything for the
format M5 runs is decided by whether a file becomes one operation or several.

**`migr.yaml` holds a map of databases and nothing said which one a bare command targets.**
[ADR 0021](0021-apply-executor.md) established that a batch belongs to one database and that no lock
spans two, ending with *"migr up loops"* — a sentence about the mechanism that could be read as a
policy.

## Decision Drivers

- CLAUDE.md §4.3: plan mode is the design lever precisely because *"there is no separate 'preview'
  implementation to drift out of sync."*
- `engine/doc.go` already refuses a zero `Scope` that means different things per direction, because
  *"a value meaning something different at a call site is one nobody can read."*
- Invariant #1: `cmd/migr` is the only package that may name a concrete driver.
- Invariant #3: no raw SQL outside a driver package.
- M5-T3, M5-T4, and M5-T6 all read what this task builds.

## Considered Options

### A. Whether a preview is an entry point or a flag

- **A1: `engine.Preview`, its own entry point returning its own type** ✔. It runs
  `read → reconcile → classify → emit → prepare` — `applyAll` and `rollbackAll` minus `perform` —
  and `locked` became generic so the release path (invariant #11) is written once.
- A2: `Options.DryRun bool`, reusing `Result`. **Rejected** on the codebase's own rule: `Result.Runs`
  is documented as *"one entry per migration applied"*, and making it mean "would apply" under a flag
  is exactly the ambiguity the zero `Scope` was rejected for.
- A3: leave it to M5-T4's `migr plan`. **Rejected**: knowing what is pending needs `classify`, which
  needs a reconciled report, so the CLI cannot assemble a queue on its own.

### B. Whether a preview ensures the history table

- **B1: no — it reads, and a database with no table is an answer** ✔. `Previewed.Recorded` reports
  it, and the queue is folded against `history.NewLog(nil)`, so every migration reads as pending.
- B2: ensure anyway, since the table is migr's own bookkeeping. **Rejected**: it is still a write, on
  the one command whose entire promise is that it makes none.
- B3: refuse on a database with no table. **Rejected**: the first thing anyone runs on a new database
  is `migr up --dry-run`, and refusing it is refusing the case it is most useful for.

This is not ADR 0019's sin. That sin is reading absent as empty **silently**, so a fully migrated
database looks virgin and the next `up` re-runs everything. A preview says the table is absent in
the first line of its output and applies nothing either way.

### C. Whether a preview takes the migration lock

- **C1: yes** ✔. It reconciles, and [ADR 0021](0021-apply-executor.md) put reconciliation under the
  lock. Unlocked, a preview reports on a database another run is still changing, and the answer can
  be stale before it finishes printing.
- C2: no, since it writes nothing. **Rejected**: it makes a dry run's output unreliable in exactly
  the situation an operator reaches for one — someone else is deploying.

### D. How a `.sql` file becomes operations

- **D1: one `ir.Raw` per statement, split by a scanner** ✔. The scanner tracks line comments, block
  comments (nesting, as PostgreSQL allows), single- and double-quoted strings, and dollar-quoted
  bodies, splitting on semicolons outside all of them.
- D2: one `ir.Raw` per section, the whole blob. **Rejected**: every `.sql` migration would report
  "1 statement" forever, `migr plan` would print a blob as statement 1, and M9-T6 could never resume
  inside one. The statement model that is the framework's identity would be dark for the only format
  M5 runs.
- D3: explicit `-- migr:statement` separators. **Rejected**: it makes ordinary SQL files unusable
  without editing, and the format exists to be the one people already have.

Splitting is a **lexical property of the file, not of the engine** — semicolons, comments and quotes
are common to PostgreSQL, MySQL and SQLite. The one case that costs data rather than tidiness is the
dollar-quoted body: cut at the first semicolon inside it, a PL/pgSQL function becomes two invalid
statements. MySQL's `DELIMITER` is a client directive rather than SQL and is not supported; the
driver that needs it can say so in M9.

### E. What a file's sections mean

Each rule is a refusal rather than a guess:

- `-- migr:up` is **required**. A file without one is refused rather than read as all-up, because
  `-- migr:Up` is then a typo that runs the down section during an apply.
- SQL above the first marker is refused, naming its line. It belongs to no direction and would
  otherwise be dropped in silence.
- `-- migr:down` is optional, and one with nothing under it is the same as none: both report
  `engine.ErrNoDownAuthored`, after which [ADR 0022](0022-rollback-executor.md)'s derivation runs,
  `Raw.Reverse` refuses, and the author is told to write the undo.
- A marker carried twice, or a down above an up, is refused naming the lines.

### F. Which database a bare command targets

- **F1: `--database`, else the `default` key, else the only one defined, else refuse naming them
  all** ✔.
- F2: every database defined, in order. **Rejected**: a `migr.yaml` holding `default` and `prod` is
  an ordinary file, and `migr up` reaching production because nobody typed a flag is not a mistake a
  tool should be able to make. ADR 0021's *"migr up loops"* describes the mechanism — no lock spans
  two databases — not a policy for the bare command.
- F3: always require `--database`. **Rejected**: tedious for the single-database project the
  scaffold writes.

Map iteration order is random, so "whichever comes first" would migrate a different database on
every invocation, which invariant #8 forbids for planning paths and common sense forbids here.

### G. Where the end-to-end test lives

- **G1: `cmd/migr`, behind the `integration` tag** ✔. Invariant #1 says `cmd/migr` is the only
  package that may name a concrete driver, so a test that drives the whole stack has one home.
- G2: `test/integration/`, as CLAUDE.md §7 expected at M5. **Rejected**: it would have to name
  `internal/driver/postgres`, which depguard denies, and the alternatives are widening the rule or
  adding a package that exists only to be imported by a test.

What the untagged suite gets instead is better than either: `cli.Execute` takes a registry, so a
**fake driver** covers flags, scopes, summaries, exit codes, and the dry run with no Docker at all.
That is possible only because registration is explicit rather than an `init()`
([ADR 0011](0011-driver-contract.md)).

## Decision Outcome

**A1, B1, C1, D1, E, F1, G1.**

```go
func Preview(ctx context.Context, conn driver.Conn, opts Options, dir ir.Direction) (Previewed, error)
```

`Previewed` carries the direction, the batch these runs would record under, whether the database has
a history table, one `Planned` per migration — each holding the `plan.Plan` it compiled to — and the
`Skipped` an apply would have reported. `migr plan` (M5-T4) prints those statements; `--dry-run`
prints their counts. One pipeline, two renderings.

`internal/source/sql` gained `Emit` beside the template M5-T1 wrote, reading the markers that package
already defines rather than restating them (ADR 0025, B). Each statement becomes
`ir.Raw{Driver: databases.<name>.driver, SQL: …}`.

**The summary prints before the error is returned, and only when there is something to say.**
`engine.Result` is populated alongside a failure, which is the whole reason it is a value rather than
a field on an error — a run that fails on the third migration shows the two that committed and then
fails. A run that failed having done nothing prints no summary at all: "nothing to do" above a
refusal is two answers to one question, and the first one is wrong.

## Consequences

### Positive

- A dry run and a real run cannot disagree, because only the last step differs. The failure mode
  where a preview grows its own reconciliation and drifts from the executor is structurally absent.
- A dry run is honest on a database migr has never touched, which is where it is most useful.
- `.sql` migrations reach the engine as statements, so statement counts, per-statement timing,
  `migr plan`'s output, and M9-T6's resume index all mean something for M5's own format.
- The untagged suite drives the entire CLI — every flag, both directions, exit codes, the dry run —
  with no Docker, because the driver registry is a parameter.

### Negative

- **`ir.Raw` carries no `NoTransaction`, so a `.sql` migration cannot express `CREATE INDEX
  CONCURRENTLY`.** It would run inside the transaction the executor opens and PostgreSQL would
  refuse it. Adding the field is an IR change — an ADR, every driver, a golden file each — and it is
  M13-T4's neighbourhood, since the executor refuses a multi-run migration anyway.
- **Progress is a summary, not a stream.** `engine.Apply` returns once, so a long migration prints
  nothing until it finishes. A live stream needs a hook in `Options`, which is a contract change this
  task did not need.
- **A preview takes the migration lock**, so `migr up --dry-run` can exit 3, and two dry runs
  serialize. That is the cost of the answer being true when it is printed.
- **A `.sql` migration is destructive by definition**, since `ir.Raw.Destructive()` is true. M10-T1
  still owes an answer; nothing here changes that, and now there are real migrations for it to be
  wrong about.

### Neutral

- Invariant #3 governs SQL migr **composes**, not SQL an operator hands it. A `.sql` fixture in a
  test above a driver package is operator input — `ir.Raw` exists to carry exactly that — and the
  format could not be tested at all under the other reading. What still holds: no test above a
  driver package composes SQL to send to a database on migr's behalf, which is why the end-to-end
  suite resets itself by running `migr down` rather than by dropping a schema.
- `migr up --json` prints no document when the run failed having done nothing. The exit code carries
  the outcome and stderr carries the reason.

## Notes

The end-to-end suite runs against PostgreSQL 17 alone. The driver-level matrix already holds both
majors, and what this suite proves — that the registry, the config, discovery, the source, the
planner, the executor and the history store compose — is not version-dependent.
