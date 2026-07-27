# 0005. Encode logical types as canonical strings in the IR

## Status

Accepted

## Date

2026-07-27

## Context

[ADR 0001](0001-architecture.md) made a database-neutral operation IR the sole contract between
language SDKs and database drivers. Almost every operation in that IR mentions a type: a column
being added, a column being altered, the element type of an index expression. Before any
operation can be defined, the IR needs a way to write down "decimal with precision 10 and scale
2" that every SDK produces identically and every driver reads identically.

The vocabulary is eighteen logical types. Fourteen stand alone — `text`, `bool`, `int16`,
`int32`, `int64`, `float32`, `float64`, `uuid`, `json`, `timestamp`, `timestamptz`, `date`,
`time`, `binary`. Four carry parameters: `string(n)`, `decimal(p,s)`, `enum(values...)`, and
`array(of)`, whose argument is itself a full type.

The question is how one of those appears inside an IR document. It has to be settled first
because everything downstream is expressed in terms of it: the operation set, the checksum
taken over emitted IR, every driver's compile signature, and every golden file.

One constraint dominates. [ADR 0003](0003-ir-versioning.md) commits every SDK to emitting
**byte-identical IR** for the same logical migration, enforced by a shared conformance suite.
That is the mechanical proof of the language-independence claim. Any encoding admitting two
spellings of one type breaks it — not subtly, but at the level where the project's central
claim stops being testable.

## Decision Drivers

- **One spelling per type.** Byte-identical IR across SDKs is a hard requirement, not a
  preference.
- **Legibility where humans read IR.** Golden files, `migr plan` output, and diffs are read by
  people during review, which is when a wrong type must be noticeable.
- **Cost of a second representation.** Anything the parser and the printer must agree on is a
  place they can disagree.
- **Stability.** The encoding is frozen for the life of `irVersion` 1; changing it is a version
  bump with the checksum consequences ADR 0003 records.

## Considered Options

### Option 1: The canonical string

A type serializes to exactly the string it parses from.

```json
{ "op": "add_column", "table": "users",
  "column": { "name": "price", "type": "decimal(10,2)" } }
```

Nested types nest textually: `array(string(255))`, `array(enum(draft,live))`.

**Pros**

- Compact and immediately readable wherever IR is reviewed — a golden file diff shows
  `string(255)` becoming `text`, not a five-line object reshuffle.
- One representation, so the parser and printer are inverses of each other by construction and
  a single round-trip property test covers the whole encoding.
- Uniqueness is enforceable and testable: every type has one canonical spelling, and a test
  asserts no two distinct types share one.
- The same string is what a human writes in a SQL-file migration and what `migr diff` emits, so
  there is nothing to translate between layers.

**Cons**

- The parser sits on the wire path. A parser bug is a wire-format bug, not merely a usability
  one.
- A reader who has never seen the grammar must learn it, where an object is self-describing.
- Malformed input is detected by the parser rather than by a JSON schema, so the quality of the
  diagnostics is the project's responsibility.

### Option 2: A structured object

```json
{ "type": { "kind": "array",
            "elem": { "kind": "string", "length": 255 } } }
```

**Pros**

- Self-describing to anyone reading raw IR without knowing the grammar.
- No parser on the wire path; the JSON decoder does the structural work.
- A JSON schema could validate shape mechanically.

**Cons**

- Verbose exactly where verbosity hurts: golden files exist to be diffed, and a nested object
  turns a one-token change into a multi-line one.
- Byte-identical output becomes a serializer concern rather than a value concern — field order,
  `omitempty` on a zero `scale`, and whether an absent `length` is `null` or missing are now
  three ways for two SDKs to disagree while meaning the same thing. Each would have to be
  specified and independently implemented in every SDK.
- A canonical string is still required for `migr plan`, error messages, and SQL-file
  migrations, so the printer does not disappear — it is duplicated, and the object encoding
  becomes a second thing that can drift from it.

**Rejected because:** it moves the uniqueness problem from one place, where it can be tested by
a single property, into the serializer of every SDK, where it must be re-specified and
re-proved for each language. The verbosity is a real cost too, but the conformance risk is the
disqualifying one.

### Option 3: Emit strings, accept both forms

Write the canonical string; accept either a string or an object when reading.

**Pros**

- Forgiving toward hand-written or machine-generated IR from another tool.
- Compact output, with an escape hatch for producers that find the grammar awkward.

**Cons**

- Two decode paths that must agree forever, tested against each other.
- The leniency is where the risk concentrates: an SDK that emitted objects would be *accepted*,
  producing IR that is semantically right and byte-wrong.

**Rejected because:** it defeats the guarantee it is layered on. A conformance suite proving
byte-identical IR is worth having only if non-conforming IR is rejected; accepting a second
form means the suite tests a convention rather than a contract. Rejected on merit, not on cost
— accepting both is *more* work than accepting one.

## Decision Outcome

**Option 1.** A logical type is encoded as its canonical string. `Type` implements
`encoding.TextMarshaler` and `encoding.TextUnmarshaler`, so JSON, YAML, and `fmt` all reach the
same code and there is one path that can be wrong.

What that commits to:

- **Exactly one spelling per type.** Parsing normalizes — whitespace around parameters is
  trimmed, so `decimal(10, 2)` is accepted — while printing always emits the tight form. Type
  keywords are lowercase; `DECIMAL(10,2)` is an error carrying the canonical spelling as a
  hint, rather than a second accepted way to say the same thing.
- **The grammar is recursive.** `array(of)` takes a full type, so `array(string(255))` and
  `array(enum(draft,live))` follow from the grammar rather than from special cases. Nesting is
  bounded only so that corrupt input arriving over a pipe produces an error instead of a stack
  overflow.
- **Malformed input is rejected with a diagnosis, not a category.** Every failure names what
  was wrong and, where one exists, the canonical form to use instead. The alternative — one
  generic "invalid type" — would satisfy a test while helping nobody.
- **Engine limits are not encoded here.** PostgreSQL caps decimal precision at 1000 and MySQL
  at 65. Those belong in the capability model ([ADR 0004](0004-safety-model.md)), which is
  where engine differences live; a type system that has to hold for every engine at once cannot
  carry any one engine's ceiling.

## Consequences

### Positive

- Golden files and plan output read as the types they describe.
- Round-trip correctness is one property — `Parse(t.String()) == t` — which is fuzzable, and is
  fuzzed.
- A new SDK implements one printer and one parser to a grammar written down in full, rather
  than a serialization convention it has to infer.

### Negative

- **The parser is wire-critical.** A bug in it is an IR bug, reachable from any SDK, and a
  malformed type string is caught later than a malformed JSON structure would be.
- The grammar is a compatibility surface of its own. Adding a fifth parameterized type, or
  permitting a new character in an enum value, is an `irVersion` concern with the checksum
  consequences ADR 0003 describes — not a local change to one package.
- Enum values are restricted to letters, digits, underscore, and hyphen so that commas and
  parentheses stay structural. A database will accept labels this cannot express, and
  supporting them later means quoting rules, which means a canonical-quoting policy.

### Neutral

- Enum value order is part of the type. That is not a serialization artifact: PostgreSQL
  compares enum labels by declaration order, so `enum(a,b)` and `enum(b,a)` genuinely differ.
- The encoding is independent of the transport. The same string appears in JSON IR, in YAML, in
  `migr plan` output, and in error messages, because all four ask the type for its canonical
  form.

## Notes

Revisit if enum labels containing reserved characters turn out to matter in practice; that
would need a quoting rule and a policy for when to apply it, and the policy is what makes it an
`irVersion` change rather than a parser tweak.
