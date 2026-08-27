# 0040. Running a Go migration from the CLI

## Status

Accepted

## Date

2026-08-28

## Context

[ADR 0039](0039-runtime-conformance-fixtures.md) settled **what a Go migration emits**, and settled
it over a *fake* `driver.Conn`: no operation is compiled, no statement executes, no history row is
written. Everything from [`internal/plan`](../../internal/plan) down was still unproven for a Go
source, and [`internal/cli`](../../internal/cli) refused `migrations.language: go` by name.

So this is the last gap in M6. A Go migration goes in as a file and comes out as committed DDL,
through the real CLI, against a real server — and comes back out again.

Three things that look like details turn out to be decisions, and each is recorded below.

## Decision Drivers

- The done-when: *"`migr up` executes a Go migration against PostgreSQL and `migr down` reverts it,
  verified by introspection snapshot comparison."*
- [ADR 0034](0034-runtime-process-lifecycle.md): a `runtime.Session` needs the `driver.Conn` its callbacks
  reach, which fixes an ordering `internal/cli` did not have.
- [ADR 0028](0028-validation-report.md): `migr validate` opens no database **at all**, which is the
  property that makes it the CI command.
- Invariant #3, and M5-T2's reading of it: no test above a driver package composes SQL to send to a
  database on migr's behalf.
- Invariant #10: every migration has both directions, and a source authoring no `Down` has one
  **derived** — machinery `.sql` can never exercise, because `ir.Raw` has no inverse.

## Considered Options

### A. Where the source is built

1. **Before the connection**, as `open()` did for `.sql`.
2. **After it**, so the connection can be handed to the session.

Chosen: **2**, and it is not a preference — a `runtime.Session` takes the `driver.Conn` its
callbacks reach, so before the connection exists there is nothing to give one. `t.source` takes the
connection as a parameter, so the ordering is visible in the signature; and because `Conn` is an
interface, building the source first is still *spellable* by passing `nil`. What refuses that is
[ADR 0034](0034-runtime-process-lifecycle.md)'s `Options.check`, which answers `ErrNoRuntime`,
`ErrNoConn`, or `ErrNoLog` before a process is started — so the reversal fails at `Start` rather
than at compile time, and every command that runs a migration fails with it.

Two consequences the ordering brings with it:

- a source that fails to start leaves a connection already open, so `open()` closes it. Otherwise a
  failed `migr up` leaks a backend on every attempt;
- `session.close` **ends the process before closing the connection**, because a callback in flight
  reaches that connection. This is the same shape as the lock-then-transaction ordering in ADR 0020:
  the two defers are not interchangeable, and the wrong order fails exactly when it matters.

### B. What `migr validate` does with a language that needs a runtime

1. **Open a database** for the languages that need one.
2. **Refuse** a Go project, as before.
3. **Stay database-free**, and report what could not be checked.

Chosen: **3**.

Option 1 trades away the property ADR 0028 exists for. *"It opens no database, so it runs anywhere
the migrations do"* is what makes `migr validate` the command a CI job runs, and making that
conditional on the language means an operator has to learn which of their projects needs a server to
lint. Option 2 leaves the language M6 just delivered with no CI gate at all.

So validate builds the source over `driver.Offline` — a `Conn` in the contract package that refuses
every database call — and **still starts the runtime**. That is worth more than it sounds: for a Go
project it now catches the module failing to build, and the registration errors
[ADR 0037](0037-go-migration-contract.md) answers at `migration.discover`, neither of which anything else
checks before a run.

A migration that never reads is then validated *in full* — both directions compiled, reversibility
checked, exactly as a `.sql` one is. A migration that **does** read is reported as `Unchecked`,
carrying its reason, and is **not** a validation failure: failing there would have a CI gate reject a
migration for reading, which migrations are allowed to do. Silence would be worse still, since a
half nothing judged would be indistinguishable from one that passed.

### C. How "it reached a database" is detected

1. **The sentinel**, `errors.Is(err, driver.ErrNoDatabase)`.
2. **The protocol error code** the refusal comes back as.
3. **The session remembers being asked.**

Chosen: **3**. Option 1 cannot work and the reason is structural: the migration runs in **another
process**, so the sentinel is a string by the time it returns. Option 2 works but couples
`internal/validate` to the wire, and still misses the case that matters most — an SDK that swallows
the failed read and emits anyway. That migration's IR is derived from a read that did not happen,
and reporting it as checked would be the one genuinely wrong answer available.

