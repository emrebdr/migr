# 0055. The SQLite file lock: flock decides, the record reports, and nothing wedges

## Status

Accepted

## Date

2026-09-28

## Context

[ADR 0020](0020-lock-contention.md) built the migration lock's contract against PostgreSQL's session
advisory lock and closed by narrowing itself, in option J:

> `table` and `file` are not built here. Nothing can select a strategy, ADR 0011 fixed the lock on
> the session so a lock the engine takes is not that lock, the only driver that would use `file` is
> SQLite (M8-T5 owns it by name) …

This is M8-T5. `sqliteConn.Lock` has refused since M8-T1 naming it, and `lock.strategy`'s `file` half
has been a word `internal/config` accepts and nothing implemented since M4-T4.

**The one sentence that makes this a different problem.** A PostgreSQL advisory lock is a row in a
server, and the server drops it when the backend holding it goes away — so ADR 0020 needed no
recovery code at all, only the TCP keepalives that make the server notice sooner (its option I). A
lock file is a file. **It does not vanish when its owner dies.** Whatever holds it has to be
something the operating system reclaims, and whatever is written in it has to be readable as stale.

That is the whole of this record: what actually holds the lock, what the file is for if it is not the
lock, and what the second half of a staleness rule can honestly promise on a filesystem where the
first half is advice.

## Decision Drivers

- Invariant #11: *"a crashed migration must not permanently wedge a database."* A lock file is the
  one strategy where this is a design problem rather than a property inherited from the engine.
- The done-when: *"concurrent migration attempts fail cleanly rather than corrupting anything."*
  A refusal an operator can retry is the goal; a half-applied migration is the failure.
- ADR 0020's contract is not reopened: `driver.ErrLockTimeout` on giving up, a `driver.LockHolder`
  read **after** the failure, one budget rather than a retry, and migr never terminates a holder.
- `forbidigo` bans `time.Now` outside `cmd/` and `internal/cli/`, so a driver has no clock.
- SQLite is in-process ([ADR 0050](0050-sqlite-session-and-pragmas.md)), so every claim here is
  cheap to measure and the whole test layer is untagged.

## Decision Outcome

### The lock is the `flock`; the file is only where it lives

`<database file>-migr.lock`, beside the database, following SQLite's own `-journal` / `-wal` / `-shm`
sidecar convention. `flock(LOCK_EX|LOCK_NB)` on it is the lock. The record written inside it — pid,
host, application, acquire instant — is what a **failed** wait has to report, and a second opinion
where flock cannot be trusted. It is never what decides.

This is what makes invariant #11 structural rather than remembered: the kernel releases a `flock`
when the process holding it dies, whatever killed it, with no cleanup code on any path and no
operator step. Measured with a real `SIGKILL`ed child: the lock became available, and **the file and
its record both survived**.

**The file is never unlinked.** Release truncates the record and drops the lock. Unlinking would
reintroduce the race it looks like it avoids — one run opens the file, another unlinks it and creates
a fresh one, and the first locks an inode that is no longer at that path while the second locks the
one that is. Two holders, no error. Closing that properly means comparing the locked inode against
the path's on every acquire; not unlinking means there is nothing to compare. A blank file beside the
database is the normal state, not an anomaly to diagnose.

### One lock per database *file*, not per installation

PostgreSQL derives its advisory key from schema + history table, because two migr installations in
one database genuinely do not contend. **SQLite takes one write lock for the whole file**, so they
do. Keying the lock to `history.table` would let two installations in one file migrate at once
against an engine that serializes their writes anyway, one statement at a time, with no transaction
boundary between them. So `history.table` does not enter the path, and a test holds that.

### The path is the engine's answer, not the DSN

`PRAGMA database_list` is asked once inside `Open`, beside the version, and the resolved path is
stored on the session — ADR 0046's *ask the toolchain rather than reimplement it*. Measured: the
answer is **absolute and symlink-resolved**. `./app.db`, `/abs/app.db`, `file:app.db?_pragma=…` and a
path with `..` in it all come back as one string, and on macOS `/var/…` comes back as `/private/var/…`
— a normalisation no string handling in this package would have performed. One database is one lock
file however it was named, and a test drives all four spellings against one holder.

**An empty answer is every in-memory and temporary spelling at once** — `:memory:`, `file::memory:`,
`file::memory:?cache=shared`, `?mode=memory`, all measured — which is why it is asked for rather than
pattern-matched. Such a database belongs to the connection that opened it (ADR 0050), so there is
nobody to exclude: the lock is the session's own flag, a second acquire is still refused, and no file
is written anywhere. That is a complete answer, not a degraded one, and `doc.go` says so.

### The acquire is a four-rung ladder

```
flock refused                                   -> keep waiting
flock granted, record names a live process
   on this host that is not us                  -> give it straight back, keep waiting
flock granted, anything else                    -> write our record; we hold it
budget spent                                    -> ErrLockTimeout, holder read from the file
```

