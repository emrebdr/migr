# 0060. The MySQL migration lock, and a holder duration that says what it measures

## Status

Accepted

## Date

2026-10-02

## Context

`internal/driver/mysql/pending.go` was the whole of MySQL's migration lock: `Lock` returned an error
naming this task. So `migr up`, `migr down`, `migr plan` and `--dry-run` failed on their first line
against every MySQL project, which is the state M9-T2 and M9-T3 each ended by pointing at. This task
deletes that file and is what makes the engine runnable.

Four pieces were already in place from M9-T1 ([ADR 0057](0057-the-mysql-session.md)), which is why
this is narrow: `lock.strategy` is validated and refused per engine, the session is **one pinned
`*sql.Conn` under `MaxOpenConns(1)`** with a test asserting the pool hands out no second connection,
and `program_name` is already sent in the handshake — ADR 0020 §H2's decision for this engine. The
done-when's *"survives connection pooling correctly"* was therefore already structural; what was owed
was the acquire, the release, the holder, and the proof.

**What MySQL's lock is.** `GET_LOCK(name, timeout)` takes a **user-level lock**, which is global to
the server rather than scoped to a database, lives on a session, and dies with it. The timeout is a
**decimal number of seconds** rather than a setting spelled as a string, which is why
`driver.Options.LockWait` is a `time.Duration` and each driver spells it itself — M4-T4 found
`Duration.String()` unusable for PostgreSQL for the mirror reason.

## Decision Drivers

- Invariant #11: locks are always released, and a crashed migration must not permanently wedge a
  database.
- ADR 0020: giving up is `driver.ErrLockTimeout`, the wait is one budget with no retry, a holder
  crosses the boundary as a value, and **a report must not assert more than it observed**.
- The lock belongs to a connection, so an acquire on one pooled connection and a release on another
  reports success and releases nothing.
- CLAUDE.md §11: if a task turns out to be wrong, fix the task rather than quietly doing something
  else.

## Considered Options

### A. The name is derived, not spelled

A user-level lock is server-global, so the database has to be part of the name or two migr
installations on one server would exclude each other — the requirement PostgreSQL's `namespaceKey`
answers with a two-integer key. The name is `migr1:` plus 32 lowercase hex characters of a `sha256`
over `database + "\x00" + table`, and three measurements settled that shape rather than a readable
`migr:db.table`:

| Measured | MySQL 8.0.46 | MariaDB 11.4.13 |
|---|---|---|
| a 65-character name | **error 4163** | accepted |
| `GET_LOCK('migr:abc')` then `IS_USED_LOCK('migr:ABC')` | **the same lock** | **two locks** |

A database and a table may be 64 characters each, so a readable name cannot fit MySQL's bound; and
lock names are compared **case-insensitively on MySQL and case-sensitively on MariaDB**, so one
spelling could mean two things. Lowercase hex is immune to both. The derived name is rendered in every
failure, so an operator has what `IS_USED_LOCK` takes.

The `1` is the derivation's version and not migr's: a release that hashed something else would lock on
a different name, and two migr builds locking on two names exclude nobody. Changing it breaks the
lock itself.

### B. Three answers, and `NULL` is neither

| `GET_LOCK` answered | Meaning | Reported as |
|---|---|---|
| `1` | granted | the lock, held |
| `0` | the wait gave up | `driver.ErrLockTimeout`, carrying a holder |
| `NULL` | neither | the server's own failure |

`NULL` is reachable and the two servers reach it differently — **measured**: MariaDB answers `NULL`
for a negative timeout and for a waiter whose query is killed, where MySQL waits forever for the
first and answers **error 1317** for the second. So the third answer is not a theoretical arm, and
telling it from `0` is what keeps "the wait gave up" from covering "the server refused".

**No wait this driver renders can mean forever.** A negative timeout waits indefinitely on MySQL —
measured, it waited for a holder and then took the lock — which is the worst failure available here:
a migr that never gives up and never says why. `Options.Waits` already substitutes the default for a
non-positive wait, and `lockWaitSeconds` clamps to a millisecond anyway, which is PostgreSQL's
`millisecondsCeil` rule in this engine's units.

**And there is nothing to refuse at construction.** `checkStatementWait` exists because
`lock_wait_timeout` clamps at 31536000 and refuses a fraction; `GET_LOCK` accepted `0.000001`,
`31536001`, `9223372036.854776` and `1e308` without complaint on both servers, so `lock.timeout` is
owed no counterpart check. The plan expected one; the measurement says a check that refuses nothing
would be a constant nothing reads.

