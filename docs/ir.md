# migr Operation IR — `irVersion` 1

The neutral middle of `migr`. Language SDKs emit it; database drivers consume it. Nothing else
crosses that boundary.

## 1. Scope

### 1.1 What this document specifies

The wire format: the document envelope, how an operation is encoded as JSON, the twenty
operations of `irVersion` 1 with their fields and constraints, the logical type grammar, and how
an operation is reversed.

It is written to be implementable without reading the Go. If something here is ambiguous enough
that two implementations could disagree on the bytes, that is a defect in this document.

The reference implementation is `internal/ir`.

### 1.2 What it deliberately does not specify

- **What an operation compiles to.** No engine SQL appears here. Translating an operation into
  statements is the driver contract, and each driver's output is pinned by golden files.
- **Capabilities and lock impact.** Whether an engine supports an operation, and what it costs a
  live database, are separate axes owned by the capability model. *Destructive* is not the same
  claim as *unsafe* or *unsupported* — see [ADR 0004](adr/0004-safety-model.md).
- **Policy.** The IR carries a destructiveness classification; refusing to run one is the
  validator's job.
- **Transport.** How a document reaches the engine is `docs/protocol.md`.
- **Engine limits.** PostgreSQL caps decimal precision at 1000 and MySQL at 65. Those belong to
  the capability model, not to a type system that has to hold for every engine at once.
- **Execution semantics.** The operations array is ordered and order is significant. Batching,
  transactions, and dependency resolution are the engine's.

There is deliberately **no JSON Schema**. It would be a second specification that can drift from
this one, and it could express neither the type grammar nor the either-or rules anyway.

### 1.3 How to read it

