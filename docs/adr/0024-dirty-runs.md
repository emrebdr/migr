# 0024. A run is dirty when its statements outlived the failure

## Status

Accepted

## Date

2026-08-11

## Context

[ADR 0013](0013-migration-history.md) chose three run outcomes over a `success` bool, because *"a
failed run left the schema unchanged and the next `up` may proceed; a dirty one did not, and the next
`up` must refuse."* It also fixed `LastStatement` as 1-based *"so zero unambiguously means no
statement succeeded — and a run where none succeeded left the schema unchanged and is failed rather
than dirty."*

Everything that reads a dirty run was then built. `Record.Validate` rejects a dirty row naming no
statement and a non-dirty row naming one. `Outcome.Blocking()` is true for exactly one value.
`history.stateOf` reports `StateDirty` ahead of every other state, and both `runnable` and
`reversible` refuse it. `pgHistory.MarkDirty` writes the row and an integration test reads it back.

Nothing produced one. `Store.MarkDirty` was the only store method with no caller, and
`engine.recordFailure` called `MarkFailed` unconditionally — so the state that
[ADR 0019](0019-history-reconciliation.md) added a fifth reconciled value for was unreachable, and
the refusal that names it fired only against hand-written fixtures.

The blocker was one branch. `checkRunnable` refused any session reporting no transactional DDL,
naming this task, so the condition dirty exists to describe could not arise.

Two things turned up while the caller was written.

**The capability flag does not identify the cases.** M4-T9's done-when says *"when a driver reports
no transactional DDL"*, and that is one of three ways statements survive a failure. The others are
already reachable in the executor M4-T5 wrote: a standalone group runs bare, outside any transaction
that could take it back; and a migration of several runs commits the earlier ones before a later one
fails. Both happen on PostgreSQL, which reports transactional DDL as `true`.

**Deleting the refusal puts `Transact` on a session where it guarantees nothing.** On MySQL the first
DDL statement implicitly commits the open transaction, so the history `INSERT` that ADR 0013 requires
to be *inside* the migration's transaction lands in a different one.

## Decision Drivers

- M4-T9's done-when, and M9-T5's: *"a mid-migration failure records the last successful statement
  index and marks the migration dirty."*
- M13-T4 deletes `checkRunnable`'s two remaining `ErrNotAtomic` branches. Whatever rule this ADR
  picks has to still be right the day that lands, or M13-T4 becomes a rewrite rather than a deletion.
- M9-T6 resumes from the recorded index, and ADR 0013 already warned that *"an off-by-one there
  replays a statement that already ran."*
- CLAUDE.md §11: a database difference belongs in the capability system, never in an
  `if driver == "mysql"` branch.
- Invariant #9: every user-facing error names the migration and the offending operation.

## Considered Options

### A. What decides that a failed run is dirty

- **A1: whether the failing run's statements survived it** ✔. They survive when the group was
  standalone, or when the session reports no transactional DDL; and every statement in an earlier
  completed group survives regardless. One rule, three cases.
- A2: the `TransactionalDDL` capability alone — dirty if it is false and any statement ran.
  **Rejected on merit.** It is literally what the done-when says and it is not enough: a standalone
  group on PostgreSQL and a multi-run migration on PostgreSQL both leave statements behind while the
  flag says `true`. Under A2 those record as `failed`, which tells the next `up` to proceed against a
  half-changed schema — the exact failure the three-outcome model exists to prevent. M13-T4 would
  have to replace the rule rather than delete a refusal.
- A3: ask the driver after the fact what survived. **Rejected**: it needs introspection, which is
  M11-T1's, and a driver that has just failed a statement is the worst-placed thing to interrogate.

### B. Whether a non-transactional session still runs through `Transact`

- **B1: yes — `runGroup` is unchanged** ✔. The driver is the only thing that knows what a transaction
  means on its engine, and MySQL's DML is transactional even where its DDL is not, so the history
  `INSERT` is not theatre. One code path.
- B2: run bare when `TransactionalDDL()` is false and write the row afterwards. **Rejected**: a second
  path through the run loop that only one driver would ever take, and the history row would lose even
  the atomicity InnoDB gives it — a crash between the last statement and the write would leave no row
  at all, where B1 leaves one. It also puts a driver-shaped decision in the engine, which §11 forbids
  in spirit even though the branch reads a capability rather than a name.

### C. What record-before-commit means where DDL auto-commits

- **C1: state it as a limit of the guarantee, not a change to it** ✔. ADR 0013's ordering is
  unchanged; what changes is that on such an engine the ordering buys less, and the window it fails
  to close is precisely what a dirty row describes.
- C2: weaken the contract to "recorded near the commit". **Rejected**: it would relax the requirement
  for PostgreSQL and SQLite, where it holds exactly, to accommodate one engine where nothing could
  make it hold.

