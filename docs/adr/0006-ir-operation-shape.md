# 0006. Flat discriminated operations, one representation per schema fact

## Status

Accepted

## Date

2026-07-27

## Context

[ADR 0001](0001-architecture.md) made a database-neutral operation IR the sole contract between
language SDKs and database drivers. [ADR 0005](0005-logical-type-encoding.md) settled how a
logical type is written down inside one. This record settles the shape of the operations
themselves — the seventeen v1 changes an SDK can express:

```
create_table  drop_table  rename_table  add_column  drop_column  rename_column
alter_column  add_index  drop_index  add_foreign_key  drop_foreign_key  add_unique
add_check  add_primary_key  create_view  drop_view  raw
```

Four questions had to be answered together, because the answers interact: how an operation is
discriminated in JSON, how much `create_table` may express when separate constraint operations
already exist, how a column default is represented without naming an engine, and where
destructiveness is decided.

Two downstream commitments constrain all four. A conformance suite walks the full operation set
against every registered driver and asserts each is compilable or declared `Unsupported`, so the
set must be enumerable and closed. And an end-to-end property requires apply-all → introspect →
diff to yield an *empty* diff on every engine, so the IR must express everything introspection
can observe, or that loop never closes.

## Decision Drivers

- **One spelling per schema fact.** [ADR 0003](0003-ir-versioning.md) requires byte-identical IR
  across SDKs. Two ways to say one thing defeats it in exactly the way that suite cannot catch.
- **Legibility where humans read IR.** Golden files and plan output are reviewed by people, and
  review is when a wrong operation must be noticeable.
- **No engine dialect in the neutral layer.** A migration that says `now()` has already stopped
  being portable.
- **Closed and enumerable.** The conformance suite and every total switch depend on it.
- **Stability.** This shape is frozen for the life of `irVersion` 1.

## Considered Options

### Question A: How is an operation discriminated?

#### Option A1: Flat, with an `op` field

Fields sit alongside the discriminator, which is written first.

```json
{"op":"add_column","table":"users","column":{"name":"email","type":"string(255)","nullable":false}}
```

**Pros**

- Shortest documents; a golden-file diff shows the field that changed rather than a reshuffled
  nesting level.
- The discriminator is written by the marshaler rather than stored on the struct, so the tag
  cannot disagree with the Go type — a class of bug removed rather than tested for.

**Cons**

- An operation field named `op` would collide. None does, and the name is reserved.
- Decoding is two-phase: read the discriminator, then decode into the concrete type.

#### Option A2: Nested under `args`

```json
{"op":"add_column","args":{"table":"users","column":{…}}}
```

**Pros**

- Uniform two-field envelope; no field can ever collide with the discriminator.
- Generic handling and schema validation are marginally simpler.

**Cons**

- A second nesting level in every document a person reads, for a collision that does not occur.

**Rejected because:** it pays a permanent legibility cost — in every golden file, every plan
output, and every specification example — to prevent a name collision that the operation set does
not have and that reserving one identifier prevents outright.

#### Option A3: One struct with every field, all `omitempty`

**Pros**

- No interface, no custom unmarshaling, the least code.

**Cons**

- An `add_column` carrying a `view` field is representable and marshals happily.
- The type system stops helping, so every driver's compile step must re-check what the struct
  permitted.

**Rejected because:** it makes invalid operations representable, and the cost lands in every
driver, forever. ADR 0005 made the same choice for types by keeping fields unexported.

### Question B: How much can `create_table` express?

#### Option B1: Columns plus a table-level primary key

**Pros**

- A primary key is intrinsic to a table, and often to its storage layout; inlining avoids a
  second lock event on a table that was empty a moment earlier.
- Every other constraint keeps exactly one representation, as its own operation.

**Cons**

- The primary key is expressible two ways — inline at create, or `add_primary_key` later. The
  canonical rule is that new tables inline it and existing tables use the operation.

#### Option B2: Columns only

**Pros**

- No duplication anywhere; the purest one-fact-one-operation split.

**Cons**

- Compiles to `CREATE TABLE` followed by `ALTER TABLE ADD PRIMARY KEY`, which on MySQL is a
  table rebuild.

**Rejected because:** it converts a free operation into a rebuild to buy a purity the primary key
does not warrant — a key is part of what a table *is*, not something added to it.

#### Option B3: Full inline constraints

**Pros**

- Matches what SQL itself permits in a single statement.

**Cons**

- A unique constraint would have two encodings, inline and `add_unique`, and the differ would
  have to pick one canonically for every constraint kind.

**Rejected because:** it multiplies the exact problem ADR 0005 was written to avoid, across five
constraint kinds instead of one.

