# 0021. The apply queue reconciles under the lock, and a batch belongs to one database

## Status

Accepted

## Date

2026-08-03

## Context

Every piece an executor needs already existed, and none of them were connected. `migration.Order`
hands back a total sequence with declared dependencies overlaid ([ADR 0018](0018-declared-dependencies.md)).
`history.Reconcile` folds a log against the files on disk into five states
([ADR 0019](0019-history-reconciliation.md)). `Conn.Lock` bounds its wait and names the holder
([ADR 0020](0020-lock-contention.md)). `Conn.Transact` brackets a closure so that "a migration is
recorded before its transaction commits" is expressible ([ADR 0017](0017-transaction-boundaries.md),
[ADR 0013](0013-migration-history.md)). `engine.Apply` ran *one* migration's already-compiled
statements and had no caller anywhere.

Nothing allocated a batch. Nothing had ever called `Store.Ensure`, `Store.Record`, `Store.Latest`,
or `Store.MarkFailed` outside a driver's own tests. `internal/plan` was a `doc.go`.

[ADR 0013](0013-migration-history.md) deferred one question to this task by name:

> **The executor carries the batch counter.** Allocating one is now engine-neutral work that M4-T5
> has to get right, including deciding what a batch number means across two databases configured in
> one `migr.yaml`.

Five things turned up while the queue was being written.

**`checkRunnable` had a hole, and the hole is this task's own subject.** M3-T5 refused a migration
that compiles to more than one group. A migration compiling to exactly one group that is *entirely*
non-transactional passed both refusals, was handed to `Transact`, and was then rejected by
`pgConn.checkStatementFits` for declaring it cannot run where the executor put it. The precondition
was never "one group": it is **one transactional group**, which is also the only place a history row
can be written.

**`Ensure` must never run inside a `Transact`, and nothing in its signature says so.**
`pgHistory.create` goes through `session.inTransaction`, which opens a transaction only when the
session has none. A history table ensured lazily inside a migration's transaction would be created
in it, and the first rollback would drop the table along with the row just written into it.

**`Ensure` must precede the read, or `migr up` never works on a fresh database.** `history.Read`
maps `ErrNoTable` to an absent log, and reconciling an absent log yields **zero statuses** — the
deliberate under-reporting of [ADR 0019](0019-history-reconciliation.md). Reading first therefore
reports "nothing to do" on every empty database, forever.

**`internal/engine` cannot read a clock.** `forbidigo` bans `time.Now` outside `cmd/` and
`internal/cli/`, `_test.go` included, and its message is *"inject a clock"*. But
`history.Record.Validate` requires a non-zero `AppliedAt` and a non-negative `Duration`, and
`internal/history`'s package doc says *"M4-T5 already owns the run, so it owns the instant the run
started."* It owns the instant and may not take one.

**`Record` lands inside the migration's transaction for free, and nothing makes that a rule.**
`pgHistory.write` goes through `session.runner()`, which returns the open transaction when there is
one. The driver contract does not require that of a store, so a driver whose history used a second
connection would break record-before-commit **silently** — the same unenforced obligation
[ADR 0019](0019-history-reconciliation.md) recorded for `ErrNoTable`.

## Decision Drivers

- **A failure must abort, never undo.** Migrations that committed stay committed, and the operator
  must be able to see which ones did.
- **What is pending must not change while the queue runs**, or two concurrent runs apply the same
  migration twice.
- **A condition knowable before the first statement must be refused before the first statement.**
- **The seam must be one M6 can satisfy.** Emitting a migration's operations there means spawning a
  runtime process that may branch on live query results ([ADR 0009](0009-migration-checksum.md)).
- **No stubs** ([ADR 0011](0011-driver-contract.md)): a field or parameter that is empty at every
  call site is worse than one that does not exist yet.

## Decision

**The executor takes a lazy `engine.Source` and asks it only about migrations that will run.**
`Emit(ctx, migration, direction) (ir.Operations, error)`, defined in the consuming package
(CLAUDE.md §9), called under the lock. M5-T2 satisfies it over `.sql` files; M6's runtime satisfies
it over the JSON-RPC protocol.

**The executor reconciles under the lock.** `Lock`, then `Ensure`, then `Read`, then `Reconcile`,
then a switch on `history.State` with no `default`, then a batch, then the queue. Each ordering is a
constraint, not a preference; the three that are not obvious are the findings above.

**A batch is per-database.** It is one more than that database's own last recorded run, and a
database with no runs starts at one. `migr up` against two databases produces one `Result` each.

**Ordinals number the runs, not the discovered migrations.** A skipped migration is not a run, so a
batch numbers what it actually did, from one.

