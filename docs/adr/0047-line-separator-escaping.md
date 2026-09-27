# 0047. U+2028 and U+2029 are emitted as UTF-8, and a scanner undoes Go's escape

## Status

Accepted

## Date

2026-09-27

## Context

`docs/ir.md` §3.3 says an IR document escapes "only what JSON requires" and emits every non-ASCII
character as UTF-8. Two of the three implementations did not.

Go's `encoding/json` escapes U+2028 (LINE SEPARATOR) and U+2029 (PARAGRAPH SEPARATOR)
**unconditionally**. The branch is in `appendString`, outside every test of the `escapeHTML` flag, and
its comment says why: both are valid in JSON and neither is valid in a JavaScript string literal, so
escaping them keeps JSONP evaluable. That is a defensible default for a general-purpose encoder and
the wrong one here — `SetEscapeHTML(false)`, which
[ADR 0003](0003-ir-versioning.md) and M1-T2 introduced to stop `<`, `>` and `&` diverging, does not
reach it. JavaScript's `JSON.stringify` escapes neither character.

So `internal/ir` and `sdk/go` emitted the six-character escape where `sdk/typescript` emitted the
character itself, for the same logical migration. It was found in M1-T6 while deriving the checksum
vectors, documented as a known divergence, pinned by the `lineSeparators` vector so it could not
drift further, and deferred to the milestone where a second SDK would make it observable.

Two things about its reach have to be stated, because both were misread while the task was being
scoped and both change what "closing it" means.

**It never reached a checksum.** A migration's IR half is `sha256(ir.Marshal(ops))`, and the engine
reaches `ir.Marshal` only *after* decoding whatever an SDK sent into `ir.Operations`. So the canonical
form is the engine's own, whatever spelling arrived, and a Go and a TypeScript migration emitting the
same change have always recorded the same checksum. What moves a checksum is `ir.Marshal` **changing**
— which is exactly what this decision does.

**A conformance fixture cannot see it.** M7-T7 proved both SDKs emit byte-identical IR for twelve
fixtures, and that proof runs through the same decode: `test/conformance/runtime` compares
`ir.Marshal` of what the host recorded, not the bytes the SDK wrote. A thirteenth fixture carrying a
separator would pass in both languages before this change and after it. The done-when that asked for
one was written before that was known, and was amended rather than satisfied.

What was left, then, was a specification two implementations did not meet, no mechanical check that
would ever say so, and an emitted-bytes change that had to land before a release rather than after.

## Decision Drivers

- **A blind rewrite of the encoder's output is wrong.** The escape appearing in the output is not
  always an escape: a string whose author wrote those six characters is encoded with the backslash
  doubled, and a `ReplaceAll` matches its tail, leaving a stray backslash in front of a raw separator
  — neither the author's document nor valid JSON.
- **Whatever proves this must be visible to all three copies of the IR, and must hold each to the
  document rather than to another copy.** That rule is why the IR is written three times in the first
  place ([ADR 0038](0038-go-schema-builder.md), [ADR 0043](0043-typescript-schema-builder.md)).
- **The blast radius must equal the claim.** The emitted bytes of an operation are a compatibility
  surface (`docs/ir.md` §4.2); the bytes of a protocol envelope are not.
- **One committed checksum vector has to move, and `explainVector` tells anyone who sees a vector
  move that they have found a disaster.** Moving one must remain exceptional and must stay derivable
  from the specification rather than from the code being changed.

## Considered Options

### Option 1: A scanner over the encoder's output, in both Go copies

`Marshal` encodes as before and then walks the result, tracking whether it is inside a string
literal, replacing the two six-byte escapes with their UTF-8 bytes, and stepping over any other
backslash two bytes at a time so a doubled one cannot be misread.

**Pros**

- Small, total, and testable in isolation; the doubled-backslash case is one test.
- Leaves `encoding/json` doing everything else, so nothing else about the format can drift.
- Idempotent by construction, which matters because an operation is re-encoded on the way into an
  array: `encoding/json` compacts a `json.RawMessage` rather than re-encoding it, and
  `appendCompact` escapes separators only when it is also escaping HTML — so the second pass finds
  nothing to do.

