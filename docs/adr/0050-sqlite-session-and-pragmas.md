# 0050. The SQLite session: one pinned connection, three pragmas, and what an in-process engine changes

## Status

Accepted

## Date

2026-09-28

## Context

M8-T1 is the second real driver. The contract it implements is settled — [ADR 0011](0011-driver-contract.md)
for the shape, [ADR 0017](0017-transaction-boundaries.md) for the transaction, [ADR 0020](0020-lock-contention.md)
for the lock, [ADR 0032](0032-host-callback-server.md) for the SQL a migration writes itself — and
PostgreSQL has implemented all of it once. What this record is for is the handful of places where
SQLite is *different in kind* rather than smaller, because each of those places has a silent-wrong
sitting in it.

**The engine is in-process.** `modernc.org/sqlite` is SQLite transpiled to Go and linked into the
binary. There is no daemon, no socket, and no second process. So a live database costs a `t.TempDir()`
and a few milliseconds, and `sqlite_version()` reports a fact about *this build* rather than about the
file that was opened.

**`database/sql` is a pool, and an in-memory database is not poolable.** A `:memory:` database belongs
to the connection that opened it. A second connection is a second, empty database, and nothing says
so: a migration applies, and the next statement sees nothing it did. Measured, not assumed — a probe
on a pool with no idle connections found the table it had just created zero times.

**Three of SQLite's defaults are wrong for a migration tool, and one of its pragmas fails silently.**
`foreign_keys` defaults **OFF** for backwards compatibility. `busy_timeout` defaults to **0**, which
with one writer means every statement fails immediately or waits forever depending on which way you
read it — and invariant #12 exists because of the second. And `PRAGMA foreign_keys` **inside a
transaction is a no-op that reports success**: SQLite accepts the statement, applies nothing, and
reads back the old value. Measured on 3.53.4.

**SQLite is dynamically typed, and `host.db.query` carries JSON strings.** A value's storage class is
a property of the value, not of the column, so one column can return an integer, text, and a blob on
three rows. A blob has no text form, and SQLite permits bytes that are not valid UTF-8 in a TEXT
column — which `encoding/json` would silently replace with U+FFFD.

One more thing surfaced the moment the CLI was pointed at a SQLite project, and it was not this
driver's: **`migr.yaml` defaulted `lock.strategy` to `advisory`**, so `internal/config` was choosing
one engine's lock for every project. ADR 0020 had already decided that the *check* belongs to the
driver; the *default* was still driver-neutral code making a driver's decision.

## Decision Drivers

- **A silent success is the worst outcome available.** Every default above fails by succeeding.
- `driver.Options.LockStrategy` already documents the sentinel: *"empty meaning the driver's own."*
- CLAUDE.md §7: a test is gated behind a build tag **when it needs something `make test` does not
  have**, which is a statement about the test's dependencies rather than about its subject.
- The framework's central claim is that one migration behaves the same on two engines. Where a value
  can be rendered the way the other driver renders it, it should be.
- M3-T1's shape: a driver ships **complete and honestly unsupported**, so the conformance suite
  passes from the first commit and every later task arrives as a diff.

## Considered Options

### A. Where this driver's live tests run

- **A1: untagged** ✔. SQLite needs no daemon, so `make test`, `make check`, and both CI OS legs run
  the pragmas, the read path, the value rendering, and the transaction against a real database.
- A2: tagged `integration`, matching `internal/driver/postgres`. **Rejected**: it would gate a test on
  something it does not need, keep the local gate from ever touching the driver, and run it on the
  ubuntu Docker job only — for a database that needs no daemon anywhere. The tag names a
  *requirement*, not a category of test.

The cost is that `make test` compiles `modernc/libc`, which is large. It is a one-time cold build, and
it is the price of no CGO that CLAUDE.md §2 already accepted for cross-compilation.

### B. `foreign_keys`

- **B1: ON at `Open`, and read back** ✔. A migration that breaks referential integrity fails instead
  of leaving dangling rows. The read-back is what makes the setting explicit rather than attempted.