**The second rung is the staleness rule's second half, and it is narrow on purpose.** Where flock is
enforced, a granted lock and a running holder cannot both be true — so the combination is not a
contradiction to resolve, it is *evidence that flock is not being enforced*, which is the network
filesystem this strategy owes a story about. It backs off rather than failing outright, so a
legitimately slow run on the other side of a bad mount still finishes and this one still acquires.

It fires only when the recorded host is ours, because **a pid means nothing on another host**; and
only when the pid is not our own, because our own leftover record is ours to overwrite. A record from
another host is therefore *taken*, and that is invariant #11 deciding it: a container that crashes
and comes back under a new hostname must not be wedged forever by its own corpse, and refusing there
would buy a cross-host guarantee the filesystem never offered.

**Liveness is `kill(pid, 0)`, and `EPERM` means alive.** Measured: `ESRCH` for a pid nothing is
using, `nil` for one we own, and `EPERM` for pid 1, which is root's and very much running. Reading a
refused signal as *gone* is the one mistake this check exists not to make — it would take a lock a
running migration holds. `os.FindProcess(…).Signal(syscall.Signal(0))` answers the same three
correctly but collapses `ESRCH` into `os.ErrProcessDone`; the syscall is used directly because the
half that matters is the half it does not translate.

### The wait is a poll, and it is still ADR 0020's one budget

`flock` has no form that takes a deadline: the blocking form waits forever, and abandoning it leaks a
descriptor that may acquire afterwards. So the wait is a non-blocking attempt on a fixed 50ms
interval inside one total `lock.timeout`.

That is not option B2 returning. B2 was rejected because `pg_advisory_lock` **queues, fairly**, so a
waiter that gives up and re-acquires loses its place to everyone who arrived meanwhile. A
non-blocking `flock` never enters a queue, so there is no place to lose — and the budget is still one
budget, measured from the first attempt, not one per attempt. No jitter, which is also all
`forbidigo`'s ban on `rand` leaves available.

A **canceled context is not contention.** It returns the context's own error, so a `Ctrl-C` does not
exit 3 and an operator is not sent looking for a run that does not exist. Neither is a lock file that
cannot be created: that names the file and `ErrLockFile`.

### The holder's age comes from SQLite's clock

The record carries `strftime('%Y-%m-%dT%H:%M:%fZ','now')`, and reading a holder computes
`(julianday('now') - julianday(?)) * 86400000`. That is ADR 0020's option E2 — *the server
subtracts* — with the in-process library as the server, and it is why no clock had to be injected
into `driver.Options`. Measured: the stamp round-trips, a 350ms sleep reads back as exactly 350, an
unreadable stamp comes back **NULL** rather than erroring, and a stamp from the future comes back
large and negative. Both of the last two are clamped to zero, which `LockHolder.String()` already
renders as *"whose session age this connection is not permitted to read"*.

One honest divergence to record: for PostgreSQL that field is the holder's **session** age, which
bounds the hold from above; here it is the hold itself, which bounds the session from below. The
shared rendering says "session" for both. The hold is the more useful of the two for this strategy,
and it is never longer than the session, so the number understates rather than overstates.

### A record nothing can read is a reason, not a failure

Readers hold no lock, so a read can catch a write mid-flight. `LockBusyError.Unidentified` already
exists for exactly this, and there are three distinguishable reasons a file names nobody: it is
empty, it holds something that is not a record, or it records no process. Reporting which one is what
buys this design out of needing an atomic write — and an unreadable record never blocks an otherwise
free lock, because flock is what decides.

## Considered Options

### A. What holds the lock

- **A1: `flock`, with the file as a rendezvous point** ✔ — the kernel reclaims it on death, which is
  invariant #11 for free on every local filesystem.
- A2: the file's *existence*, created `O_EXCL`. **Rejected**: this is the failure mode the task names
  in its own text. A crash leaves the file, the next run finds it, and there is nothing but a pid
  and a guess to distinguish a holder from a corpse — so either every crash needs an operator or
  every stale-looking lock gets stolen.
- A3: `fcntl` record locks on a byte range. **Rejected**: they are per-process rather than per
  descriptor, so two sessions in one process would not see each other — and the untagged test layer
  runs every contention case in one process. It is also what SQLite's own VFS uses on the database
  file, which is a good reason for migr to use something else on a different file.
- A4: SQLite's own `BEGIN EXCLUSIVE` held for the migration. **Rejected**: ADR 0017 makes a
  transaction a closure the driver brackets, so a transaction held across the whole run is not a
  thing this contract can express — and it would make the lock and the migration's own transaction
  the same object, which ADR 0011 separated deliberately.

### B. Whether the file is unlinked on release

- **B1: never; truncate the record instead** ✔.
- B2: unlink, with an inode re-check after each acquire. **Rejected**: it adds a race and then a
  check for it, to remove a blank file nobody minds. The check is also easy to get subtly wrong and
  its failure mode is two holders.

### C. flock granted while the record names a live local process

- **C1: give the lock back and keep waiting; report it as contention if the budget runs out** ✔ —
  and the report says *why* a granted lock was refused, since that sentence is the only warning an
  operator will get that their filesystem is not enforcing locks.