**`internal/plan` compiles; `internal/engine` orchestrates.** CLAUDE.md §3 already assigns
`plan/` the job "IR → ordered, compiled execution plan" and `engine/` the job "apply/rollback
orchestration, transactions, locking, batches". A `plan.Step` carries the operation *and* the
statements it became.

**A migration is a sequence of runs, written now.** Each transactional group goes through
`Transact`; each standalone group executes bare; the history row is written inside the last
transactional run. Two preconditions remain, and M13-T4 deletes preconditions rather than rewriting
an executor.

**A failed run is recorded after its rollback, best-effort, joined into the failure.**

**A drifted or dirty migration refuses the whole run**, including migrations queued ahead of it.

**The clock is a required `Options` field with no default.**

## Considered Options

### A. Where a migration's operations come from

- **A1: a lazy `Source` asked under the lock, only for what will run** ✔.
- A2: all operations emitted up front, before the lock. **Rejected on merit.**
  [ADR 0009](0009-migration-checksum.md) permits a migration to branch on live query results, so a
  plan built before the lock is built against a database another run may still change. M6 also
  spawns a runtime process per migration: emitting for migrations that will be skipped is N
  subprocesses of side effects for an answer nothing reads.
- A3: the executor reads migration files itself. **Rejected**: it binds the engine to a filesystem
  and to `.sql`, and M6's source is a subprocess speaking JSON-RPC.
- A4: no seam — the caller compiles and hands over statements. **Rejected**: the checksum's IR half
  is taken over what the migration emitted, so the caller would have to compute it too, and every
  future caller would recompute the same pairing.

The cost is stated in Consequences: a broken migration at position five is discovered only after
one through four have applied.

### B. Who decides which migrations are pending

- **B1: the executor, under the lock** ✔. The lock exists to make that answer stable, and
  `internal/history`'s package doc already names M4-T5 as one of three tasks that switch on `State`
  exhaustively.
- B2: the caller decides and hands over a queue. **Rejected**: the selection then happens outside
  the lock, so two runs can both conclude the same three migrations are pending and both apply
  them. The `(batch, ordinal)` key would not catch it — the batches differ.
- B3: the executor re-checks each migration immediately before running it. **Rejected**: it is B1
  with N extra reads and the same answer, since nothing can change under the lock.

### C. What a plan step carries

- **C1: the operation and its statements, one step per emitted operation** ✔.
- C2: statements only. **Rejected**: invariant #9 requires every user-facing error to name the
  offending operation, and a flat statement list cannot answer which operation statement seven came
  from. M13-T5's "an operator must be able to see that one declared operation became three
  statements" is unprintable from it.
- C3: operations only, compiled inside the engine. **Rejected**: `migr plan` (M5-T4) must print
  statements without a database, and the engine is the one thing that has one.

Capability verdicts are deliberately absent. `Compile` needs no session and a `capability.Set` is
built inside `Open`, so a `Verdict` field would be zero at every call site today. M13-T6 adds it as
a `Planner` field.

### D. Where the clock comes from

- **D1: `Clock func() time.Time` on `Options`, required, refused when nil** ✔.
- D2: a `forbidigo` exemption for `internal/engine`. **Rejected**: the exemption would cover every
  planning path this package ever grows, which is the opposite of invariant #8.
- D3: a `Clock` interface. **Rejected**: [ADR 0013](0013-migration-history.md) already rejected an
  interface built for one consumer, and this repository's idiom for a one-method seam is a func
  type — `driver.Work`, `driver.Unlock`, `capability.Rule`.
- D4: the caller supplies the instant and the duration. **Rejected**: a caller cannot know when a
  run inside the lock began, and the duration has to be measured around the transaction it is
  written into.

### E. What a batch means across two databases in one `migr.yaml`

- **E1: per-database, from that database's own `Latest`** ✔.
- E2: one shared number per invocation. **Rejected**: no lock spans two databases, so nothing
  allocates it; `migr down` reverses *one* database's last batch, and a shared number can name a
  batch that database never used; and `migr up --database` targets one, so there is not always a
  second to coordinate with.
- E3: a wall-clock invocation id. **Rejected**: it needs a clock, and
  [ADR 0013](0013-migration-history.md) already rejected the clock as a key because two rows can
  share a millisecond.

`Latest` is read rather than the log already in hand, because the batch is a decision about
*writing* while the log was read for *reconciling* — and M4-T8 will filter that log by `--step` and
`--to`, at which point a maximum taken from it is the maximum of a filtered slice.

### F. Does a failed run write a row

