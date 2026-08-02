# 0020. Lock contention: one budget, no retry, and a holder read after the fact

## Status

Accepted

## Date

2026-08-03

## Context

[ADR 0011](0011-driver-contract.md) put the migration lock on the pinned session and then recorded,
in its Consequences, the one thing it could not settle:

> No sentinel for lock contention is defined yet. What contention means — how long to wait, how to
> recover a lock held by a dead process, what to tell the operator — is genuinely undecided, and a
> sentinel with no implementation would be the same kind of empty promise this record rejects
> elsewhere.

M3-T4 built the acquire and bounded it with `lock_timeout` rather than a context, because pgx closes
the connection when a context is canceled mid-query and that would kill the one session able to hold
the lock. M3-T5 pinned the per-statement bound in the startup packet, since only a startup-packet
value sets `reset_val` and the acquire's own `RESET` would otherwise clear it. Both left a field
carrying migr's own default and a comment saying M4-T4 would supply migr.yaml's.

Four things turned up when that wiring was actually written.

**The wiring did not exist to extend.** Nothing anywhere constructs a driver from a config. The
whole `lock:` and `safety:` block was write-only: `internal/config` parsed and validated it, and no
other package read it.

**`config.Duration.String()` produces strings PostgreSQL refuses.** `lock_timeout` takes an integer
and at most one unit from `us, ms, s, min, h, d`; Go's canonical duration form is multi-unit. Both
servers answer `'2m0s'`, `'1h0m0s'` and `'1m30s'` with *invalid value for parameter*. A refused
`set_config` means the bound is **lost**, not applied. Every value anyone tries by hand — `30s`,
`3s`, `150ms` — happens to round-trip, so the defect is shaped to survive testing, and the first
operator to write `lock.timeout: 2m` would have found it in production.

**`lock.strategy` was validated and then ignored.** `advisory | table | file` are accepted by the
loader, and `LockTable` and `LockFile` have no referencing call site anywhere. `lock.strategy: file`
against PostgreSQL loaded, validated, and silently took an advisory lock.

**A dead client is not a dead backend.** M3-T4 proved the server releases a session advisory lock
when the backend goes away, using a graceful `Close`. A client killed with `SIGKILL`, OOM-killed, or
partitioned away sends no FIN: the backend stays blocked in `ReadCommand` holding the lock until TCP
keepalives expire, which is **two hours** by default on Linux. That is invariant #11 — "a crashed
migration must not permanently wedge a database" — failing in the one case it exists for.

## Decision Drivers

- **A configured bound that silently does not apply is worse than no bound**, because it reads as
  protection.
- **An operator must be able to act on the failure**, not merely be told one happened.
- **A report must not assert more than it observed.** Naming a holder aims `pg_terminate_backend`
  at a session.
- **The lock lives on the session** (ADR 0011), so anything about it is per-driver work.

## Decision

