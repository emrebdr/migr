# 0030. The runtime protocol, specified

## Status

Accepted

## Date

2026-08-13

## Context

[ADR 0002](0002-runtime-protocol.md) chose the transport: bidirectional line-delimited JSON-RPC 2.0
over the child process's standard streams, with host callbacks for schema operations and read-only
queries. It named the methods. It did not say what any of them carry, what happens when one fails,
how either side stops, or how the two agree on a version.

M6-T1 is the task that answers all of that, and it is the last moment where the answers are free.
`docs/protocol.md` did not exist; `internal/runtime` was three `doc.go` files. Everything after this
— the codec (M6-T2), the host server (M6-T3), the Go SDK (M6-T7), the TypeScript client (M7-T4) —
implements whatever this document says, and every SDK that ever exists is written against it.

Three things the protocol has to carry were not obvious from ADR 0002:

**Two refusals that are not failures.** `engine.ErrNoDownAuthored` has the engine derive an inverse
through `ir.ReverseAll` ([ADR 0022](0022-rollback-executor.md)); `engine.ErrDeclaredIrreversible`
halts the whole rollback with the *author's* reason ([ADR 0028](0028-validation-report.md)). Both
are ordinary answers to "how is this migration undone", and both have to be distinguishable on the
wire from a migration that crashed. M5-T5 found this hole for `.sql` migrations and closed it with
a file marker; a runtime-backed source has the same hole and needs the same closing.

