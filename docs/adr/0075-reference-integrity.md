# 0075. Judging the references migrations make against two projections rather than one

## Status

Accepted

## Date

2026-10-09

## Context

M10-T3's done-when is that *"adding a column to a nonexistent table, or a foreign key to a
nonexistent column, is caught before execution."*

Both are already `fold.Refusal`s with the right wording, because
[ADR 0073](0073-the-schema-projection.md) built `internal/fold` so that this check would be what
reads it: `Forward` *folds and does not validate*, so `addColumn` onto a table nothing created gives
*"this schema holds no such table"* and `referenceable` gives *"it references column %q, which this
schema does not hold"*. So this task is a seventh `validate.Check` that reads them, and the whole of
the problem is **what to fold from**.

[ADR 0064](0064-validating-against-a-server.md) C2 rejected the obvious answer. Checking against the
live catalog calls a correct migration broken whenever the migration that creates its table has not
run yet, and M10-T2 left `migr validate --connect` deliberately *not* introspecting for that reason,
with a test asserting it — *"the projection that would fix that has no task"*. M11-T5 built the
projection. This is that task.

### What was measured first

Four probes against SQLite and one coherent hand-built project; nothing from them ships, and each
decided code. Two of them decided the whole design.

**1. A coherent project folds clean; the repository's own fixtures do not.** Folding
`test/conformance/runtime`'s twelve manifests in version order refuses operations on `orders` and
`legacy` — tables no fixture creates, that set exercising one operation per file rather than
describing a sequential project. A hand-built coherent project, both halves, authored downs and a
derived `ir.ReverseAll` inverse alike: **zero** refusals.

**2. Folding already-applied operations onto the live schema they produced refuses every one of
them.** `create_table users` → *"this schema already holds a table of that name"*; `add_index` →
*"it already carries an index"*. The same list from an empty snapshot refuses nothing. **So the live
schema alone is not a basis** — it is ADR 0064 C2 reached from the other side.

**3. Pending operations folded onto the live schema refuse exactly the real defects.** A correct
`create_table orders` plus a key to `users.id` that only the live schema can satisfy, beside an
`add_column` on a table nothing holds and a key to a column nothing holds: refused precisely the
latter two, which are the done-when's two cases.

**4. The project's own beginning alone is not a basis either, and `migr diff` is what proves it.**
Running the check offline over this repository's own SQLite diff suite — apply, drift the database
behind migr's back, `migr diff`, then validate what it wrote — refused **all three** operations of
the generated migration:

```
reference  up operation 1 (drop_column): "people": it holds no column "stray"
reference  up operation 2 (add_column):  "people": it already holds a column "note"
reference  up operation 3 (add_index):   "people": it already carries an index "idx_people_email"
```

A drift correction re-adds an index the project already created and drops a column the project never
had, so **replaying it from nothing cannot work** — and a check reporting that would have `migr
diff`'s own output fail `migr validate`, which is the tool disagreeing with itself.

**`Forward` does not mutate the snapshot it is given**, confirmed over `add_index`, `drop_index`,
`drop_primary_key`, `add_column` and `drop_table`: `Canonical()` clones every slice through
`sortedByName`. That is what lets the down half fold onto the post-up state with no copy.

## Decision

### A. Two projections, and a reference is reported only where every one of them refuses

```go
const (
    FromProject  = "the project's own beginning"
    FromDatabase = "the database as it is"
)
```

Probes 2 and 4 are each other's mirror, and together they settle it:

| Starting point | A migration that has run | A pending migration with a real defect | A reference only the database satisfies | A `migr diff` drift correction |
|---|---|---|---|---|
| the live schema | **refused** — wrong | refused | clean | clean |
| the project's beginning | clean | refused | **refused** — wrong | **refused** — wrong |
| **both** | clean | **refused** | clean | clean |

So the check folds each half through every projection it has and reports what *all* of them refused,
matched on the operation's index within that half. That is the question stated precisely: **a
reference that cannot be satisfied from the project's own beginning, and cannot be satisfied by the
database as it is either.** Three things follow rather than being arranged:

- an applied migration is silent, its conflict appearing in the live run alone;
- a project that legitimately builds on objects no migration of its own creates — migr adopted into
  an existing database, the ordinary case — is silent;
- **a genuine duplicate is still caught**: two migrations creating one table, against a database
  holding neither, is refused by both runs. Suppressing conflicts as a *class* would have lost that,
  which is why there is no such class.

Where two runs refuse one operation for different reasons, the one reported is the **live** run's,
since that is the state the migration will actually meet. The projections are therefore ordered, and
a test holds the order.

### B. The check needs the database, and does not run without it

`projectionsFor(nil)` returns nothing, so a run with no connection judges no references and the
report says so — the sentence `capability` already owed, widened to name both checks:

