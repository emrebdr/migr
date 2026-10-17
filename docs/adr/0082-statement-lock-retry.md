# 0082. A statement that gave up on a lock is tried again, after a jittered backoff, a bounded number of times

## Status

Accepted

## Date

2026-10-17

## Context

Invariant #12 has two halves:
- every DDL statement runs under a lock timeout;
- a timeout "retries with backoff rather than queueing".

The first half has held since M3. PostgreSQL sets `lock_timeout` in the startup packet (ADR 0020).
MySQL sets `lock_wait_timeout` and `innodb_lock_wait_timeout`. SQLite sets `busy_timeout`. All
three take their value from `safety.lock_timeout`.

The second half was missing. A statement that spent its bound failed the run like any other
error. `safety.lock_retries` was validated, defaulted to 5, and read by nothing. Nothing could tell
a lock wait spent apart from a syntax error either.

The bound matters because of how waiting works. An `ALTER TABLE` waiting for a lock queues every
later query on that table behind it. A migration that does 3 ms of work therefore takes the table
offline for as long as the holder ahead of it lives. Measured on PostgreSQL 16 and 17, MySQL 8.0 and
MariaDB 11.4, with one session holding a read transaction on a table:

- **With a 3–4 s bound,** a read sent once the server reported the `ALTER` waiting came back only
  after more than half of that bound.
- **With migr's bound and retries,** a reader looping throughout answered about 10,000 times across
  four attempts and their backoffs, and none of those reads waited past one lock wait plus a margin.

ADR 0020 settled that the *migration* lock never retries: `pg_advisory_lock` waits in a fair FIFO
queue, so re-acquiring only loses the session's place. This ADR is about the per-statement bound.

## Decision Drivers

- While migr is backing off, it must not keep holding locks other sessions are waiting for, because
  that is the outage the timeout exists to end.
- Statements that already committed must not be sent again.
- Retries are bounded, and running out of them is a clean failure that names what was tried.
- Planning paths stay deterministic (invariant #8): no randomness and no clock in `internal/engine`.
- One retry rule for every driver, as there is one grouping rule (ADR 0021).

## Considered Options

### Option 1: The engine reruns the migration's transaction, or continues from the statement that waited

**Pros**

- One rule, in one place.
- On transactional DDL, the rollback has already released every lock, so a backoff holds nothing.
- On MySQL, continuing from the statement that waited is the resume mechanism (`e.from`).

**Cons**

- A MySQL migration is between states while it backs off, and a crash at that moment leaves no row
  saying so. That is the same window a crash between two of its statements already has.

### Option 2: Retry the one statement in place, behind a savepoint

**Rejected because:** the transaction keeps every lock its earlier statements took. A backoff would
then hold readers off those tables for its whole length, which is worse than not retrying. It would
also need SQL above a driver (invariant #3).

### Option 3: Each driver retries inside `Exec`

**Rejected because:** three drivers would reach three answers, and none of them can see the
transaction its statement runs in. Only the engine knows whether a rollback undid the earlier
statements or they committed.

### Option 4: Full jitter (a delay drawn from zero up to the doubled base)

**Rejected because:** a draw near zero retries straight back into the queue the statement just
timed out of. Equal jitter, half the delay fixed and half random, still spreads out two runs that
collide.

### Option 5: Derive the delay from `safety.lock_timeout`, adding no key

**Rejected because:** the two values answer different questions. One is how long a reader may be
held behind the `ALTER`; the other is how long the holder ahead of it gets to finish. Tying them
together made five retries back off for about 93 s.

## Decision Outcome

Option 1.

**A contract sentinel.** `driver.ErrStatementLockTimeout` means a statement gave up waiting for a
lock. It is distinct from `ErrLockTimeout`, which is the migration lock. Each driver wraps its own
error:
- PostgreSQL wraps SQLSTATE `55P03`.
- MySQL and MariaDB wrap error 1205. Both servers answer a metadata-lock wait and a row-lock wait
  with that number, as the integration tests show.
- SQLite wraps `SQLITE_BUSY`, on a statement and on a COMMIT. In the default rollback journal,
  another writer makes the statement busy and a reader makes the COMMIT busy. Either way SQLite's own
  transaction is over afterwards, because an explicit `ROLLBACK` then reports that none is active.
  So the session is clean, and rerunning the transaction is sound. Measured, with an untagged test
  holding it.

**The engine retries.**
- `perform` runs a migration through `runRetrying`, and each chunk of a chunked run goes through
  `retryChunk`.
- Only an error that `errors.Is` the sentinel is retried.
- **On transactional DDL,** the whole migration is rerun from where this run started. The steps are
  restored from a copy taken before the first attempt, so an operation compiled against the database
  is expanded again from the catalog the rollback put back (ADR 0071).
- **On an engine whose DDL commits,** the rerun continues from the statement that waited, past
  `durable(err)`. The steps already expanded are kept, because they ran and committed against the
  catalog they were rendered from. A rerun that started over would hit error 1060, a duplicate
  column; that is measured.
- Between attempts nothing is written to the history. The final failure is recorded as before:
  failed, or dirty with what survived.

**Backoff.**
- `engine.Backoff` is the seam, like `Pacer`. `engine.Exponential` doubles `safety.lock_backoff`,
  default 500 ms, up to a 30 s cap, for `safety.lock_retries` retries, default 5.
- The delay is half fixed and half random. The random part comes from an injected `Jitter`, which
  `internal/cli` draws from `crypto/rand`, so neither forbidigo nor gosec needs an exemption. An
  unset jitter takes the whole span, which keeps tests deterministic.
- The wait goes through `Options.Sleep`.
- `lock_retries` became a pointer, so `0` means *try once*. Before, zero was silently defaulted to 5.

**Reporting.**
- Every retry calls `Options.Retried`, and `internal/cli` logs it as a **warning**. Contention on a
  live table is what an operator must see without passing `-v`.
- `engine.Run.Retries` reaches the summary as *after N lock retries*, and `--json` as `lock_retries`.
- Retries running out produce `engine.LockRetryError{Attempts, Waited}`, which wraps the last
  failure.
- `ExitCode` resolves the sentinel to **3**, as it does for the migration lock: the same command
  succeeds once whoever holds the table lets go.

**Not retried:**
- a migration's own `host.db.exec`, whose error the author's code receives;
- `Ensure` and the history writes;
- any preview, which sends no DDL.

## Consequences

### Positive

- A blocked `ALTER` holds a table's readers for at most one `lock_timeout` per attempt, and the
  retries let the queue behind the holder drain between attempts. Each driver's integration suite
  measures this against a control that shows the pileup without the bound.
- A short-lived holder costs a few warnings rather than a failed deploy.

### Negative

- A MySQL migration can be partly applied for the length of its backoff, and no row records it.
  Writing a dirty row before each wait and resuming after it was rejected: an attempt that then
  succeeds would have to finish as a resume of itself.
- Ten seconds of retries is ten seconds the migration lock is held, which blocks other migr runs.
- With `lock_retries: 0` and `lock_timeout` left at 3 s, a busy table fails the deploy at once.

### Neutral

- The advisory acquire still waits once on `lock.timeout`, as ADR 0020 decided.

## Notes

M13-T3's server-side cancellation is the other half of what an operator needs when a statement
*runs* long rather than *waits* long. That half is out of scope here.
