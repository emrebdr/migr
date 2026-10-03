# 0066. Lint rules: what a severity may decide, and what it may not

## Status

Accepted

## Date

2026-10-03

## Context

[ADR 0028](0028-validation-report.md) built `internal/validate` to take another check, and two tasks
have since added one by writing a rule and a renderer rather than a command: M10-T1's `destructive`
and M10-T2's `capability`. The check list, the per-migration phases, the `Problem` shape with its
operation index and kind, the `--json` document and exit code 2 were all already there.

What was not there is any notion of **severity**. All five existing checks are failures by nature —
a compile refusal is not a warning — so `Report.Problems()` has meant exactly *"what makes this run
exit 2"*, and `migr validate`'s `finish` reads its length and nothing else.

M10-T4's own note is the binding constraint, and it points away from the obvious design:

> **M10-T1 is the contrast, not the precedent** ([ADR 0063](0063-destructive-operations.md)).
> Destructive detection is a hard gate for the reason M10-T5 gives about lock safety: a warning that
> can be ignored is not a safety mechanism. A project turns it off by naming the kinds it permits,
> which is a statement rather than a silencing — so whatever `off` means here, it must not become a
> second way to say that.

Two facts found while reading decided the shape:

- **`internal/lint` is already taken**, by this repository's own `go/ast` comment checker that
  `make lint` runs through `internal/lint/cmd`. It is named in the Makefile, the README, two
  `.golangci.yml` comments, `sdk/typescript/README.md`, and in the prose of ADRs 0039 and 0041 —
  which are immutable records.
- **No task asks for third-party rules.** M15 gives a plugin protocol to *drivers* (M15-T1) and to
  nothing else, so "pluggable" here means in-process: a value M10-T5 appends to a list.

### What was measured first

Three probes, nothing from them committed.

**Both encoders sort map keys.** `migr config` marshals the whole `Config`, so a
`map[rule.ID]rule.Severity` renders through `goccy/go-yaml` and `encoding/json`. Both emit
alphabetical order, stable across five runs, so the resolved rules need no `MarshalYAML` of their
own and a test can hold the order.

**A bare `off` survives YAML 1.1's boolean coercion.** `off` is a boolean in YAML 1.1, so
`rules: {unbounded-string: off}` could plausibly have arrived as `false`. Decoded into a
string-kinded type, goccy answers `"off"` — and so do `no`, `false` and `OFF`, each as its own
literal text, which the severity check then refuses by name. Writing the resolved value back
re-quotes it as `"off"`, so a round trip through `migr config` is stable.

**No existing fixture trips either shipped rule.** The four typed `cmd/migr` e2e projects all
declare `PrimaryKey("id")`, no fixture declares a `text` column, and every `.sql` project compiles
to `ir.Raw`, which carries no columns to read. Confirmed by running the whole suite with both rules
at their defaults before any fixture was touched.

## Decision Drivers

- M10-T4's done-when: *"rules are pluggable and individually configurable in `migr.yaml`, with
  severity levels (error / warn / off)"*.
- The note above: `off` must not become a second way to permit destruction.
- CLAUDE.md §5: *"A report exits 0 having found a problem"*, and the exit-code rule from
  [ADR 0029](0029-exit-codes-and-failure-output.md).
- CLAUDE.md §9: *"`migr init` writes only the keys that have no default, and `migr config` prints
  the resolved set."*
- Invariant #8: deterministic, no map-iteration order in anything that reports.

## Considered Options

### A. Where the framework lives, and what configures it

