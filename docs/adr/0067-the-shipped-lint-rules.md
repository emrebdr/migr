# 0067. The shipped lint rules: what each reports, and what each declines

## Status

Accepted

## Date

2026-10-04

## Context

[ADR 0066](0066-lint-rules.md) built `internal/rule` and shipped two rules, because a framework with
no consumer proves nothing. It closed by naming three things M10-T5 would inherit, and this is the
task that spends them:

> M10-T5 inherits three things […]: that its row-count rule cannot know whether a table is empty and
> must say so; that its *"data migration mixed into a schema migration"* rule fires on **this
> repository's own TypeScript e2e fixture**, which execs a `CREATE TABLE` beside an `addColumn` on
> purpose, so it should arrive as a `warn`; and that `Subject.Undo` is what makes its *"missing down
> migration"* rule expressible.

Nothing in the framework moves. `Subject` already carries both halves with their `Direct` statements
and an `Undo`; `Set` already sorts, stamps and skips a rule set to `off`; `internal/config` already
validates ids against the registry; `internal/cli` already prints warnings and names the rule in the
column that says what found a problem. This ADR records what the three rules *judge* and, more
importantly, what each declines to judge — because for all three of them the declined cases are the
design.

### What was measured first

**Nothing in `make check` moves when the three rules are registered.** Probed in two steps: three
rules judging nothing, then the real ones. The only failing assertion either time was
`TestTheShippedRulesAreTheOnesThisTaskClaims`, the one literal list — every other test over the set
is driven from `rule.IDs()`, `rule.Defaults()` or `All()`. The staged `internal/cli` projects are
either `.sql` or scripted with raw operations, and the one typed act answers its own down, declares
no `add_column` and sends no statement of its own.

**Three of this repository's own fixtures do trip a rule**, and all three on purpose:
`20260727T160100_add_e2e_go_balance.go` and `20260727T170100_add_e2e_ts_balance.ts` omit their `Down`
so invariant #10's derivation runs against a real database, and
`20260727T170300_audit_e2e_ts_accounts.ts` execs a `CREATE TABLE` beside an `addColumn` to prove
ADR 0032's warning. Measured: the Go project reports one warning and the TypeScript project two,
both still exiting 0. Both suites now assert it, which is the only place these rules meet a real SDK
over a real runtime.

**`host.db.exec` under `mode: plan` never reaches the session** — `Server.exec` holds the statement
and answers `executed: false` before touching `opts.Conn`. So `Half.Direct` is populated in a command
that opened no database, which is what makes `mixed-data-migration` work offline at all.

## Decision Drivers

- M10-T5's done-when, which names the three remaining rules.
- ADR 0064 C: `migr validate` holds no connection *for the migrations*, with or without `--connect`.
- ADR 0066 C3: a rule judges one migration, because a report names one.
- ADR 0066 E: `migr up` never reads a rule, so `error` means *fail CI* and nothing more.
- CLAUDE.md §11: *"prefer an honest `Unsupported` over a clever emulation"* — the same instinct
  applied to a rule, which should report what it knows and say what it does not.

## Considered Options

### A. `missing-down` — which answer to "what undoes this" is reported

`Subject.Undo` has five values, and exactly one of them is this rule's.

- **A1: `UndoDerived` alone** ✔. The migration authors no down and the engine inverted its up
  instead. That is a real risk and nothing else reports it: at rollback time the inverse is derived
  from whatever the up emits *then*, which ADR 0009 permits to differ from what it emitted when the
  migration was applied. The finding names the down half with no operation index — the first
  whole-half finding any rule produces, which the existing renderer already handles, `CheckFormat`
  and `CheckReverse` problems having none either.
- A2: also `UndoNone`. **Rejected**: `CheckReverse` already reports it, and two sentences about one
  defect under two different names is how a report stops being read (ADR 0028).
- A3: also `UndoUnread`. **Rejected**: that is a `format` problem — the down could not be read, which
  is a different statement from no down having been written.
- A4: also `UndoDeclared`. **Rejected**: the author answered the question. `-- migr:irreversible:`
  and `Irreversible()` exist precisely so a one-way migration is a declaration rather than an
  omission; flagging it would make the declared form worse than the undeclared one.

### B. `missing-down`'s default severity

- **B1: `warn`** ✔. Derivation is a feature invariant #10 provides, exercised deliberately by two of
  this repository's own fixtures and by `internal/engine`'s whole `derive` path. What is missing is
  not correctness but a human having reviewed the inverse. A project that wants every down authored
  sets it to `error`, which is exactly what per-rule configuration is for.
- B2: `error`. **Rejected on the measurement**: it would flip the Go e2e project, the TypeScript
  project and the mixed project to exit 2 over a path the framework exists to provide, and either
  those fixtures lose the coverage or every project pays for a policy one project wanted.

### C. `not-null-without-default` — what it reads, and the one thing it can know

- **C1: `ir.AddColumn` with `Nullable` false and no `Default`** ✔, with the detail saying migr opened
  no database and could not count the table's rows. The warning's content *is* that uncertainty:
  against a table holding any row the server refuses the statement, and against an empty one it is
  fine. Remedy in the same line — give it a default, or add it nullable, backfill, and tighten it in
  a later migration.
