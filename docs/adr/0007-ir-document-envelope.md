# 0007. A versioned document envelope, outside the checksum boundary

## Status

Accepted

## Date

2026-07-27

## Context

[ADR 0003](0003-ir-versioning.md) decided that every IR document carries an `irVersion` and that
the engine refuses anything it does not recognise. It did not say what a document *is*, because
at the time nothing had been built. [ADR 0006](0006-ir-operation-shape.md) then defined the
operations, and the natural encoding of an ordered list of them is a JSON array — which cannot
carry a version field.

So implementing ADR 0003 requires an envelope, and choosing its shape raises three further
questions that ADR 0003 left open: what goes in it, what the version key is called, and — the one
that matters most — whether the envelope is inside or outside the bytes a migration checksum
covers.

That last question is not new. ADR 0003 recorded it as a negative consequence it could not
resolve:

> A version bump interacts badly with checksums, and this needs a documented upgrade path before
> the first one happens. […] If an `irVersion` bump changes the IR emitted for unchanged source,
> every applied migration's checksum changes and drift detection fires across every existing
> installation.

This is the first point at which that can be designed rather than discovered, because the
envelope is the thing that decides it.

## Decision Drivers

- **A version must be impossible to omit.** A guarantee that depends on remembering to attach
  something is not a guarantee.
- **The error must name the actual problem.** A version mismatch usually surfaces as an unknown
  operation, which is a symptom, not a cause.
- **Checksum stability.** Applied migrations are immutable and drift is a hard error, so anything
  that moves a checksum without the migration changing is an incident across every installation.
- **Byte-identical IR across SDKs.** Every field in the envelope is a field two SDKs must agree
  on exactly.

## Considered Options

### Question A: What is the envelope, and what does it contain?

#### Option A1: `{irVersion, operations}`, and a Recording marshals to it

The recorded operations of one migration execution *are* the document.

**Pros**

- "Every emitted IR document carries `irVersion`" holds by construction: there is no code path
  that emits operations without one.
- Smallest possible surface for two SDKs to agree on byte-for-byte.
- Nothing in the envelope is machine- or SDK-specific, so it is stable across a laptop and CI.

**Cons**

- Changes what a recording serializes to, so anything that assumed a bare array must be updated.

#### Option A2: A separate document type; recordings stay bare arrays

**Pros**

- Distinguishes the callback payload — `host.schema.apply` carries operations, and the version is
  negotiated once at handshake — from the document at rest.

**Cons**

- Emitting an unversioned array remains possible, so the guarantee is a convention.

**Rejected because:** the task is to make unversioned emission impossible, and an option that
leaves the door open achieves that only by discipline. The payload/document distinction is real
but is better expressed by exposing the operations explicitly than by leaving the default unsafe.

#### Option A3: Also carry migration ID, direction, or generator information

**Pros**

- A document written to disk explains itself without surrounding context.
- Generator information would make diagnosing version skew concrete rather than inferred.

**Cons**

- The migration ID and direction are already named by `migration.execute` and held by the
  recording; putting them in the document too creates fields that can disagree with their own
  container.
- An SDK name and version change on every SDK release, so they would have to be excluded from
  the checksum — and ADR 0003 rejected field-aware checksum exclusions as a second specification
  that silently rots. They would also differ between the Go and TypeScript SDKs for the same
  migration, breaking byte-identical IR outright.

**Rejected because:** each addition either duplicates something that already has an owner or
introduces a field that cannot be hashed and cannot be identical across SDKs.

### Question B: `irVersion` or `ir_version`?

Every other key in the format is snake_case — `primary_key`, `references_table`, `on_delete`.

#### Option B1: `irVersion`, as the existing documents state

**Pros**

- [ADR 0003](0003-ir-versioning.md)'s Decision Outcome names it this way, as does the project's
  engineering contract.
- ADRs are immutable historical records; renaming the key would leave a committed document
  describing something that does not exist.

**Cons**

- One camelCase key in a snake_case format, which a third-party implementer must remember.

#### Option B2: `ir_version`, consistent with every other key

**Pros**

- One casing convention across the whole wire format, with no exception to document.
- Free to change now; permanent once v1 ships.

**Cons**

- Requires amending the engineering contract and leaves [ADR 0003](0003-ir-versioning.md) naming
  a key that no longer exists.

**Rejected because:** consistency is worth real money, but not at the cost of a committed record
that lies. The exception is documented in the package doc and the specification, so an
implementer is told rather than left to discover it. Rejected on the cost of the correction, not
on the merits of consistency — if the IR is ever versioned to 2 for other reasons, renaming is
worth reconsidering as part of that break.

### Question C: Is the envelope inside or outside the checksum?

#### Option C1: Hash the operations; the envelope sits outside

**Pros**

- A version bump that reshapes no operation leaves every checksum untouched, so no drift storm.
- A bump that *does* reshape operations still changes checksums, which is correct — the emitted
  IR really did change.
- Narrows ADR 0003's recorded problem to the cases where it is genuinely unavoidable.

**Cons**

- The checksummed bytes are no longer literally "everything that was emitted", so the boundary
  has to be stated rather than assumed.

#### Option C2: Hash the whole document

**Pros**

- One rule, no boundary to explain.

**Cons**

- Every version bump becomes a migration-wide incident for every installation, including a bump
  that changes nothing about any operation.

**Rejected because:** it makes the outcome ADR 0003 warned about unavoidable rather than
occasional, in exchange for a simpler sentence.

## Decision Outcome

**A1, B1, and C1.** The document is `{irVersion, operations}`; a recording marshals to it; the
key keeps the spelling the existing records use; and the hashable unit is the operations, not
the envelope.

What that commits to:

- **The version is checked before the operations are decoded.** A future document very likely
  contains an operation this build has never heard of, and decoding first reports that unknown
  operation — accurate, useless, and pointing at the wrong layer. Version-first says the
  `irVersion` is unreadable, which tells the operator to upgrade the CLI.
- **A missing `irVersion` is its own error and is never defaulted.** Assuming a version is the
  lenient behaviour ADR 0003 rejected: it is precisely how a mismatch becomes a migration that
  reports success having done less than it said.
- **The supported set is a set.** A build declares which versions it reads, so a future engine
  that accepts 1 and 2 needs a longer list rather than a new shape.
- **An unstamped document refuses to marshal** rather than emitting `"irVersion":0`, the same
  stance `schema.Type` takes toward its zero value.

## Consequences

### Positive

- Nothing can emit operations without a version, so the ADR 0003 guarantee is structural.
- A cosmetic version bump no longer invalidates every applied migration's checksum, which was
  ADR 0003's most uncomfortable open consequence.
- Version skew produces one actionable error at the boundary instead of a confusing one deeper in.

### Negative

- The checksum boundary must be stated and kept: "hash the operations, not the document" is a
  rule someone can forget, and forgetting it reintroduces the drift storm silently.
- `irVersion` remains an acknowledged inconsistency in an otherwise snake_case format.
- Anything that previously read a bare array of operations must now read a document, including
  fixtures and any tooling written against the earlier shape.

### Neutral

- The distinction between the callback payload (operations) and the document (versioned) survives
  in the API rather than in the serialization: `Operations()` gives one, `Document()` the other.
- A version bump that genuinely reshapes operations still changes checksums. That is not a gap
  in this decision; it is the honest signal that the emitted IR changed.

## Notes

Revisit the key spelling if the IR is ever versioned to 2 for independent reasons — a break that
is already happening is the cheapest moment to also fix the casing.
