# 0031. The codec, and what ends a session

## Status

Accepted

## Date

2026-08-13

## Context

[ADR 0030](0030-runtime-protocol-specification.md) wrote `docs/protocol.md` and the names it
assigns. Nothing implemented it. M6-T2 is the codec: the types, the framing, and the rules a
receiver enforces before anything is dispatched.

Writing the reader is what found two holes in that specification, and each is a defect by the
document's own standard — §1.1: *"If something here is ambiguous enough that two implementations
could disagree on the bytes or on the sequence, that is a defect in this document."*

**A line that cannot be framed has no `id` to answer.** §2.3 said an over-long line is a protocol
error and "the reader reports it and the session ends"; §7.2 assigned `-32700` to a line that is not
JSON. But §2.4 said *"`id` is an integer. JSON-RPC permits strings and null; this protocol does
not"* — so there was nothing to put in an error response, and the document never said whether one is
written at all. Two SDKs would have disagreed on whether a malformed stream produces a message or a
closed pipe.

**§7.2's reserved codes carried no slug.** §7.1 says `data` "always carries `slug`" with no
exemption, and §7.2's table had a *When* column and no *Slug* column. An SDK could not know what
slug a `-32700` carries. §7.3's table already answered to `protocol.Codes()`; §7.2's answered to
nothing.

Both are amendments rather than breaking changes, and both were free: `protocolVersion` 1 has never
shipped and no SDK exists. §10.2's *"changing an error code's number or slug"* does not cover
assigning one that was never specified.

## Decision Drivers

- The done-when: *"malformed input is rejected without hanging; a fuzz test finds no panics."*
- `docs/protocol.md` §2.2–§2.5, which the codec exists to enforce rather than to reinterpret.
- §10.2: adding an optional parameter or a result member is **not** a breaking change, so an
  implementation that does not know a member ignores it.