**A second acquire is refused rather than counted.** Measured: `GET_LOCK` on a name this session
already holds succeeds and *counts*, and after one `RELEASE_LOCK` the lock is still held. A second
acquire behind one deferred release would leave the lock held for the session's life with the engine
believing it had given it back.

### C. There is no reset, and no retry

PostgreSQL bounds its acquire by setting `lock_timeout`, which it must then put back. `GET_LOCK`'s own
argument *is* the bound, so nothing is set and nothing is reset — the acquire touches no session
state, and a wait that gives up leaves the session usable because giving up is a value the server
returns rather than an error that ends anything. ADR 0020's "one budget, no retry" is therefore
structural here rather than a decision to implement.

### D. A release whose session is gone is the release it already was

A canceled context makes go-sql-driver close the connection — by design, measured: the canceled
statement answers the context's error and the **next** statement on that pinned connection answers
`database/sql/driver.ErrBadConn` on both servers. So `defer unlock(context.WithoutCancel(ctx))` can
reach a connection that cannot carry a statement.

It reports **success**, because a user-level lock dies with its session and nothing is left for a
release to do. Reporting a failure would attach an error an operator cannot act on to the one that
actually ended the run, which `internal/engine` joins together.

The wording is "set in motion" rather than "done", and that is measured too: the server dropped the
lock about three seconds after the connection was abandoned — **when the statement the client had
abandoned finished and it next touched the closed socket**. A `RELEASE_LOCK` the server *answers* is
a different matter: `0` for another session's lock and `NULL` for a lock nobody holds, measured on
both, and neither is a release.

### E. The holder is an id plus an optional row

Two reads, and the second is allowed to find nothing:

```sql
SELECT IS_USED_LOCK(?)                              -- the connection id, no privilege needed
SELECT USER, HOST, TIME FROM information_schema.PROCESSLIST WHERE ID = ?   -- needs PROCESS
```

**Measured, and this is the shape's whole reason:** a user holding nothing but `SELECT` on one
database was answered the holder's connection id by `IS_USED_LOCK` and **zero rows** by
`PROCESSLIST`, on both servers. A production migr user has no `PROCESS` grant, so that is the ordinary
case and not an error.

So a holder is the id plus whatever else this connection may see, and **never nothing** — where
PostgreSQL's holder is one row and a missing row means no holder. Throwing the id away would lose the
one thing an operator can `KILL`. `Process` is `connection <id>` for that reason.

`Application` is `USER@HOST`. MySQL has no `application_name`; this driver does send a `program_name`
connection attribute and an ordinary user cannot read it back — a 1142 denial on both servers, which
ADR 0057 measured and recorded as this task's problem. The account and the host are what a report can
carry, and what an operator recognizes anyway.

Both reads run on `context.WithoutCancel`, PostgreSQL's rule for PostgreSQL's reason: a wait is
abandoned exactly when its context is done, so reading the holder on that context would destroy the
session that exists to explain the failure.

### F. The duration says what it measures, and two drivers were corrected

`driver.LockHolder.Connected` was documented as *"how long the holder's session had been open"*.
MySQL records a session's start time **nowhere**: `PROCESSLIST.TIME` is seconds in the thread's
*current state*, measured as `3` against a holder three seconds into a statement. Putting that in a
field rendered as "whose session has been open for 3s" would be the one thing ADR 0020 forbids.

And the field was already carrying two meanings: SQLite puts the **hold** there (ADR 0055 §G, the
better of the two numbers) and rendered it as a session age too.

So `Connected` becomes `Elapsed` and gains a `Measured` label:

| Driver | `Measured` | What it reads |
|---|---|---|
| postgres | `session age` | `backend_start`, which bounds the hold from above |
| sqlite | `time holding the lock` | the lock file's own stamp |
| mysql | `time in its current state` | `PROCESSLIST.TIME` |

**The label and not the duration is what says whether anything was measured**, and the live suite is
what forced that: a holder that has just taken the lock and gone idle reads `TIME = 0`, which under
the old sentinel rendered "this connection is not permitted to read" — false, since the test user has
`PROCESS`. Zero is a real answer here, so `sessionAge` and `heldFor` now report whether they read one
and label it only then. An `Elapsed` with no label renders a line naming that omission as a driver
bug, the shape `capability.Emulated` with no strategy already has.

### G. What a crashed migr costs, measured rather than feared

The plan asserted a `kill -9` could wedge the lock for hours. **It does not, and the measurement is
the correction:** the kernel closes a dead process's sockets, so the server reaps the session as soon
as it next touches one. Measured at the CLI against both servers — `migr` killed with `SIGKILL`
mid-migration, the lock still held one second later, **free by four**, which is exactly when the
statement the dead client had abandoned finished. The next `migr up` then acquired normally.