So `OfflineConn` records having been reached, and the rule is *if the migration reached a database
during this half, that half is unchecked* — whatever it made of the refusal, and whether or not it
went on to succeed. `Capabilities` and `ServerVersion` deliberately do **not** count: they are facts
about the session that need no server, and counting them would make every migration calling
`host.info` unverifiable.

The capability set an offline session hands out refuses every operation with a reason naming why.
That is a refusal rather than a lie about the engine, and a migration branching on it falls inside
the envelope §4.3 already documents and declines to hide: plan mode may take a different branch than
a later apply.

### D. Where the schema digest lives

1. **In `cmd/migr`'s test**, beside the suite that compares it.
2. **Through the public contract alone** — status, history, and a second `up`.
3. **In a package the driver owns**, exported.

Chosen: **3**. Option 1 widens exactly the rule M5-T2 narrowed, and would put PostgreSQL-specific
SQL in the package whose whole job is to be engine-neutral wiring. Option 2 is not nothing — a second
`up` really does catch a rollback that recorded success without dropping anything — but it cannot see
a rollback that dropped the *wrong* thing, which is precisely what a derived inverse might do.

`internal/driver/postgres/pgtest` therefore holds the catalog probes, and depguard already says who
may import it: the deny is on the `internal/driver/` prefix with `cmd/migr` carved out, so the one
package permitted to drive the whole stack is the one package permitted to ask.

`Digest` takes an **`Ask`** rather than a connection, because its two callers cannot reach the server
the same way. `cmd/migr` asks through `driver.Conn.Query` and inherits the read-only transaction
[ADR 0032](0032-host-callback-server.md) gives it; this driver's own suite describes a schema from
*inside* an open transaction, to see what a migration changed before the rollback discards it — and
[ADR 0017](0017-transaction-boundaries.md) has a session refuse a second transaction, so `Query`
cannot serve it at all. Duplicating the probes so each caller could own its fetcher would produce two
digests that drift, which is the one thing a digest must not do.

This is **not** introspection. `internal/schema`'s `Snapshot` is M11-T1 and per-driver introspection
is M11-T2; M11-T6 is where the round trip moves onto the neutral snapshot and `pgtest` goes away.
Until then, a done-when asking for "introspection snapshot comparison" has nothing else it can mean —
the same wall M3-T5 hit, recorded there under `snapshotSupersededBy`.

### E. What the end-to-end Go migrations do

1. **One migration**, authoring its `Down`.
2. **Two**, one authoring a `Down` and one omitting it.

Chosen: **2**. The second is the point. A `.sql` migration can never reach the derivation path,
because `ir.Raw` has no inverse to derive — so invariant #10's machinery had never run against a real
database. Rolling back a Go migration that authors no `Down` makes the engine ask that same migration
for its `up` as a **question** in `ModePlan` and invert it through `ir.ReverseAll`, which is
[ADR 0033](0033-where-the-mode-lives.md)'s *"the mode is the call's, never the session's"* proved end
to end rather than on the wire.

The digest is what proves it landed. The history says the migration is undone either way.

### F. depguard, once a driver has a child package

The blanket deny on `internal/driver/` blocks a driver from importing its own subpackage — a case
`.golangci.yml` had written down as a comment and prescribed the fix for. The fix is a negated
`files` entry, but taken alone it would exempt `postgres` from the deny **entirely**, letting it
import `mysql` and trading invariant #2 for nothing.

So the exemption comes with a rule set of its own: files under `internal/driver/postgres/` are denied
`mysql`, `sqlite`, and `internal/runtime/` by name. A fourth driver growing a child package needs the
same pair, and the day it does is the day it is written down.

## Consequences

- `migr up`, `migr down`, `migr plan`, `migr status`, and `migr validate` all work against a Go
  project, and `cmd/migr`'s integration suite runs the round trip against PostgreSQL 16 and 17.
- `migr validate` is **narrower for Go than for `.sql`, by design rather than by omission**, and the
  report says so per migration rather than in the abstract.
- `migr validate` on a Go project now requires the Go toolchain and builds the module. That is a real
  cost and it is the point: a project that will not build is a validation failure worth catching.
- `internal/cli` gained a wrapper `engine.Source`. It exists only for validate, and only because
  "reached a database" is a fact about the session rather than about the error.
- The digest is `M11-T6`'s to delete, along with `pgtest`.
- TypeScript inherits all of this: M7-T8 is the wiring task, and by then `sourceFor` needs only the
  `sdkTask` row for `typescript` removed.
