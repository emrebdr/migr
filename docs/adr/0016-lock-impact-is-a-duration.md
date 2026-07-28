# 0016. Lock impact is a duration, not a lock level

## Status

Accepted

## Date

2026-07-28

## Context

[ADR 0004](0004-safety-model.md) gave the capability model a lock-impact axis with four values —
`LockFree`, `BriefExclusive`, `LongExclusive`, `TableRewrite` — and [ADR 0012](0012-capability-resolution.md)
gave it a shape. Neither said what the four values *mean*, because until PostgreSQL was classified
(M3-T3) nothing had to decide.

Writing the first real table forced the question, and the obvious reading turns out to be wrong.
The names suggest they describe **which lock is taken**: `LockFree` means no lock, `BriefExclusive`
means an exclusive one, and so on. Under that reading PostgreSQL's table is almost entirely
`BriefExclusive` and above, and nothing distinguishes `DROP COLUMN` — a catalog update — from
`CREATE INDEX`, which holds its lock for as long as the index takes to build. Both take a lock that
blocks somebody.

Nearly every `ALTER TABLE` in PostgreSQL takes `ACCESS EXCLUSIVE`, which blocks readers as well as
writers. `CREATE INDEX` takes `SHARE`, which blocks writers and admits readers. So "which lock"
does not order the operations by what an operator actually cares about, and it puts a lighter lock
held for a minute below a heavier lock held for a millisecond.

The reading that works is **how long the lock is held, and what that duration scales with**:

- `LockFree` — nothing that already exists is locked at all.
- `BriefExclusive` — a lock is taken, and the work under it is bounded and independent of the
  table's size. A catalog update.
- `LongExclusive` — the work under the lock scales with the table: a full scan to validate, or an
  index build.
- `TableRewrite` — every row is written to new storage.

This matters most for the case the milestone plan calls canonical. PostgreSQL 11 stores a new
column's default in `pg_attribute` and applies it on read instead of writing every row, which is
universally described as making `ADD COLUMN ... DEFAULT` "instant". It does not make it free: the
statement still requests `ACCESS EXCLUSIVE`, and invariant #12 exists precisely because a blocked
`ALTER TABLE` queues every subsequent query on that table behind it. An operation that takes three
milliseconds once it holds the lock can still take a table offline for the length of the
long-running `SELECT` it is waiting on.

## Decision

**The four verdicts describe the duration of the lock, not its level.** PostgreSQL's declaration
follows from that, and every driver's should.

Only two v1 operations are `LockFree` — `create_table`, and `create_view` without `or_replace` —
because neither touches an object that already exists. The PostgreSQL 11 improvement moves
`ADD COLUMN ... DEFAULT` from `TableRewrite` to **`BriefExclusive`**, not to `LockFree`.

Two consequences of the duration reading are worth stating because they are not obvious from the
names:

**`add_index` is `LongExclusive` even though `SHARE` admits readers.** The vocabulary has no value
for "blocks writers only", and inventing one would be a fifth severity ADR 0012 argued against
adding for `LockUnknown`. A writer blocked for the length of an index build is the cost an operator
needs to see, and that is what `LongExclusive` says.

**`raw` is `LockUnknown` permanently.** Every other unknown in this project is a worklist entry.
This one is not: the framework cannot know what a raw statement does, so no later task resolves it.
A driver with nothing left to classify still reports one unknown, which means "unknown means
unclassified" is the wrong reading of the verdict.

### What the verdict is derived from

Invariant #13 requires the answer to come from the connected server rather than from documentation,
and PostgreSQL's declaration does so in two places:

- The **version**, detected in `Open` (M3-T1) and closed over by every rule, which is what
  separates the PostgreSQL 10 answer from the 11+ one.
- The **volatility of a default function**, read from `pg_proc`. This is the part documentation
  omits: release 11's stored-default optimization only applies when every row gets the *same*
  value. A volatile default such as `gen_random_uuid()` needs a distinct value per row, so it
  rewrites the table on every version. The driver asks the server rather than carrying a list.

