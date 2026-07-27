# 0010. Recorded prior state distinguishes "no default" from "nothing recorded"

## Status

Accepted

## Date

2026-07-28

## Context

An `alter_column` operation changes a column's type, nullability, or default. To be reversible it
records what it replaced, in an optional `from` payload holding the column's prior type,
nullability, and default. [ADR 0008](0008-reverse-operations.md) established that pattern across
ten operations: supplied, the operation inverts exactly; absent, it is irreversible with a reason.

Writing the wire specification exposed a defect in how that payload was designed.

Each of its three members is optional and omitted when unset, so an absent member means *nothing
was recorded about this*. But a column that has no default is a real and common prior state, and
the payload had no way to say so. "The column had no default" and "I am not telling you about
defaults" were the same document.

That collision has two consequences, both confirmed against the implementation.

**Setting a default on a column that had none could not be reversed.** The natural encoding of
its prior state is a payload asserting only *there was no default*, which is an empty payload,
which reads as nothing recorded. The operation refuses to invert. The only way through was to
record some unrelated unchanged member — the column's existing type, typically — purely so that
the payload read as non-empty. The test covering this case did exactly that, and passed for that
reason rather than because the operation worked.

**The absence was inferred instead.** Because the payload could not state it, the inverse guessed:
if the payload mentioned no default and the operation set one, the inverse dropped the default.
That is wrong whenever the payload is simply silent about defaults — a payload recording only a
nullability change, for a migration that also sets a default, would invert to an operation that
drops a default it never recorded and never set.

**And an operation could be reversed once but not twice.** An `alter_column` whose only change is
dropping a default produced an inverse whose own prior state was empty — because "this operation
dropped a default" was, again, unsayable. The rollback of a rollback failed.

The timing matters. No driver compiles operations yet, no migration has been applied anywhere, and
`irVersion` 1 has never been released. Every one of these is only ever more expensive to fix.

## Decision Drivers

- **Never guess.** A reverse that restores the wrong schema is worse than one that refuses,
  because it fails silently and at the moment of recovery.
- **An unsayable state is a design defect,** not a limitation to document. The framework should
  not report a gap in its own vocabulary as a property of the database.
- **Consistency with the operation itself.** Whatever the payload does, it should read the same
  way as the operation whose prior state it records.
- **Checksum blast radius.** Any change to the emitted bytes invalidates every applied migration
  in every installation. Today that set is empty; it will not stay empty.

## Considered Options

### Question A: How the payload says "there was no default"

#### Option A1: An explicit member, `no_default`

The payload gains a boolean, omitted when false. It cannot appear together with `default`.

**Pros**

- Mirrors the operation exactly. `alter_column` already carries both `default` and `drop_default`,
  for precisely this reason: an omitted field means *leave it alone*, so removal needs its own
  instruction. The payload records the same kind of information and now reads the same way.
- The absence is stated by the producer rather than inferred by the consumer, which removes the
  guess entirely. A payload that says nothing about defaults now inverts to an operation that
  changes nothing about defaults — honest, and the conservative direction.
- Makes the operation a genuine involution: reversing a fully recorded `alter_column` twice
  returns the original document exactly, which no other operation in the set can claim.
- Self-describing on the wire. A reader of a recorded document can see what was meant.

**Cons**

- A fourth member on the payload, and a second either-or rule to validate.
- Producers must now say `no_default` explicitly where the old inference silently covered the
  common case.

#### Option A2: Treat a present-but-empty payload as "nothing was set"

JSON distinguishes an absent member from a present empty object, so `"from": {}` could mean
*recorded: the column had no type change, no nullability change, and no default*.

**Pros**

- No new member. The distinction already exists in the encoding and is simply not being used.

**Cons**

- It gives one meaning to a document that is much more likely to be a mistake. An empty object is
  what a careless producer emits, and it would be read as a positive assertion about the schema.
- It does not generalize. An `alter_column` that changes a type and has `"from": {}` would invert
  to an operation requesting no change at all, which is not a valid operation — so the inverse
  would be a document that fails its own validation.
- It says "no default" only by saying "nothing at all", so it cannot express "the type was
  `string(255)` and there was no default".

**Rejected because:** it overloads emptiness to mean a specific assertion, which is exactly the
conflation this decision exists to remove — moved one level down rather than eliminated.

#### Option A3: Make the default member a three-state value, using JSON `null`

`"default": null` would mean "there was none", distinct from an absent member.

**Pros**

- No new member, and the distinction is carried by a value rather than a flag.

**Cons**

- The format states that an optional member is omitted when unset and never emitted as `null`. A
  single exception makes that rule something an implementer has to remember rather than apply.
