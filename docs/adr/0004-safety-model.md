# 0004. Lock impact as a capability axis; zero-downtime as a v0.1 correctness property

## Status

Accepted

## Date

2026-07-27

## Context

Migrations run against live production databases. A migration that generates correct SQL and
blocks every query against a table for four minutes has failed, even though nothing errored and
the schema afterwards is exactly right.

The original roadmap treated this as a maturity concern: ship correct schema changes first, add
online migration strategies after 1.0. That ordering was reconsidered for two reasons.

First, the project's stated goals name safe online migrations for large datasets, gh-ost-class
strategies, and zero-downtime deployment explicitly. Deferring them past 1.0 would mean 1.0 did
not do what the project says it is for.

Second, and more decisive: an audit of the roadmap found that the cheapest pieces to build now
were the most expensive to retrofit. The engine's transaction model and the capability model
both needed structural room for safety concerns *before* three database drivers were written
against them. Adding an axis to a capability model that three drivers already implement means
reopening all three.

[ADR 0001](0001-architecture.md) established the capability model as the place where
engine-specific knowledge lives. This ADR decides what that model must express.

## Decision Drivers

- **Users cannot verify this themselves.** Whether an `ALTER TABLE` blocks writes depends on
  engine, version, column type, and the specific alteration. If the framework does not know,
  nobody does.
- **Lock behaviour is engine- and version-specific.** The correct answer changes between minor
  releases of the same database.
- **Retrofit cost.** Some pieces must exist before drivers are written; others can follow.
- **A wrong answer is worse than no answer.** "Safe" is a claim users act on.

## Considered Options

Two questions are decided here, because the second only makes sense once the first is settled.

### Question A: Where does zero-downtime sit in the roadmap?

#### Option A1: A full v0.1 milestone, including shadow-table online DDL

Lock-impact classification, lock timeouts with retry, safe-by-construction operation rewriting,
multi-deploy expand/contract gating, replica-lag-aware throttling, and gh-ost-class shadow-table
migration — all before 1.0.

**Pros**

- 1.0 does what the project says it does.
- The structural pieces land before the drivers that depend on them.
- Safety becomes a property the framework proves rather than one operators supply.

**Cons**

- Materially enlarges and delays v0.1; this becomes the largest milestone in the release.
- Shadow-table migration is a subtle subsystem — change capture, chunked copy, cutover, abort
  and cleanup — and it must be correct under concurrent write load.

#### Option A2: Foundations in v0.1, heavy online DDL after 1.0

Lock timeouts, the lock-impact axis, the transaction opt-out, and throttling now; shadow-table
migration later.

**Pros**

- Captures every cheap-now/expensive-later piece.
- Keeps v0.1 substantially smaller.

**Cons**

- The headline capability — online migration of a large table — is still absent at 1.0.

**Rejected because:** it stops just short of the thing the project advertises. The foundations
alone make migrations *safer*, not *online*.

#### Option A3: Foundations only, no dedicated milestone

**Rejected because:** the individual pieces do not add up to a coherent, verifiable property
without something that ties them together — a preflight, a gate, and a conformance suite that
proves the claim under load.

#### Option A4: Leave it post-1.0 entirely — the original roadmap

**Pros**

- Smallest, fastest v0.1.
- Defers the hardest subsystem until the engine is proven.

**Cons**

- The transaction opt-out and the lock-impact axis would be retrofitted into an executor and a
  capability model that three drivers already implement.

**Rejected because:** of the retrofit cost, not the sequencing instinct. Deferring hard
subsystems is usually right; here two of the prerequisites are structural, and structural
changes get more expensive with each driver written against the old shape.

### Question B: How is lock cost modelled?

#### Option B1: A second, independent capability axis

Support answers *can this operation run* — Native, Emulated with a named strategy, or
Unsupported with a reason. Lock impact separately answers *what does it cost a live database* —
LockFree, BriefExclusive, LongExclusive, or TableRewrite.

**Pros**

- The two questions are genuinely orthogonal: an operation can be natively supported and
  catastrophic, or emulated and cheap.
- Keeps engine-specific knowledge in the capability model rather than in engine-specific
  branches in the planner.
- Gives preflight checks and CI gates something concrete to threshold against.

**Cons**

- Every driver must answer twice as many questions, per operation and per server version.

#### Option B2: Fold cost into the support verdict

Extend the existing verdict — Native, NativeSlow, Emulated, Unsupported.