**Two version lines meeting at one handshake.** `initialize` carries a protocol version and an
`irVersion`, and [`ir.md` §10.4](../ir.md#104-four-independent-version-lines) says nothing couples
them. Which raises the question that document does not have to answer: what does either side *do*
when the numbers do not line up.

**A specification is a compatibility surface with no compiler.** `docs/ir.md` solved that by making
every fenced example a checked conformance vector. There is no protocol codec until M6-T2, so the
same trick is not available at the same strength.

## Decision Drivers

- CLAUDE.md invariant #5: *"IR and protocol are versioned. A breaking change bumps the version …
  Never change the wire format silently."*
- ADR 0002: plan mode is a host behavior rather than a separate code path, and Go is not
  privileged.
- [ADR 0018](0018-declared-dependencies.md): a dependency is **declared**, never computed — because
  a graph that could branch on live data would let `migr plan` and `migr up` order the same
  migrations differently.
- The M4 executor, which is built and tested: `engine.Source.Emit` returns a whole operation list,
  which is then hashed, compiled, grouped, and executed in one transaction with the history row
  written before it commits.
- The done-when: *"precisely enough that someone could write a third-party SDK from the document
  alone."*

## Considered Options

### A. Version negotiation at `initialize`

- **A1: the engine offers a set, the runtime picks one** ✔. The choice is binding and stated back,
  so both sides know what they settled on before the second message.
- A2: one integer, exact match, as `ir.UnmarshalDocument` treats `irVersion`. **Rejected**: an
  `irVersion` is a property of a document at rest, and a document that will not load is a clear
  failure. A protocol version is a property of two programs installed by different people at
  different times — an SDK from a package registry and a CLI from a release — and neither can make
  the other upgrade. Exact match makes a patch release of one a breaking change for the other.
- A3: both announce a highest version and drop to the minimum. **Rejected**: it works, and nothing
  ever says out loud what was agreed. A mismatched assumption then surfaces three messages later as
  a decode failure pointing at the wrong place.

### B. Cancellation

- **B1: a `$/cancel` notification, then a kill after a grace period** ✔. The notification lets a
  migration stop between chunks; the kill is what makes the guarantee unconditional.
- B2: kill only. **Rejected**: M12-T3's chunked backfill has to resume from the last committed
  chunk, and a runtime that never learns it is being cancelled leaves that resume point to be
  reconstructed rather than recorded.
- B3: notification only. **Rejected**: a runtime that ignores it, or is wedged, holds the migration
  lock until `lock.timeout`. Cancellation would be a request rather than a promise, and `Ctrl-C`
  would sometimes do nothing.

### C. How the two down-refusals cross the wire

- **C1: JSON-RPC errors, with reserved codes and a slug in `data`** ✔. They are refusals, and a
  client library already routes an error to the path an SDK author will write a handler in.
- C2: a successful result with an `outcome` discriminant. **Rejected**: it makes every SDK's happy
  path a switch, and the failure mode is the wrong one. A client that forgets a case reports *zero
  operations* — a migration that undoes nothing and says it succeeded — where a forgotten error
  case raises.

### D. Which band the application codes live in

- **D1: `-32000` to `-32099` only** ✔, which is the band JSON-RPC 2.0 delegates to the
  implementation.
- D2: reuse LSP's `-32800` for a canceled request. **Rejected**: it sits in `-32768` to `-32000`,
  which JSON-RPC reserves for its own predefined errors. LSP took that liberty; a document claiming
  to be unambiguous should not borrow from a band it does not own.
  `TestEveryApplicationCodeIsInTheBandJSONRPCLeavesToUs` holds it.

### E. Reserving names for methods that do not exist yet

- **E1: reserve the namespace, not the names** ✔. `host.*` belongs to the host, permanently; an
  unknown method is `-32601`; adding a method is not a breaking change.
- E2: declare `host.progress` and friends now, for M12-T3 and M13-T8. **Rejected**: a name reserved
  before its payload is known constrains nothing and dates badly. The rule that *adding* a method
  is compatible is what actually leaves room, and it is worth more than a guess at a signature.

### F. How much of the specification is machine-checked

- **F1: a structural checker plus a method and code list in `internal/runtime/protocol`** ✔. Every
  method in the code has a section and every section names a real method; every code is tabulated
  under the right slug; every `json` block is a one-line JSON-RPC message naming a real method; and
  every worked conversation shows both directions without reusing a request id within one.
- F2: document only, with the checker arriving in M6-T2 alongside the codec. **Rejected**: the drift
  that actually happens is a method renamed in the code and not in the document, and that is
  checkable today without a codec.
- F3: full round-trip vectors, as `internal/ir/spec_test.go` does. **Rejected on timing**: it needs
  the message types, which is M6-T2's codec moved into this task. Worth doing then, and the document
  says so where a reader will look for it.

## Decision Outcome

`docs/protocol.md`, `protocolVersion` 1: thirteen sections covering framing, the session, both
directions of methods, plan mode, the error taxonomy, cancellation and timeouts, the process
contract, versioning, an SDK conformance checklist, and two worked conversations.

Twelve application error codes are assigned, all inside JSON-RPC's implementation band, each with a
slug an SDK branches on instead of the number. `internal/runtime/protocol` declares the method names
and the codes; `spec_test.go` holds the document and the code to each other.

## Consequences

### Positive

- A third-party SDK can be written from one file. The conformance checklist in §11 is that claim
  made checkable by its author rather than only by us.
- The two down-refusals are first-class on the wire, so a Go or TypeScript migration can be
  legitimately one-way — which `.sql` migrations gained in M5-T5 and nothing else had.
- Adding a method, an optional parameter, or a result member is a compatible change, which is what
  makes M12's progress reporting and M13's pacing addable without a version bump.
- A method renamed in the code without the document, or documented without existing, fails
  `make test`.

### Negative

- **CLAUDE.md §4.3's sketch was wrong about plan mode, and this document supersedes it.** It said
  `mode=apply` means the host "executes ops in a transaction" as they arrive. The M4 executor does
  not, and cannot: it collects a migration's whole operation list, hashes it — that hash is half the
  migration's checksum ([ADR 0009](0009-migration-checksum.md)) — compiles it through the driver,
  groups the statements, and runs them in one transaction. None of that can begin until the
  migration has finished emitting. So `host.schema.apply` **records in both modes**, and plan mode
  changes exactly one callback: `host.db.exec`. This is a smaller difference than ADR 0002 implied,
  and a better one — plan and apply diverge in one stated place instead of several.
- **The examples are not conformance vectors**, and `docs/ir.md`'s are. A reader who has met that
  document will expect to be able to copy bytes; §1.3 says plainly that they cannot yet, and F3
  names when they will be able to.
- **`host.db.query` leaks the engine into a migration.** Placeholders are `$1` on PostgreSQL and `?`
  on MySQL and SQLite, and `database.driver` is how a migration knows which. There is no neutral
  query language and inventing one is far outside this task; the Operation IR is the portable half,
  and a migration is portable exactly as far as its SQL is.
- **The 16 MiB line limit is a number chosen from a worst case, not measured.** A migration whose
  operations exceed it must split them across calls, which the specification allows and no
  implementation yet does.

### Neutral

- The wire is camelCase and the IR payload inside `host.schema.apply` stays snake_case. Two
  spellings in one document is a cost; the alternative is re-spelling bytes that are hashed.
- Every value in a `host.db.query` result is a string, including numbers. A `bigint` does not
  survive a JSON number in JavaScript, and it fails silently at 2^53 — which is exactly the size a
  backfill on a large table reaches.
- `$/cancel` follows LSP's `$` convention for protocol machinery, while deliberately not following
  its error code (D2). Borrowing the readable half and not the incorrect half is intentional.

## Notes

M6-T2 builds the codec. When it does, the examples in this document can become round-trip vectors
the way `docs/ir.md`'s are, and F3 stops being rejected on timing.

M6-T3 owns *how* `host.db.query` is enforced read-only. The specification states that it is, and
deliberately not how — a syntactic check, a read-only transaction, or a session-level setting are
all conforming, and they differ per engine.

M6-T5 owns the timeouts as configuration. §8.2 states the defaults as the protocol's; surfacing them
in `migr.yaml` is that task's, and the `migration.execute` timeout is deliberately absent from the
table for a reason that should survive being made configurable — a backfill legitimately runs for
hours, and a default that killed it would be a data-loss bug wearing a safety flag.