- B2: OFF at `Open`, which is what several migration tools do. **Rejected**: it makes the table
  rebuild trivial and makes `drop_table` on a referenced table succeed silently, so `migr down` can
  restore a schema whose data no longer satisfies it. That is a correctness loss traded for
  convenience in a task that has not been written yet.
- B3: a new `migr.yaml` key. **Rejected**: two projects would mean different things by the same
  migration, and the capability model would have to answer per setting.

**The rebuild must therefore use `defer_foreign_keys`.** The engine runs a migration inside one
transaction (ADR 0017), and `foreign_keys` cannot be changed there. `defer_foreign_keys` *can*: it is
transaction-scoped, holds enforcement until COMMIT, and reads back as set inside one — measured here,
so M8-T4 inherits the answer. `PRAGMA foreign_key_check` before the commit is the other half.

### C. The other two pragmas, and what is not touched

- **C1: `busy_timeout` is `safety.lock_timeout` in milliseconds** ✔. It is the whole of invariant #12
  for this engine, since SQLite has one writer and no per-statement lock timeout.
- **C2: `query_only` is toggled per `Query`** ✔ — §E.
- **C3: `journal_mode` is not touched** ✔. WAL would stop readers blocking writers, and it is a
  durable change to the operator's file rather than a session setting a migration tool may make for
  them.
- **C4: an empty connection string is refused** ✔. SQLite reads an empty filename as a private
  temporary database deleted at exit, so every migration would apply to a file nobody can find and
  report success. Measured.
- **C5: `mode=ro` is not refused** ✔. A read-only database fails loudly at the first write, and
  `migr status` against one is a legitimate thing to run.
- **C6: a DSN that sets one of the three pragmas is refused, naming it** ✔. PostgreSQL *overrides* a
  DSN that set `lock_timeout` ([ADR 0020](0020-lock-contention.md)); `_pragma=foreign_keys(0)` is
  different in kind, because it asks to change what a migration means, and answering that by ignoring
  the operator is worse than refusing. A query string that cannot be decoded is refused with it:
  `url.Query()` drops what it cannot read and reports nothing, so a `_pragma` behind a bad escape
  would read as no `_pragma` at all.

### D. One pinned connection

- **D1: `MaxOpenConns(1)` plus one `*sql.Conn` held for the session's life** ✔, asserted by a test
  that creates a table on an in-memory database and reads it back.
- D2: use the `*sql.DB` and let the pool do its job. **Rejected**: for `:memory:` it is a different
  database per connection, and the three pragmas above are connection-scoped, so the pool would
  silently hand out sessions with SQLite's defaults.

ADR 0011 already required pinning for the migration lock. Here it is correctness before it is a lock.

### E. How a read is enforced read-only

- **E1: `PRAGMA query_only` for the length of the read, with `SQLITE_READONLY` classified into
  `driver.ErrNotReadOnly`** ✔. The engine refuses the write, not migr, which is ADR 0032's rule.
- E2: a keyword check. **Rejected there and rejected here**, with a better example than PostgreSQL's:
  SQLite has no data-modifying CTE, but `PRAGMA user_version = 5` begins with `PRAGMA`, passes any
  leading-keyword test, and writes the database header. Measured: `query_only` refuses it.

### F. What a value comes back as

- **F1: integers and floats through `strconv`; text verbatim; a blob as `\x48656c6c6f`; a text value
  that is not valid UTF-8 refused, naming its column** ✔. The blob rendering is PostgreSQL's own text
  output for `bytea`, so one migration reading one column gets the same answer from either engine.
- F2: hex only when the bytes are not valid UTF-8. **Rejected**: a blob whose bytes happen to be
  valid UTF-8 would come back as text where PostgreSQL hexes it, and nothing in the reading migration
  could see why the two engines disagreed.
- F3: refuse any blob. **Rejected**: `SELECT *` on a table with a blob column is a legitimate read a
  migration may make for reasons unrelated to that column.

What the type switch rests on — that modernc returns `int64`, `float64`, `string`, `[]byte`, and
`nil` per storage class — is pinned by a test rather than assumed of the library.