**Pros**

- One axis to implement and reason about.

**Cons**

- Conflates two independent properties, so the combinations that matter cannot be expressed.

**Rejected because:** "natively supported" and "cheap" are not correlated. `ALTER TABLE … TYPE`
is natively supported by PostgreSQL and rewrites the whole table; SQLite's emulated column drop
on a small table is fine. A single axis cannot say both.

#### Option B3: Static per-driver lock tables

A lookup table per driver mapping each operation to a fixed verdict.

**Pros**

- Trivial to implement, inspect, and test.
- No runtime detection, so no dependency on the connected server.

**Cons**

- Wrong, and confidently so. `ADD COLUMN … DEFAULT` rewrites the entire table on PostgreSQL 10
  and is instant from 11 onward. MySQL 8's choice between INSTANT, INPLACE, and COPY depends on
  the column type and the specific alteration, not on the operation name.

**Rejected because:** a static table would report "safe" for an operation that rewrites a
hundred-million-row table on a server one minor version older than assumed. A confidently wrong
safety verdict is worse than no verdict — it is acted upon.

#### Option B4: Lint rules that warn about known-dangerous operations

**Pros**

- Cheap; fits the existing lint framework.
- No capability model changes.

**Cons**

- Advisory only, and pattern-matched on operation shape rather than resolved against the target.

**Rejected because:** a warning that can be ignored is not a safety mechanism. The operations
that cause outages are ordinary operations against large tables, and the warning would fire on
every schema change until people stopped reading it.

## Decision Outcome

**A1 and B1.** Zero-downtime is a v0.1 correctness property with a full milestone, and lock
impact is a second, independent axis of the capability model.

What that commits to:

- **Lock impact is resolved at runtime from the connected server's version and capabilities,
  never hard-coded.** When a driver cannot determine the answer, it reports the pessimistic
  verdict. A driver that reports the wrong lock impact is worse than one reporting
  `Unsupported`.
- **No DDL statement waits unbounded for a lock.** Every statement runs under a lock timeout
  with bounded retry and backoff. This is not an optimisation: on PostgreSQL the lock queue is
  FIFO, so an `ALTER TABLE` blocked behind a long-running query blocks *every subsequent query
  on that table* behind it. A migration doing three milliseconds of work takes the table down
  for as long as the query ahead of it runs. This is the most common cause of
  migration-induced outages and the single highest-value item in the milestone.
- **Unsafe operations are decomposed into safe sequences where the engine allows** — a
  constraint added as `NOT VALID` then validated, a `SET NOT NULL` backed by a validated check,
  a concurrent index build — and every rewrite is visible in plan output, never silent.
- **An honest `Unsupported` beats a clever emulation.** A clear failure at validation time is
  worth more than a surprising table rebuild in production.
- **The claim is proved mechanically.** A conformance suite runs each operation against every
  driver under concurrent read/write load and asserts no client query exceeds a latency budget.
  A driver that cannot meet the budget must declare its lock impact honestly; declaring
  `TableRewrite` always passes, silently blocking never does. Without this suite the
  zero-downtime claim is marketing.

## Consequences

### Positive

- Safety is a verified property of the framework rather than operator discipline.
- A CI job can block a merge whose migration would exceed a configured lock-impact threshold,
  turning an operational concern into a code-review one.
- Engine-specific lock knowledge accumulates in one place with a defined shape.

### Negative

- v0.1 is materially larger and later. This is the largest milestone in the release and the one
  most likely to expose a wrong assumption made earlier.
- The capability model carries two axes from the moment it exists, even though drivers report
  lock impact as unknown until the classification work lands. The slot is built before it is
  filled, deliberately, because adding it later means reopening every driver.
- The executor must accommodate operations that cannot run inside a transaction — a concurrent
  index build being the motivating case — so "one migration equals one transaction" cannot be
  assumed anywhere, including in drivers that do support transactional DDL.
- Runtime capability detection means the same migration can be classified differently against
  two servers. That is correct, and it makes plan output server-specific.

### Neutral

- Some operations cannot be made zero-downtime on some engines. SQLite's single-writer model
  means most schema changes block writers for their duration. The framework reports this
  honestly rather than implying a guarantee it cannot keep.

## Notes

The retrofit-cost argument is the load-bearing one. If a future decision looks similar — cheap
now, structural, expensive once N implementations exist — this ADR is the precedent for
building the slot early even when the feature is deferred.