That same lookup answers a support question M3-T2 could not (see [ADR 0015](0015-payload-dependent-refusals.md)):
`gen_random_uuid()` moved into core in PostgreSQL 13, so a server that does not have it now gets a
refusal from the declaration at validation time instead of an error from the server at apply time.
Asking beats testing the version — a PostgreSQL 12 with `pgcrypto` installed has the function, and
`AtLeast(13)` would have refused it.

### What a live server can and cannot settle

A table rewrite is **observable**: it replaces the table's `relfilenode`. So the `TableRewrite`
claims are verifiable directly, and an integration test asserts the implication that matters —
**nothing rewrote that was not declared a rewrite**. Transactional DDL is equally observable, by
creating a table in a transaction and rolling it back.

The `BriefExclusive` / `LongExclusive` boundary is not observable this way, because it is a duration
under contention rather than a state change. That is what M13-T12's zero-downtime suite measures,
and this ADR does not claim it has been measured.

## Consequences

**The implication is one-directional, deliberately.** `alter_column` with a type change is declared
`TableRewrite` unconditionally, and PostgreSQL does *not* rewrite for a binary-coercible cast such
as `varchar(255)` → `varchar(512)` or `varchar` → `text` — confirmed against PostgreSQL 16 and 17.
The operation carries the old type only as `from`, which `docs/ir.md` §8.2 calls trust rather than
proof: it is what a migration author recorded, not what the column is. Over-reporting costs a flag;
under-reporting costs an outage. The integration test logs each over-report by name, so the
imprecision is visible rather than assumed.

**`Facts.TransactionalDDL` exists now rather than in M4-T9.** M3-T3's done-when requires it declared
accurately, so M4-T9 reads a field instead of adding one and reopening every driver.

**Three capabilities the milestone plan names are not declared, because no v1 operation can request
them.** `add_index` has no predicate, so a partial index cannot be asked for; no operation carries
`DEFERRABLE`; and `CREATE INDEX CONCURRENTLY` is something M13-T11 chooses to emit rather than
something a migration requests. A field recording a server capability that no operation can reach
is a constant nothing reads. When the IR gains the field, the capability follows it.

**Native enums stay unsupported, and no task will change that.** [ADR 0015](0015-payload-dependent-refusals.md)
refused them pending this task; this task declines to build the lifecycle, because the blocker is
structural rather than scheduling. A PostgreSQL enum is a `CREATE TYPE` object and the v1 IR has no
operation that owns one, so nothing drops it and apply → roll back → apply fails on a type that
already exists. The refusal now names what would be needed — an operation that owns the type — and
stops naming a milestone task, because a reason that names a task promises work nobody scheduled.

## Rejected

**Reading the four values as lock levels.** It is what the names suggest, and it makes the axis
useless: it cannot distinguish a catalog update from an index build, and it ranks a heavy lock held
briefly above a lighter one held for minutes. An operator asks "how long is my table unavailable",
not "which lock mode".

**A fifth value for "blocks writers, admits readers".** It would describe `CREATE INDEX` and
`ADD CONSTRAINT ... FOREIGN KEY` precisely. But ADR 0012 already rejected adding a fifth severity
for `LockUnknown`, on the grounds that the ceiling vocabulary an operator configures should stay
small enough to reason about, and `safety.max_lock_impact` would gain a value whose ordering
against `LongExclusive` depends on the workload rather than on the engine.

**Reporting the PostgreSQL 11+ `ADD COLUMN ... DEFAULT` as `LockFree`.** It is what the word
"instant" implies and what a hand-written fixture in `internal/capability/set_test.go` uses to
demonstrate the mechanism. It would be a claim that the operation cannot block anything, which is
false — it requests `ACCESS EXCLUSIVE` like every other `ALTER TABLE`, and `--require-safe` with a
`lock_free` ceiling would then pass an operation that can still take a busy table offline while it
waits for the lock.

**Keying the retype verdict to `from.type`.** Rejected on merit, not on cost: the recorded prior
state is unverified, and a lock verdict is a safety decision. Worth revisiting only if
introspection (M11) supplies the column's real current type at plan time, at which point the input
would be proof rather than trust.

**Hard-coding `gen_random_uuid()` to PostgreSQL 13+.** Cheaper by one round trip and wrong for any
server carrying `pgcrypto`, which is how everyone had that function for the decade before it moved
into core.