- **C2: never a column of a table this migration creates** ✔. That table holds nothing, so the
  constraint has nothing to fail on. `createdIn` collects `ir.CreateTable` tables exactly as
  `keyedIn` collects `ir.AddPrimaryKey` ones, and it is order-blind for the same reason. This is the
  **only** inference about data anything in `internal/rule` makes, and it is structural rather than a
  guess — which is why `ir.CreateTable`'s own NOT NULL columns are not read either, and why this rule
  reads one operation where `unbounded-string` reads three.
- C3: also `ir.AlterColumn` setting `Nullable` to false. **Rejected on merit, not scope.** That is a
  real hazard and a *different* one: the statement fails because existing rows hold NULL, and a
  default does not fix it — `SET DEFAULT` backfills nothing. Folding it in would give wrong advice
  under an id that promises a default is the answer. It is its own rule if anyone wants it, and
  deliberately not this one.
- C4: report it at `error`, since the statement really does fail against a non-empty table.
  **Rejected**: the rule cannot tell whether the table has rows, and a gate that fails CI over
  something it is guessing about is how a project learns to switch rules off. `warn` is what a
  judgement with a stated hole is worth.

### D. `mixed-data-migration` — when a migration is doing two jobs

- **D1: both operations and `Direct` statements in the up half** ✔. One finding per migration,
  counting both. The detail says what the pattern costs — ADR 0032's window: those statements commit
  on their own before the operations' transaction opens, so a failure afterwards leaves them behind
  and no rollback reaches them.
- D2: any migration that sends a statement of its own, mixed or not. **Rejected**: a migration doing
  only the data half is a data migration, which is M12's, and the engine already refuses one that
  compiles to no statements. The rule's name and the done-when both say *mixed*.
- D3: the down half too. **Rejected**: a down's own statements are the author undoing their own data
  change, which is what you want if the up mixed. All five rules judge the up half alone, which is
  one sentence rather than five exceptions.
- **D4: count the statements, never quote them** ✔. `migr plan` already counts bound arguments
  rather than interpolating them, for the reason that printing SQL which is not the SQL that runs is
  worse than printing none. A test asserts the detail carries neither statement's text.

## Decision Outcome

Three rules, one file each, all defaulting to `warn`:

| Rule | Default | Reports |
|---|---|---|
| `missing-down` | `warn` | `Undo == UndoDerived`: the migration authors no down, so a rollback inverts whatever its up emits when the rollback runs. One finding per migration, naming the down half |
| `not-null-without-default` | `warn` | an `ir.AddColumn` that is NOT NULL with no default, to a table this migration did not create. One finding per column, saying that the table's rows were not counted |
| `mixed-data-migration` | `warn` | an up half holding both schema operations and statements the migration sends itself. One finding per migration, counting both |

The set is now five, with `missing-primary-key` the one `error` among them — so the severity axis is
carried by the defaults rather than by anything a project has to configure to see it.

**And no rule can fire on a `.sql` project, which is now three facts rather than one.** ADR 0066
closed with *"every statement there being `ir.Raw` with no columns to read"*, which covers three of
the five. `mixed-data-migration` is silent because that format has no `host.db.exec` to send, and
`missing-down` is silent because such a project never reaches `UndoDerived` at all — a raw operation
has no inverse, so deriving one always fails and `CheckReverse` is what reports it. A test constructs
all three shapes a `.sql` migration can take rather than asserting the sentence.

## Consequences

### Positive

- All five of M10-T5's done-when rules exist, and `migr validate` now says something useful about a
  migration that is correct but unwise — which is the gap between `migr up`'s three gates and what a
  review would catch.
- `not-null-without-default` demonstrates the shape ADR 0064 C forced: a rule that reports what it
  sees and *states its own hole* in the detail, rather than either guessing or staying silent.
- Two tagged suites now assert a warning against a real SDK over a real runtime, so the rules are
  measured against migrations a person wrote rather than against constructed IR alone.
- The one inference about data — a table this migration creates is empty — is structural, so it
  cannot be wrong, and it removes the rule's most common false positive (a new table's own columns).

### Negative

- **`migr validate` is noisier by default for a project that derives every down.** A project that
  relies on derivation deliberately sets `missing-down: off`, and that line is the statement ADR 0066
  F wants it to be. The alternative was a rule nobody could discover.
- **`not-null-without-default` will warn about safe migrations.** A NOT NULL column added to a table
  that happens to be empty in production is fine, and this rule cannot know that. The detail says so,
  which is the most it can do without a connection it is not given.
- **`mixed-data-migration` cannot distinguish a data backfill from an escape-hatch DDL.** Both arrive
  as `host.db.exec`, and ADR 0032's cost is the same for either, so the warning is honest — but the
  name says "data" where the fixture that trips it execs a `CREATE TABLE`.
- **`missing-down` reports nothing about the quality of the derived inverse.** A derivation that
  would fail is `CheckReverse`'s to report; this rule fires exactly where the derivation *works*,
  which reads oddly until you have the Undo vocabulary in mind.

### Neutral

- No contract moved: no new `Check`, no new field, no config key, no rendering change, no exit code.
  Three files and a three-line `builtIn()`, which is what ADR 0066 B1 said adding a rule would cost.
- `migr init` still writes nothing about rules: every one has a default (CLAUDE.md §9).
- A sixth rule is the same three-line change. What it cannot be is anything needing the whole pending
  set or a live database — both limits are in `internal/rule`'s own doc, and C3 of ADR 0066 is where
  the first of them would be lifted.