- **A1: `internal/rule`, configured under `validation.rules`** ✔. A leaf package holding the
  contract, the three severities, the registry and the built-in rules — the shape `internal/capability`
  has, which is the precedent that matters: `internal/config` already validates
  `safety.max_lock_impact` against `capability.Ceilings()`, and rule ids need exactly that treatment.
  A leaf keeps the config package's graph light (`config → rule`, not `config → validate →
  engine/plan/driver`), and `validation.rules` sits beside `validation.allow_destructive` so one
  command's configuration stays in one block. `rule.Rule` stutters, as `driver.Driver` and
  `migration.Migration` already do.
- A2: rename the comment checker to `internal/commentlint` and take `internal/lint`, with a
  top-level `lint:` block. **Rejected on cost**: the Makefile, the README, two linter comments and
  the TypeScript README all name that package, and ADRs 0039 and 0041 name it in prose — those are
  never edited, so the rename would leave two records pointing at a package that no longer exists.
- A3: the contract in `internal/validate` with rules in `internal/validate/rule/`, per §9's
  *"interfaces are defined in the consuming package"*. **Rejected**: `internal/config` must validate
  rule ids, and that would drag `engine`, `plan` and `driver` into the config package's import
  graph to read two string vocabularies.

### B. What a rule is

- **B1: a struct with a func field** ✔. `Rule{ID, Default, Summary, Judge}` — the identity, the
  default severity and the one-line summary are *data*, and only the judgement is behavior. M10-T5
  then appends values to one list and writes one file per rule, with no switch to extend and no
  `init` to remember. `capability.Rule` is the func-type precedent.
- B2: an interface with four methods. **Rejected**: it buys nothing a struct does not, and costs
  four method declarations per rule for three constants.
- B3: registration in `cmd/migr`, the way drivers and runtimes are wired. **Rejected**: invariant #1
  is about *concrete implementations of engine and language behavior*, and a rule names neither —
  it reads `ir.Operations`. Nothing outside the binary can add one, so a registry with an error
  return would be ceremony around a slice literal.

### C. What a rule judges

- **C1: one migration, both halves, plus where its down came from** ✔. `Subject` carries the
  migration, the up and down `Half`s (operations *and* the statements the migration sends itself,
  which ADR 0036 made the other half of what a migration does), and an `Undo` saying whether the
  down was authored, derived, declared irreversible, impossible, or unreadable. Those five are what
  `validate.undo` already distinguishes, and `Undo` is what makes M10-T5's *"missing down migration"*
  expressible at all — `CheckReverse` reports a migration nothing *can* undo, which is a different
  statement from one whose author wrote no down.
- C2: one half at a time. **Rejected**: a rule seeing a derived down cannot tell it from an authored
  one, so the first rule M10-T5 lists could not be written.
- C3: the whole pending set, so a rule could see that a later migration adds the key this one omits.
  **Rejected for now**, and the limit is written down rather than discovered: a report names one
  migration, and `missing-primary-key`'s own detail says it judged this migration alone.
- **C4: nothing live** ✔. M10-T5 wants *"`NOT NULL` without a default on a non-empty table"*, which
  needs a row count. [ADR 0064](0064-capability-preflight.md) C settled that `migr validate --connect`
  buys the capability set and nothing else, and that migrations are asked over `driver.Offline`. So
  that rule will report what it can see and say it cannot know whether the table has rows — which is
  what a `warn` is for.

### D. Where a warning lands

- **D1: a third list** ✔. `Report` gains `Warnings` beside `Problems` and `Unchecked`, so the three
  halves stay *judged-and-failed*, *judged-and-noted* and *not-judged*. `Problems` keeps meaning
  "this run exits 2", so `finish`, `Clean()`, `Migrations()` and every existing `--json` consumer are
  untouched. It is how ADR 0040 added `Unchecked` rather than widening `Problem`.
- D2: a `Severity` field on `Problem`, with `finish` exiting 2 only for errors. **Rejected**: it
  changes what the `problems` array *means* underneath every consumer already counting it, including
  this repository's own tests and the five checks that have no severity.

### E. Whether `migr up` reads the rules

- **E1: no — `migr validate` alone** ✔. The run's three gates (drift, destruction, capability) are
  each about whether an operation is correct or permitted. A rule is a practice judgment, `error`
  means *fail CI*, and exit 2 from the command built for CI is exactly that. Refusing a deploy over
  `missing-primary-key` is how a project learns to set everything to `off`.
- E2: an `error`-severity rule gates the run too, reusing ADR 0063's one-policy-two-callers shape a
  third time. **Rejected on the task's own note**: it would make `warn` and `off` the only ways to
  deploy, which is the silencing pressure the note warns about, and the engine would start refusing
  migrations for reasons that are not about the database.

### F. How `off` is kept away from destruction

All three at once, in increasing order of how mechanical each is:

- **Destruction is not a rule.** It stays `CheckDestructive`, read from `engine.Destructive` by the
  validator and by `forward`, so no severity can reach it.
- **An unknown rule id fails the config load**, naming every id that exists. So `rules: {drop_table:
  off}`, `{raw: off}` or any other hopeful spelling is a refusal rather than something that appears
  to work. This is the mechanism that matters: the failure mode was never a rule *about* destruction
  — it was a line in `migr.yaml` that reads like permission and silently does nothing.
- **A test holds the sentence**: a project with every rule `off` and an unpermitted `drop_table`
  still gets the `destructive` problem from `migr validate` and is still refused by `migr up` with
  exit 2, having run no statements.

## Decision Outcome

`internal/rule` ships the contract, the three severities, the kebab-case id grammar, `Set` with its
constructor refusals, the registry, and two rules. `internal/validate` gains `CheckLint`,
`Problem.Rule`, `Report.Warnings()`, `Options.Rules *rule.Set` and a lint pass that runs once per
migration after the five checks. `internal/config` gains `validation.rules`, defaulted to every
shipped rule's own default and validated against the registry. `internal/cli` renders the warnings
section, prints the rule id in the column that says what found a problem, carries `rule` and
`warnings` in `--json`, and prints a remedy naming the rules that **failed** — not the ones that
warned, because a remedy explains how to get past something that stopped the command, and advice to
silence advice is not that.

The two rules were moved out of M10-T5's list:

| Rule | Default | Why that default |
|---|---|---|
| `missing-primary-key` | `error` | M13 copies a table by identifying its rows, so a keyless table is a future hard failure rather than a style preference. It judges one migration, reading both the inline key and any `add_primary_key` in the same migration |
| `unbounded-string` | `warn` | `text` is a legitimate choice for a body or a note, so this is advice. It reads `create_table`, `add_column` and a retyping `alter_column` |

Both judge the **up half only**: inverting a `drop_table` re-creates the table it recorded, and
flagging that would report the same column twice. Neither can fire on a `.sql` project, every
statement there being `ir.Raw` with no columns to read — the mirror of ADR 0064's closing note.

## Consequences

### Positive

- A sixth check with two real consumers, and `migr config` lists every rule and its resolved
  severity, so the ids are discoverable without a `migr lint --list` that does not exist.
- The severity axis is observable rather than asserted: the two shipped rules default differently,
  so one failing run shows a problem and a warning side by side.
- `off` cannot silence a hard gate, and the one spelling that could have *looked* like it does
  — naming an operation kind under `validation.rules` — fails to load.
- A warning costs no exit code, so `migr validate` stays usable in CI for projects that have not yet
  acted on every rule.

### Negative

- **`error` is a weaker word here than anywhere else in migr.** Everything else that exits 2 is
  wrong; a rule at `error` is merely something this project decided it will not ship. That is the
  price of E1, and the alternative was worse.
- **A rule cannot see across migrations**, so `missing-primary-key` reports a table whose key a
  *later* migration adds. A project doing that sets the rule to `warn`, and the detail says the rule
  judged one migration. C3 is where that is fixed, if anything ever needs it.
- **`Problem` carries warnings too.** The type's name says "problem" for a value sitting in the
  warnings list; what distinguishes them is which list they are in. Naming a second identical struct
  was the alternative and is worse.
- **`Options.Rules` is a pointer whose nil means "no lint pass"**, which is a quiet default: a
  caller that forgot to build a set gets no lint rather than an error. `ErrUnbuiltRules` catches the
  *zero* set, and the CLI tests catch the forgotten one, but the hole is real and is the same shape
  ADR 0064 accepted for `Capabilities`.

### Neutral

- `migr init` writes nothing new: every rule has a default, so §9's rule says the scaffold stays as
  it is.
- No new exit code. A lint `error` is exit 2 like every other validation failure, and a warning is
  exit 0 — one row added to `internal/cli/failure_test.go`'s matrix, for a rule id nothing ships.
- `internal/lint` keeps its name and its job. A reader meeting both packages has `internal/rule` for
  what migr checks about migrations and `internal/lint` for what this repository checks about itself.

## Notes

M10-T5 inherits three things, recorded in TASKS.md where someone will be standing when they need
them: that its row-count rule cannot know whether a table is empty and must say so; that its
*"data migration mixed into a schema migration"* rule fires on **this repository's own TypeScript
e2e fixture**, which execs a `CREATE TABLE` beside an `addColumn` on purpose, so it should arrive as
a `warn`; and that `Subject.Undo` is what makes its *"missing down migration"* rule expressible.
