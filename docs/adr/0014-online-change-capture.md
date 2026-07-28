# 0014. Online change capture reads the write-ahead log, and cutover waits for a human

## Status

Accepted

## Date

2026-07-28

## Context

[ADR 0004](0004-safety-model.md) promoted online DDL into v0.1 and named shadow-table migration
as the subtle part of it: *"change capture, chunked copy, cutover, abort and cleanup — and it must
be correct under concurrent write load."* It did not decide how change capture works, and the
tasks written against it did: M13-T10's acceptance criterion said **trigger-based** capture while
its own title said "gh-ost / pt-osc class". Those are two different tools with two different
mechanisms, and the criterion had quietly picked the older one.

The lifecycle those tasks describe is create a shadow table, copy the existing rows in chunks,
capture writes that land during the copy, then swap the two tables by rename. What they do not
describe is what happens between "the copy has finished" and "the swap": M13-T9 reads
`copy → sync → cutover` as a pipeline that runs to completion on its own.

That gap is the decision this record exists to close, and it is not a missing step. A rename that
happens the moment the copy finishes gives an operator no moment to look at the result, and it
happens at whatever time of day the copy happens to end — which for a large table is exactly the
time nobody chose.

Two further facts constrain the answer. `driver.Conn` is one pinned session that executes
statements, takes the migration lock, answers for capabilities, and holds the history store
([ADR 0011](0011-driver-contract.md), [ADR 0013](0013-migration-history.md)). Reading a
replication stream is not that: it is a second connection of a different kind, with different
server privileges. And the driver contract is about to be implemented for the first time — M3 is
the next milestone — so anything the contract needs is far cheaper to record now than to add
after three drivers exist. That is the argument [ADR 0012](0012-capability-resolution.md) made for
the lock-impact axis, applied a second time.

## Decision Drivers

- The reason to run a shadow-table migration at all is that the direct `ALTER` would hurt
  production. A capture mechanism that hurts production differently has not solved the problem.
- Cutover is the only irreversible moment in the lifecycle, and it is brief. Everything before it
  can be abandoned by dropping a table.
- Whatever holds the change stream open holds a server-side resource. On PostgreSQL an abandoned
  replication slot makes the server retain WAL until the disk fills — the tool causing the outage
  it exists to prevent.
- Managed databases routinely withhold replication privileges, so a mechanism that requires them
  is unavailable to some users on some plans.
- The capability model exists precisely so that an engine difference is declared rather than
  branched on in the engine (CLAUDE.md §11).

## Considered Options

### Question A: how are writes captured during the copy?

**A1 — Triggers on the original table,** writing every insert, update, and delete into a changelog
table or directly into the shadow. This is `pt-online-schema-change`, and it is what M13-T10's
acceptance criterion currently says.

**Pros**

- Needs no special privileges and no replication configuration, so it works on managed databases
  that withhold both.
- Engine-agnostic in shape: every engine has triggers.
- The change stream cannot fall behind independently of the writes, because it *is* the writes.

**Cons**

- **Every production write does extra synchronous work for the entire duration of the copy**,
  which on a large table is hours. The migration's cost is paid by the application's write path —
  the thing the shadow-table approach exists to protect.
- Creating and dropping the triggers each take a metadata lock on the original table, so the
  approach that avoids a long lock introduces two short ones at its most dangerous moments.
- Triggers compose badly with triggers the application already has, and MySQL's per-table limits
  historically made this a hard failure rather than a slowdown.

**A2 — Read the engine's replication log:** MySQL's binlog in row format, PostgreSQL's WAL through
logical decoding. This is `gh-ost`'s model, and avoiding triggers is the reason it exists.

**Pros**

- **Production writes do no extra work.** The reader is a consumer of a stream the server is
  already producing for its replicas.
- The reader can be throttled, paused, or restarted without touching the write path, which is what
  makes an indefinite steady state possible at all (Question C).
- Position in the stream is an explicit, storable value — a GTID or an LSN — so resuming is
  reading from a recorded offset rather than reconstructing state.

**Cons**

- Requires a second connection of a different kind and server-side privileges the ordinary
  migration connection does not need.
- Requires server configuration the tool cannot supply: `binlog_format=ROW` on MySQL,
  `wal_level=logical` on PostgreSQL.
- **Holds a server-side resource with an outage attached.** A PostgreSQL replication slot left
  behind after a crash retains WAL indefinitely.
- Two genuinely different implementations, sharing a shape but no code.

**A3 — Both, declared per driver.**

**Pros**

- A managed database that forbids replication still gets online DDL, by the slower route.
- The capability model already exists to express exactly this kind of per-engine, per-server
  difference.

**Cons**

- Two capture implementations per engine, and the shadow-table subsystem was already named the
  subtle part of the largest milestone.
- The two mechanisms have different failure modes, so the tests double rather than share.

### Question B: where does the capture contract live?

**B1 — A method on `driver.Conn`.**

**Rejected because:** a `Conn` is one pinned session, and its pinning exists so that a
session-scoped advisory lock is released by the backend that took it. A replication stream is a
separate connection with a separate lifetime — it outlives individual statements and must be
closed even when the migration connection is healthy. Hanging it off the migration session
conflates two lifetimes that fail independently.

**B2 — A method on `driver.Driver` that opens its own connection,** returning a stream with its
own `Close`.

**Pros**

- The stream's lifetime is its own, which is what its cleanup requirements demand.
- `Driver` already knows how to open a connection; this is a second kind of one.

**Cons**

- `Driver`'s existing methods need no database (`Name`, `Compile`) except `Open`, so this adds a
  second I/O method to an interface that was deliberately kept thin.