- [ADR 0009](0009-migration-checksum.md) and [`ir.md` §4](../ir.md#4-the-checksum-boundary): the
  operations array inside `host.schema.apply` is hashed, so the bytes that cross the wire must be
  the bytes that are hashed. Nothing may re-spell a payload.
- CLAUDE.md invariant #8: no map-iteration order in anything that produces wire bytes.

## Considered Options

### A. What a receiver does with a line it cannot frame

- **A1: one best-effort error carrying `"id": null`, then the session ends** ✔. The peer learns
  *what* it sent rather than finding a closed pipe and an exit code. `null` is the only honest value
  — the `id` is precisely the part that could not be read.
- A2: end the session and write nothing. **Rejected**: closest to §2.3's existing words and strictly
  less diagnosable. The cost of A1 is one write on a pipe that is ending anyway.
- A3: skip the line and keep reading, which is JSON-RPC's own guidance for a parse error.
  **Rejected**, and it is the important rejection: the `id` was unreadable, so whoever was waiting on
  that request waits forever. That is the hang the done-when forbids, and a hung runtime holds the
  migration lock until `lock.timeout`.

The reader is therefore **spent** after any framing failure: every later read returns the same
error. Recovery is not a quality-of-implementation choice left open — it is ruled out.

### B. One `Message` struct, or three types

- **B1: one struct whose present members decide its shape** ✔, with `Validate` naming which of the
  three it is.
- B2: separate `Request`, `Notification`, and `Response` types. **Rejected on two counts**: decoding
  needs a discriminated union Go has no syntax for, so a `Decode` would return `any` and every
  caller would type-switch; and declaration order would stop being wire order, which is what
  byte-identical re-encoding rests on (F).

### C. Strict envelope, open payload

- **C1: `DisallowUnknownFields` on the envelope; `params`, `result`, and `data` are
  `json.RawMessage` and never inspected** ✔. This is the asymmetry §10.2 requires, expressed as a
  type rather than as a rule to remember: an added result member is compatible *because nothing
  decodes the result here*, and an added envelope member is not covered by §10.2 at all.
- C2: lenient everywhere. **Rejected**: a misspelled `jsonrpc` or a stray `metod` would be silently
  ignored and surface as a missing response.

Carrying payloads verbatim is also what keeps the checksum boundary honest: the operations array
that crosses the wire is the array that is hashed, not a re-marshalling of it.

### D. An unterminated final line

- **D1: a truncated message, refused** ✔. A child that dies mid-write is exactly what produces one,
  and a crash can land on an object boundary — a complete-looking message its author never finished
  sending.
- D2: accept it, as `bufio.Scanner` does. **Rejected**: acting on that message is acting on a
  sentence that was cut off, and `Scanner` cannot tell the two cases apart at all, which is the
  reason the reader is hand-rolled on `bufio.Reader`.

### E. Where the slug rule is enforced

- **E1: the writer requires it, the reader does not** ✔. `Message.Validate` covers the shape and
  both sides call it; `Error.Validate` covers §7.1's slug and only the writer calls it.
- E2: enforce it on both sides. **Rejected on the failure mode.** A peer that sends
  `{"code":-32011,"message":"boom"}` with no `data` is not conforming, but ending the session over it
  means the operator reads *"the runtime sent a malformed message"* instead of *"your migration
  failed: boom"*. Strictness there destroys the one piece of information the message was carrying.
  migr never emits such an error; a third-party SDK that does is caught by the conformance suite,
  which is the right place for it.

### F. How much of the specification the codec is held to

- **F1: every checked example in `docs/protocol.md` is a byte-exact round-trip vector** ✔ — it
  decodes through `Decode` and re-encodes to the same bytes. This is ADR 0030's F3, which was
  *rejected on timing* because there was no codec; there is one now.
- F2: table-driven cases in the test file only. **Rejected**: it leaves member order and spelling
  specified in prose and checked nowhere, which is how an encoder and its own document drift.

## Decision Outcome

`internal/runtime/protocol` gains `message.go` (the envelope, the three-state `ID`, `Encode` and
`Decode`), `error.go` (the JSON-RPC error object, which is also a Go error), and `frame.go` (a
bounded `Reader`, a flushed and concurrent-safe `Writer`, and `Refuse`). `code.go` gains §7.2's five
reserved codes, kept in their own list so `Codes()` and `Valid()` keep meaning *the twelve migr
assigns*.

`docs/protocol.md` gains §2.6, a `Slug` column on §7.2's table, the `id` carve-out in §2.4, two
checklist rows in §11, and a §1.3 that now claims the round trip.

The scope stops at framing. §2.5's correlation rules — two id sequences, one pending table per
direction — belong to whoever owns a read loop: M6-T3 for the host side, M6-T5 for the engine side.

## Consequences

### Positive

- The 42 examples are the codec's conformance run. An encoder that renders `id` after `method`, or
  that pretty-prints, fails `make test` rather than failing an SDK author six months from now.
- A malformed stream cannot hang. The line budget is refused **at the budget** rather than after a
  delimiter that may never arrive, and there is no recovery path that could leave a caller waiting.
- `host.schema.apply`'s operations array crosses the wire untouched, so the checksum boundary holds
  by construction rather than by care.
- Two fuzz targets, seeded from the specification's own messages: `FuzzDecode` asserts that anything
  that decodes re-encodes and re-decodes stably, and `FuzzRead` asserts that every read either
  consumes input or ends the reader for good.

### Negative

- **The `ID` type is three-state and that is unusual.** Absent, an integer, and null are genuinely
  different on the wire, and a `*int64` cannot express the third — `omitempty` would render null as
  absent, turning the one framing error into a notification. It costs a custom `MarshalJSON`,
  `UnmarshalJSON`, and `IsZero`, and it is the one type in the package a reader has to stop at.
- **A truncated final line is refused, and a peer that wrote a complete message and then died
  without its newline loses it.** That is deliberate (D) and it is a real cost.
- **The error object is decoded strictly**, so an unknown member inside `error` ends a session.
  JSON-RPC 2.0 defines that object as exactly `code`, `message`, and `data`, and adding to it would
  be a version bump — but it is stricter than §10.2 strictly requires.
- **The 16 MiB budget counts the terminator.** A message of exactly 16 MiB plus its newline is
  refused by one byte. §2.3 says "lines", and a line includes what ends it; nothing is likely to sit
  on that boundary, and the constant says which side it falls on.

### Neutral

- `Decode` accepts a request naming a method this version does not define. That is not leniency: it
  is what makes `-32601` reachable, and therefore what makes adding a method a compatible change. A
  *notification* for such a method is still refused, because §2.4 closes that set.
- The reader skips empty lines rather than reporting them, per §2.2. A stream of nothing but
  newlines therefore makes progress without producing a message, which is a peer wasting its own
  time and not a failure mode.

## Notes

M6-T3 builds the host callback server on this and owns the incoming dispatch loop, including
`-32601` for a method nothing serves and `-32602` for parameters of the wrong shape — neither of
which the codec can answer, since both need to know what a method expects.

M6-T5 owns the engine's side of the same loop: the id sequence, the pending table, and the timeouts
in §8.2. §2.5's rule that a pending table is **per direction** is stated in the package doc and is
enforced nowhere yet, because nothing yet keeps one.

M7-T4 writes the same framing in TypeScript. The 42 vectors are what will hold the two to each
other, and they are why §1.3 now says member order is specified.
