# 0063. Destructive operations: who refuses, and what a project may permit

## Status

Accepted

## Date

2026-10-03

## Context

Invariant #7 has been written down since M0 and enforced by nothing: *"`drop_table`, `drop_column`,
`drop_view`, narrowing `alter_column`, and any `raw` op are destructive by default. The validator
blocks them unless `--allow-destructive` is passed or the migration declares intent explicitly."*

Everything it needed was already built, and three pieces of it were dead:

- `ir.Operation.Destructive()` has classified all twenty operations since M1-T7, with `AlterColumn`
  reporting true for any retype because *"the operation does not carry the column's current type"*;
- `validation.allow_destructive` has been in `migr.yaml` since M0-T7 and **nothing ever read it**;
- `--allow-destructive` is documented in CLAUDE.md §5 as a global flag and was **registered
  nowhere**;
- `internal/validate` was built to take a fourth check ([ADR 0028](0028-validation-report.md)): the
  check list, the per-migration phases, the `Problem` shape with its operation index and kind, the
  `--json` rendering and exit code 2 all existed.

One fact decides most of this task. **Every `.sql` migration compiles to `ir.Raw`** (M5-T1,
[ADR 0025](0025-sql-as-the-first-source-format.md)), and `ir.Raw.Destructive()` is true because its
effect is unknowable to the framework. So a literal reading of the invariant makes
`--allow-destructive` mandatory for an entire source language — which is how a safety flag becomes
something people alias away, and the format in question is the one the quickstart uses.

## Decision Drivers

- M10-T1's done-when: the five kinds *"are all blocked without `--allow-destructive`, and the
  message names the migration, the operation, and the affected object."*
- Invariant #7's own escape: *"or the migration declares intent explicitly."*
- M10-T4 is a lint rule framework with severities, and M10-T5's note says lock-safety rules are
  **not** lint rules *"because a warning that can be ignored is not a safety mechanism"*. The same
  reasoning puts this in M10-T1 as a hard gate rather than in M10-T5 as a rule.
- [ADR 0029](0029-exit-codes-and-failure-output.md): *the argument is wrong → 2*, *the world is not
  ready → 1*.
- CLAUDE.md §5: *"A report exits 0 having found a problem. `migr status` over a drifted database
  exits 0 while `migr up` against that same database exits 4."*

## Considered Options

### A. What the gate blocks

- **A1: `migr validate` reports and `migr up` refuses** ✔. One policy with two callers: a new
  `validate.Check` reports every destructive operation in the whole pending set, and the engine
  refuses the migration it reached before compiling it. `engine.ErrDestructive` resolves to exit
  **2** in `ExitCode`, the way `ErrDrifted` resolves to 4 — ADR 0029's rule fits exactly, since the
  operator retypes the command or names the kind and the same command then runs.
- A2: `migr validate` only, which is the invariant's literal wording. **Rejected on merit**: a
  global flag that `migr up` accepted and ignored is worse than no flag, and the command that
  actually destroys things would be unguarded. M10-T5's note is the same argument one milestone over.
- A3: gate inside `plan.Planner`, where M13-T6 is already headed with a `capability.Set` field.
  **Rejected**: `migr plan` compiles through the same planner, and the one command that should
  *show* an operator what to permit must not refuse to.

### B. How a `.sql` project lives with `raw`

- **B1: a per-kind allowance in `migr.yaml`** ✔. `validation.allow_destructive` becomes a list of
  operation kinds, validated against `ir.DestructiveKinds()` the way `safety.max_lock_impact` is
  validated against `capability.Ceilings()`. A SQL project commits `[raw]` once, in a reviewed file,
  and `drop_table` stays refused. `--allow-destructive` remains a bool meaning *all of them, this
  run*.
- B2: keep the bool and add a second one, `validation.allow_raw`. **Rejected**: two overlapping
  booleans, and `allow_destructive: true` with `allow_raw: false` is a combination that means
  little. The list also means a sixth destructive operation does not silently widen an existing
  project's permission.
- B3: a per-file declaration, `-- migr:destructive: <reason>`, as the sibling of
  `-- migr:irreversible:` (ADR 0028 §D suggested exactly this). **Rejected on merit, and this is the
  finding**: a `.sql` file holds nothing *but* `raw`, so the marker would be mandatory on **every**
  one of them — a purely additive `CREATE TABLE` migration included. That is the same "mandatory for
  a language" failure the flag has, one level down, and a marker nobody can omit stops being read.
  The irreversible marker escapes this because the *normal* answer there is a `-- migr:down`
  section, and the marker is for the exception.
- B4: exempt `raw` that came from a `.sql` file, which migr can tell from the extension (M7-T10).
  **Rejected**: it needs no new configuration at all and leaves the flagship format with no
  destructive protection whatsoever, silently.

### C. Which of a migration's halves is judged

- **C1: the up half** ✔. `ir.CreateTable.Reverse()` **is** `DropTable` and `ir.AddColumn`'s is
  `DropColumn`, so judging a down would report every project that ever created a table and make the
  permission mandatory for `migr down` outright. A rollback is the operator's own instruction, and
  this judges what *applying* a migration would destroy.
- C2: both halves, the literal reading. **Rejected on a measurement**: the repository's own typed
  e2e fixtures put every `dropColumn` in a `down`, so C2 would have flagged four projects that
  destroy nothing on the way forward.