**`driver.ErrLockTimeout` is part of the contract, and `postgres.ErrLockTimeout` is deleted.**
`internal/cli` cannot import a driver (invariant #1), so exit code 3 is reachable only through a
sentinel above the driver line. Deleting rather than aliasing follows
[ADR 0019](0019-history-reconciliation.md): two sentinels for one condition let a caller check the
driver-local one, be right on PostgreSQL, and be wrong on SQLite. The driver wraps rather than
replaces, so the message still carries the advisory key an operator matches in `pg_locks` and
`errors.Is` finds `postgres.ErrLock`, `driver.ErrLockTimeout`, and the server's own error alike.

**A holder crosses the boundary as a value, not a sentence.** `driver.LockHolder` carries a
`Process`, an `Application`, and a `Connected` duration; `driver.LockBusyError` carries the driver,
the bound it gave up at, and either a holder or a stated reason there is none.

**`lock.timeout` is one total budget. There is no retry.** PostgreSQL's lock manager is a fair FIFO
waiter, so giving up and re-acquiring puts a session at the back of a queue it was at the front of.

**A wait is spelled to the server in integer milliseconds, rounded up**, and one longer than
`lock_timeout` can store is refused at construction naming the migr.yaml key that set it. Rounding
up rather than truncating is load-bearing: `Duration.Milliseconds` would turn `500µs` into `0`, and
`lock_timeout = 0` is PostgreSQL's spelling for waiting indefinitely.

**`lock.strategy` reaches the driver, and a driver refuses one it does not implement.** Two checks
answering two different questions: `internal/config` asks whether a value is a word migr knows, and
the driver asks whether this engine can do it.

**migr identifies itself, and pins TCP keepalives**, both in the startup packet, both overriding a
DSN that set them.

**The holder is read after the failure, and the message says so.** PostgreSQL records no grant time
for an advisory lock, so `Connected` is the holder's *session* age, which bounds the hold from
above. The report is present-tense — "it is held by" — and carries the caveat verbatim.

**migr never terminates a holder itself.**

**`table` and `file` are not built here**, and M4-T4's text is corrected per CLAUDE.md §11.

## Considered Options

### A. Where the contention sentinel lives

- **A1: on `internal/driver`, the driver-local one deleted** ✔ — one spelling, and the only shape
  that lets `internal/cli` produce exit code 3 without importing a driver.
- A2: per-driver sentinels. **Rejected**: ADR 0019's reasoning exactly — a caller checks the
  PostgreSQL one and is silently wrong on the next engine.
- A3: a new `internal/lock` package. **Rejected**: CLAUDE.md §3 enumerates the package set and has
  no such entry, and the sentinel's only consumers are the contract and the CLI.

### B. One budget or a retry loop

- **B1: one total budget, no retry** ✔.
- B2: N attempts with exponential backoff and jitter. **Rejected on merit, not timing.** The
  decisive point is specific to advisory locks: `pg_advisory_lock` **queues**, fairly, so a session
  that gives up and re-acquires loses its place to everyone who arrived while it waited. Backoff
  exists to break contention convoys on a primitive that does not queue. Invariant #12's retry rule
  is about DDL statements, where staying queued *is* the outage because every later query on that
  table piles up behind the blocked `ALTER`; an advisory lock blocks nothing but other migr runs.
  `safety.lock_retries` also sits beside `safety.lock_timeout` rather than beside `lock.timeout`,
  and M13-T2's done-when claims that behavior verbatim. Finally, `forbidigo` bans `rand` outside
  `cmd/` and `internal/cli/` including `_test.go`, so jitter needs an injected source — a decision
  M13-T2 should make deliberately rather than inherit.
- B3: a deadline with internal re-issues. **Rejected**: B2 with extra steps and the same queue
  penalty.

### C. How a duration is spelled to the server

- C1: `Duration.String()`. **Rejected**: refuted against both servers, and shaped to survive
  testing.
- **C2: integer milliseconds with an explicit unit, rounded up, refused above `INT_MAX`** ✔.
- C3: a bare integer, relying on `lock_timeout`'s base unit. **Rejected on defense, not
  correctness**: identical behavior today, and silently wrong if the base unit ever moved. It also
  makes `pg_settings` unreadable.

### D. What crosses the boundary as the holder

- D1: a preformatted string. **Rejected**: `--json` could never render it as fields, and every
  driver would invent a format.
- **D2: a neutral three-field struct** ✔.
- D3: engine-shaped fields, `Pid int32`. **Rejected**: MySQL's holder is a connection id, SQLite's
  is an OS pid in a lock file, PostgreSQL's is a backend pid — an int is honest for one of three.

### E. What "since when" means

- E1: `backend_start` as an absolute time. **Rejected**: comparing a server timestamp to a client
  clock is wrong across skew, and `forbidigo` gives the driver no clock to compare with anyway.
- **E2: the server subtracts, and the field is named after the session it measures** ✔.
- E3: `xact_start`. **Rejected**: NULL for an idle holder — which is exactly what a session holding
  a lock and doing nothing is — so it renders "held for 0s" for a hold of an hour.

### F. How honest the report is

- **F1: a post-hoc snapshot, present tense, with the caveat in the message** ✔.
- F2: assert it as the cause. **Rejected**: unprovable, and it aims a termination at a session that
  may have taken the lock after this run gave up.
- F3: omit the holder whenever it might have changed. **Rejected**: that is every case, and
  whoever holds it now is exactly who an operator has to deal with.

### G. Does migr terminate a stale holder

- **G1: never; report the pid** ✔.
- G2: automatically after N failures. **Rejected**: the usual victim is a legitimately slow
  migration, and killing a session on the strength of an advisory row is not a decision a migration
  tool makes silently.
- G3: behind an explicit `migr unlock --force`. **Deferred, not rejected** — reasonable, and it
  needs a command surface that does not exist yet.

### H. Does the driver set `application_name`, and who wins

- H1: leave it. **Rejected**: every report degrades to a bare pid.
- **H2: migr's wins over the DSN's** ✔, on `pinLockTimeout`'s own precedent. The holder report is
  what this exists to make truthful, and a DSN that renamed the session would make every report
  anonymous in exactly the environments most likely to set one.
- H3: the DSN's wins. **Rejected** for that reason.

### I. Are TCP keepalives pinned

- I1: document the hole. **Rejected**: a two-hour wedge from a `kill -9` is invariant #11 failing
  where it matters most, and a crashed CI run has no operator present to read a message.
- **I2: yes — 60/10/3, ignored without error where unsupported** ✔.

### J. Do `table` and `file` land here

- J1: yes, per the done-when. **Rejected**: nothing can select a strategy, ADR 0011 fixed the lock
  on the session so a lock the engine takes is not that lock, the only driver that would use `file`
  is SQLite (M8-T5 owns it by name), no v0.1 engine lacks a native lock and so none needs `table`,
  and invariant #3 forbids the shared implementation reaching raw SQL. It is M3-T5's rejected move:
  another milestone's work under this task's name.
- **J2: narrow to the wiring plus reporting, and make an unimplemented strategy a refusal rather
  than a silent substitution** ✔.

## Consequences

### Positive

- A configured lock wait now applies, in a spelling both servers accept, with no value migr can
  produce meaning "wait forever".
- A blocked run names a pid an operator can terminate, and an integration test terminates the pid
  taken out of the error to prove the advice works.
- `migr.yaml`'s defaults come from the contract's constants, so the duplication M3-T4 and M3-T5
  both flagged in prose cannot drift.
- A crashed migr wedges the lock for about ninety seconds rather than about two hours.
- `ExitLock` stops being a constant used only in a table test.

### Negative

- **`internal/config` now imports `internal/driver`**, so loading a config links the driver
  contract. The alternative put the mapping in a package that could not test it, and `config`
  already imports `internal/capability` for the same reason.
- **migr's `application_name` overrides an operator's**, which costs someone who set one for their
  own monitoring. G3's command is where an escape hatch would go.
- **The severed-TCP case is documented rather than tested.** A Go proxy closing a socket sends FIN
  or RST, both of which the server notices immediately — the opposite of the failure being modeled.
  Reproducing "no FIN" needs packet dropping, which the compose environment cannot do portably.
- A third widening of `driver.Options`.

### Neutral

- No `ErrLockBusy` distinct from `ErrLockTimeout`. A second sentinel with no second caller is the
  empty promise ADR 0011 rejects.
- `postgres.ErrLockHeld`, `ErrLockNotHeld` and `ErrLockRelease` stay driver-local. They describe
  this engine's own failure modes and nothing neutral branches on them; the first thing that does
  is when they move.
- `safety.lock_retries` still has no consumer. It belongs to M13-T2, which is now recorded as its
  owner; until then it is a validated field nothing reads.

## Notes

Two mutations are known to survive a sweep and are recorded so nobody hunts them: dropping the `ms`
suffix from the encoded wait (the GUC's base unit is already milliseconds, so a bare integer behaves
identically — see C3), and `clock_timestamp()` → `now()` in the holder query (the read runs in
autocommit, so the two agree to within a statement).

M8-T5 owes a `driver.LockHolder` read from its lock file, and a staleness rule: a file, unlike a
backend, does not vanish when its owner dies, which is why that strategy has real recovery machinery
and this one does not. M9-T4 owes the same for `GET_LOCK`.
