# 0044. The TypeScript protocol client

## Status

Accepted

## Date

2026-08-28

## Context

[ADR 0043](0043-typescript-schema-builder.md) left the TypeScript SDK able to describe a schema
change and unable to send one. `newSchema` took a bare emit sink, five of the six callbacks did not
exist, and nothing in the package had ever put a byte on a pipe.

This is the wire, the six callbacks, and the session loop that serves them. It is written from
[`docs/protocol.md`](../protocol.md) rather than from `sdk/go/internal/wire`, and held to the same
**43 byte-exact round-trip vectors** M6-T2 made of the document's checked examples — that is what
the vectors are for, and it is what catches an implementation written from the other side's code.

## Decision Drivers

- The done-when: *"JSON-RPC over stdio works, including async host callbacks."*
- [§11](../protocol.md#11-what-an-sdk-owes), the checklist an SDK is finished against.
- [§2.6](../protocol.md#26-a-line-that-cannot-be-framed): a line that cannot be framed ends the
  session, after one best-effort `"id": null` error, with no attempt to recover.
- [§2.5](../protocol.md#25-ordering-and-concurrency): an id is unique only within its own direction,
  and one table for both mismatches responses.
- [§9](../protocol.md#9-the-process): end of file on stdin ends the process; after `shutdown` it
  closes stdout and exits 0.
- [ADR 0037](0037-go-migration-contract.md) E: the print target is redirected in `Run`, not at
  import, because reaching into an importer's process is worse than the hole.

## Considered Options

### A. Where the six callbacks' payloads are built

- **A1: a raw `{ call, notify }` host; `schema.ts` builds and decodes every payload** ✔. The session
  frames, numbers, and matches answers, and knows nothing about §5. The shapes sit beside the
  migration that reads them.
- A2: a typed six-method host, implemented by the session. **Rejected**: it is easier to fake in a
  test, and it puts §5's shapes one layer from the `Schema` a migration holds, so a reader of
  `schema.ts` can no longer see what `query` sends.

### B. How far the stdout guard goes

[§2.1](../protocol.md#21-two-streams-two-jobs) calls a corrupted stdout *"the single most common way
a new SDK fails"*, and notes that it fails as a parse error three messages later rather than where
the print happened.

- **B1: take over `process.stdout.write`, keeping the original bound for the protocol** ✔. One
  interception point, and the mirror of Go's `silence()`, which assigns `os.Stdout = os.Stderr` and
  keeps the original handle. `console` is covered as a consequence, because it writes through that
  same stream object — which the spawned-child test asserts for both a `console.log` and a direct
  `process.stdout.write`.
- B2: replace `globalThis.console` with one bound to stderr. **Rejected**: it covers every console
  method and leaves `process.stdout.write` open, which is a hole a migration can drive straight
  through.

The hole that remains is the one Go has: a print from module scope runs before `run` is called, so
before the guard exists. It fails loudly rather than silently — the engine reports a line it could
not frame, naming it ([ADR 0031](0031-protocol-codec-and-framing.md)).

### C. What reads the lines

- **C1: a hand-written reader over the stream's chunks, with a byte budget and a fatal UTF-8
  decoder** ✔.
- C2: `node:readline`. **Rejected mechanically, not on taste.** It bounds no line, so §2.3 is
  unimplementable on it; and `Buffer.toString("utf8")` replaces invalid bytes with U+FFFD silently
  where `new TextDecoder("utf-8", { fatal: true })` throws, so §2.6's UTF-8 row is too. A reader
  that cannot implement two of the document's rules is not a reader for this protocol.

The budget is measured against the **unterminated** prefix — after a scan finds no newline in what
is held — rather than against everything buffered, because one chunk may carry several whole
messages. Overshoot is bounded by one chunk, which is what "refused at the budget" costs in a
stream that delivers in chunks rather than in lines.

### D. What decides the emitted bytes

- **D1: `decode` produces a typed message and `encode` rebuilds the envelope in the documented
  order** ✔, carrying `params`, `result`, and `data` across exactly as they arrived.
- D2: keep the parsed object and re-stringify it. **Rejected because it would pass the test while
  testing nothing.** All 43 vectors are canonically compact, so the document's member order *is*
  parse order: `JSON.stringify(JSON.parse(line))` round-trips every one of them without the codec
  ever deciding an order. Rebuilding is what makes the round trip an assertion about this
  implementation — the same reason [ADR 0043](0043-typescript-schema-builder.md) D gave the IR a
  shape table.

The envelope is closed and a payload is open: an unknown member at the top level is `-32600`, and one
inside a payload is ignored. That is §10.2's compatibility rule made structural, and it is what keeps
the operations array inside `host.schema.apply` byte-identical to the array the checksum covers.

### E. What happens to a response nothing is waiting on

- **E1: it is dropped, and the session carries on** ✔.
- E2: end the session, as `internal/runtime.Session` does on the engine's side.
  **Rejected because of the cancel race.** When `$/cancel` abandons a callback, the waiter has
  already rejected and retired its id; the host's answer is still in flight and arrives for an id
  nothing holds. Ending the session over it would turn every cancellation into a broken channel.

The asymmetry is deliberate and worth stating: the engine is stricter because a response it did not
ask for means it lost track of its own requests, and it has no equivalent race.

### F. Which of §8.2's timers the runtime holds

- **F1: none** ✔. Every row of that table is a timer the engine keeps and a kill the engine performs
  — the handshake, the request timeout, the cancel grace, the shutdown grace. `migration.execute`
  has no timeout at all, on purpose, and no `migr.yaml` key either.
- F2: a defensive timeout of its own. **Rejected**: it would invent a limit the document assigns
  elsewhere, and the one it would most plausibly guard is the one a backfill legitimately exceeds.

### G. Whether the session keeps a state for after shutdown

- **G1: two states, `new` and `ready`** ✔. The loop ends when `shutdown` is answered and the process
  exits 0, so *"nothing more is answered"* is the loop's shape.
- G2: a third `closed` state answering `session_state`, as `sdk/go` has. **Rejected as unreachable**:
  the loop returns on `shutdown`, so no later request is ever read, and a branch nothing can reach is
  a branch no test can hold.

## Decision Outcome

`sdk/typescript/src/wire/` holds the envelope, the framing, the codes, the methods, and the payloads.
It is internal by construction rather than by convention: the package exports only `"."`, so
`@migr/sdk/wire/message.js` is a specifier no consumer can resolve.

`src/session.ts` holds `run({ load, input, send })`. One read loop keeps `$/cancel` readable while
work is running; `migration.execute` and `migration.validate` go to a task of their own. Two tables,
never one: **pending** is this side's callbacks keyed by this side's sequence, and **running** is the
engine's requests, each with an `AbortController` that `$/cancel` aborts and that every `Schema`
callback then refuses against.

`load` is called on first need rather than during `initialize`, so a project whose migrations cannot
be read fails at `migration.discover` — which is a request with an error response, the same place
[ADR 0037](0037-go-migration-contract.md) B put Go's registration problems.

`migration.validate` reaches no callback at all, because [ADR 0042](0042-typescript-migration-contract.md)'s
`validate()` takes no `Schema`. §4's rule that a `host.schema.apply` during validation is a protocol
error is therefore unspellable here rather than enforced.

## Consequences

**A bug the vectors did not catch was caught by a real pipe.** The writer handed its sink the
rendered message and never the newline that terminates it, and every in-memory test passed: the
harness split nothing, and `decode` tolerates trailing whitespace. The spawned child put two
messages on one line the first time it answered twice. The sink now takes the whole line, terminator
included, and the framing test asserts it — which is the case for spending a spawned child on three
process-level rules.

**Node's self-reference does not cross a `node_modules` boundary.** [ADR 0043](0043-typescript-schema-builder.md)
relies on tsc's self-reference for the template typecheck, from a directory inside `node_modules`;
Node's ESM resolver refuses the same import. The spawned runtime therefore imports
`dist/esm/index.js` by absolute path, which drives what ships either way.

**The nested directory surfaced a latent build bug.** `rewriteDeclarations` and the declaration
assertion in `dist.test.ts` each walked one level, so `dist/*/wire/*.d.ts` shipped pointing at `.ts`
sources that no consumer can resolve. Both walk the tree now, and the assertion refuses a walk that
finds almost nothing.

**A migration can now reach the database, and the SDK is complete.** What is missing is the process:
M7-T5's `migr-runtime-node` supplies `load`, which walks `migrations.dir` and imports each file.
`internal/cli` already names that task.
