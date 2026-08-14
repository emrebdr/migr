# 0032. The host callback server, and the SQL a migration wrote

## Status

Accepted

## Date

2026-08-14

## Context

[ADR 0030](0030-runtime-protocol-specification.md) specified the six callbacks; ADR 0031 built the
wire under them. M6-T3 serves them, and it is where a migration written in another language can
first *do* something: emit operations, ask the database a question, and branch on the answer.

Most of what the callbacks need was already built. `ir.Recorder` (M1-T3) was written with this
caller named in its done-when. `capability.Set` answers `host.capability.get` through `Resolve`, and
its `Facts` — driver, server version, `TransactionalDDL` — is `host.info` almost verbatim.
`capability.Support` and `capability.LockImpact` already spell themselves the way §5 says they cross
the wire.

**One thing was missing entirely: `driver.Conn` cannot read.** It had `Exec(ctx, Statement) error` —
no rows, no row count — so `host.db.query` and `host.db.exec` were unservable. The contract had to
grow, which is an ADR under CLAUDE.md §9, and the cost is at its lowest now: mysql and sqlite are
still `doc.go` alone, so postgres is the only implementation to write.

Designing against it surfaced two places where `docs/protocol.md` said something the built engine
does not do, in the same shape as ADR 0030's plan mode and ADR 0031's unframeable line.

**§5 said the wire bytes are hashed.** *"those exact bytes are what a migration's checksum covers …
so the array that crosses the wire is the array that is hashed."* `checksum.OfOperations` is
`ir.Marshal(ops)` over the *decoded* operations, and `ir.Recorder.Append` clones through the
canonical form before that. The engine's behavior is the better one; the document was overstating
it.

**§5 never said `host.db.exec` runs outside the migration's transaction.** Every callback fires
during `engine.Source.Emit`, which completes before the engine compiles anything or opens one. So an
escape-hatch write is committed on its own and a migration that fails afterwards does not undo it.
An operator would reasonably assume the opposite.

## Decision Drivers

- The done-when: *"`host.db.query` is enforced read-only at the host, not merely by convention."*
- ADR 0030's deliberate silence: a syntactic check, a read-only transaction, and a session-level
  setting all conform and they differ per engine, so §5 states *that* the guarantee holds and not
  *how*.
- [ADR 0017](0017-transaction-boundaries.md): a transaction is a closure the driver brackets, which
  is a structural guarantee rather than a remembered one.
- §10.2: adding an optional parameter or a result member is not a breaking change, so an
  implementation that does not know a member ignores it.
- CLAUDE.md invariant #1: this package cannot name a driver.

## Considered Options

### A. How the read path reaches the contract

- **A1: two methods, `Query` and `Perform`** ✔. Read-only is a property of *which method you call*.
  A `Query` came out of a migration and a `Statement` came out of `Compile`, so the argument types
  carry the distinction too.
- A2: one method with a `ReadOnly bool` on the argument. **Rejected**: the one bug this whole
  done-when is about is a call site that leaves the flag unset, and a boolean is exactly the kind of
  thing that gets left unset. It is the same reason ADR 0017 made a transaction a closure rather
  than a handle.
- A3: change `Exec` to return a row count and reuse it. **Rejected**: every DDL call site would
  take a value it ignores, and the argument types differ anyway — a `Query` carries `Args` and no
  `NoTransaction`.

### B. Where read-only is enforced

- **B1: the driver runs it in a read-only transaction and the server refuses the write** ✔.
  PostgreSQL answers *"cannot execute DELETE in a read-only transaction"*, which is a message the
  migration's author can act on.
- B2: a syntactic check at the host — an allowed set of leading keywords. **Rejected by a single
  case**: `WITH d AS (DELETE FROM users RETURNING *) SELECT count(*) FROM d` begins with `WITH` and
  deletes every row. A check that passes that is a lie detector, which is what "merely by
  convention" means. `TestAReadThatWritesIsRefusedByTheServerAndChangesNothing` runs exactly that
  statement against PostgreSQL 16 and 17 and asserts on the **rows**, not on the error.
- B3: both, for a clearer early message. **Rejected**: two rules to keep in agreement, and the
  host-side one would refuse statements some engine allows. The server's own message is already the
  clear one.

`driver.ErrNotReadOnly` lives on the contract for the reason `ErrLockTimeout` does:
`internal/runtime/host` cannot name a driver, and `-32020` is otherwise unreachable.

### C. How a value crosses the wire

- **C1: the server's own text output, taken verbatim** ✔. pgx is asked for text result format and
  the raw bytes are copied, so it is PostgreSQL that renders a `numeric`, a `timestamptz`, or an
  array.
