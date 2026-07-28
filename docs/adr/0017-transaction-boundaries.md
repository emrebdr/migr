# 0017. A transaction is a closure the driver brackets, not a handle the caller holds

## Status

Accepted

## Date

2026-07-28

## Context

`migr` applies a migration by compiling it to a list of engine statements and running them in
order. The framework's central safety claim is that a failure partway leaves the schema exactly as
it was — on an engine with transactional DDL, that means the whole migration is one transaction.

Until now nothing in the codebase ran a migration. The driver contract (ADR 0011) gave a session
`Exec`, `Lock`, `Capabilities`, `History`, `ServerVersion`, and `Close`, and none of those can open
or end a transaction. Something had to be added, and the shape of that addition is load-bearing for
three things that are already decided and not yet built:

**Not every statement can be in a transaction.** `CREATE INDEX CONCURRENTLY` is the motivating
case: PostgreSQL refuses it inside a transaction block, and it is exactly the statement a
zero-downtime migration most wants. `driver.Statement` therefore carries a `NoTransaction` flag,
added before any executor existed precisely so the first executor would be written to **group**
statements into runs rather than to **wrap** a migration in one transaction. An executor built on
"one migration = one transaction" cannot be retrofitted cheaply, and the split itself lands later
(the flag's own implementation task).

**A migration is recorded before its transaction commits.** ADR 0013 fixes this: the history row is
written inside the migration's transaction, so a crashed run leaves no row at all rather than a row
claiming a migration was applied that was not. Whatever opens the transaction must leave the caller
able to write that row between the last statement and the commit.

**A session is pinned, and a transaction lives on it.** ADR 0011 established that a `Conn` is one
pinned connection rather than a pool, because a PostgreSQL session advisory lock belongs to a
backend. The same reasoning applies to a transaction: it belongs to a session, and a transaction
object that can be separated from its session is an object that can be used against the wrong one.
That record's stated driver was "wrong pairings should not compile; a contract that relies on
callers being careful is a contract that will eventually be used carelessly."

## Decision Drivers

- **The release path must be unforgettable.** The same standard invariant #11 sets for locks: a
  failure must not leave a transaction open on a pinned session, and the path that discards it must
  not depend on the caller remembering a `defer`.
- **A canceled context must not prevent the cleanup.** The context that caused the failure is
  already canceled, so the rollback cannot inherit it.
- **The record-before-commit position must exist.** ADR 0013's guarantee must be expressible
  without a later change to this contract.
- **Grouping must stay possible.** The shape must let one migration become several runs, some
  transactional and some not, without the transaction mechanism itself needing to change.
- **No stubs.** ADR 0011 rejected adding a contract method nothing could honestly implement, and
  the driver conformance suite fails a `Compile` that "reports success and changes nothing." A
  parameter that is nil at every call site is the same thing in a different position.

## Considered Options

### Option 1: `Conn.Transact(ctx, work) error` — a closure the driver brackets

The driver begins the transaction, runs the caller's function, and commits if and only if that
function returns nil. There is no transaction object.

```go
err := conn.Transact(ctx, func(ctx context.Context) error {
    for _, stmt := range group.Statements {
        if err := conn.Exec(ctx, stmt); err != nil {
            return err
        }
    }
    return nil                 // the history write goes here, before the commit
})
```

**Pros**

- Rollback on error and rollback on panic are structural, not remembered. A caller cannot leave a
  transaction open on a pinned session.
- `context.WithoutCancel` on the rollback path is written once, inside the driver, beside the two
  places that already need it.
- The record-before-commit position is the last line of the closure body — lexical order rather
  than a callback contract, so no parameter is needed for it, now or ever.
- Nothing exists that can be held past the call, so a transaction cannot be paired with the wrong
  session or used after it ended.
- Grouping is unaffected: the split happens *outside* the closure. One `Transact` per transactional
  run, a bare `Exec` per statement that must stand alone.

**Cons**

- Control is inverted; a caller that wants a transaction open across two separate phases of its own
  logic cannot have one.
- The driver decides the commit, so a caller cannot commit early and keep working on the session.

### Option 2: `Conn.Begin(ctx) (Tx, error)` where `Tx` has only `Commit` and `Rollback`

A handle, mirroring `Lock(ctx) (Unlock, error)`. Statements keep going through `Conn.Exec`.

**Pros**

- Symmetrical with the lock, which the contract already spells as a handle.
- The caller controls the commit point directly, so record-before-commit is just the line above
  `tx.Commit`.

**Cons**

- Two objects govern one session with no rule saying which is correct for what. A statement runs
  through `Conn`, but the transaction ends through `Tx` — and after `Commit`, a stray `Conn.Exec`
  silently runs in autocommit. That is DDL outside a transaction with no diagnostic.
- The `defer` idiom does not come for free. pgx's `Tx.Rollback` returns `ErrTxClosed` after a
  commit rather than nil, so `defer tx.Rollback(...)` would join a spurious error into the return
  value of every successful migration unless every driver wraps it.
- Nothing prevents a caller from returning early with the transaction still open.

**Rejected because:** it fails the first decision driver on merit. The rollback is only as reliable
as the caller's `defer`, and the reliability it does offer has to be re-implemented by each driver
rather than existing once. It is the shape ADR 0011 described as relying on callers being careful.

### Option 3: The same handle, but carrying `Exec` as well

**Pros**

- Removes the two-doors ambiguity: statements run through the transaction while one is open.
- Gives a closed-transaction check on every statement for free.

**Cons**

- Reintroduces exactly the object ADR 0011 rejected — one that holds a session, can be passed
  somewhere else, and whose misuse is a runtime error rather than a compile error.
- The history store has no transaction handle. `history.Store`'s methods take only a context, by
  ADR 0013, because the store is bound to a session at `Open`. A `Tx` that owns `Exec` implies the
  store should route through it, which would mean threading a handle into a type that deliberately
  does not have one.

**Rejected because:** it solves option 2's ambiguity by making the object more powerful, and a more
powerful detachable object is further from ADR 0011, not closer.

### Option 4: `Begin`, `Commit`, and `Rollback` directly on `Conn`

**Pros**

- Smallest possible addition; no new type at all.
- Honest about the fact that the session *is* the transaction's scope — there is nothing to hand to
  the wrong connection.

**Cons**

- Nothing at all makes forgetting the rollback a compile error, or even visible at a call site.
- Every caller re-implements the same `defer`, the same `context.WithoutCancel`, and the same
  "commit only on success" decision, and each is a place to get it wrong.

**Rejected because:** it is option 2 with the handle removed and none of the safety added — the
worst position on the first two decision drivers.

### Option 5: `Conn.Apply(ctx, stmts) error` — the driver runs the whole migration

**Pros**

- The transaction never escapes the driver, and a driver whose DDL is not transactional can
  implement the same method honestly in its own way.
- The engine hands over a compiled migration and receives a verdict.

**Cons**

- The commit point moves inside the driver, so the caller cannot write anything between the last
  statement and the commit.
- Grouping — which statements share a transaction — would become a per-driver decision, so three
  drivers could reach three different answers about the same statement list.

**Rejected because:** it breaks ADR 0013 outright. Recording a migration before its transaction
commits is impossible if the caller never holds a position inside that transaction, and that
guarantee is what makes a crashed run leave no history row.

## Decision Outcome

**Option 1.** `driver.Conn` gains one method:

```go
// Transact runs work inside one transaction, committing it only if work returns nil.
Transact(ctx context.Context, work Work) error

// Work is what runs inside a transaction: the statements of one group, and nothing that ends it.
type Work func(ctx context.Context) error
```

A driver must: refuse to open a transaction on a session that already has one, however that one was
opened; run the work; commit only when the work returns nil; discard the transaction on any other
outcome, including a panic, on a context that outlives whatever canceled the work; and treat a
commit the server answers by rolling back as a failure rather than a success.

Deciding *which* statements share a transaction is not the driver's. That is grouping, it lives in
the engine, and it reads `Statement.NoTransaction` alone.

## Consequences

### Positive

- A migration that fails partway leaves nothing behind, and that property is a consequence of the
  contract's shape rather than of every caller's discipline.
- ADR 0013's record-before-commit needs no parameter, no hook, and no later contract change: it is
  a line in the closure body.
- The statements a session runs go through one door. A driver that routes `Exec` through the open
  transaction makes "the history write is inside the migration's transaction" true by construction.
- A driver reporting no transactional DDL is refused by the engine, by capability, before anything
  runs — never by a driver-name comparison.

### Negative

- A caller cannot hold a transaction open across two phases of its own logic, and cannot commit
  early and continue. Nothing planned needs that; if something does, it will need a new record.
- The closure hides the boundary from the call site. The rule that the migration lock is taken
  *before* the transaction begins — so the deferred release never runs inside an aborted
  transaction, where PostgreSQL refuses every command — is invisible in the signature and has to be
  written down instead.
- A driver must implement the "already inside one" refusal itself. The contract states it; the
  compiler does not.

### Neutral

- `Statement.NoTransaction` remains unset by every operation any driver compiles today. The flag,
  the grouping, and the engine's refusal of a migration that needs more than one run all exist
  ahead of the first statement that will set it.
- The engine refuses a migration that groups into more than one run, naming the task that will
  split it. Two runs means two commits, and neither the history store nor the dirty-state model
  that would make a half-applied migration recoverable exists yet.

## Notes

Revisit if a driver appears whose transactions cannot be expressed as "begin, do, end" — a
two-phase commit, or an engine where the boundary is set by a connection option rather than by
statements. Neither PostgreSQL, MySQL, nor SQLite is such an engine.

The pgx behaviour cited above was read from `pgx/v5`'s `tx.go`: `Rollback` on a closed transaction
returns `ErrTxClosed`, and `Commit` returns `ErrTxCommitRollback` when the server answers `COMMIT`
with the command tag `ROLLBACK`. Both were confirmed against PostgreSQL 16 and 17.
