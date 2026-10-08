# 0072. Detecting a rename, and asking before acting on one

## Status

Accepted

## Date

2026-10-08

## Context

[ADR 0070](0070-the-snapshot-differ.md) matched tables, columns and views by name, so a rename
reached the operations as a drop and a create. That is safe only because invariant #7 makes
`drop_table` and `drop_column` destructive — `migr up` refuses a generated migration that destroys
what the project has not permitted — and it is not *useful*: the migration an operator is handed
throws away the data a rename would have kept.

M11-T4's done-when is that *"likely renames are detected heuristically and **confirmed
interactively** rather than guessed — a wrong rename/drop guess destroys data."* Two snapshots that
differ by a rename record exactly what two snapshots differing by an unrelated drop and create
record, so there is no reading of the pair that is not a guess. Detection can propose; only a person
can decide.

The task also fixed the seam: *"`diff.Diff` is a plain function of two snapshots on purpose, so a
confirmed rename arrives as a parameter rather than as a field nothing can yet supply"*
([ADR 0011](0011-driver-contract.md)'s rule).

### What was measured first

Two throwaway probes against all five engines, and one read of the finished pipeline by hand.
Nothing from them ships; all three changed the design.

**An index follows a renamed column and keeps its own name, and a foreign key in another table
follows a renamed table — on every engine.** After `RENAME COLUMN total TO amount`,
`pr_orders_total_idx` covers `amount` on PostgreSQL 16/17, MySQL 8.0, MariaDB 11.4 and SQLite; after
`RENAME TABLE pr_users TO pr_accounts`, the other table's `pr_orders_user_fk` points at
`pr_accounts`. So the rewrite in option C is not a convention this differ invents — it is what every
server does by itself, and a differ that did not do it would propose rebuilding each one.

**A check expression is rewritten by the engine too, and this differ cannot do that.** PostgreSQL
turns `((total > 0))` into `((amount > 0))`; MariaDB turns `` `total` > 0 `` into `` `amount` > 0 ``.
An expression is engine text ([ADR 0065](0065-the-schema-snapshot.md)) and a textual substitution
here would be the guess this package refuses — a column name appears inside string literals and
inside other tables' columns. That decided option C's one exclusion.

**Only a check constraint blocks a column rename, and only on MySQL 8.0.** That server answers
`Error 3959 (HY000): Check constraint 'po_t_chk' uses column 'total', hence column cannot be dropped
or renamed`. MariaDB 11.4 allows it. An index, a unique, a foreign key and a primary key over the
renamed column are all fine on all three servers, and dropping the check first works on all three.
That is what put `drop_check` above `rename_column` in option E, and it is the one place ADR 0070's
mirror moved.

**The first heuristic found nothing at all on the most realistic rename.** Read by hand against a
plausible pair — `users` → `accounts`, with `email_addr` → `email` and `signup_date` →
`signed_up_at` and a new `plan` column — `Candidates` proposed **nothing**, because only `id`
matched by name and the majority floor failed. The operator was handed a `drop_table`. That is the
case this task exists for, so it decided two things: a column is accounted for by type as well as by
name (option B), and detection is asked again once something is confirmed (option G). Afterwards the
same pair reduces from eight operations, one of them destroying the table, to four that destroy
nothing.

## Decision Drivers

- A wrong rename guess and a wrong drop guess are not symmetric: one keeps data that should have
  gone, the other destroys data that should have stayed, and only the second is gated by anything.
- Invariant #8: planning paths are deterministic. `Diff` and `Candidates` are both planning paths.
- Invariant #10: both directions, always. A generated migration's derived down has to *run*.
- CLAUDE.md §11: a database difference belongs in the capability system, not in a branch on a driver
  name. `internal/diff` cannot name an engine, so what MySQL 8.0 refuses has to show up as an
  ordering rule rather than as a special case.
- ADR 0070's shape: `Diff` returns no error, and everything it meets is a refusal rather than a
  failure.

## Considered Options

### A. Where the confirmation sits

- **A1: three layers, with the person between two pure calls** ✔.
  `diff.Candidates(from, to, confirmed)` proposes, `cli.ConfirmRenames(in, out, from, to)` asks, and
  `diff.Diff(from, to, confirmed)` is told. Both `diff` calls stay pure and deterministic, and the
  one that talks to a terminal is the one that is allowed to.
- A2: hand `Diff` a `Confirmer` interface. **Rejected**: it makes a planning path's output depend on
  something other than its arguments, which invariant #8 forbids outright — the same two snapshots
  would diff two ways, `migr plan` and `migr diff` could disagree, and nothing could be golden-tested.
- A3: have `Diff` take the candidates and a decision callback per candidate. **Rejected** for A2's
  reason plus one more: a differ that asks mid-walk cannot be re-run, and `Candidates` is useful on
  its own to a report that proposes nothing.

`Diff`'s signature changes rather than gaining a second entry point, because one function with one
answer is what this package already is; two spellings of "diff these snapshots" is exactly the
duplication M11-T5's own task text refuses for the file writer.

### B. What makes a rename likely

- **B1: a floor per kind, then a greedy best-first one-to-one pairing** ✔. Only a pair that would
  otherwise be a drop and a create is considered at all. A column pair must share a type — a renamed
  column keeps its type, and a pair that does not share one is a drop and an add. A table pair must
  have a **majority of the table being left accounted for**, counting a column matched by name *and*
  value as strong evidence and a column matched by **type alone** as weaker evidence, which is what
  the hand-read forced. Then every possible pair is scored, sorted, and taken greedily, so N losses
  against M gains ask at most `min(N, M)` questions rather than `N × M`.
- B2: no floor, offer every cross-product pair and let the person sort it out. **Rejected**: a
  person shown ten implausible pairs stops reading the plausible one, and a confirmation prompt
  people hold down `y` through is not a confirmation.
- B3: name similarity as the primary signal. **Rejected**: the name is the thing that changed. It is
  kept as the weakest tiebreak and is measurably worth having there — two same-type columns swapped
  out paired `created_at → updated_at` and `user_label → account_label` rather than crossing them.
- B4: require a majority of the table being *joined* to be accounted for as well. **Rejected**: a
  renamed table may gain any number of columns, so that refuses a rename for the unrelated reason
  that the refactor also grew the table.

The score is never shown to anybody. It orders the questions; a prose **evidence** clause answers
them, because what a person checks a rename against is their own intent: *"both are `string(255)`,
neither accepts null, and neither has a default"* is checkable and `0.83` is not.

### C. How a confirmed rename is applied

- **C1: emit the rename, rewrite the schema being changed as the engine would leave it, and hand the
  rest to the differ unchanged** ✔. A rename says the object in one schema named X is the object in
  the other named Y, so the whole feature is a pre-pass: `table.go`, `column.go`, `constraint.go` and
  `view.go` are untouched, and the two schemas then simply agree about the renamed object. The
  rewrite covers the name and every **structured** reference to it — primary key, index, unique and
  foreign key column lists, plus `ReferencesTable` and `ReferencesColumns` on *every* table's keys,
  not only the renamed one — which is exactly what the probes measured each server doing.
- C2: special-case the rename inside the existing matching passes. **Rejected**: it threads a rename
  through five files, and every constraint comparison would need to know about it.
- C3: rewrite check expressions and view bodies too. **Rejected** on the measurement: they are engine
  text, and substituting a column name into SQL is a guess that reaches string literals and other
  tables' columns. A check or a view over a renamed object is therefore proposed as a drop and an add,
  or as a replacement, which lands on the target schema either way — and on MySQL 8.0 the drop is
  *required* regardless.

### D. A confirmed rename the two schemas do not support

- **D1: a `Refusal`, with the drop and the create still proposed** ✔. The parameter arrives from
  outside, so it is checked rather than trusted: a source the schema being changed does not hold, a
  target the target schema does not hold, a source the target schema **still** holds, a name already
  taken, and two renames claiming one object. Each is refused and the diff still has an answer — the
  drop and the create it would have proposed anyway, which invariant #7 gates.
- D2: return an error. **Rejected**: it is the one thing ADR 0070 settled that this package does not
  do, and it would make a stale confirmation fail a whole `migr diff` rather than produce a
  migration with one line of explanation in it.
- D3: trust the caller. **Rejected**: an operator confirms, the schema moves underneath, and the
  rename then names an object that is not there — which the engine would refuse mid-migration
  instead of the differ refusing before one is written.

### E. The emission order, and the mirror that genuinely breaks

- **E1: the renames come first, and ADR 0070's palindrome applies to the other seventeen** ✔.
  A rename establishes the name every operation after it speaks — that is C1 — so going forward it is
  first, and `ir.ReverseAll` therefore puts it **last** coming back, which is exactly where a
  rollback needs it: the rest of the inverse speaks the new name, and the rename restores the old one
  at the end. Two operations may precede a rename, and each records something the rename would
  otherwise move out from under it: a dropped view's recorded body names the old table, and a dropped
  check's recorded expression names the old column — which is also what MySQL 8.0 requires. Every
  other drop records a column list read off the renamed schema, so those follow.
- E2: keep the order exactly palindromic. **Impossible, not merely rejected.** Both renames are
  self-inverse, and a self-inverse kind preserves a mirror only at its exact center — which
  `alter_column` holds. A test asserts that neither rename is in the mirrored core, so a future
  attempt to tidy them into it fails rather than quietly costing the property.
- E3: a topological sort. **Rejected** for ADR 0070's reason, unchanged: no operation carries a
  foreign key inline, so every real dependency is a boundary between two kinds.

The check moves ahead of the foreign key at **both** ends — `drop_check` above `drop_foreign_key`,
`add_check` below `add_foreign_key` — so the swap is symmetric and the core's mirror survives it.

**This is measured rather than argued.** With the renames moved to the end of the order, the
conformance suite's rename leg first *passed*: a gap that is nothing but names has nothing for the
renames to be out of order with. The leg now also adds a column to the renamed table and an index
over the renamed column, each of which speaks a name only the rename above it establishes — and with
the renames last the server itself refuses the list: `no such table: migr_diff_moved`.

### F. The prompt's default

- **F1: no, with the asymmetry as the argument** ✔. A declined rename becomes a drop and a create,
  which invariant #7 gates — `migr up` refuses it until the project permits that kind. A confirmed
  rename is `Destructive() == false` and is gated by **nothing at all**. So the answer nothing else
  checks is the one a person has to type: an empty line, an unrecognized answer re-asked and then
  abandoned, a stream with nothing left in it, all decline.
- F2: default to yes, since detection already applied a floor. **Rejected**: it puts the ungated
  outcome one keystroke from an operator who was not reading, which is the failure mode the
  done-when names.
- F3: offer "yes to all". **Rejected**: that is a guess wearing a confirmation's clothes for every
  candidate the operator did not read. `q` is the safe bulk answer, because what it bulk-applies is
  the side that has its own gate.

Each prompt states **both** outcomes — the rename and the drop-and-create that declining means —
because that is the choice. A prompt reading `rename x to y? [y/N]` hides what the alternative costs.

A stream that ran out and an operator typing `q` are reported apart, because they are different
facts: one is somebody stopping, the other is nobody having been there. Both decline. And a prompt
with **no streams at all** is `ErrNotConfirmable` rather than a silent no — the one place declining
would be wrong, since the caller would read it as an operator's own answer.

### G. A rename inside a rename

- **G1: `Candidates` takes what is already confirmed and proposes one round deeper; the prompt drives
  the rounds** ✔. A column rename inside a table that may itself be renamed is a candidate
  conditional on another candidate, which is not a question anybody can answer. So detection reads a
  state where the confirmed renames have been applied — the same rewrite C1 does — and
  `ConfirmRenames` asks again after each round that confirmed something. It terminates because a
  round that confirms nothing ends it, and it never re-asks, because the prompt is the only thing
  that knows what it has already put to somebody.
- G2: one pass, and say in the docs that the compound case is not detected. **Rejected on the
  measurement**: that compound case *is* the realistic one — renaming a table is when a project
  cleans up its column names — and the hand-read showed the result was a `drop_table`.
- G3: propose the column renames inside a *candidate* table pair in the same round. **Rejected**:
  confirming the column rename while declining the table rename is a sensible-looking pair of answers
  that produces a refusal, since the column rename would then name a table the schema being changed
  does not hold.
- G4: put the loop in the caller. **Rejected**: the caller does not know which candidates were
  declined, so every round would re-propose them forever.

### H. Rejected outright

- **A confidence number in the prompt.** See B.
- **Matching by name similarity alone.** See B3.
- **A `Rename(snapshot, confirmed)` exported for callers to apply renames with.** It would have only
  one snapshot, so its refusals would be a strict subset of D1's — a second, weaker contract for the
  same question. The conformance suite writes its own target instead, which also stops the thing
  under test from writing its own expectation.
- **Emitting `raw` to close what a rename cannot express.** Unchanged from ADR 0070.

## Consequences

`internal/diff` gains `rename.go` (the model, the candidates, the pairing), `likeness.go` (the two
floors and the evidence) and `applied.go` (the rewrite and the five refusals). `order.go` now states
a `core` of seventeen and derives `order` by inserting the two renames; `neverEmitted` keeps `raw`
alone. `internal/cli` gains `rename.go`, whose one caller is **M11-T5** — writing it there would have
left this task's done-when unmet, and the policy in F is this task's to decide rather than that
command's.

`Diff`'s signature changes, and the two call sites in `test/conformance/driver` pass `Renames{}`.

Four things are now asserted that were not:

- every rename fixture is a `comparison`, so ADR 0070's properties — every operation validates, the
  list inverts, `engine.Unrestorable` permits it, the same pair proposes the same thing twice — all
  cover a confirmed rename without any of them naming one;
- a reversed list's renames come last, and neither rename is in the mirrored core;
- only `drop_view` and `drop_check` may precede their rename, each with the payload reason;
- and the conformance suite's **fourth leg** runs the whole thing on an engine: detect both rounds,
  confirm, assert nothing destructive is proposed and the renames lead, apply, compare, then apply
  `ir.ReverseAll` of the forward list — which is what `internal/engine` hands a rollback when the
  migration `migr diff` wrote authored no down. SQLite runs it untagged; PostgreSQL and MySQL run it
  under `integration` against all four servers.

What is still a limitation, stated rather than designed around: a check constraint or a view over a
renamed object is proposed as a replacement, because its body is engine text. The engine rewrites its
own, so the proposed drop and add is redundant work rather than a wrong result — and on MySQL 8.0 the
drop is required anyway.