**Cons**

- It is a post-process over JSON, which is a thing to be suspicious of on principle.
- Duplicated in `internal/ir` and `sdk/go`, since the two are separate modules and neither may
  import the other.

### Option 2: Hand-write the string encoder

Stop using `encoding/json` for IR and emit strings directly, escaping exactly what §3.3 lists.

**Pros**

- No post-process; the rule is stated once, positively, where the bytes are produced.

**Cons**

- Re-implements the part of a JSON encoder most worth not re-implementing: the control characters,
  the surrogate handling, the invalid-UTF-8 replacement.
- The escaping is the *only* thing wrong; everything else `encoding/json` does is already exactly
  what the document asks for, verified character by character in M7-T3.

**Rejected because:** it trades a twenty-line scanner for a hand-written encoder on the one code
path whose output is hashed. The risk is not symmetrical.

### Option 3: Leave the encoders alone and relax the specification

Declare the escaped form the canonical spelling, and have the TypeScript SDK escape it to match Go.

**Pros**

- No Go change, no checksum movement, no ADR needed beyond this one.

**Cons**

- The rule would then be "escape only what JSON requires, plus these two, because of a JSONP
  vulnerability in a language none of this runs in". Every future SDK pays for it.
- It makes the specification describe Go, which is the thing `docs/ir.md` §1.1 exists not to do.

**Rejected because:** the divergence is Go's, and a specification that codifies one implementation's
accident stops being implementable from prose.

### Option 4 (for the proof): a canonicality check in the runtime conformance suite

Thread the raw operations array out of `host.Call` through `engine.Emitted` so
`test/conformance/runtime` can assert, for every SDK, that the bytes received equal
`ir.Marshal` of what they decode to.

**Pros**

- General: it would catch any future byte divergence — member order, omitted optionals, a type
  spelling — not only this one.
- It is the only thing that sees what M7-T7 could not.

**Cons**

- `engine.Emitted` is a contract type ([ADR 0036](0036-plan-mode-and-sources.md)) whose two fields
  each answer a question a caller asked. A third field that no production path reads is exactly the
  stateful accessor that ADR said no to, in the other direction.
- It would tempt a later reader into making the engine *refuse* non-canonical input, which
  contradicts §3.2: on input, member order is insignificant and whitespace is ignored.

**Rejected because:** cost, not merit. The two checks chosen below cover this divergence completely,
and a third SDK — or a second divergence — is the event that makes this worth paying for.

## Decision Outcome

**U+2028 and U+2029 are emitted as UTF-8, like every other non-ASCII character.** `docs/ir.md` §3.3
states it positively, names Go as the implementation that has to undo its own encoder, and says that
undoing it takes a scanner and why. `internal/ir.Marshal` and `sdk/go`'s `marshal` each gained that
scanner. The TypeScript SDK was already conforming and did not change.

**The proof is a checked vector plus one wire test per SDK.** `docs/ir.md` carries a second
`create_view` example whose query holds both characters raw — the only form that is a byte-exact
vector, and an invisible one. All three copies of the IR already decode every checked example and
re-encode it byte for byte, so the vector went red in both Go copies and green in TypeScript the
moment it was added, with no new machinery. Because an invisible character can be normalised away by
an editor, `internal/ir` also asserts that some checked example still carries both — the shape M7-T7
used to keep its `enum` guard from being tidied into uselessness.

Each SDK then holds its own `host.schema.apply` **line**, because on both sides the codec the spec
test exercises is not the code that writes the wire: in Go the operation crosses as a
`json.RawMessage` that the envelope encoder compacts, so `SetEscapeHTML(false)` there is load-bearing
and nothing said so; in TypeScript the array is stringified by the envelope, so `operation.ts`'s own
`encode` never runs in production at all.