Every fenced block tagged `json` inside a section of [§7](#7-the-operation-set) is a complete,
valid operation document, and is **checked by the test suite**: it is decoded and re-encoded, and
the result must match the block exactly once insignificant whitespace is removed. The examples are
therefore conformance vectors, not illustrations, and they cannot drift from the implementation.

Blocks tagged `jsonc` are fragments — a payload on its own, or a snippet showing one field. They
are not checked.

Each operation is a level-3 heading holding its wire name and nothing else, so
`#alter_column` addresses it.

---

## 2. The document envelope

### 2.1 Shape

A document is an object with exactly two members:

```jsonc
{
  "irVersion": 1,
  "operations": [ /* zero or more operation objects */ ]
}
```

Nothing else goes in the envelope. The migration's identity and direction are named by the
protocol call that requested the migration and are held alongside the recording; repeating them
here would create fields that can disagree with their own container.

### 2.2 `irVersion`

An integer. `1` is the only version this format defines.

It is the one `camelCase` key in an otherwise `snake_case` format. That is deliberate rather than
an oversight: the decision records that established the envelope name it that way, and those
records are immutable, so renaming the key would leave a committed document describing a key that
does not exist. See [ADR 0007](adr/0007-ir-document-envelope.md).

### 2.3 The version is read before the operations

A consumer must determine `irVersion` and reject an unsupported one **before** decoding the
operations array.

The ordering carries the whole value of versioning. A document from a future version very likely
contains an operation this build has never heard of. Decoding first reports that unknown
operation — true, useless, and pointing at the wrong problem. Reading the version first reports
an unreadable `irVersion`, which tells the operator to upgrade.

A document with no `irVersion` is an error. It is never defaulted to `1`; assuming a version is
how a mismatch becomes a migration that reports success having done less than it said.

### 2.4 The empty document

A migration that records nothing is legal — a `Down` that does nothing still ran. Producers emit
an empty array, never `null`:

```jsonc
{ "irVersion": 1, "operations": [] }
```

---

## 3. Encoding

### 3.1 One object per operation, discriminated by `op`

Every operation is a flat JSON object whose `op` member names it. `op` comes first.

`op` is not a field of any operation; it is written by the encoder and stripped before decoding,
so the tag and the operation's identity cannot disagree. The name `op` is reserved across the
whole format, permanently.

Operations are flat rather than nested under an arguments object. See
[ADR 0006](adr/0006-ir-operation-shape.md).

### 3.2 Key order and whitespace

Emitted documents carry no whitespace between tokens and no trailing newline. Member order is
fixed: `op` first, then the operation's fields in the order this document lists them, then `from`
where the operation has one.

On **input**, member order is insignificant and whitespace is ignored — an ordinary JSON parser.
On **output**, both are fixed, because the emitted bytes are hashed. See [§4](#4-the-checksum-boundary).

### 3.3 Escaping

Only what JSON requires: `"`, `\`, and U+0000–U+001F. In particular `<`, `>`, and `&` are **not**
escaped, because a check expression such as `total >= 0` must produce identical bytes from every
SDK, and language defaults disagree here.

Non-ASCII characters are emitted as UTF-8, not as `\u` escapes. **U+2028 (LINE SEPARATOR) and
U+2029 (PARAGRAPH SEPARATOR) are ordinary non-ASCII characters here**, emitted as UTF-8 like any
other. They are not line terminators on any stream migr reads: a frame ends at U+000A and nowhere
else ([protocol.md §2.2](protocol.md#22-one-message-per-line)).

That rule costs some implementers work, so what it asks is stated plainly. Go's `encoding/json`
escapes both whatever it is told — `SetEscapeHTML(false)` does not reach them — where JavaScript's
`JSON.stringify` escapes neither. An implementation on the wrong side of that must undo it, and must
do so with a scanner that tells an escape from the text of one: a blind replacement over the encoder's
output also matches the tail of a doubled backslash, which is how a string whose author wrote that
escape as six literal characters is encoded. The `create_view` section carries a checked vector, and
[ADR 0047](adr/0047-line-separator-escaping.md) records the decision.

An IR string is well-formed UTF-8 — Unicode scalar values only. A lone surrogate is not expressible,
and an implementation that emits one has it replaced with U+FFFD by any decoder that reads scalar
values, which changes the document. The wire refuses the worse half outright: a line that is not
valid UTF-8 ends the session rather than being decoded.

### 3.4 Unknown fields are rejected

A member that the operation does not define is an error, not something to skip. Decoding what you
understand and ignoring the rest is how a version mismatch becomes a migration that reports
success having done less than it said.

Duplicate members in one object are unspecified. Do not emit them.

### 3.5 No JSON numbers appear inside an operation

Every field is a string, a boolean, an array of strings, or a nested object built from those. A
column's length and a decimal's precision travel inside the type string — `string(255)`,
`decimal(10,2)` — not as numbers.

That is not incidental. Go and JavaScript agree on many number renderings and not on all of them,
and a single disagreement would break byte-identical output across SDKs. `irVersion` is the only
number in the format, and it sits in the envelope, outside the hashed bytes.

### 3.6 Absent, null, and empty

An optional field is **omitted** when unset. It is never emitted as `null`.

That matters most for the fields that describe change: an absent field means *leave this alone*.
Removing something therefore needs its own explicit instruction — which is why `alter_column` has
both `default` and `drop_default`.

---

## 4. The checksum boundary

### 4.1 What is hashed

A migration's checksum covers two things: its normalized source, and **the exact bytes of its
emitted operations array** — the array alone, without the envelope. The `irVersion` and the
surrounding object are outside the boundary, so a version bump that reshapes no operation leaves
every applied migration's checksum untouched.

### 4.2 The emitted bytes are a compatibility surface

This is the consequence most easily missed, so it is stated plainly.

Once a migration has been applied, its checksum is recorded and immutable. A later mismatch is a
hard error with no repair flag. **Any change to how an operation is encoded therefore invalidates
every applied migration in every installation** — not as a bug, but as the mechanism working
exactly as designed against a format that moved underneath it.

Changes that move the bytes:

- reordering the members of an emitted object
- adding a member that is emitted unconditionally, rather than omitted when unset
- renaming a member, or changing an enum's wire spelling
- changing how a logical type renders — `decimal(10, 2)` for `decimal(10,2)` is a different
  document
- changing what is escaped

Changes that do not:

- adding an optional member that is omitted when unset, for documents that do not use it
- anything in the envelope
- anything about attribution, source paths, or timing, none of which is emitted

*(In the Go implementation these correspond to struct field order and the `omitempty` tag. The
rule is about the emitted bytes, not about any one language's mechanism for producing them.)*

### 4.3 What this asks of an implementer

Emit members in the order this document lists them. Omit optional members that are unset. Do not
pretty-print. Treat a change to any of that as a change to the format, requiring a decision
record — not as a refactor.

See [ADR 0009](adr/0009-migration-checksum.md).

---

## 5. Logical types

A migration names a logical type. Each driver maps it to a native one. No native type name ever
appears in a document.

### 5.1 Grammar

```
type := ident [ "(" arg { "," arg } ")" ]
```

A type is encoded as this string — not as a nested object. One representation means golden files
stay readable, plan output needs no translation, and there is a single code path that can be
wrong. See [ADR 0005](adr/0005-logical-type-encoding.md).

### 5.2 The eighteen kinds

Fourteen stand alone:

`text` · `bool` · `int16` · `int32` · `int64` · `float32` · `float64` · `uuid` · `json` ·
`timestamp` · `timestamptz` · `date` · `time` · `binary`

Four take parameters:

| Form | Meaning |
|---|---|
| `string(n)` | Variable-length text with a maximum length. `n` is at least 1. |
| `decimal(p,s)` | Exact numeric with precision `p` and scale `s`. `p` at least 1, `s` at least 0 and no greater than `p`. |
| `enum(a,b,c)` | One of an ordered list of labels. At least one, all distinct. Labels are letters, digits, `_` and `-`. |
| `array(of)` | A list whose element is itself a full type, so `array(string(255))` and `array(enum(draft,live))` follow from the grammar. Nesting is bounded at 32. |

### 5.3 One spelling per type

Parsing normalizes — whitespace around parameters is accepted, so `decimal(10, 2)` reads — but
**printing always emits the tight form**, with no spaces. Two SDKs emitting different bytes for
the same type would break byte-identical output, so a consumer must not preserve the input
spelling.

Type keywords are lowercase. `DECIMAL(10,2)` is an error.

**Enum order is part of the type.** `enum(a,b)` and `enum(b,a)` are different types, because
PostgreSQL compares enum labels by declaration order.

### 5.4 What is not checked here

Upper bounds. `string(1000000)` and `decimal(2000,4)` parse; whether an engine accepts them is
that driver's capability declaration. Likewise `array(array(int32))` parses regardless of whether
any engine supports it. Nesting is bounded only so that corrupt input arriving over a pipe
produces an error instead of a stack overflow.

---

## 6. Shared structures

### 6.1 Column

| Field | Type | Required | Notes |
|---|---|---|---|
| `name` | string | yes | |
| `type` | logical type | yes | |
| `nullable` | boolean | yes | **Always emitted**, including when false. |
| `default` | [Default](#62-default) | no | Omitted when there is none. |

```jsonc
{ "name": "email", "type": "string(255)", "nullable": false }
```

`nullable` is unconditional here because a column is either nullable or not — there is no third
state. Contrast `alter_column`, where `nullable` is omitted when unset and absent means *leave
nullability alone*.

### 6.2 Default

Exactly one of the two members is present.

| Field | Type | Notes |
|---|---|---|
| `literal` | string | A constant value. |
| `function` | string | One of the neutral functions below. |

```jsonc
{ "literal": "anonymous" }
{ "function": "current_timestamp" }
```

**`literal` is always a JSON string**, never a number or a boolean — `"0"`, not `0`. A JSON number
does not survive a round trip through a dynamically typed decoder intact: `1` can come back as
`1.0`, which is a different default and a different document. The driver applies the value in the
column's type.

### 6.3 Default functions

`current_timestamp` · `current_date` · `current_time` · `uuid`

A closed set. Engine SQL such as `now()` is not accepted here — it is PostgreSQL's spelling of one
of these, and the point of the neutral name is that each driver writes its own. Adding a fifth
function changes the format.

### 6.4 Referential actions

`no_action` · `restrict` · `cascade` · `set_null` · `set_default`

Also closed. Used by `on_delete` and `on_update`; omitted when unset, which leaves the engine's
default behavior.

`set_default` is the one action an engine may have to refuse. MySQL and MariaDB both accept the clause
and neither performs it — the delete is refused as though the action were `restrict`, MySQL records
the clause in its catalog anyway and MariaDB drops it — so a driver that passed it through would give
one migration a different meaning on each engine.

### 6.5 Recorded prior state — the `from` payloads

An operation that destroys information it does not otherwise carry may record what it replaced, in
a member named `from`. Supplied, the operation inverts exactly. Absent, it is irreversible with a
reason. See [§8](#8-reversal).

| Payload | Members | Used by |
|---|---|---|
| **TableDef** | `columns` (array of Column, at least one), `primary_key` (array of string, omitted when empty) | `drop_table` |
| **Column** | as [§6.1](#61-column) | `drop_column` |
| **ColumnsDef** | `columns` (array of string, at least one, distinct) | `drop_index`, `drop_unique`, `drop_primary_key` |
| **ForeignKeyDef** | `columns`, `references_table`, `references_columns`, `on_delete`, `on_update` | `drop_foreign_key` |
| **CheckDef** | `expression` (string) | `drop_check` |
| **ViewDef** | `query` (string) | `create_view`, `drop_view` |
| **ColumnState** | `type`, `nullable`, `default`, `no_default` — all optional | `alter_column` |

`ColumnsDef` is shared by index, unique, and primary key because all three are literally a list of
columns. That is type reuse, not three spellings of one fact.

**ColumnState** carries `no_default: true` to record that the column *had no default*, which is a
different statement from saying nothing about defaults at all. Without it, an `alter_column` that
sets a default on a column that had none could not be reversed without recording some unrelated
field as padding. See [ADR 0010](adr/0010-recorded-absence-of-a-default.md).

```jsonc
{ "type": "string(255)", "nullable": false }
{ "no_default": true }
```

`default` and `no_default` cannot both be present.

### 6.6 `from` means two different things

The member name `from` is used for two unrelated purposes, and an implementation must not confuse
them:

- In **`rename_table`** and **`rename_column`**, `from` is a **string**: the old name. It pairs
  with `to`.
- Everywhere else, `from` is an **object**: recorded prior state, as in [§6.5](#65-recorded-prior-state--the-from-payloads).

The two never appear on the same operation, so the type is always determined by `op`.

---

## 7. The operation set

### 7.1 Summary

| `op` | Destructive | Inverse | Records `from` |
|---|---|---|---|
| [`create_table`](#create_table) | no | `drop_table` | — |
| [`drop_table`](#drop_table) | yes | `create_table` | TableDef |
| [`rename_table`](#rename_table) | no | itself, arguments swapped | — |
| [`add_column`](#add_column) | no | `drop_column` | — |
| [`drop_column`](#drop_column) | yes | `add_column` | Column |
| [`rename_column`](#rename_column) | no | itself, arguments swapped | — |
| [`alter_column`](#alter_column) | when `type` is present | itself, restoring `from` | ColumnState |
| [`add_index`](#add_index) | no | `drop_index` | — |
| [`drop_index`](#drop_index) | no | `add_index` | ColumnsDef |
| [`add_foreign_key`](#add_foreign_key) | no | `drop_foreign_key` | — |
| [`drop_foreign_key`](#drop_foreign_key) | no | `add_foreign_key` | ForeignKeyDef |
| [`add_unique`](#add_unique) | no | `drop_unique` | — |
| [`drop_unique`](#drop_unique) | no | `add_unique` | ColumnsDef |
| [`add_check`](#add_check) | no | `drop_check` | — |
| [`drop_check`](#drop_check) | no | `add_check` | CheckDef |
| [`add_primary_key`](#add_primary_key) | no | `drop_primary_key` | — |
| [`drop_primary_key`](#drop_primary_key) | no | `add_primary_key` | ColumnsDef |
| [`create_view`](#create_view) | no | `drop_view`, or a restoring `create_view` | ViewDef |
| [`drop_view`](#drop_view) | yes | `create_view` | ViewDef |
| [`raw`](#raw) | yes | *none* | — |

Note that most drops are **not** destructive. Dropping an index, a unique constraint, a check, a
foreign key, or a primary key removes a rule, not a row. Only `drop_table`, `drop_column`,
`drop_view`, and `raw` can lose data.

> [ADR 0006](adr/0006-ir-operation-shape.md) settled the *shape* of an operation and named
> seventeen. [ADR 0008](adr/0008-reverse-operations.md) added `drop_unique`, `drop_check`, and
> `drop_primary_key`, and records why the set was asymmetric without them. Decision records are
> immutable snapshots of a decision at a point in time, so 0006 is earlier, not wrong. The current
> set is twenty.

### 7.2 Conventions the format does not enforce

Three rules that producers must follow and that no validation can check. A producer that ignores
them emits a valid document that means something else.

**A column is NOT NULL unless the migration says otherwise.** [`nullable`](#61-column) is always
emitted, so an SDK whose builder lets an author declare a column without mentioning nullability has
to choose what that means. Choosing differently from another SDK emits different bytes for the same
schema change — the same failure the rule below describes, arrived at from the author's side rather
than the document's. The choice is stated here rather than in each SDK because there is one answer,
not one per language.

**Primary keys have two spellings.** A new table states its key inline in `create_table`;
`add_primary_key` is for a table that already exists. Both are valid, so nothing rejects the wrong
choice — but two producers disagreeing here emit different bytes for the same schema change, which
fails byte-identical conformance even though both documents are correct.

**Identifiers are not normalized.** A name is checked for being non-empty and is otherwise passed
through exactly. `" users "` is a valid table name with spaces in it, and is a different table from
`"users"`. Trim before emitting.

### 7.3 The operations

Each entry gives the operation's fields, its destructiveness, its inverse, and a complete example.
Every field named `table`, `column`, `name`, or `view` is a string that must be non-empty.

---

### `create_table`

Creates a table with its columns and, optionally, its primary key.

- **Destructive:** no
- **Inverse:** `drop_table` naming the same table, carrying no `from`. That inverse is itself
  irreversible — see [§8.4](#84-reversing-twice-is-not-the-identity).

| Field | Type | Required | Notes |
|---|---|---|---|
| `table` | string | yes | |
| `columns` | array of [Column](#61-column) | yes | At least one. Names must be distinct. |
| `primary_key` | array of string | no | Omitted when empty. Every entry must name a column in `columns`. |

```json
{
  "op": "create_table",
  "table": "users",
  "columns": [
    { "name": "id", "type": "uuid", "nullable": false },
    { "name": "email", "type": "string(255)", "nullable": false },
    {
      "name": "created_at",
      "type": "timestamptz",
      "nullable": true,
      "default": { "function": "current_timestamp" }
    }
  ],
  "primary_key": ["id"]
}
```

---

### `drop_table`

Removes a table and everything in it.

- **Destructive:** yes
- **Inverse:** `create_table` rebuilt from `from`. Irreversible without it.

| Field | Type | Required | Notes |
|---|---|---|---|
| `table` | string | yes | |
| `from` | [TableDef](#65-recorded-prior-state--the-from-payloads) | no | The table's prior definition. Required for reversal. |

Recording `from` restores the table's structure, not its rows. Nothing in this format can restore
data, and a rollback that recreates an empty table is not the same thing as never having dropped
it.

```json
{
  "op": "drop_table",
  "table": "sessions",
  "from": {
    "columns": [
      { "name": "id", "type": "uuid", "nullable": false },
      { "name": "expires_at", "type": "timestamptz", "nullable": false }
    ],
    "primary_key": ["id"]
  }
}
```

---

### `rename_table`

Renames a table, preserving its contents.

- **Destructive:** no
- **Inverse:** itself, with `from` and `to` swapped. Always reversible.

| Field | Type | Required | Notes |
|---|---|---|---|
| `from` | string | yes | The current name. A **string**, not a recorded-state object — see [§6.6](#66-from-means-two-different-things). |
| `to` | string | yes | The new name. Must differ from `from`. |

```json
{ "op": "rename_table", "from": "users", "to": "accounts" }
```

---

### `add_column`

Adds one column to an existing table.

- **Destructive:** no
- **Inverse:** `drop_column` naming the same column, carrying no `from`.

| Field | Type | Required | Notes |
|---|---|---|---|
| `table` | string | yes | |
| `column` | [Column](#61-column) | yes | |

```json
{
  "op": "add_column",
  "table": "users",
  "column": {
    "name": "nickname",
    "type": "string(64)",
    "nullable": true,
    "default": { "literal": "anonymous" }
  }
}
```

---

### `drop_column`

Removes a column and the data it holds.

- **Destructive:** yes
- **Inverse:** `add_column` rebuilt from `from`. Irreversible without it.

| Field | Type | Required | Notes |
|---|---|---|---|
| `table` | string | yes | |
| `column` | string | yes | |
| `from` | [Column](#61-column) | no | The column's prior definition. Its `name` must equal `column`. |

The `name` inside `from` is checked against `column` because the inverse re-adds the recorded
definition verbatim: a mismatch would restore a column that was never dropped while leaving the
dropped one gone.

```json
{
  "op": "drop_column",
  "table": "users",
  "column": "nickname",
  "from": {
    "name": "nickname",
    "type": "string(64)",
    "nullable": true,
    "default": { "literal": "anonymous" }
  }
}
```

---

### `rename_column`

Renames a column, preserving its data.

- **Destructive:** no
- **Inverse:** itself, with `from` and `to` swapped. Always reversible.

| Field | Type | Required | Notes |
|---|---|---|---|
| `table` | string | yes | |
| `from` | string | yes | The current name. A **string** — see [§6.6](#66-from-means-two-different-things). |
| `to` | string | yes | Must differ from `from`. |

```json
{ "op": "rename_column", "table": "users", "from": "email", "to": "email_address" }
```

---

### `alter_column`

Changes a column's type, nullability, or default. At least one change must be requested.

- **Destructive:** when `type` is present. The operation does not carry the column's current type,
  so it cannot tell a widening from a narrowing and gives the conservative answer. Over-reporting
  costs a flag; under-reporting costs data.
- **Inverse:** itself, restoring `from` and recording the current values as the new `from`.
  Irreversible when `from` is absent or records nothing.

| Field | Type | Required | Notes |
|---|---|---|---|
| `table` | string | yes | |
| `column` | string | yes | |
| `type` | logical type | no | Absent means leave the type alone. |
| `nullable` | boolean | no | Absent means leave nullability alone. Unlike in a [Column](#61-column), this is omitted when unset. |
| `default` | [Default](#62-default) | no | Sets a default. Cannot appear with `drop_default`. |
| `drop_default` | boolean | no | `true` removes the existing default. Omitted when false. |
| `from` | [ColumnState](#65-recorded-prior-state--the-from-payloads) | no | Prior state. Required for reversal. |

Removing a default needs `drop_default` rather than `"default": null` because an absent field
means *leave it alone*. For the same reason, the inverse of *set a default* is an explicit
`drop_default`, not an omitted `default` — and that inverse can only be produced when `from` says
`no_default: true`. A `from` that is merely silent about defaults inverts to an operation that
leaves the default alone, because inferring absence from silence is a guess that would destroy a
default the migration never recorded.

A type change, which is destructive:

```json
{
  "op": "alter_column",
  "table": "users",
  "column": "email",
  "type": "string(512)",
  "from": { "type": "string(255)" }
}
```

Setting a default on a column that had none, which is not:

```json
{
  "op": "alter_column",
  "table": "users",
  "column": "status",
  "default": { "literal": "draft" },
  "from": { "no_default": true }
}
```

---

### `add_index`

Creates a non-unique index. A unique constraint is [`add_unique`](#add_unique) — one schema fact,
one spelling, so there is no `unique` flag here.

- **Destructive:** no
- **Inverse:** `drop_index` naming the same index, carrying no `from`.

| Field | Type | Required | Notes |
|---|---|---|---|
| `table` | string | yes | |
| `name` | string | yes | |
| `columns` | array of string | yes | At least one, all distinct. Order is significant. |

```json
{ "op": "add_index", "table": "users", "name": "idx_users_email", "columns": ["email"] }
```

`columns` names columns and carries **no prefix length**, and v1 has no field for one. An engine that
requires a prefix to index a column of some type therefore refuses the operation rather than choosing
a prefix itself — a prefix is a different index, and on `add_unique` a different constraint. MySQL
requires one for a text or blob column and for a key past 3072 bytes; MariaDB requires one only for a
primary key. The same applies to [`add_unique`](#add_unique) and [`add_primary_key`](#add_primary_key).

---

### `drop_index`

Removes an index. The table's rows are untouched.

- **Destructive:** no
- **Inverse:** `add_index` rebuilt from `from`. Irreversible without it.

| Field | Type | Required | Notes |
|---|---|---|---|
| `table` | string | yes | |
| `name` | string | yes | |
| `from` | [ColumnsDef](#65-recorded-prior-state--the-from-payloads) | no | The indexed columns. Required for reversal. |

```json
{
  "op": "drop_index",
  "table": "users",
  "name": "idx_users_email",
  "from": { "columns": ["email"] }
}
```

---

### `add_foreign_key`

Adds a foreign key from one table's columns to another's.

- **Destructive:** no
- **Inverse:** `drop_foreign_key` naming the same constraint, carrying no `from`.

| Field | Type | Required | Notes |
|---|---|---|---|
| `table` | string | yes | |
| `name` | string | yes | |
| `columns` | array of string | yes | At least one, all distinct. |
| `references_table` | string | yes | |
| `references_columns` | array of string | yes | Must have the same number of entries as `columns`; they pair by position. |
| `on_delete` | [referential action](#64-referential-actions) | no | Omitted leaves the engine default. |
| `on_update` | [referential action](#64-referential-actions) | no | |

```json
{
  "op": "add_foreign_key",
  "table": "orders",
  "name": "fk_orders_user",
  "columns": ["user_id"],
  "references_table": "users",
  "references_columns": ["id"],
  "on_delete": "cascade",
  "on_update": "restrict"
}
```

---

### `drop_foreign_key`

Removes a foreign key. Rows in both tables are untouched.

- **Destructive:** no
- **Inverse:** `add_foreign_key` rebuilt from `from`. Irreversible without it.

| Field | Type | Required | Notes |
|---|---|---|---|
| `table` | string | yes | |
| `name` | string | yes | |
| `from` | [ForeignKeyDef](#65-recorded-prior-state--the-from-payloads) | no | The constraint's prior definition. Required for reversal. |

```json
{
  "op": "drop_foreign_key",
  "table": "orders",
  "name": "fk_orders_user",
  "from": {
    "columns": ["user_id"],
    "references_table": "users",
    "references_columns": ["id"],
    "on_delete": "cascade"
  }
}
```

---

### `add_unique`

Adds a unique constraint over one or more columns.

- **Destructive:** no
- **Inverse:** `drop_unique` naming the same constraint, carrying no `from`.

| Field | Type | Required | Notes |
|---|---|---|---|
| `table` | string | yes | |
| `name` | string | yes | |
| `columns` | array of string | yes | At least one, all distinct. |

```json
{ "op": "add_unique", "table": "users", "name": "uq_users_email", "columns": ["email"] }
```

---

### `drop_unique`

Removes a unique constraint. The rows stay, and duplicates become possible again.

- **Destructive:** no
- **Inverse:** `add_unique` rebuilt from `from`. Irreversible without it.

| Field | Type | Required | Notes |
|---|---|---|---|
| `table` | string | yes | |
| `name` | string | yes | |
| `from` | [ColumnsDef](#65-recorded-prior-state--the-from-payloads) | no | The constrained columns. Required for reversal. |

Re-adding the constraint on rollback fails if duplicates were written while it was gone. That is a
property of the data, not of this operation, and no recorded prior state can prevent it.

```json
{
  "op": "drop_unique",
  "table": "users",
  "name": "uq_users_email",
  "from": { "columns": ["email"] }
}
```

---

### `add_check`

Adds a check constraint.

- **Destructive:** no
- **Inverse:** `drop_check` naming the same constraint, carrying no `from`.

| Field | Type | Required | Notes |
|---|---|---|---|
| `table` | string | yes | |
| `name` | string | yes | |
| `expression` | string | yes | **Engine SQL.** Not translated, not portable. |

`expression` is the one place besides [`create_view`](#create_view) and [`raw`](#raw) where engine
SQL sits in a neutral document. A neutral expression language is a project of its own; until there
is one, a check constraint written for PostgreSQL may not run on MySQL. That is a property of the
operation, not a defect in a particular document.

```json
{
  "op": "add_check",
  "table": "orders",
  "name": "ck_orders_total",
  "expression": "total >= 0"
}
```

---

### `drop_check`

Removes a check constraint. The rows stay, including any that would now violate it.

- **Destructive:** no
- **Inverse:** `add_check` rebuilt from `from`. Irreversible without it.

| Field | Type | Required | Notes |
|---|---|---|---|
| `table` | string | yes | |
| `name` | string | yes | |
| `from` | [CheckDef](#65-recorded-prior-state--the-from-payloads) | no | The prior expression. Required for reversal. |

```json
{
  "op": "drop_check",
  "table": "orders",
  "name": "ck_orders_total",
  "from": { "expression": "total >= 0" }
}
```

---

### `add_primary_key`

Adds a primary key to a table that has none. A table created with an inline `primary_key` does not
need this — see [§7.2](#72-conventions-the-format-does-not-enforce).

- **Destructive:** no
- **Inverse:** `drop_primary_key` naming the same table, carrying no `from`.

| Field | Type | Required | Notes |
|---|---|---|---|
| `table` | string | yes | |
| `name` | string | **no** | Omitted when unset. MySQL has no name for a primary key. |
| `columns` | array of string | yes | At least one. Order is significant. |

Unlike `create_table`, this operation cannot check its columns against a declared list — it has
nothing to check against, since the table already exists.

```json
{ "op": "add_primary_key", "table": "legacy", "name": "pk_legacy", "columns": ["id"] }
```

---

### `drop_primary_key`

Removes a table's primary key.

- **Destructive:** no
- **Inverse:** `add_primary_key` rebuilt from `from`. Irreversible without it.

| Field | Type | Required | Notes |
|---|---|---|---|
| `table` | string | yes | |
| `name` | string | no | Omitted when unset. |
| `from` | [ColumnsDef](#65-recorded-prior-state--the-from-payloads) | no | The key's columns. Required for reversal. |

Not destructive by this format's definition — no row is lost — but on some engines it rewrites the
whole table. That cost is lock impact, a separate axis owned by the capability model.

```json
{
  "op": "drop_primary_key",
  "table": "legacy",
  "name": "pk_legacy",
  "from": { "columns": ["id"] }
}
```

---

### `create_view`

Creates a view, or replaces an existing one.

- **Destructive:** no
- **Inverse:** `drop_view` when `or_replace` is absent or false. When `or_replace` is true, a
  `create_view` restoring `from.query` — and irreversible without `from`, because the operation
  may have overwritten a definition it did not record.

| Field | Type | Required | Notes |
|---|---|---|---|
| `view` | string | yes | |
| `query` | string | yes | **Engine SQL.** Not translated, not portable. |
| `or_replace` | boolean | no | `true` replaces an existing view. Omitted when false. |
| `from` | [ViewDef](#65-recorded-prior-state--the-from-payloads) | no | The query this replaced. Meaningful only with `or_replace`. |

With `or_replace`, the operation cannot assume the view was new. Inverting it to a `drop_view`
would destroy a definition the migration never created, so it inverts to a restoration or refuses.

```json
{
  "op": "create_view",
  "view": "active_users",
  "query": "SELECT id FROM users WHERE active",
  "or_replace": true,
  "from": { "query": "SELECT id FROM users" }
}
```

A view query is engine SQL and may carry any character the engine accepts. This second vector is the
one [§3.3](#33-escaping) exists for: the query below holds a **raw U+2028 after `id`** and a **raw
U+2029 after `users`**, invisibly, because that is what an emitted document looks like. It is the
byte-exact case every copy of the IR is held to, and an editor that normalises either character away
turns it into a vector that proves nothing — so a test asserts this file still carries them.

```json
{
  "op": "create_view",
  "view": "active_users",
  "query": "SELECT id FROM users WHERE active"
}
```

---

### `drop_view`

Removes a view.

- **Destructive:** yes
- **Inverse:** `create_view` rebuilt from `from`. Irreversible without it.

| Field | Type | Required | Notes |
|---|---|---|---|
| `view` | string | yes | |
| `from` | [ViewDef](#65-recorded-prior-state--the-from-payloads) | no | The view's prior query. Required for reversal. |

A view holds no rows, so nothing is lost that the query cannot rebuild — but the definition itself
is information, and dropping one without recording `from` discards it permanently.

```json
{
  "op": "drop_view",
  "view": "active_users",
  "from": { "query": "SELECT id FROM users WHERE active" }
}
```

---

### `raw`

The driver-scoped escape hatch: SQL run verbatim against the engine it names.

- **Destructive:** yes — the framework cannot know what the statement does, so it assumes the
  worst.
- **Inverse:** none, ever. Write the undo as another `raw` operation in the migration's `Down`.

| Field | Type | Required | Notes |
|---|---|---|---|
| `driver` | string | yes | The engine this SQL targets. |
| `sql` | string | yes | Run verbatim. |

Naming the driver is what turns "this migration does not work on MySQL" from a runtime failure
into a validation failure. It does not make the statement portable; nothing can.

```json
{
  "op": "raw",
  "driver": "postgres",
  "sql": "CREATE EXTENSION IF NOT EXISTS pgcrypto"
}
```

---

## 8. Reversal

### 8.1 The contract

Every operation answers what undoes it: another operation, or a refusal carrying a reason. There
is no third answer, and no operation may decline to answer.

A refusal always states *why*. "Irreversible" alone tells an operator nothing about whether the
situation was avoidable; a reason that names the missing `from` field reads as a gap in the
recorded data, which is actionable.

### 8.2 `from` is trust, not proof

Nothing verifies that a recorded prior definition matches what was actually there. A producer that
fills `from` in carelessly produces a reverse that restores the wrong schema — silently, and at the
moment of recovery.

A schema differ knows both sides of every change and can always populate `from` correctly. A
hand-written migration often cannot, and should then leave it out and be honestly irreversible
rather than guess.

### 8.3 Reversing a list

To reverse a document, invert every operation **and** reverse their order.

This fails whole. If any operation in the list cannot be inverted, the reversal produces nothing at
all rather than a partial list — a rollback that stops halfway leaves a schema in a state no
migration describes.

The rollback executor consumes that guarantee one level up: it inverts every migration in the queue
before running any of them, so an operation with no inverse halts the run before the first statement
rather than after the newer migrations have already been undone. A list is inverted only for a
migration whose source authored no `Down` of its own; an authored one is run as written. See
[ADR 0022](adr/0022-rollback-executor.md).

### 8.4 Reversing twice is not the identity

Worth stating because it surprises people. Operations fall into three groups:

**Self-inverse.** `rename_table` and `rename_column` swap their arguments, so applying the inverse
twice returns the original exactly.

**Fully round-tripping when `from` is recorded.** `alter_column` records the values it is setting
as the new `from`, so its inverse is itself reversible and `Reverse(Reverse(op))` is the original.
`create_view` with `or_replace` does the same.

**One-way.** Every `add_*` and `create_*` inverts to a drop that carries **no** `from` — it has
nowhere to put one, since the drop identifies its target by name alone. So `create_table` inverts
to `drop_table`, and that `drop_table` is irreversible. This is correct: rolling a migration
forward again means running the original `Up`, not reversing its `Down`.

### 8.5 Two cases that are easy to get wrong

**Reversing "set a default" must restore "no default" explicitly.** Omitting the `default` field
in the inverse means *leave the default alone*, which would leave the new default in place after
a rollback — a silent partial restoration. The inverse must carry `drop_default: true`, and it can
only know to do so if `from` recorded `no_default: true`.

**`create_view` with `or_replace` must not assume the view was new.** See
[`create_view`](#create_view).

---

## 9. Validation and policy

### 9.1 Checked here

Structure only: that required fields are present and non-empty, that lists are non-empty and
distinct where the operation requires it, that paired lists are the same length, that enum members
are in their sets, that either-or fields are not both set, that a `create_table`'s primary key
names its own columns, and that a `drop_column`'s recorded name matches the column being dropped.

A document that fails any of these is not an operation. Decoding one is an error.

### 9.2 Not checked here

Everything requiring a connection or a driver:

- whether the table, column, or constraint exists
- whether the engine supports the operation, and what it costs a live database
- whether a destructive operation is permitted by policy
- whether an `expression`, `query`, or `sql` string is valid for the target engine
- whether a type's parameters are within an engine's limits

These are the validator's and the capability model's, and they run before execution against a real
schema. A structurally valid document is not a runnable one.

---

## 10. Versioning and stability

### 10.1 What `irVersion` 1 covers

The operation set, every field name and its position in the emitted object, the logical type
grammar and its canonical spellings, the two closed enums, and the escaping rules.

### 10.2 What is a breaking change

Anything a current producer could emit that a current consumer would reject, or that changes the
bytes for an unchanged migration: removing or renaming an operation or field, changing a member's
position, adding a member emitted unconditionally, altering a wire spelling, or extending a closed
enum.

A breaking change bumps `irVersion`, updates this document, and updates the conformance suite for
every SDK. It also moves every affected migration's checksum — see [§4](#4-the-checksum-boundary).

Adding an operation, or adding an optional member that is omitted when unset, does not move the
bytes of documents that do not use it.

### 10.3 Adding an operation

Five parts, in one change: the operation type with a round-trip test; a compile implementation in
every registered driver, or an explicit unsupported capability with a reason; a golden file per
driver; a defined inverse or an explicit irreversible marker; and an entry in this document.

The last one is enforced by the test suite — [§1.3](#13-how-to-read-it).

### 10.4 Four independent version lines

The framework version, this `irVersion`, the runtime protocol version, and the checksum scheme.
Nothing couples them. A change to one does not imply a change to any other, and a reader who
assumes otherwise will make a wrong change.

---

## 11. Decision records

The reasoning behind this format, including the options that were rejected:

| # | Decision |
|---|---|
| [0001](adr/0001-architecture.md) | Go core with an operation IR in the middle |
| [0003](adr/0003-ir-versioning.md) | Version the operation IR explicitly |
| [0005](adr/0005-logical-type-encoding.md) | Encode logical types as canonical strings |
| [0006](adr/0006-ir-operation-shape.md) | Flat discriminated operations, one representation per schema fact |
| [0007](adr/0007-ir-document-envelope.md) | A versioned document envelope, outside the checksum boundary |
| [0008](adr/0008-reverse-operations.md) | Every operation declares an inverse or a reason it has none |
| [0009](adr/0009-migration-checksum.md) | A migration checksum is a scheme-versioned pair |
| [0010](adr/0010-recorded-absence-of-a-default.md) | Recorded prior state distinguishes "no default" from "nothing recorded" |
