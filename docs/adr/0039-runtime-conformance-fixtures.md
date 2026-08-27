# 0039. The runtime conformance fixtures

## Status

Accepted

## Date

2026-08-27

## Context

Everything under this decision is proved and everything above it is asserted.

[`internal/ir`](../../internal/ir) and [`sdk/go`](../../sdk/go) each hold their operation structs to
[`docs/ir.md`](../ir.md), so the two copies agree on every documented vector
([ADR 0038](0038-go-schema-builder.md), E). [ADR 0033](0033-where-the-mode-lives.md) proves a
migration's callbacks produce identical IR in either mode — with a Go closure playing the SDK.
[ADR 0035](0035-generated-go-runner.md) generates a runner, builds it, starts it, and completes a
handshake, and stops at `migration.discover`.

So nothing had ever read **what a migration emits** off the wire from a real source. *"Both emit
the same IR, byte for byte"* is the framework's central claim and the README's second paragraph,
and no test measured it.

The fixtures are also the thing M7 is written against rather than a Go artifact. M7-T3 mirrors the
Go builder, M7-T7 compares the two SDKs, and both need one definition of what a migration *means*
rather than a Go table and a TypeScript table that were derived from each other.

## Decision Drivers

- The done-when: *"`test/conformance/runtime` defines at least 10 canonical migrations with
  expected IR, and the Go SDK produces byte-identical output for all of them."*
- [`docs/ir.md` §3.2](../ir.md#32-key-order-and-whitespace): emitted bytes are fixed and hashed, so
  "identical" means bytes and not structures.
- Invariant #1, enforced by depguard: only `cmd/migr` may name a concrete runtime.
- M2-T4's argument against a build tag on a suite that needs nothing, and M6-T6's finding that the
  Go toolchain is present by definition when `go test` is what is running.
- [ADR 0032](0032-host-callback-server.md) and [ADR 0036](0036-escape-hatch-statements-in-a-preview.md):
  `host.db.exec` is held under `plan` and run under `apply`, and nothing had shown that from an SDK.

## Considered Options

### A. Where the expected IR lives

1. **Per language**, beside each SDK's own tests.
2. **One neutral manifest per fixture**, in a tree neither language owns.

Chosen: **2**. Two tables is two contracts. M7-T7 would compare each SDK to the table it was
written against, which proves each SDK self-consistent and nothing about the pair. A manifest is
also the only place a *shared* fact can be stated — the SQL a migration reads, the rows it is given,
the reason a rollback halts with.

The manifest's own bytes are held to being exactly what `ir.Marshal` emits, so a definition written
any other way fails at load. A fixture nothing could match would otherwise fail every language and
look like a bug in all of them.

### B. Whether a fixture may read

1. **Every fixture is pure**, and the suite's database refuses every callback.
2. **The manifest pins the reads**, and the suite answers them in order.

Chosen: **2**. A migration that never looks at the database is not the interesting half of the
polyglot claim, and ADR 0009 already established that migrations branch on live query results. Two
languages given different rows would emit different IR for a reason that says nothing about either
SDK, so the rows have to be part of the definition.

The suite checks the statement it was actually sent, not just the order, so a language that reads
different SQL fails naming both. One fixture branches on the pinned answer, which is the strongest
form of *identical IR from one source* available: change the row and the expected IR must change
with it.

### C. What a fixture states about its down direction

1. **An operation list, always** — every fixture authors both directions.
2. **A list or a refusal**, since §7.4 defines two answers that are not failures.

Chosen: **2**. `no_down_authored` and `declared_irreversible` are what invariant #10 permits a
migration to say, and until now they were tested only inside `sdk/go` with in-process fakes. A
manifest states one, and the suite requires it to arrive as `engine.ErrNoDownAuthored` or as a
`DeclaredIrreversibleError` **carrying the author's own reason**. Every SDK now owes both answers
from a real source.

The loader refuses a manifest stating both or neither, which is §9.1's either-or rule applied to
the fixture format itself.

### D. The build tag

1. **`//go:build conformance`**, as the tag list said.
2. **Untagged.**

Chosen: **2**, for M2-T4's reason reaching the same answer a second time. The suite needs no
database, and the Go run needs no toolchain `make test` does not already have. Tagged, both would
be invisible to `golangci-lint` — invoked with no `--build-tags` — and to CI, which calls `lint`,
`test`, and `build` and never a conformance target. A skeleton nothing runs is worth less than no
skeleton.

The tag keeps its purpose: a toolchain this machine may not have, which begins at Node in M7. The
sentence in CLAUDE.md §7 that named M6-T9 was written before M6-T6 found the Go exception, and is
amended rather than worked around.

### E. Where a language's run lives, and what the suite is given

depguard permits only `cmd/migr` to name a concrete runtime, so `test/conformance/runtime` cannot
import `internal/runtime/golang`. Each adapter therefore runs the suite **from its own package**,
exactly as M5-T2 put the end-to-end CLI suite in `cmd/migr` for the same reason.

`Suite` takes the adapter and a **prepared project** — two fields, no case list, no options, no skip
hook. Assembling sources into something a runtime can start is that language's own problem, because
a `go.mod` and a `package.json` are not the same shape. `Sources(language)` hands a caller its own
expressions and nothing else.

That is also why the fixture tree carries **no `go.mod`**: `//go:embed` skips any directory holding
one, verified in the toolchain source, and a module manifest is a Go fact in a tree that belongs to
no language. The Go caller writes one.

## Decision Outcome

`test/conformance/runtime` holds twelve fixtures. Each is a manifest naming what every SDK must
emit in both directions, plus what it reads and what it sends itself; each has one expression per
language under the same filename stem, which is also how one fixture carries one version across
languages (ADR 0037, B).

Every fixture is asked **four questions** — both directions in both modes. The operations must be
identical across modes, which is ADR 0033's claim proved from a source rather than a closure, and
which incidentally forbids a fixture from branching on `Result.Executed`. The escape hatch is the
only permitted difference: under `plan` the statements come back in `Emitted.Direct` and the
database sees none of them; under `apply` `Direct` is empty and the database sees exactly those
statements, in order.

Coverage is derived from `ir.Kinds()`: an operation no fixture emits fails the suite, and so does
the twenty-first operation on the day it is added. The count in the done-when is written down
nowhere.

## Consequences

**One language passing this is not two languages agreeing.** The suite is the machinery M7-T7 will
use and not the proof itself. What it does settle now is that the Go SDK's output is pinned, so a
TypeScript SDK written against the same manifests cannot drift silently — and neither can Go.

**The fixtures are not linted.** They live under `testdata`, which `internal/lint` skips and the go
tool ignores — the latter deliberately, since the sources import `github.com/emrebdr/migr/sdk/go`
and would otherwise break `go build ./...` in a module that does not require it. They are still
exemplars an author may read.

**A fixture's expected bytes are maintained by hand.** There is no regeneration flag, on purpose:
`make golden-update` exists because a driver's SQL is the driver's to decide, and this file is the
opposite — it is what an SDK is held to, so an SDK changing what it emits must produce a diff a
person reads rather than one a flag rewrites.

**Adding an operation is now a six-part change in practice.** Invariant #4 lists five; a twenty-first
operation also fails this suite until a fixture emits it. That is the intent, and it is written in
`ir.Kinds()` rather than in a checklist.

**The suite's database is a fake, so nothing here proves a driver.** A fixture's SQL is never
executed and its operations are never compiled. That is the driver conformance suite's job and the
integration tests', and the separation is why this one can run in `make test` at all.
