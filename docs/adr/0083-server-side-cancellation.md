# 0083. A statement whose context ends is stopped on the server, and two bounds end it

## Status

Accepted

## Date

2026-10-17

## Context

M13-T3 asks for two things:
- per-migration and per-statement timeouts;
- a cancel that stops the statement **on the server**.

Its premise was that a client-side context cancel leaves the DDL running and still holding its lock.
`safety.statement_timeout` (default 30s) was validated and read by nothing, and there was no
per-migration bound.

The premise was measured before anything was built. On all four servers, a holder session read a
table while a second connection waited to `ALTER` it, and the second connection's context was
canceled. It held, but not in the form the task gave:

| Server | What the library does on cancel | The statement on the server | The session |
|---|---|---|---|
| PostgreSQL 16, 17 | pgx's default handler sets a deadline on its socket; the dead connection then sends a cancel request **from a goroutine**, and terminates | stops, but **after** `Exec` has returned | gone |
| MySQL 8.0 | go-sql-driver closes its connection | **still waiting** after 5s, for the whole of its `lock_wait_timeout` | gone |
| MariaDB 11.4 | the same | stopped about 650ms later: MariaDB polls a waiting client | gone |

On every server the session ends. That session holds the migration lock. It is also the one a failed
run writes its history row through, so a canceled run could not record its own failure. On MySQL the
`ALTER` kept every reader queued behind it, which is the outage invariant #12 exists to prevent.

`database/sql` adds one more trap: `BeginTx(ctx)` rolls the transaction back itself when `ctx` ends,
while a statement may still be in flight on it.

## Decision Drivers

- After a cancel, nothing migr started may still run, or wait, on the server.
- The session survives, so the failure is recorded on it and its lock is released by the release
  path rather than by a dead backend.
- One rule for every driver about what a stopped statement reports.
- No privilege that a deployment lacks.
- The operator's `Ctrl-C` is told apart from migr's own bound.

## Considered Options

### Option 1: Every driver stops the statement before the call returns, and the engine owns both bounds

**Pros**

- Each bound is a context deadline, so one mechanism covers every driver and both causes.
- A stopped statement is a failure like any other: it is rolled back where the engine allows,
  recorded, and the lock is released.

**Cons**

- MySQL needs a second connection to stop anything.

### Option 2: Each engine's own server-side statement timeout

**Rejected because:** MySQL has none for DDL. `max_execution_time` applies to `SELECT` alone. A
PostgreSQL-only `statement_timeout` would bound one engine and leave the cancel half untouched on
every engine.

### Option 3: Close the connection and let the server notice

**Rejected because:** that is the baseline. MySQL 8.0 did not notice within its whole lock wait.

## Decision Outcome

Option 1.

**The contract.** An `Exec`, `Query`, `Perform` or `Chunk` whose context ends returns only once the
server has stopped the statement. Its error is that ending joined to what the server answered, so
`errors.Is(err, context.Canceled)` holds on every driver. The session stays usable.

**PostgreSQL.** `Open` sets pgx's `CancelRequestContextWatcherHandler`. It sends the protocol's cancel
request, which is what `pg_cancel_backend` sends, and waits for the backend to answer `57014`. pgx
already guards against a late cancel hitting the next statement. A socket deadline of 5s is the
fallback for a server that never answers, and the session is lost only then. `stoppedBy` joins the
context's ending to the error, because `57014` alone does not say why it happened.

**MySQL and MariaDB.**
- Every round trip a migration drives runs on `context.WithoutCancel`, so the library never closes
  the session. That covers `Exec`, `Perform`, `Query`, `Chunk`, and the `BeginTx` behind `Transact`.
- A watcher sends `KILL QUERY <CONNECTION_ID()>` when the real context ends. The kill goes over a
  second pool through the session's own connector, since the session's pool holds its one
  connection. The call returns once both the statement and the kill have come back. The statement
  answers `1317`.
- Three measurements back this:
  - A `KILL QUERY` that lands on an idle connection does nothing to the next statement, so a late
    kill is harmless.
  - A user may kill their own thread without `PROCESS`, as the compose user `migr_plain` shows.
  - An `ALTER ... FORCE, ALGORITHM=COPY` killed partway through 400,000 rows left the table whole and
    no `#sql` table beside it, on both servers.

**SQLite** needed nothing. The engine is in-process, so the cancel reaches `sqlite3_interrupt` and
the statement stops inside it. Measured: a write that would have run for minutes stopped about 100ms
after its deadline, and the session was usable.

**The engine's two bounds.**
- `Options.StatementTimeout` bounds each statement and each chunk.
- `Options.MigrationTimeout` bounds each migration, its lock retries and their backoff included.
- Each is `context.WithTimeoutCause` with a cause of its own, so `context.Cause` tells either bound
  from the operator's interrupt. A bound reached becomes `TimeoutError{Bound, Limit}`, naming the
  key to raise.
- A stopped statement is not a lock wait and is never retried.
- A migration bound that cuts off a lock-retry backoff reports what it was waiting on. It is not
  reported as retries spent, and it exits **3**, because the cause was a lock someone else holds. A
  bound reached by a statement that was running exits **1**.
- History writes, `Ensure` and the migration lock are not bounded here; `lock.timeout` bounds the
  last.

**Configuration.**
- `safety.statement_timeout` keeps its 30s default, now enforced.
- `safety.migration_timeout` is new and unbounded when unset.
- Validation refuses a `statement_timeout` no longer than `lock_timeout`. Otherwise a statement still
  waiting for its lock would be stopped as too slow, and M13-T2's retry would never run.
- Validation refuses a `migration_timeout` shorter than one statement's.

## Consequences

### Positive

- `Ctrl-C` is safe on all three engines. The statement stops on the server, the transaction rolls
  back where the engine has one, the run records its failure, and the next `migr up` starts at once.
- A statement holding a live table longer than `statement_timeout` is stopped rather than left to
  finish.

### Negative

- **A statement that legitimately needs more than 30s fails until the bound is raised.** An index
  build on a large table is one. That is the default this task chose: fail closed.
- A MySQL kill opens a second connection, so a server at `max_connections` cannot be told. The
  failure says so (`ErrKill`), and the statement's own result still comes back.
- **A `SIGKILL`ed migr cancels nothing.** MySQL notices only at the statement's end. PostgreSQL's
  keepalives (ADR 0020) and MariaDB's client polling bound it differently.

### Neutral

- The PostgreSQL baseline was not the task's: the statement stopped and the session did not
  survive. The fix is the same handler, and the test holds both halves.

## Notes

`client_connection_check_interval` (PostgreSQL 14+) would let a backend notice a vanished client
mid-statement. It is not set here, because this driver connects to 10. Whether to set it per version
is a separate decision.