### D. Whether the failure carries the window it opened

- **D1: a typed `engine.DirtyError`** ✔, following [ADR 0009](0009-migration-checksum.md)'s successor
  `DriftError`: fields for M5-T2 and M9-T5 to render, a message that says how far the run got, and
  `Unwrap` reaching both `ErrDirty` and the driver's own error.
- D2: leave the existing `StatementError` message. **Rejected**: it names the statement that failed
  and says nothing about the three before it that are still in the database. The operator is looking
  at that output now; making them run `migr status` to discover their schema is between states is a
  worse first experience of the state than the state itself.

## Decision Outcome

**A1, B1, C1, D1.**

`runMigration` returns how many statements a failure left behind, computed from the group that failed
rather than from the session's declaration:

```go
func (e *executor) durable(group Group, committed int32, err error) int32 {
	if group.Transactional && e.conn.Capabilities().TransactionalDDL() {
		return committed
	}

	return e.reached(err)
}
```

`reached` is `StatementError.Position - 1` where a statement failed, and the running counter
otherwise — because a commit or a history write that fails on a non-transactional session fails after
every statement in the group has already taken effect.

`recordFailure` then chooses: zero survivors go to `MarkFailed`, one or more to `MarkDirty`. That
split is not a convention the engine remembers — `Record.Validate` refuses a dirty row naming no
statement, so the contract enforces it.

`checkRunnable`'s `!caps.TransactionalDDL()` branch is deleted. The two `ErrNotAtomic` branches
remain and their messages now name M13-T4 alone: after this ADR, a multi-run migration is refused
because nothing splits it, not because nothing could record a partial failure.

## Consequences

### Positive

- The rule is right for M13-T4 the day it lands. Deleting those two refusals produces multi-run
  migrations that already record partial progress correctly, on PostgreSQL as much as on MySQL.
- MySQL's whole failure story is designed and tested before the driver exists, which is what M9's
  goal (*"the hardest rollback case, proven"*) asks for.
- `Store.MarkDirty` stops being the one method with no caller, and `StateDirty` stops being a state
  only fixtures can reach.
- One `perform` serves both directions, so a rollback that stops partway is dirty on the same path
  with `Direction: down` — no second call site to keep in agreement.

### Negative

- **No dirty run is producible end-to-end against PostgreSQL.** It reports transactional DDL and
  `checkRunnable` still refuses multi-run migrations, so the untagged suite with a fake session is
  the only place the whole path runs. M9-T7's done-when — *"a deliberately-failing migration produces
  the documented dirty state"* — is where a real one first appears, and M13-T4 is where PostgreSQL
  can reach one. The integration suite holds the other half: a dirty row written to a real table
  reads back, folds to `StateDirty`, and refuses both directions.
  *(M9-T5 is where a real one first appeared, one task earlier than this expected: MySQL 8.0 and
  MariaDB 11.4 both record it by failing. Doing so found that `recordFailure` recorded this ADR's
  `*DirtyError` as the row's cause — the window paragraph rather than the statement that stopped the
  run — which [ADR 0061](0061-refusing-a-dirty-database.md) §D corrects. It remains true that
  PostgreSQL cannot produce one.)*
- **Record-before-commit is not a guarantee on an engine whose DDL auto-commits.** ADR 0013's
  ordering is unchanged and still exact on PostgreSQL and SQLite. On MySQL the row and the schema
  change are not one unit, and nothing available can make them one — which is the reason this state
  exists rather than a defect in it.
- **`ErrDirty` now covers two senses**: a migration that *is* dirty, refused before anything runs, and
  a run that just *became* one. `errors.As` on `*DirtyError` distinguishes them and nothing else does.
  The alternative was a second sentinel, which would make `errors.Is(err, ErrDirty)` — the obvious
  thing a caller writes — miss half the cases.

### Neutral

- A dirty failure exits 1. CLAUDE.md §5 has no code for it, and exit 2 is for a failure knowable with
  no database. M5-T6 owns the mapping if one is wanted.
- `Result.Runs` still excludes the migration that went dirty, as it excludes any that failed. The
  error carries the detail; the summary carries what committed.

## Notes

M9-T6 resumes from `LastStatement`, which is the last statement that **succeeded**, not the one that
failed. ADR 0013 flagged the off-by-one and this ADR is where the producing side is written:
`reached` subtracts one from `StatementError.Position`, and a test asserts the recorded index against
a five-statement migration failing at its fourth.

M13-T4 needs no change here. Deleting `checkRunnable`'s two `ErrNotAtomic` branches makes the
multi-run cases reachable, and `durable` already answers for them — one of them is tested today
through `runMigration` directly, since that is the only way to reach the shape.
