# 0038. The Go schema builder

## Status

Accepted

## Date

2026-08-27

## Context

[ADR 0037](0037-go-migration-contract.md) shipped the contract, the protocol client, and **one
operation**. `migr.Operation` was a sealed interface — `Kind()` plus an unexported `check()` — with
`Raw` as its only implementation, and `Schema.Apply` marshalled whatever it was handed and let the
host validate it as an IR document. A Go migration could therefore only reach its engine by name,
which is precisely the database independence the Operation IR exists to provide, given away.

This is the second time `sdk/go` copies something that lives on the engine side, and the reason has
not changed: importing `internal/ir` and `internal/schema` would make this module require the CLI
module, dragging pgx, cobra, and a YAML parser into every program that writes a migration.

The difference from the wire is that this copy is **user-facing**. Nobody outside the SDK
constructs a `wire.Message`; the operation structs are what a migration author reads, holds, and
writes by hand, and the done-when compares the builder against exactly that.

## Decision Drivers

- The done-when: *"the builder covers all 20 v1 ops with idiomatic, chainable Go
  (`schema.Table("users").AddColumn(...)`) and emits IR identical to hand-constructed IR."*
- [`docs/ir.md`](../ir.md) is normative, and [§9.1](../ir.md#91-checked-here) states exactly which
  structural rules are checked at the format's own level.
- [§7.2](../ir.md#72-conventions-the-format-does-not-enforce): conventions a producer must follow
  that no validation can check, because two producers disagreeing emit different bytes for one
  change.
- [ADR 0005](0005-logical-type-encoding.md): a type is one canonical string, and printing never
  preserves the input spelling.
- Invariant #10: inversion lives in `internal/engine`, so three SDKs cannot reach three inverses.
- M7-T3 mirrors this builder and M7-T7 compares the two SDKs byte for byte.

## Considered Options

### A. Where the flush happens

The done-when's spelling hands the author a builder, and a builder that is built and never sent
emits nothing. A migration that reports success having changed nothing is the failure
[§3.4](../ir.md#34-unknown-fields-are-rejected) names as the reason unknown fields are refused, so
that hole cannot simply be documented.

1. **A handle whose chain ends in `Apply(ctx)`**, with the window tracking what it handed out and
   failing the migration for a chain still holding operations when `Up` or `Down` returns.
2. **A closure the SDK brackets**, `s.AlterTable(ctx, "users", func(t *migr.Table) { … })`, which
   is [ADR 0017](0017-transaction-boundaries.md)'s argument for `Transact` applied here: there is
   nothing to hold, so nothing can be forgotten.
3. **Eager emission**, one callback per method, with `ctx` and an `error` at every link.

Chosen: **1**. The closure makes the flush structural and is the stronger shape in the abstract,
but the guard closes the same hole without changing how a migration reads, and it does so *loudly*
— the failure names the table and counts what was never applied. Option 3 removes chaining
altogether and costs one round trip per operation.

The guard's condition is **built and non-empty**, not merely built, so a chain a branch opened and
never added to is not a false alarm.

### B. How a builder reports a problem

A chain cannot return an error at each link, so errors accumulate and surface at `Apply`. That
settles the logical types too: `internal/schema.String(255)` returns `(Type, error)` and cannot be
inlined, which would cost a statement and a branch before every chain that uses a parameterized
type.

1. **`(Type, error)` constructors**, mirroring the engine exactly.
2. **A `Type` alone**, where one that has no canonical spelling carries the reason it has none, and
   that reason surfaces at the emit alongside the builder's own.

Chosen: **2**. `migr.String(0)` is not a valid type and never becomes one — `MarshalText` refuses
it, so it cannot reach the wire — and the reason arrives where an author is already reading for
errors. The cost is that a `Type` value can be wrong without the compiler saying so, which is the
same trade the builder itself makes.

### C. How much of §9.1 this copy repeats

The host decodes every operation as an IR document and runs all of §9.1 again, so a local check is
not what makes a bad operation safe.

1. **Required fields only**, leaving the rest to the host.
2. **All of §9.1.**

Chosen: **2**. Two reasons, neither of which is safety. The failure names the call that built it
rather than a JSON document — `invalid operation 1 (add_column): column "email": string length must
be at least 1` instead of a decode error about a struct. And two SDKs must refuse the same
operations before M7-T7's byte-for-byte comparison can mean anything; a rule enforced on one side
only is a rule that has already diverged.

### D. What an unqualified column means

`nullable` is always emitted ([§6.1](../ir.md#61-column)), so a builder that lets an author declare
a column without mentioning nullability has to choose what that means.

1. **NOT NULL**, mirroring the IR struct's zero value.
2. **Nullable**, mirroring SQL.

Chosen: **1**. The done-when is that a chain and a hand-constructed operation emit identical bytes;
choosing the opposite of the struct's zero value would make `migr.Column{Name: "email", Type: …}`
and `t.AddColumn("email", …)` differ unless the author spelled it.

The choice is written into [`docs/ir.md` §7.2](../ir.md#72-conventions-the-format-does-not-enforce)
rather than into this SDK, beside the two conventions already there, because there is one answer
and not one per language. It is additive prose about producers, so
[§10.2](../ir.md#102-what-is-a-breaking-change) does not reach it and `irVersion` stays 1.

### E. How the second copy of the IR is held honest

The same question [ADR 0037 F](0037-go-migration-contract.md) answered for the wire.

1. **Hold the SDK to `internal/ir`**, by importing it or by a cross-module test.
2. **Hold both to `docs/ir.md`**, as `internal/ir/spec_test.go` already holds the engine.

Chosen: **2**. `sdk/go/spec_test.go` reads the same file, asserts the same biconditional between
`Kinds()` and the document's sections, and **decodes all 21 checked vectors into this copy's structs
and re-encodes them byte for byte**. Decoding is the part that matters: a test that only constructed
operations could not miss a field this copy forgot to declare, because it would never have written
one. The eighteen logical types and the two closed enums are read out of the document the same way.

Transitively, the two copies agree on every documented vector without either naming the other.

### F. Which discriminator writes `op`

`Raw` shipped with an exported `Op Kind` field an author was told not to set, and `stamped()`
type-switched on the one implementation to fill it in. Neither survives twenty operations.

Each operation now carries a `MarshalJSON` over shared `marshalOp`/`splice` helpers transcribed
from `internal/ir`, so `op` comes first because the encoder puts it there
([§3.1](../ir.md#31-one-object-per-operation-discriminated-by-op)) rather than because a field
happened to be declared first. The inner encoder sets `SetEscapeHTML(false)`, which
[§3.3](../ir.md#33-escaping) requires: `total >= 0` in a check expression must produce identical
bytes from every SDK, and language defaults disagree.

## Decision Outcome

Package `migr` gains the other nineteen operations, the six `from` payloads, `Column`, `Default`,
the two closed enums, and the logical type system, in files mirroring `internal/ir`'s layout so the
two copies stay comparable file by file. Three departures from a straight transcription, each
forced: `schema.Kind` becomes `TypeKind` because `ir.Kind` already took the name in a package that
flattens both; there is no `Reverse()` or `Destructive()`, because inversion is the engine's by
invariant #10 and destructiveness is the validator's; and the discriminator moves into the encoder
as F describes.

Over that sit four entry points — `Schema.CreateTable`, `Schema.Table`, `Schema.View`,
`Schema.Raw` — each returning a chain whose methods are the operations that object accepts. A
method carrying modifiers returns a small change builder that **embeds its parent**, so its own
modifiers return itself and everything else is promoted. Recording prior state is one verb, `Was`,
on every change that carries a `from`.

The builder is sugar over `Schema.Apply`, never a second path. A table of one chain and one
hand-constructed operation per kind, with coverage derived from `Kinds()`, asserts they emit the
same bytes — which is the done-when, stated mechanically.

Two conventions §7.2 says nothing can check are structural here rather than remembered: an
unqualified column is NOT NULL, and `CreateTable(...).PrimaryKey(...)` and
`Table(...).AddPrimaryKey(...)` are different calls, so a new table states its key inline and an
existing one states it as an operation.

## Consequences

**The IR is now written twice, and will drift if the document stops being the contract.** The same
consequence ADR 0037 recorded for the wire, with the same mitigation and the same hole: an
operation added to one copy without a documented vector passes on that side and fails nothing on
the other. What is new is that this copy is user-facing, so drift reaches an author as a migration
that compiles and is refused at `host.schema.apply`.

**A `Type` can be wrong without the compiler saying so.** B's trade, paid in full: `migr.String(0)`
is a value, not a compile error, and a migration that builds one and never emits it never learns.
The reason travels inside the value and is reported the moment the operation using it is emitted.

**The unapplied-chain guard is a heuristic about intent, not a proof.** It catches a chain still
holding operations when the migration returns. It cannot catch a chain applied to the wrong object,
or one whose operations are correct and whose order is not.

**Nullability is now a documented cross-SDK contract**, which means M7-T3 is constrained by
`docs/ir.md` §7.2 rather than by reading this SDK. That is the point, and it also means changing
the default later is a documentation change with two implementations behind it.

**A migration author has two ways to say everything.** The structs are exported and `Schema.Apply`
takes them, so a chain and a literal both work. That is deliberate — the done-when compares them —
but it is surface area, and the runtime conformance fixtures (M6-T9) should be written in the
builder so the tested path is the one authors are shown.