### G. `lock.strategy`'s default

- **G1: `internal/config` stops defaulting it; empty means the driver's own** ✔. Validation still
  refuses a word migr does not know, so ADR 0020's two questions both still get asked.
- G2: leave the default and have SQLite accept `advisory`. **Rejected**: an operator who wrote
  `advisory` would get a file lock, and ADR 0020 says a driver refuses a strategy it does not
  implement rather than substituting its own.
- G3: default it per driver inside `internal/config`. **Rejected**: invariant #1 — that package may
  not know an engine.

Without this, every SQLite project created by `migr init` failed **every command** with a complaint
about a lock strategy nobody had set. It is a config bug that only a second driver could surface.

## Decision Outcome

**A1, B1, C1–C6, D1, E1, F1, G1.**

The driver ships complete and honestly unsupported: all twenty operations are declared `Unsupported`
naming M8-T2, `Compile` refuses identically because the declaration is derived from it, and
`test/golden/sqlite/` records twenty refusals. `Lock` refuses naming M8-T5 and `History()` returns a
store whose every method refuses naming M8-T6 — values rather than nils, because the engine calls
`Conn.History()` without being able to check for one.

`Transact` is implemented here rather than deferred. SQLite's DDL is transactional, no later M8 task
owns it, and without it the driver could not run a single migration. Two details differ from
PostgreSQL's and are recorded rather than discovered: `database/sql` rolls a transaction back itself
when the context it was begun with is canceled, so the rollback tolerates `sql.ErrTxDone`; and SQLite
exposes no transaction status to SQL — `sqlite3_get_autocommit` is C-API only — so the guard against a
transaction this driver did not open is its own bookkeeping plus SQLite's loud refusal of a nested
`BEGIN`.

A configured `schema:` is refused rather than ignored: a SQLite database is one file, and the only
other namespaces are the ones `ATTACH` creates, which nothing here opens.

The version floor is **3.25**, the release whose `ALTER TABLE` semantics this driver is written
against. It cannot fire against the library this binary links, and that is the point: invariant #13
says measured, and the version is asked for rather than hardcoded.

## Consequences

### Positive

- A second engine runs end to end: `migr config`, `migr validate`, and the read path all work against
  a real database, and every refusal names the task that lifts it.
- The driver's real behavior — pragmas, read-only enforcement, value rendering, transactions — is
  inside `make check` rather than behind Docker, which is the first time that has been true of any
  driver.
- A blob and a bigint read the same on SQLite and PostgreSQL, which is the language-independence claim
  applied to the read path.
- `lock.strategy` is the driver's decision at the default as well as at the check, so `migr init`'s
  scaffold stays engine-neutral.

### Negative

- **`make test` now compiles `modernc/libc`**, which is a large generated package. Cold builds are
  slower; the CGO-free binary is what it buys.
- **`migr status` on a SQLite project fails until M8-T6**, because reconciliation needs the history
  store. ADR 0027's promise that a report needs no toolchain is intact; what it needs is a store.
- **`Query` costs four statements**: set, read back, read, reset. In-process that is noise, and it is
  what makes the pragma explicit rather than attempted.
- **`lock.strategy` now renders as `""` in `migr config`** for a project that named none, beside
  `schema: ""`, which already behaved that way.

### Neutral

- Nothing was added to `test/docker-compose.yml` or the CI matrix. §8's step 8 does not apply to an
  engine with no daemon, and saying so is what keeps it from reading as an omission.
- The `sqlite_version()` this driver reports moves when `modernc.org/sqlite` is upgraded, which is a
  dependency bump rather than a server upgrade. M8-T3's version-dependent verdicts read it the same
  way either way.

## Notes

Revisit A1 if a SQLite test ever needs something `make test` does not have — M13's load suite already
will, and it is tagged `zerodowntime` for that reason. Revisit C3 if an operator asks migr to manage
`journal_mode`; it would be a `migr.yaml` key and a durable change, which is a different decision from
a session pragma.