- C2: decode into Go values and format them. **Rejected**: a Go formatter guesses at spellings the
  server already knows — `1.10::numeric` keeps its scale, `ARRAY[1,2]` is `{1,2}`, `true` is `t` —
  and every guess is a place two drivers could differ for one logical value.

A SQL null is a nil pointer and never an empty string, which is why `Rows.Values` is `[][]*string`.

### D. Where the dispatch boundary sits

- **D1: `Serve(ctx, request) → response`, one message at a time** ✔. The server never touches a
  `Reader`.
- D2: the server owns the read loop. **Rejected**: the same stream carries responses to the
  engine's own requests, and §2.5's pending table is per direction. A server owning the stream would
  have to own that table, which is M6-T5's. It also makes every test here need a pipe and a
  goroutine.

### E. Whether the strictness of a payload matches the strictness of an envelope

- **E1: payloads are lenient at runtime and strict in the spec test** ✔. `UnmarshalPayload` ignores
  a member it does not know, because §10.2 says an added parameter is a compatible change. Required
  members are the *handler's* check, reported as `-32602`.
- E2: `DisallowUnknownFields` at runtime, as the envelope has. **Rejected**: it would make an added
  optional parameter a breaking change, which §10.2 explicitly says it is not.

The spec test is strict for the opposite reason: a member the *document* states and no struct
declares is one an SDK will send and the host will ignore.

### F. Whether §7.1's slug rule binds a receiver

- **F1: the writer requires it, the reader does not** — carried forward from ADR 0031, E, and
  applied here to the six callbacks unchanged.

## Decision Outcome

`driver.Conn` grows `Query` and `Perform`, taking a `driver.Query` and returning `driver.Rows` and a
row count. `internal/runtime/host` serves the six callbacks behind a `Call` window that makes §5.1
mechanical, answering `-32601` for a method nothing serves and `-32602` for parameters of the wrong
shape — neither of which the codec could answer, since both need to know what a method expects.

`internal/runtime/protocol` gains typed payloads for all six, shared with M6-T5's client so the two
directions cannot spell one member two ways. §5's examples are now checked against those types:
strictly in, byte-identical back out.

Plan mode changes one handler. `host.db.exec` holds the statement and answers `"executed": false`
rather than a fabricated row count.

## Consequences

### Positive

- The read-only guarantee is proved against real servers rather than asserted, and the assertion is
  on the table's contents rather than on an error string.
- A `bigint` past 2^53 survives, because nothing between the server and the wire ever makes it a
  number.
- The window is a type. A callback outside a call, a `host.schema.apply` during a validation, and a
  second call while one is in flight are each refused by the same small piece of state.
- A seventh callback fails `TestEveryCallbackIsServed` the day it is added, because the set comes
  from `protocol.HostMethods()` rather than from what the suite chose to ask about.

### Negative

- **`driver.Conn` is now eight methods and will be nine after M11-T1's `Introspect`.** ADR 0011
  accepted that this interface is not small, for the stated reason that everything on it needs the
  same pinned session. This adds to a cost already taken.
- **A migration's read cannot run inside a transaction**, because `Query` opens its own and ADR 0017
  forbids nesting. Nothing reaches that today — `Emit` completes before the transaction opens — and
  the refusal is what keeps §5's new sentence true rather than incidental.
- **`host.db.query` still leaks placeholder style** (`$1` versus `?`) into a migration. ADR 0030
  recorded this and nothing here improves it; the Operation IR remains the portable half.
- **The read path is not in the driver conformance suite.** It cannot be: the suite is untagged by
  design (M2-T4) and a read needs a server. What holds it instead is `driver.Rows.Validate`, which
  the host calls on every result and reports as `-32603` naming the driver, plus each driver's own
  integration tests. A driver that returns a row narrower than its columns is found at the first
  read rather than never.

### Neutral

- `Rows.Values` being `[][]*string` reads awkwardly and is the only shape that keeps a null distinct
  from an empty string without a wrapper type per value.
- `host.log` is filed with the engine's own `*slog.Logger`, and a line this version cannot read is
  logged as a warning rather than dropped — a notification cannot be refused, and silence would be
  the worst of the three outcomes.

## Notes

M6-T4 keeps the half of plan mode that needs a runtime: the proof that plan and apply emit identical
IR for a migration with no data-dependent branching. The behavior it describes is implemented here,
because shipping a server that runs the escape hatch during a preview would be a known-wrong branch
in the tree.

M6-T5 owns the loop that decides which messages reach `Serve`, the per-direction pending table, and
§8.2's timeouts.

M8 and M9 implement `Query` and `Perform` for SQLite and MySQL. Each owes its own mechanism —
SQLite's `query_only` pragma, MySQL's `START TRANSACTION READ ONLY` — and its own classification of
the refusal into `driver.ErrNotReadOnly`.