- C2: trust flock; the record is only ever a report. **Rejected**: it has nothing whatever to say on
  a mount where flock is advisory, and two concurrent migrations is the outcome the done-when names.
- C3: refuse outright rather than backing off. **Rejected**: a recycled pid over a stale record would
  then refuse a free lock for good, and this rung cannot tell that case from a real one.

### D. flock granted while the record names a live process on **another** host

- **D1: take the lock** ✔, on invariant #11.
- D2: refuse, naming the file as the remedy. **Rejected**: a pid is not checkable across hosts, so
  this refuses on a record's mere presence — which is A2's failure with extra steps, and wedges a
  rebuilt container against its own predecessor.
- D3: age the record out after a threshold. **Rejected**: it needs a clock the driver does not have,
  a threshold nobody can choose correctly, and it steals the lock from any migration slower than the
  guess.

### E. Where the lock file's path comes from

- **E1: `PRAGMA database_list`** ✔ — measured to normalise relative paths, `..`, `file:` URIs and
  symlinks, none of which this package would otherwise agree about with itself.
- E2: the DSN, cleaned and made absolute. **Rejected**: it is a reimplementation of the engine's own
  resolution, and every disagreement between the two is two lock files for one database.
- E3: a fixed directory keyed by a hash of the DSN. **Rejected**: two spellings of one database hash
  differently, which is the same defect with the evidence hidden.

### F. What an in-memory database's lock is

- **F1: the session's own flag** ✔.
- F2: refuse. **Rejected**: it removes `:memory:` from every command for a promise nothing needed,
  and the whole untagged suite would have to move to files to say anything.
- F3: a file in the temp directory keyed by the DSN. **Rejected**: processes cannot share an
  in-memory database, so it would lock against nothing while looking like it locks.

### G. Where the holder's age comes from

- **G1: SQLite's clock, written into the record and subtracted by SQLite** ✔.
- G2: a clock on `driver.Options`. **Rejected**: a contract change to every driver so that one of
  them can render one field, when the engine already answers.
- G3: leave `Connected` zero. **Rejected**: the age is sitting in the file, and the rendering for a
  zero says the connection is not permitted to read it, which would be false.

## Consequences

### Positive

- `lock.strategy: file` is a strategy rather than a word, and `New` refusing anything else
  (M8-T1) now refuses in favour of something real.
- A crashed `migr` leaves a SQLite database immediately usable, with no operator step — proved at the
  CLI, with an external process holding the lock file, killed, and the next `migr up` acquiring.
- A blocked run exits 3 with a message naming the lock file, the pid, the host, the application and
  how long it has been held, on stderr plainly and as one document under `--json`.
- Every contention case is untagged and in `make check`, on both CI legs, because flock conflicts
  between open file descriptions rather than between processes — measured, and the reason the suite
  needed only one subprocess rather than one per case.
- `internal/driver/sqlite/pending.go` is down to the history store alone.

### Negative

- A blank `<db>-migr.lock` stays beside every SQLite database migr has ever run against.
- On a filesystem where `flock` is advisory, the second rung is a heuristic and not a guarantee.
  SQLite's own locking is unsafe there for the same reason, so this is a limit inherited rather than
  introduced — but it is a limit, and `doc.go` states it rather than leaving it to be discovered.
- A recycled pid over a record a release could not blank will refuse a lock that is free, until the
  file is emptied. The message names the file.
- The driver builds on non-unix platforms and `Lock` refuses there by name, following
  `internal/runtime/group_other.go`. Nothing in CI exercises that half.

### Neutral

- `lock.timeout` reaches a second engine, and the two spellings of a wait stay per-driver: PostgreSQL
  renders integer milliseconds into `lock_timeout`, SQLite counts a Go timer. That is M4-T4's finding
  about `Duration.String()` holding for the second driver by not being relevant to it.

## Rejected

**Making the lock the database's own `-journal` or `-wal` file.** Those are SQLite's, it deletes and
recreates them, and locking one would be a race against the engine rather than against another migr.

**A process-local registry keyed by the resolved path, beside the flock.** It was in the plan as the
fallback if flock turned out not to conflict within one process. It does conflict — measured — so the
registry would be a second source of truth with nothing to add, and the one case it would cover
differently (the same session locking twice) is refused explicitly instead.

**`migr unlock --force`.** Still reasonable, still needs a command surface that does not exist, and
ADR 0020 option G3 already deferred it. Nothing in this design needs it: the only state an operator
may have to clear is a record, and `true > file` clears it.

## Notes

The measurements are in the task's findings in `TASKS.md` and the ones that shaped a decision are
quoted above. Two of them changed the design rather than confirming it: the in-process flock conflict
is what let the whole test layer stay untagged, and `EPERM`-means-alive is what stopped the liveness
check from stealing a held lock.

M8-T6 is what makes `migr up` against SQLite reach the database at all; until it lands, the lock is
taken and released on every run and the failure that follows names the history store.