### Question C: How is a column default represented?

#### Option C1: A literal or a named neutral function

```json
{"default":{"literal":"0"}}
{"default":{"function":"current_timestamp"}}
```

**Pros**

- No engine SQL in the neutral layer; each driver maps `current_timestamp` to its own spelling.
- Literals are encoded as strings and disambiguated by the column's own type, so byte-identical
  IR survives — a JSON number would round-trip `1` as `1.0` through a dynamically typed decoder.

**Cons**

- The function set is closed, so a default outside it needs a version bump or a `raw` operation.

#### Option C2: A free-form expression string

```json
{"default":"now()"}
```

**Pros**

- Simplest, and expresses anything the engine accepts.

**Cons**

- `now()` is PostgreSQL SQL; the same migration emits something MySQL rejects.

**Rejected because:** database independence is the project's first claim, and this breaks it at
the one field people use most. A neutral layer that carries dialect in its most common field is
not a neutral layer.

#### Option C3: C1 plus a driver-scoped expression escape

**Rejected because:** on timing, not merit. It is a reasonable extension once a real case
appears, and `raw` already provides an escape hatch today. Adding a second one before anything
needs it widens the surface M10 has to validate for no demonstrated gain.

### Question D: Where is destructiveness decided?

#### Option D1: On the operation type

**Pros**

- The fact belongs with the type, so adding an operation forces a verdict instead of leaving a
  gap in a table that lives in another package.
- The validator becomes enforcement rather than a second classification.

**Cons**

- `alter_column` cannot answer precisely without knowing the current type.

#### Option D2: In the validator

**Rejected because:** the five-part rule for adding an operation does not mention
destructiveness, so a classification held elsewhere is one a new operation can silently miss.
Keeping it on the type makes the omission a compile-time gap rather than a runtime default.

## Decision Outcome

**A1, B1, C1, D1.** Operations are flat and discriminated, `create_table` carries columns and a
primary key, defaults are a literal or a named function, and each operation classifies its own
destructiveness.

What that commits to:

- **`add_index` has no `unique` flag.** A plain index and a unique constraint are different
  schema facts with different operations. One fact, one spelling.
- **The operation set is closed.** `Operation` is a sealed interface that only the IR package
  implements, which is what makes a switch over it total rather than merely exhaustive-looking,
  and what lets the conformance suite claim it walked everything.
- **Unknown fields are rejected, not ignored.** ADR 0003's reasoning applies unchanged: decoding
  what you understand and skipping the rest turns a version mismatch into a migration that
  reports success having done less than it said.
- **IR is encoded without HTML escaping.** Go's `encoding/json` escapes `<`, `>` and `&` by
  default and JavaScript's `JSON.stringify` does not, so a check expression like `total >= 0`
  would serialize to different bytes in the Go and TypeScript SDKs for the same migration. That
  is a byte-identical-IR violation and a checksum change, and it is not obvious from either
  language's defaults. The IR package owns its encoder for this reason.
- **`alter_column` reports destructive on any type change.** Whether it narrows depends on the
  current type, which the operation does not carry. Over-reporting costs an
  `--allow-destructive` flag; under-reporting costs data.

## Consequences

### Positive

- Golden files and plan output read as the operations they describe.
- Adding an operation cannot silently skip a destructiveness verdict or a conformance case.
- A new SDK targets a closed set with one spelling each, rather than inferring conventions.

### Negative

- **`add_check.expression` and `create_view.query` are engine SQL in the neutral layer**, which
  invariant #3 otherwise forbids outside a driver package. There is no v1 alternative: a neutral
  expression language is a milestone of its own, and both operations are in the v1 set. They do
  not port between engines, and that is a property of the operations rather than a bug in them.
- The `op` field name is reserved across the whole operation set, forever.
- The default-function set is closed, so supporting a new one is an `irVersion` concern with the
  checksum consequences ADR 0003 describes — not a local change.
- Two spellings survive for a primary key. The canonical rule (inline when creating, operation
  when altering) is a convention the differ must follow rather than something the types enforce.

### Neutral

- `alter_column`'s optional fields are pointers, so *unset* and *set to the zero value* stay
  distinct, and `drop_default` is separate from `default` because removing a default and setting
  no default are different instructions.
- Structural validation lives with the types; policy — destructive gating, capability preflight,
  reference integrity — stays in the validator, which has the driver and live schema this layer
  does not.

## Notes

Revisit Option C3 when a real default falls outside the neutral function set. Revisit the
`add_check` and `create_view` dialect leak if a neutral expression IR is ever built; that would be
an `irVersion` change, not a package refactor.