**The change is scoped to the two IR encoders.** `internal/runtime/protocol` and `sdk/go/internal/wire`
still escape separators in their own string fields. Their bytes are neither hashed nor compared, no
engine-to-runtime payload carries IR, and the one payload that does carries it as raw bytes they
already leave alone.

**No `irVersion` bump.** A bump means a document an older reader cannot read or an older writer
cannot produce ([ADR 0003](0003-ir-versioning.md)); decoding is untouched here, and every copy has
always accepted both spellings on input. M1-T7 set the precedent when `no_default` was added: no bump,
on the stated condition that v1 has never been released. The same condition holds and is the reason
this landed now rather than later — the alternative is every applied migration in every installation
reading as drifted, with no repair flag (invariant #6).

**The `lineSeparators` checksum vector moved, once, and only its IR half.** Its source is ASCII, so
`ead95624…` is unchanged; the IR half is now
`6182bc8567542e93bcbcbcc38472bbd024f28b5323931ce3e1faa504ad0a1a9f`. It was derived the way M1-T6
derived the original — by hashing the expected bytes by hand, outside the package being changed —
and hashing the *old* bytes the same way reproduces the old value exactly, which is what says the
derivation models the encoder instead of guessing at it. `explainVector` now names this task and this
record as the one sanctioned reason a vector has ever moved, so the norm survives its exception.

**Invalid UTF-8 and lone surrogates are a stated non-goal.** `docs/ir.md` §3.3 now says an IR string
is well-formed UTF-8 and what happens to a producer that emits otherwise. It is not enforced, and
mostly cannot arise: `internal/runtime/protocol/frame.go` ends the session on a line that is not valid
UTF-8, so no runtime can deliver invalid bytes, and a lone surrogate — which only a JavaScript string
can hold — is replaced with U+FFFD by any decoder reading scalar values, before anything is hashed.
Refusing it would mean a per-string pass in three copies to close a hole nothing can reach.

## Consequences

### Positive

- The specification is implementable from prose again on the point it was least implementable from:
  an author now reads the rule, the reason their encoder may disagree, and the one wrong way to fix
  it.
- A migration whose view query or check expression carries either character now produces identical
  bytes from every producer, not merely an identical checksum after the engine has normalised them.
- Two previously unpinned couplings are pinned: `SetEscapeHTML(false)` in the Go SDK's envelope
  encoder, and the fact that TypeScript's wire bytes come from the envelope rather than from
  `operation.ts`.

### Negative

- The scanner is duplicated, and a third Go implementation would duplicate it again. The duplication
  is the same one the whole IR already carries, for the same reason, with the same hole: a fix applied
  to one copy and not the other passes on that side.
- `docs/ir.md` now contains two invisible characters, deliberately. A guard test is what keeps that
  from decaying, and the guard is the kind of thing that looks like paranoia until it fires.
- `Marshal` is no longer a thin wrapper over `encoding/json`, so it costs a `bytes.Contains` on every
  call and a copy on the rare one that matches.

### Neutral

- The runtime conformance suite is unchanged and still proves what it proved: identical *canonical*
  IR. That it cannot see a byte divergence is now written down in three places rather than discovered
  twice.
- `sdk/typescript` changed only by gaining a test. The SDK written last against the document was the
  one that already complied — which is the argument for holding every copy to the document rather
  than to the first implementation.

## Notes

The condition that would justify revisiting the proof is a third SDK, or a second divergence of this
species. Option 4 is the general form and is written above in enough detail to be picked up: the seam
is the raw `params` in `host.applySchema`, and the reason not to reach for it is a contract type, not
difficulty.

[ADR 0009](0009-migration-checksum.md)'s Notes and
[ADR 0043](0043-typescript-schema-builder.md)'s Consequences both recorded this as open and pointed
at the milestone that would close it. Neither is edited: this repository's ADRs are historical
records, and a Status line is the only permitted edit (see this directory's README). Neither is
superseded either — 0009's scheme and 0043's builder both stand unchanged, and what closes here is a
note each left open. This record is what they were pointing at, and `docs/ir.md` is where a reader
who needs today's rule rather than its history looks.