- C3: the up half plus an authored down, exempting only a derived inverse. **Rejected**: a
  distinction nothing in the output could explain to an operator.

### D. What a preview does

- **D1: mark, and exit 0** ✔. `migr plan` marks each operation with what it destroys and says what
  `migr up` will refuse; `--dry-run` counts them. That is CLAUDE.md §5's rule applied without
  exception — a report finds a problem without failing — and it is what gives an operator the list
  before the gate stops them.
- D2: refuse in the preview too. **Rejected**: `migr plan` and `--dry-run` share one pipeline with
  `migr up` (`executor.forward`), so refusing in a preview removes the only command that can tell
  you what to permit. The mode is what separates them, which is [ADR 0033](0033-execution-mode.md)'s
  distinction already.

### E. What names the affected object

- **E1: a type switch in the policy, total over the twenty kinds** ✔, returning the table, the
  qualified column, the view, or for `raw` the statement's first line.
- E2: an `Object()` method on `ir.Operation`, which is the IR package's own principle (*"every
  operation classifies itself, so adding one forces a verdict rather than leaving a gap in a table
  somewhere else"*). **Rejected for now**: twenty methods and an IR contract change for something no
  other package needs yet, and the IR doc's principle is about *classification*, which
  `Destructive()` already carries.

**The `exhaustive` linter does not read a type switch**, which E1 assumed it would. So the guard is
a test instead: `drivertest.Operations` holds one operation per kind and is itself held to
`ir.Kinds()`, so a twenty-first operation with no subject fails `internal/engine`'s suite.

## Decision Outcome

**A1, B1, C1, D1, E1.**

```yaml
validation:
  allow_destructive: [raw]   # drop_table | drop_column | drop_view | alter_column | raw
```

```console
$ migr up
migr: 20260101T000000_add_users.sql: migr cannot read what this migration does, so it cannot say
that it destroys nothing.

  operation 1 (raw) is engine SQL, and migr cannot read what it does: CREATE TABLE users (id bigint);

This project permits no destructive operation at all.

Permit it for one run with --allow-destructive, or for this project by naming the kinds in
migr.yaml, which is a decision a review can see:

  validation:
    allow_destructive: [raw]
```

Three details earned their place by being wrong first:

**The headline is raw's own.** Leading every `.sql` refusal with *"this migration destroys data"*
asserts of a `CREATE TABLE` something migr has no way of knowing — which is the one claim this whole
refusal exists to avoid making on an operator's behalf. `ErrDestructive`'s own one-line text says
*may destroy* for the same reason, since `--json` carries it as the summary.

**`migr init` writes the allowance.** The scaffold commits to `language: sql`, so a scaffolded
project cannot run a single migration without permitting `raw` — and a project that cannot run its
own first migration is the failure the scaffold's `${DATABASE_URL:-…}` fallback already exists to
avoid. It is written with the reason in a comment: a decision the project is making, not a default
being restated, which is what keeps `migr init`'s rule ("only the keys that have no default") intact.

**A resume is not gated.** `engine.Resolve` reaches neither `forward` nor the gate, and that is
deliberate: the schema is already half-changed, and refusing to finish what a permitted run started
would leave a database between states with nothing but the asserted repairs to settle it.

## Consequences

### Positive

- Invariant #7 is enforced for the first time, at both the gate and the report, and the three dead
  pieces (`Destructive()`, the config key, the flag) all have readers.
- The per-kind allowance expresses what a bool could not: permitting `raw` is not permitting
  `drop_table`, and the refusal says which kinds a project already permits.
- The repository's own fixtures became the proof. Only the two `.sql` e2e projects needed a
  statement, because every typed fixture's destruction is in a `down` — which is C1 measured rather
  than argued.

### Negative

- **`validation.allow_destructive` changed type**, so `allow_destructive: false` no longer loads.
  `goccy/go-yaml` refuses it with the line and a caret under the value, which is the diagnostic
  quality M0-T7 chose that parser for; nothing else in the repository had to migrate.
- **A `.sql` project permits `raw` permanently**, which is every destructive operation that format
  can express. That is the honest consequence of migr not reading SQL, and the alternative was
  refusing the format outright.
- **The subject's totality is a test rather than a compiler guarantee**, because `exhaustive` does
  not check type switches.
- **A dry run exits 0 over a run that will be refused.** It says so in a line of its own, but the
  code differs from the run it describes — the same asymmetry `migr status` and `migr up` already
  have over drift.

### Neutral

- `--allow-destructive` is registered on `migr up` and `migr validate` only, so CLAUDE.md §5's
  global-flag list lost an entry it had never had an implementation for.
- A permitted destructive operation is still marked in `migr plan`, with "permitted" beside it: the
  plan reports what a migration does, not only what would stop it.

## Notes

M10-T4 makes lint rules pluggable with `error`/`warn`/`off` severities. This is not one of them, for
the reason M10-T5 records about lock safety: a warning that can be ignored is not a safety
mechanism. A project turns this off by naming the kinds it permits, which is a statement rather than
a silencing.

M11's introspection can narrow `alter_column`, which is destructive here for any retype because the
operation does not carry the column's current type. Comparing against the live schema would make
*narrowing* decidable, and `ir.DestructiveKinds()` is the one place that would change.