| Case | Cost |
|---|---|
| migr crashes, host reachable | the length of the statement in flight |
| migr's **host** vanishes with no FIN | `wait_timeout`, 28800s on both images |

The second is the real gap, and MySQL cannot close it the way PostgreSQL does: ADR 0020 §I2 pins
`tcp_keepalives_*` in the startup packet, which configures the **server's** probing of its own
backend, and MySQL has no per-connection equivalent. Shortening `wait_timeout` for the session is
**rejected rather than deferred**: the pinned connection is idle whenever a language runtime is
deciding what a migration does, so a short `wait_timeout` would disconnect migr mid-run — trading a
recoverable wedge for a broken migration. What migr does instead is name the connection id to `KILL`,
and ADR 0020 §G3's `migr unlock --force` stays the deferred real answer.

## Decision Outcome

`internal/driver/mysql/lock.go` holds the name, the acquire and the release;
`internal/driver/mysql/lock_holder.go` holds the two reads. `pending.go` is deleted. `internal/driver`
carries `Elapsed` and `Measured` in place of `Connected`, and both other drivers label what they read.

## Consequences

### Positive

- **MySQL runs end to end.** `migr up`, `down`, `plan` and `--dry-run` all work against 8.0.46 and
  11.4.13, driven at the CLI including a full up-then-down round trip. The committed op-set round trip
  is still **M9-T7's**.
- A blocked run exits **3** naming the lock, the bound, the connection id, the account and a labeled
  duration — and `--json` carries the same as one document. The id is what `KILL` takes, and a live
  test kills the id the report gave rather than one it looked up itself.
- Three engine differences are written down rather than discovered later: the name's length bound and
  its case sensitivity, and what a negative or interrupted wait answers.
- The report's duration is honest on all three engines for the first time, and the inaccuracy that
  fixed was already shipped.
- **A user-level lock needs no staleness rule**, and now that is measured rather than asserted: the
  lock dies with its session, which `KILL` and a `SIGKILL` both demonstrate.

### Negative

- **A migr whose host vanishes without a FIN wedges the lock for `wait_timeout`** (§G), where
  PostgreSQL recovers in about ninety seconds. Stated in `doc.go`, and the operator action is named.
- `Connected` was a published field name on a contract type. Renaming it touched two drivers and
  their tests; nothing outside `internal/driver` read it.
- The holder's duration is the weakest of the three. It under-reports a busy holder — a session an
  hour old that just started a statement reads near zero — and there is no better number on this
  engine.

### Neutral

- `lock.strategy: advisory` was already accepted and anything else already refused (M9-T1), so no
  configuration moved.
- No golden file changes: this task emits no compiled SQL.
- A mid-run crash leaves the history in whatever state it reached, which is **M9-T5's** dirty-state
  model and not this task's. The lock being free is what this task owes.

## Rejected

**A readable lock name.** §A. `migr:db.table` cannot fit MySQL's 64 characters for two 64-character
identifiers, and it means two things across the two engines' case handling. The derived name is
printed in every failure, which is what an operator actually needs it for.

**A `checkLockWait` at construction.** §B. Every positive value the engine was offered was accepted,
up to `1e308`. A check that refuses nothing reads as protection and is not.

**Treating a `NULL` from `GET_LOCK` as a timeout.** §B. MariaDB answers `NULL` for a negative wait
and for an interrupted one, neither of which is contention — reporting them as `ErrLockTimeout` would
send an operator looking for a holder that does not exist.

**Reporting a failure when the release cannot run.** §D. The lock is already going; the error is one
nobody can act on, arriving beside the error that matters.

**Returning no holder where `PROCESSLIST` has no row.** §E. That is the *ordinary* production case,
and it would discard the connection id — the only actionable thing in the report.

**Keeping `Connected` and caveating `PROCESSLIST.TIME` in prose.** §F. The rendered sentence would be
false, and a caveat in a doc comment cannot reach the operator reading the message.

**Shortening `wait_timeout` to bound a wedged lock.** §G. It would disconnect migr whenever a runtime
took longer than the bound to decide what a migration does.

## Notes

`performance_schema.session_connect_attrs` would give the holder's `program_name`, which is closer to
what `Application` means than an account is. Revisit if migr ever documents a grant for operators to
apply — as things stand an ordinary user is denied it, and a report that degraded to nothing for the
common case would be worse than one carrying the account.