> Neither what this server refuses nor the references the migrations make was checked;
> --connect asks the one migr.yaml names.

That is ADR 0064's shape unchanged: a check needing the target database says so rather than
answering from what it could reach. It is also the only honest reading of probe 4 — an offline run
has one basis, and one basis is wrong in a way that would make the tool contradict itself.

The task text says *"`--connect` is optional here"*, and that is true of the **mechanism**:
`fold.Forward` starts from whatever it is given, so nothing breaks without a connection. It is not
true of the findings, and the task's own primary framing is the one that holds — *"this needs the
target database … beside `capability`"*.

### C. `internal/fold` is unchanged

An earlier design gave `fold.Refusal` a `Cause` — absent, conflict, unfoldable, malformed — so the
check could suppress conflicts. Rejected: it is 46 refusal sites to thread, and it suppresses the
real duplicate along with the spurious one. The intersection needs nothing from that package, which
is what ADR 0073 intended by *"the refusals **are** this task's findings"*.

### D. A raw operation ends the projection: one `Unchecked`, and the check stops

Engine SQL has no readable effect, so the schema after it is unknowable by folding — but the fold
itself carries on, refusing that operation and folding the next, which would leave every later
migration judged against a schema that is quietly wrong. So the first raw records one `Unchecked`
naming the migration and operation, says the references were not checked **and that no migration
after it was judged either**, and switches the check off for the rest of the run.

A `.sql` project therefore reports **one** entry rather than one per migration, every statement in
that format being `ir.Raw`. That is ADR 0040's half, which the task expected, rather than `migr
diff`'s whole-command refusal — a validator reports what it could not judge.

### E. Both halves, the down onto the state its own up just left

`destructive` judges the up alone; `compile` and `capability` judge both. This judges both, and the
down folds onto the post-up state because that is where a rollback meets it: a rollback runs newest
first, so when migration N's down runs the schema is the state after N. What it leaves is discarded,
which probe 4's immutability finding is what makes free.

It catches an authored down whose references are wrong, which nothing else does — `compile` is
schema-blind and `reverse` only asks whether an inverse exists at all. And it costs nothing on
correct input, measured.

**The down is judged only where the up projected cleanly.** A down is derived from the up where its
author wrote none, so a broken up makes a broken down: the first by-hand read reported one typo
twice, once per direction, and that is two complaints about one defect.

### F. A finding is exit 2

*The argument is wrong → 2; the world is not ready → 1* (ADR 0029). A finding here is refused from
the project's own beginning **as well as** from the database, so it is the migration at fault and
not the database's state — which is a further consequence of A rather than a separate rule.

## Alternatives rejected

- **Read the history and fold only the pending migrations onto the live schema.** The most precise
  answer, and the task text's own wording. Rejected because the intersection is correct in every
  case measured without it, and this would have `migr validate` read history — which ADR 0027 says
  it does not — and owe a reading of `history.ErrNoTable` that CLAUDE.md calls the worst failure in
  the contract.
- **The live schema alone.** Probe 2: refuses every operation of every migration that has run.
- **The project's own beginning alone.** Probe 4: refuses every operation of a migration `migr diff`
  wrote. Ten of this repository's own test fixtures also describe schemas that cannot exist, which is
  the same signal from a different direction.
- **A `Cause` on `fold.Refusal`.** C.
- **Reporting offline findings as warnings.** `Warnings` is what a lint rule set to `warn` found and
  is tied to `rule.Severity`; widening it to carry a check's findings would make the one channel mean
  two things, and a warning in CI is read by nobody anyway.
- **Running the fold once and classifying afterwards by what the projected schema holds.**
  `Refusal.Subject` is a table name for some kinds, a constraint name for others and a qualified
  column for others, so there is nothing to look up uniformly.

## Consequences

`migr validate --connect` introspects, which M10-T2's `TestValidateSaysNothingAboutWhatOnlyARunCanJudge`
asserted it must not. That test is inverted rather than deleted: the half that mattered — a
migration whose `create_table` the live schema contradicts is **not** reported — is unchanged, and
is now held by the mechanism rather than by not looking.

Five of `internal/validate`'s own fixtures and one of `internal/cli`'s staged operations against
tables nothing creates, and are now coherent: the destructive fixture's down *undoes* its up instead
of repeating it, and the emulated and capability fixtures stage the column they alter. That is
better test data regardless, and it is the third time this milestone that a fixture describing an
impossible schema has been found by something reading it.

A driver answering no snapshot and no error is carried as a run with no live schema rather than
crashed over. `internal/cli`'s fake does exactly that on purpose, and the report names what it was
able to project, so nothing claims a basis it did not have.

What this does not change: `internal/fold` is untouched, `internal/validate` still opens no database
and is handed a value, the check takes no lock and reads no history, and `migr up` still reads no
validate check.