- **F1: after the rollback, best-effort, `errors.Join`ed into the failure** ✔.
  [ADR 0013](0013-migration-history.md)'s chosen option is "every run appends a row, **including
  rollbacks and failures**", `migr history` is what an operator reads after an incident, and
  `MarkFailed` has no other call site in the roadmap.
- F2: inside the transaction. **Impossible**: the transaction is being discarded, and PostgreSQL
  refuses every statement in an aborted one until it ends.
- F3: no row at all. **Rejected**: the log then has nothing to say about the event most worth
  looking up.

Joining rather than replacing is the point: reporting only the bookkeeping failure swaps the
operator's problem for a diagnostic's, and reporting only the migration's hides that `migr history`
will not show it.

### G. Does a drifted or dirty migration block the ones queued before it

- **G1: refuse the whole run before anything executes** ✔.
- G2: run until the queue reaches it. **Rejected**: it leaves a partially migrated database for a
  condition that was knowable before the first statement ran.
- G3: skip it and continue. **Rejected**: drift is exit code 4 and a dirty run left the schema
  between states; continuing past either applies migrations on top of a database nothing can
  describe.

### H. How far "designed here, implemented in M13-T4" goes

- **H1: write the run loop; narrow the refusal to what is actually missing** ✔. A migration is a
  sequence of runs, the row is written inside the last transactional one, and what remains refused
  is "more than one run, and nothing can record partial progress across two commits" — which is
  M4-T9's dirty state. Plus the case the loop makes visible: a migration with *no* transactional
  run has nowhere to write its row.
- H2: keep M3-T5's group-count refusal untouched. **Rejected**: M13-T4 would then rewrite an
  executor rather than delete a precondition, which is precisely what M4-T5's own done-when says
  must not happen.
- H3: implement the split now. **Rejected**: it needs M4-T9's dirty-state model to record partial
  progress, and building that here is another task's work under this one's name.

## Consequences

### Positive

- `migr up` exists as a function: discover, reconcile, lock, apply, record, abort.
- Record-before-commit is a property a test can state as a slice — `begin, exec, record, commit` —
  and an integration test proves it for PostgreSQL by discarding a transaction *after* the row was
  written and finding the row gone.
- The three refusals whose conditions are knowable with no database fire before the lock, so a
  misconfigured run never holds one.
- `Store.Ensure`, `Store.Latest`, `Store.Record` and `Store.MarkFailed` all have call sites, so the
  contract M3-T6 implemented is now exercised by something other than its own tests.
- `internal/plan` has a job, and M13-T5's decomposition has a seam to land in that does not move
  anything else.
- The latent `checkRunnable` hole is closed, and closing it produced the second half of the
  precondition M13-T4 will delete.

### Negative

- **A broken migration late in the queue is found only after the earlier ones applied.** That is
  the cost of the lazy seam, and it is mitigated rather than removed: `migr plan` emits and
  compiles the whole queue with no database, so CI catches it. That is another reason
  `internal/plan` must stay database-free.
- **The recorded duration excludes the commit.** The value has to be inside the row the commit
  makes durable, so there is no reading of the clock that could include it.
- **`Apply` returns a populated `Result` alongside a non-nil error**, which is unusual in Go and is
  documented at the function. Without it, "a failure aborts the remaining queue" is unactionable —
  the operator cannot see which migrations committed.
- **"A store must write through the caller's open transaction" is a driver obligation nothing
  checks mechanically.** The contract cannot express it; it is stated in `internal/driver/doc.go`
  and asserted per driver, exactly as `ErrNoTable` is.
- A refusal that names a future task means messages will change when those tasks land, and the
  tests assert on those substrings deliberately.

### Neutral

- `engine.ErrDrifted` exists here, and the `→ ExitDrift` mapping in `internal/cli/exit.go` is
  M4-T7's, following M4-T4's `driver.ErrLockTimeout` precedent. Until then drift exits 1.
- `MarkDirty` still has no call site. It belongs to M4-T9, and the run loop it will bracket is
  already written.
- `StatementError` is unchanged. It reports a bare position because that is what M4-T9 records and
  M9-T6 resumes from; migration identity is a wrapper, not a field on it.
- Two databases in one invocation get unrelated batch numbers. Anything grouping by batch groups
  per database, which is what `migr down` already does.

## Notes

The duplicate-version refusal duplicates a guarantee `migration.Discover` already makes. It is kept
because the executor's input is a caller's slice rather than a directory walk, and reusing
`migration.ErrDuplicateVersion` rather than minting a second sentinel keeps one spelling for one
condition.

M4-T6's `Rollback` inherits `Options` unchanged — `Source.Emit` already takes a direction, and the
record path, the batch and the refusals are the same ones.
