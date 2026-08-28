# 0043. The TypeScript schema builder

## Status

Accepted

## Date

2026-08-28

## Context

[ADR 0042](0042-typescript-migration-contract.md) shipped the contract and left `Schema` holding the
call's own facts and its signal. A TypeScript migration could be written, loaded, and typechecked,
and it could not describe a schema change.

This is [ADR 0038](0038-go-schema-builder.md) done a second time, and that ADR was written knowing
it would be: *"M7-T3 mirrors this builder and M7-T7 compares the two SDKs byte for byte."* So the
rules are read out of [`docs/ir.md`](../ir.md) rather than out of `sdk/go` — both copies answer to
the document, and neither is the definition of correct.

The IR becomes the **third** copy, and it gets what the first two got: a spec test that decodes all
21 checked vectors and re-encodes them byte for byte. Decoding is the half that matters, for ADR
0038 E's reason — a test that only constructed operations could not miss a member this copy forgot
to declare, because it would never have written one.

## Decision Drivers

- The done-when: *"the builder mirrors the Go SDK operation-for-operation; logical types are typed
  such that invalid combinations fail at compile time."*
- [§3.2](../ir.md#32-key-order-and-whitespace) and [§4.2](../ir.md#42-the-emitted-bytes-are-a-compatibility-surface):
  member order and omitted optionals are a compatibility surface, because the bytes are hashed.
- [§7.2](../ir.md#72-conventions-the-format-does-not-enforce): the two conventions no validation can
  check, which exist in the document precisely so two SDKs cannot disagree.
- [ADR 0038](0038-go-schema-builder.md) D and E: an unqualified column is NOT NULL, and the
  operations are exported so a chain and a hand-written literal both work.
- `erasableSyntaxOnly` ([ADR 0041](0041-typescript-package-scaffold.md)): no `enum`, no parameter
  properties, nothing Node cannot strip.

## Considered Options

### A. What a logical type is

- **A1: the canonical spelling itself, branded** ✔. [§5.1](../ir.md#51-grammar) encodes a type as a
  string and [§5.3](../ir.md#53-one-spelling-per-type) requires the tight form on output, so a
  `Type` here *is* that string. It needs no `toJSON`, no parser, and no normalization step: what can
  only be constructed by `t` can only be spelled one way.
- A2: a value mirroring Go's `Type` struct, carrying the reason it has no spelling. **Rejected**: it
  needs a `toJSON` for the encoder and a parser for the decoder, and it keeps a wrong type silent
  until the operation using it is emitted. ADR 0038 B chose the deferral because a *chain* cannot
  return an error at each link — and a type constructor is not a chain link, so this copy need not
  pay for it.

The cost is that the two SDKs fail at different moments for the same mistake: `migr.String(0)` in Go
is a value that fails at `Apply`, and `t.string(0)` here fails where it is written. Both fail the
migration, and M7-T7 compares valid fixtures, so nothing observable diverges.

### B. How the eighteen constructors are reached

- **B1: one namespace object, `t`** ✔ — `t.string(255)`, `t.enum("draft", "live")`. A reserved word
  is a legal property name, so `enum` keeps the spelling the document gives it, and nothing shadows
  the global `string`.
- B2: named exports. **Rejected**: `enum` cannot be a named import at all, so exactly one of the
  eighteen would have to be spelled differently from the document — an exception nobody can state.
- B3: a namespace import, `import * as migr`, mirroring `migr.String(255)` literally.
  **Rejected**: it works, but the package would then carry an export (`enum`) that a plain named
  import can never reach, which is a wart in the surface rather than in one call site.

### C. How far the types go

Every rule keeps its runtime check regardless, because **a computed argument is invisible to the
type system** — `t.string(n)` where `n` came from a query is a plain `number`.

- **C1: the rules a literal's own type answers** ✔ — a non-positive or non-integer length,
  precision, or scale; an empty `enum`; duplicate `enum` labels. Template-literal types over the
  argument's literal, with the refusing branch carrying a sentence so the diagnostic reads.
- C2: also `scale <= precision`, the label charset, and the 32-deep array bound. **Rejected on cost,
  not on merit**: a tuple-based numeric comparison would refuse the `decimal(2000,4)` that
  [§5.4](../ir.md#54-what-is-not-checked-here) explicitly permits, so it would need digit-by-digit
  comparison over the literal's string form — several hundred lines of type-level machinery in a
  package whose comment rules no linter here can check.
- C3: arity only, which the signatures give for free. **Rejected**: the done-when asks for more than
  what a function signature does by itself.

### D. What decides the emitted bytes

- **D1: an ordered member list per operation and per shared structure, which the encoder rebuilds
  every object from** ✔.
- D2: trust the object's own insertion order, which `JSON.stringify` preserves. **Rejected because
  ADR 0038 E exports the operations**: a hand-written `{ op, column, table }` is a supported path
  and would stringify in the order it was written. The shape also drives decoding, where a member
  this copy does not declare must be an error rather than something skipped
  ([§3.4](../ir.md#34-unknown-fields-are-rejected)), and drives the spec test's comparison against
  the document's own field tables — which is what Go gets from struct field order and reflection
  over its json tags.

`op` is a member of the value here where [§3.1](../ir.md#31-one-object-per-operation-discriminated-by-op)
says it is not a member of the document. TypeScript has no runtime type identity and `drop_index`,
`drop_unique`, and `drop_primary_key` are otherwise the same shape, so the discriminant is how a
value names itself. The encoder still writes it first, and the shape does not list it.

### E. How a chain ends, and what catches the one that does not

- **E1: `apply()`, with the window sealing two holes** ✔.
- E2: a thenable chain, where `await` is the terminal call. **Rejected**: it collapses the two holes
  into one, which is elegant, and it stops the two SDKs reading alike for a benefit the seal already
  provides.

Go has one hole and TypeScript has two. A chain built and never applied emits nothing, which is the
failure §3.4 names — Go's guard, unchanged, and still *built and non-empty* so a branch that opened
a chain and added nothing is not a false alarm. The second is new: an `apply()` that was sent and
never awaited carries its failure into a promise nobody reads. The window keeps both — the chains it
handed out, and a handler on every promise it returned — and reports the first failure when the call
ends. Attaching that handler is also what keeps an un-awaited rejection from reaching Node's
`unhandledRejection` before the seal runs.

### F. How a change builder promotes its chain

Go embeds `*Table` in `*ColumnChange`, so the modifiers return the small builder and everything else
is promoted.

- **F1: `Object.create(chain)`, a view whose prototype is the live chain** ✔. It works only because
  every method is a closure over the shared builder: nothing reads `this`, so a view cannot shadow
  what it inherits, and the object's current name lives in the builder where a rename can move it
  for every view at once.
- F2: classes with `#private` fields. **Rejected mechanically**: a private field is brand-checked,
  and an object made with `Object.create(instance)` does not carry the brand — every inherited
  method touching one would throw.
- F3: hand-written delegation, re-exposing seventeen methods on each of six change builders.
  **Rejected**: it is the same thing written six times.

## Decision Outcome

`@migr/sdk` gains the twenty operations, the seven `from` payloads, `Column`, `Default`, the two
closed sets, the eighteen logical types, and all of [§9.1](../ir.md#91-checked-here), in files
mirroring `sdk/go`'s layout so the copies stay comparable file by file.

Over that sit the four entry points — `createTable`, `table`, `view`, `raw` — each returning a chain
whose methods are the operations that object accepts, and each ending in `apply()`. Recording prior
state is one verb, `was`, on every change that carries a `from`.

§7.2's two conventions are structural rather than remembered, as they are in Go: an unqualified
column is NOT NULL, and `createTable(...).primaryKey(...)` and `table(...).addPrimaryKey(...)` are
different calls.

`migr create --lang ts` now writes what the Go template writes: the chain in the doc comment above
`up`, and what forgetting `apply` costs. `internal/cli` asserts what it says and the SDK typechecks
it against the published package.

## Consequences

**`JSON.stringify` is byte-compatible with `ir.Marshal`, and this task deliberately did not close
the two places it is not.** Go 1.26's encoder special-cases `\\`, `"`, `\b`, `\f`, `\n`, `\r`, `\t`
and writes `\u00XX` for the rest below 0x20, which is exactly what `JSON.stringify` does;
`SetEscapeHTML(false)` already aligns `<`, `>`, and `&`. What remains is U+2028/U+2029, which Go
escapes unconditionally, and invalid UTF-8, which Go renders `�` where JavaScript emits a
lone-surrogate escape. Both are [M7-T9](../../TASKS.md)'s, which depends on M7-T7 and must land
before a released build applies anything or behind an `irVersion` bump — closing either here would
move every checksum.

**The IR is now written three times**, with the same mitigation and the same hole as the first two:
an operation added to one copy without a documented vector passes on that side and fails nothing on
the others.

**A migration can describe a change and cannot yet ask a question.** The six host callbacks are
M7-T4 and the process that runs any of it is M7-T5. The window takes an emit sink rather than a
host, so the whole builder is exercised with a recording sink and no process.

**The type system's answer is an addition, never a replacement.** Nothing about the compile-time
rules reduces the runtime ones, and a reader of `type.ts` sees each rule twice for that reason.

**A migration author has two ways to say everything**, as in Go: the operations are exported and
`apply` takes them, which is what makes "a chain and a literal emit the same bytes" a claim a test
can hold. It is surface area, and the runtime conformance fixtures should be written in the chain.