**B3 — A separate interface a driver optionally implements,** discovered by type assertion.

**Rejected because:** optional-by-assertion is how a capability becomes silently absent. The
conformance suite's "no third possibility" rule exists to stop exactly that, and a driver that
cannot capture changes must say so with a reason rather than by failing an assertion.

### Question C: does cutover happen automatically, or does a human confirm it?

**C1 — Automatic on copy completion.** What the tasks describe today.

**Rejected because:** the rename is the only irreversible moment, and it would land at whatever
hour the copy happened to finish. It also gives nobody a chance to compare the two tables before
the old one stops receiving writes.

**C2 — Always waits for an explicit confirmation.**

**Pros**

- The irreversible step happens at a time a human chose.
- There is a defined moment at which both tables exist, are in sync, and can be compared.

**Cons**

- A migration is no longer a single command that finishes, which every other part of this tool
  assumes.
- Something must hold the sync while nobody is looking at it, for an unbounded time.

**C3 — Waits by default, with an opt-out for unattended use.**

**Pros**

- CI and small tables do not need a human in the loop; a 400GB table does.
- The default is the safe one, which is the posture invariant #13 takes elsewhere.

**Cons**

- Two paths through the most dangerous moment in the system, and the unattended one will be less
  exercised.

### Question D: what happens to the original table after the rename?

**D1 — Drop it as part of cleanup.** What M13-T9's "cleanup" currently implies.

**Rejected because:** it discards the only copy of the pre-migration data at the moment the new
table has the least operational history behind it. If the shadow table is wrong, the evidence is
already gone.

**D2 — Retain it under a derived name, dropped by a separate explicit step.**

**Pros**

- A rollback is a second rename rather than a restore from backup.
- The drop is a decision with its own moment, which is what invariant #6's posture asks for
  everywhere else.

**Cons**

- Disk usage doubles until someone acts, and a forgotten retired table is a real cost on a large
  table.
- It needs a name, and the obvious ones collide with things that already have meanings here.

## Decision Outcome

**A2, B2, C3, and D2.**

What that commits to:

- **Change capture reads the replication log.** MySQL row-format binlog, PostgreSQL logical
  decoding. Triggers are not implemented — not as a fallback, and not as an option. A server that
  will not permit log-based capture gets an honest refusal naming what it lacks, in the same shape
  as any other unsupported operation. Option A3 is what a later decision would revisit if managed
  databases prove to be a common enough target; adding a second declared mechanism is a capability
  fact, not a reopening.
- **The capture stream is opened by the `Driver`, not by a `Conn`,** and it owns its own
  connection and its own `Close`. Its types live in a new package that must never import
  `internal/driver` — the same one-way dependency ADR 0011 recorded for the history store, and
  which the compiler now enforces for the same reason.
- **A stream position is an explicit, recordable value.** Resuming is reading from a stored offset
  rather than inferring where the copy got to.
- **Cutover waits for an explicit confirmation by default,** and the wait is unbounded. Between
  copy completion and confirmation the shadow table is held continuously in sync, and the
  framework reports how far behind it is — because "it is up to date" is the claim the operator is
  being asked to act on, and an unproven claim at that moment is worse than no claim.
- **Confirmation is a first-class command surface,** not a prompt. A migration that waits for a
  human must be inspectable and confirmable from a different terminal, a different host, and after
  a restart.
- **The original table is retained after the rename**, under a derived name, and dropped only by a
  separate explicit step. It is *not* the history table: `migr_migrations` is the migration log
  (ADR 0013) and this is a retired data table. The two never share a name.
- **A capture resource left behind is treated as an outage, not as litter.** An abandoned
  PostgreSQL replication slot retains WAL until the disk fills, so detecting and reporting orphaned
  slots is part of the abort path rather than part of cleanup.

## Consequences

### Positive

- Production writes pay nothing for the copy, which is the property that made this approach worth
  the milestone in the first place.
- The irreversible moment is chosen by a person and is visible before it happens.
- A rollback immediately after cutover is a rename, not a restore.
- The contract additions land before the first driver, so M13 does not reopen three of them —
  which is the whole reason this record exists now rather than at M13.

### Negative

- **Managed databases that withhold replication privileges cannot use shadow-table migration at
  all**, and will be told so rather than silently given a slower path. That is a real loss of
  reach, taken deliberately.
- **A migration can now be a thing that waits**, and nothing else in this tool works that way.
  `migr up` finishing no longer means the schema changed, which every existing command, exit code,
  and status description assumes.
- The unattended opt-out is a second path through the most dangerous moment in the system, and it
  will be the less exercised one.
- Disk usage doubles from cutover until the retired table is dropped, and nothing forces that to
  happen.
- Two capture implementations that share a shape and no code, in the milestone already identified
  as the one most likely to expose a wrong assumption made earlier.

### Neutral

- `Conn` is unchanged. Capture hangs off `Driver`, so ADR 0011's list of deferred `Conn` methods
  is still `Introspect` and `ServerVersion`.
- Whether a server permits log-based capture is a `capability.Facts` field, so it is a struct field
  added later rather than an interface method — the retrofit-cost argument
  `internal/capability` was built around, used a third time.
- This is a fifth independent version line's worth of engine configuration (`binlog_format`,
  `wal_level`) that migr reads and never sets. Changing either requires a server restart on some
  configurations, so it is a precondition to report, not a step to perform.

## Notes

Nothing here is implemented. M3 through M12 land first, and the shadow-table subsystem is M13. The
value of deciding now is confined to what the driver contract and the capability model have to
carry, which is exactly the part that is expensive to change later.
