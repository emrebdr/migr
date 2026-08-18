# 0036. An escape-hatch statement in a preview

## Status

Accepted

## Date

2026-08-18

## Context

[ADR 0032](0032-host-callback-server.md) made `host.db.exec` the one callback plan mode changes: in
`ModeApply` it reaches the database, and in `ModePlan` it is **recorded** rather than executed. That
recording lives in `host.Call.Statements()`, and until now nothing read it.

The reason it mattered is the rest of what ADR 0032 established:

> Every callback fires while the migration is deciding what it does, which is before the engine has
> compiled anything or opened a transaction — so an escape-hatch write is committed on its own,
> under the migration lock, and a migration that fails afterwards **does not undo it**.

`engine.Source.Emit` returned `ir.Operations` alone, so `engine.Previewed` had no field for the held
statements and `migr plan` could not print them. A Go migration that adds a column and backfills it
through the escape hatch would have been previewed as though it only added the column — an omission
about precisely the statement that is *not* rolled back.

It was speculative until M6-T6, because `internal/source/sql` is the only `Source` that existed and
a `.sql` migration reaches the database as `ir.Raw`, with no callback to hold. `runtime.Session` is
the first `Source` that can hold a statement.

The same change makes a second hole visible. A rollback that authors no `down` has one derived by
asking the `up` as a question — in `ModePlan`, so the escape hatch does not fire. Those held
statements are now returned to exactly the code that is building the inverse, which has no inverse
for them.

## Decision Drivers

- Invariant #10: both directions, always; an operation with no inverse halts the whole rollback
  before its first statement.
- Invariant #6: a preview is what an operator reads before agreeing to a run.
- [ADR 0017](0017-transaction-boundaries.md): a value that describes one call must come back from
  that call, not from a second one that remembers it.
- CLAUDE.md §5: *"a capability is never silently degraded"* — the same standard applied to what a
  plan shows.

## Considered Options

### A. How the held statements leave a `Source`

- **A1: `Emit(...) (engine.Emitted, error)`, a struct holding `Operations` and `Direct`** ✔. It
  matches how the rest of this codebase returns more than one thing — `Previewed`, `Result`, `Rows`
  — and a third field later costs no signature change.
- A2: a third return value, `(ir.Operations, []driver.Query, error)`. **Rejected**: smaller at each
  call site, but shapeless, and the next addition is another signature change across every
  implementation and every test.
- A3: a `Held() []driver.Query` method the engine calls after `Emit`. **Rejected on this project's
  own precedent.** It can only ever describe *the last* `Emit`, which is the temporal coupling
  ADR 0017 refused when it made a transaction a closure rather than a handle. Worse, made optional
  behind a type assertion, a `Source` that forgot to implement it would report nothing — which is
  the silent omission this ADR exists to remove.

Roughly forty call sites changed, nearly all in tests and all mechanically.

### B. Where a preview carries them

- **B1: `engine.Planned.Direct`, one list per migration** ✔, populated only by a preview: under
  `ModeApply` the statement ran, so there is nothing to hold.
- B2: a flat list on `Previewed`. **Rejected**: it loses which migration sends which, which is the
  only thing an operator can act on.

### C. How `migr plan` renders them

- **C1: above the operations, marked `!`, with a note saying they are committed on their own** ✔ —
  because above is *when they run*. `--json` carries `direct` as `{sql, args}` objects and a
  `direct_statements` count.
- C2: beneath the operations, as a footnote. **Rejected**: it would read as *afterwards*, which is
  the opposite of true and the wrong thing to believe about a statement a failure does not undo.

The text form prints the SQL and, when arguments are bound, the line `-- N arguments bound`, rather
than interpolating them. Arguments are sent separately; a plan that spliced them in would print SQL
that is not the SQL that runs.

### D. A derived down over a migration that used the escape hatch

- **D1: refuse the derivation, naming the statements** ✔. The whole rollback halts before its first
  statement, which is what invariant #10 already requires of an operation with no inverse.
- D2: warn and derive the schema half anyway. **Rejected**: the schema returns to its prior shape
  and the data change stands, silently. It is the same omission this ADR exists to remove, one level
  down, and it fails in the direction the project has already said it will not — *"the framework's
  credibility is rollback correctness."*
- D3: leave the derivation alone. **Rejected**: the statements are now visible at exactly the point
  the inverse is built, so ignoring them would be a known hole rather than an unknown one.

D1 is consistent rather than novel: a `.sql` migration's up is `ir.Raw`, `ir.Raw` has no inverse,
and a `.sql` file with no authored down is already refused today. The escape hatch is the same
thing reached a different way. The way out is the same too — author a `Down`, or declare the
migration irreversible with a reason.

The refusal wraps `ir.ErrIrreversible`, so the exit code and every existing rendering are unchanged
and only the reason is new. `internal/validate` reports it as a `CheckReverse` problem, which is
where an author actually reads it: `migr validate` is the check built for CI, and it reports every
problem rather than the first.

## Decision Outcome

`engine.Emitted` carries what a migration did — the operations it emitted and the statements it
sent itself. `engine.Planned` records the second, `migr plan` prints it above the operations in both
renderings, and `engine.derive` refuses to invert a migration that has any.

## Consequences

**A migration that backfills through the escape hatch and authors no `down` now fails a rollback
that used to succeed.** That is the point — it succeeded by leaving the data change in place — but
it is a behavior change, and the fix is to author a `down`.

**`Emitted.Direct` is empty in `ModeApply`, always.** A reader that treats a non-empty `Direct` as
"this will run" is right; one that treats an empty one as "nothing will" is wrong for an apply, and
the field's own comment says so.

**`internal/source/sql` returns an empty `Direct` forever**, because a `.sql` migration has no
callback to make. That is not a stub: the field is genuinely inapplicable to that format.

**A migration can still reach the database in ways a plan cannot show.** `host.db.query` executes
live in both modes by design, and a `SELECT` with a data-modifying CTE is a write — which is why
[ADR 0032](0032-host-callback-server.md) made the driver run `Query` in a read-only transaction and
let the *server* refuse it. A plan showing every escape-hatch statement does not make the read path
safe; the read-only transaction does.
