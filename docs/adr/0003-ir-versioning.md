# 0003. Version the operation IR explicitly

## Status

Accepted

## Date

2026-07-27

## Context

[ADR 0001](0001-architecture.md) made a database-neutral operation IR the sole contract between
language SDKs and database drivers. [ADR 0002](0002-runtime-protocol.md) established that SDKs
are separate artefacts — an npm package, a Go module — released on their own cadence, spawned
as child processes by the engine.

That combination guarantees version skew. A user upgrades `@migr/sdk` but not the CLI, or
installs the CLI from a package manager while the SDK comes from a lockfile six months old. The
engine and the SDK that produced a migration's IR are, in general, not the same age.

The question is what happens when they disagree, and the answer matters more here than in most
systems: the artefact being exchanged describes irreversible changes to production data. The
failure mode to design against is not an error — it is an apparent success that did less than
the migration asked for.

## Decision Drivers

- **Mixed versions must fail loudly.** Silent partial application is unacceptable; a migration
  either does what it says or refuses.
- **Adding an operation must not silently break a driver.** A new operation reaching a driver
  that has never heard of it must be a build-time or validation-time failure, not a runtime
  surprise.
- **Applied migrations are immutable.** Their recorded meaning must not drift underneath them.
- **The compatibility signal must be independent of release cadence.** Users need to reason
  about SDK/engine compatibility without decoding the CLI's release history.

## Considered Options

### Option 1: A single monotonic `irVersion` on every IR document

Every emitted document carries a version field. The engine compares it against what it supports
and refuses anything it does not recognise, naming both the expected and received versions.

**Pros**

- One negotiation point, checked once at handshake.
- Unknown versions produce an immediate, actionable error instead of degraded behaviour.
- Cheap to implement and impossible to forget, because the field is required.

**Cons**

- Coarse: any breaking change bumps the version for everything, even operations that did not
  change.

### Option 2: No version; rely on structural compatibility

Decode the fields you understand and ignore anything unrecognised, the way many JSON APIs
handle forward compatibility.

**Pros**

- Simplest possible scheme; nothing to maintain.
- Forward-compatible by construction — old readers keep working against new writers.

**Cons**

- An older engine receiving a newer operation would skip it silently. The migration reports
  success having never added the column.

**Rejected because:** it converts a version mismatch into silent partial application. A
migration that claims to have succeeded while having done less than it said is precisely the
failure class this project exists to prevent, and it is undetectable at the moment it happens —
the damage surfaces later, as application errors against a schema that is not what anyone
believes it to be.

### Option 3: Use the framework's semantic version as the compatibility signal

No separate IR version; SDK and engine compare their release versions.

**Pros**

- One number for users to reason about.
- No additional concept to document.

**Cons**

- Couples IR stability to release cadence. A patch release could not carry an IR correction
  without either breaking semver or lying about compatibility.
- Every release implies a possible IR change, so compatibility can only be checked
  pessimistically.
- Third-party SDKs would have to track the framework's release history rather than a contract.

**Rejected because:** the IR changes far less often than the framework does. Tying them forces
either needless incompatibility on ordinary releases or dishonest version claims.

### Option 4: Per-operation versioning

Each operation carries its own version; compatibility is negotiated per operation.

**Pros**

- Fine-grained: a change to one operation does not invalidate the rest.
- Long-lived documents stay valid longer.

**Cons**

- Turns one compatibility question into a matrix of them, across every operation, SDK, and
  driver.
- Substantially harder to reason about and to test; the conformance suite would have to cover
  version combinations rather than operations.

**Rejected because:** the cost is paid immediately and the benefit only arrives once the
operation set is large, stable, and evolving unevenly. Rejected on timing, not on merit — worth
revisiting if the v1 operation set proves to churn in isolated pieces.

### Option 5: Capability negotiation instead of a version

At handshake the engine and SDK exchange the set of operations each supports, and the SDK
adapts.

**Pros**

- Most flexible; no version arithmetic.
- Allows graceful degradation to what both sides understand.

**Cons**

- Graceful degradation is the wrong behaviour for this artefact.
- Considerably more moving parts in the handshake.

**Rejected because:** "adapt to what the other side supports" is Option 2's failure wearing a
more sophisticated outfit. A migration that silently does less than it asked for is a data
correctness bug regardless of how politely it was negotiated. Capability exchange can be layered
on later for reporting — telling a user *why* a version is incompatible — without giving it
authority to proceed anyway.

## Decision Outcome

**Option 1.** Every IR document carries `irVersion`. The engine rejects unknown versions with an
error naming both the expected and the received version.

Two rules give the version something to protect:

**Adding an operation is a five-part change,** all in the same change set: the type plus a JSON
round-trip test; a compile implementation in *every* registered driver, or an explicit
`Unsupported` capability with a stated reason; a golden file per driver; a defined reverse
operation, or an explicit irreversible marker; and a specification entry. A partially added
operation cannot exist.

**Every SDK must emit byte-identical IR** for the same fixture migrations. This is enforced by a
shared conformance suite and is the mechanical proof of the language-independence claim from
[ADR 0001](0001-architecture.md) — if two SDKs produce different IR for the same logical
migration, the central claim is false and the suite says so.

## Consequences

### Positive

- Version skew produces one clear error at handshake instead of undefined behaviour deep in an
  apply.
- The five-part rule makes it structurally impossible for a driver to silently lack support for
  an operation that SDKs can emit.
- Third-party SDKs and drivers have a single, stable contract to target.

### Negative

- **A version bump interacts badly with checksums, and this needs a documented upgrade path
  before the first one happens.** Migration checksums cover the normalised source *and* the
  emitted IR, so that modifying an applied migration is detectable. If an `irVersion` bump
  changes the IR emitted for unchanged source, every applied migration's checksum changes and
  drift detection fires across every existing installation. The alternatives — excluding IR
  from the checksum, or special-casing version boundaries — each weaken a guarantee. Recorded
  here so the first bump is designed rather than discovered.
- Coarse granularity: an unrelated operation's breaking change bumps the version for everything.
- Every breaking change carries mandatory work across the specification and every SDK's
  conformance run, which is friction by design and will feel like friction.

### Neutral

- Version checking happens once per run at handshake, so it costs nothing per operation.
- The version is independent of the framework's release version; the two need not move together.

## Notes

Revisit Option 4 if the operation set grows large and churns unevenly, and Option 5 as a
reporting layer — improving the *explanation* of an incompatibility — but not as a mechanism
for proceeding despite one.