- Several languages' JSON libraries do not distinguish an absent key from a null one without extra
  care, so this is the encoding most likely to be reproduced incorrectly by a future SDK.

**Rejected because:** it buys the same expressiveness as A1 by weakening a format-wide rule, and
it is the option a cross-language reimplementation is most likely to get wrong.

#### Option A4: Leave it; document the padding requirement

State in the specification that a producer must record an unrelated unchanged member to make such
an operation reversible.

**Pros**

- No change to the format at all.

**Cons**

- The rule is arbitrary and unenforceable, and a producer that follows it is writing a field it
  does not mean in order to work around an encoding gap.
- The inference stays, so the wrong-guess case stays with it.

**Rejected because:** it writes down a workaround instead of removing the need for one. Rejected
on merit — the cost of the alternative is one optional field.

### Question B: Does this bump `irVersion`?

#### Option B1: No bump; `irVersion` 1 freezes at first release

**Pros**

- The new member is omitted when unset, so no document that does not use it changes by a single
  byte. The committed checksum vectors pass unchanged, which is the mechanical demonstration.
- The one genuinely breaking direction — an older engine rejecting an unknown member — has no
  subject. `irVersion` 1 has never been released; there is no older engine anywhere.
- A bump now would spend the mechanism's first use on a change nobody can observe, and teach
  readers that version numbers move for reasons that do not affect them.

**Cons**

- "Unreleased, therefore unfrozen" is a rule that has to be stated, or the next person infers from
  this record that additive changes never bump the version. They will, once there is a release.

#### Option B2: Bump to `irVersion` 2

**Pros**

- Mechanically honest: the set of documents this build produces is not a subset of what a v1 build
  accepts, because unknown members are rejected.
- Exercises the version machinery once before it matters.

**Cons**

- Every consumer of v1 would have to be updated to accept v2, and there are no consumers. It is
  ceremony performed for an empty audience.

**Rejected because:** on timing, not merit. The argument for it is sound and becomes decisive the
moment `irVersion` 1 ships. Until then it has nothing to protect.

## Decision Outcome

**A1 and B1.** Recorded prior state gains an explicit `no_default` marker, which cannot appear
alongside a recorded default. `irVersion` stays 1, and freezes at the first release.

What that commits to:

- **The inverse restores what was recorded and nothing more.** A prior state silent about defaults
  inverts to an operation that leaves the default alone. Inferring an absence from a missing field
  is a guess, and the guess destroys a default the migration never recorded.
- **An empty payload remains "nothing recorded"** and therefore irreversible. That is now the
  honest answer rather than a collision, because the state it used to be confused with is sayable.
- **`alter_column` is an involution.** Reversing a fully recorded one twice returns the original
  document. It is the only operation for which that can hold, because it is the only one whose
  inverse records its own prior state.
- **A separate fix, folded in here because it is the same class of failure:** `drop_column`'s
  recorded definition carries its own name, and nothing checked it against the column being
  dropped. Since the inverse re-adds the recorded definition verbatim, a mismatch restored a
  column that was never dropped while leaving the dropped one gone. The names must now agree.

## Consequences

### Positive

- The common case — setting a default on a column that had none — is reversible without recording
  a field the producer does not mean.
- One guess is removed from the rollback path, and the direction it is removed in is the safe one:
  an incompletely recorded prior state now under-restores rather than over-destroys.
- Reversing twice is now a testable property rather than an accident, and it holds.
- No existing document changes by a byte, demonstrated by the checksum vectors passing unchanged.

### Negative

- Producers must set `no_default` explicitly where the inference used to cover them silently. A
  schema differ will get this right by construction; a hand-written migration will forget, and the
  result is an operation that is reversible but restores less than it could.
- The payload now has two either-or rules — `default` against `no_default` — mirroring the
  operation's own. Symmetry is the point, but it is more surface to reimplement in every SDK.
- The recorded state still cannot express "there was no type" or "nullability was not tracked",
  because neither is a real column state. If a future member has a meaningful absent value, it
  will need the same treatment, and the pattern is now set.

### Neutral

- `from` remains trust rather than proof, unchanged by this decision. Nothing verifies a recorded
  prior definition against the live schema; introspection can close that later.
- The `drop_column` name check rejects only documents that were already going to produce a wrong
  rollback, so it changes no valid document and moves no checksum.

## Notes

Revisit Question B at the first release: from that point an additive change to the operation
payloads is a version bump, because a released engine rejects members it does not know.

The involution property is worth preserving deliberately. It holds only because the inverse
records the values the operation set, and it would be easy to lose in a refactor that treated that
recording as redundant.
