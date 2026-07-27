# 0012. Capability resolution: one rule per operation, resolved against one server

## Status

Accepted

## Date

2026-07-28

## Context

[ADR 0004](0004-safety-model.md) decided that the capability model carries two independent axes.
*Support* answers whether an operation can run at all — `Native`, `Emulated` with a named
strategy, or `Unsupported` with a human-readable reason. *Lock impact* separately answers what the
operation costs a live database — `LockFree`, `BriefExclusive`, `LongExclusive`, or
`TableRewrite`. It also rejected static per-driver lock tables, because `ADD COLUMN … DEFAULT`
rewrites the whole table on PostgreSQL 10 and is instant from 11 onward, and because MySQL 8's
choice between `INSTANT`, `INPLACE`, and `COPY` depends on the column type and the specific
alteration rather than on the operation's name.

That settled *what* the model must express. It did not settle the shape of the Go API, and the
shape is where the difficulty is, because two requirements pull in opposite directions:

- The lock verdict **cannot come from a lookup table keyed by operation name.** MySQL's applicable
  algorithm depends on the column type and the alteration; PostgreSQL's depends on the server
  version; SQLite gained native `DROP COLUMN` in 3.35 and emulates it with a full table rebuild
  before that. Every one of those is a property of a *specific operation against a specific
  server*, not of an operation kind.
- The model must nevertheless be **enumerable per operation kind.** The driver conformance suite
  walks the full operation set and asserts each one is either compilable or refused with a reason,
  with no third possibility and no per-driver exemptions. Published documentation carries a
  per-driver capability and lock-impact table. Neither is possible against an opaque function.

A third requirement constrains where the answer may be computed. Plan output prints the compiled
statements for a migration *without touching the database*, so resolving a capability must not
perform I/O. But the answer depends on the connected server's version, which is only knowable by
having connected. Those are reconcilable only if the version enters the model at one specific
moment.

Finally, the model has to be usable **before it is filled in.** Honest lock classification is a
later milestone that depends on all three drivers existing; until then every driver reports its
lock impact as unknown. A model that cannot represent "unknown" would force drivers to invent an
answer, and an invented safety verdict is the failure ADR 0004 exists to prevent. A model that
represents "unknown" badly is worse: if an unresolved verdict compares as cheap, a CI gate that is
supposed to block dangerous migrations passes everything.

## Decision Drivers

- **A wrong answer is worse than no answer.** A safety verdict is acted upon. Every path that can
  fail must fail toward refusing, never toward approving.
- **Resolution must be pure.** Plan output, golden tests, and validation all walk operations
  without a database in hand at the point of the walk.
- **The set must be checkable for completeness.** "No third possibility" is only enforceable if
  something can enumerate what a driver declared.
- **A verdict must survive the wire.** Capability answers are served to language SDKs over
  JSON-RPC and rendered into published tables, so their spellings are a compatibility surface.
- **Retrofit cost.** ADR 0004 built this second axis early precisely because adding one to a
  shipped model means reopening every driver. The same argument applies to the model's own shape.

## Considered Options

Five questions are decided here. A and B are the shape; C and D are how the model behaves at its
edges; E is a piece of ownership that was already promised and is collected now.

### Question A: What does a capability resolve against?

#### Option A1: A verdict table keyed by operation kind

`map[Kind]Verdict` per driver.

**Pros**

- Trivial to write, read, enumerate, print, serialize, and diff.
- Completeness is a length check.

**Cons**

- Cannot express any of the three motivating cases, all of which depend on the operation's
  payload or the server's version rather than on the kind.

**Rejected because:** ADR 0004 rejected it once already, on the support axis's behalf; nothing has
changed except that the lock axis makes it more obviously wrong. A table would answer "instant"
for a MySQL column change whose actual algorithm is `COPY`.

#### Option A2: A method taking a context and a connection

`Resolve(ctx, conn, op) (Verdict, error)`.

**Pros**

- The server is available at the moment of the question, so nothing must be cached.
- Naturally handles a server whose capabilities change while connected.

**Cons**

- Puts I/O on a path walked once per statement in plan output, which is required to run without
  touching the database.
- Re-derives the server version on every call, or grows a cache and the invalidation question
  that comes with one.
- Returns an error, so every caller must decide what a capability lookup failure means, and the
  safe answer — refuse — is one every caller can get wrong differently.

**Rejected because:** it makes the pure path impure to serve a case that does not arise. A
migration run holds one connection to one server for its duration; the version does not change
underneath it.

#### Option A3: A per-kind rule, taking the whole operation

`map[Kind]Rule` where `Rule` is `func(op Operation) Verdict`. The map is keyed by kind, so the set
is enumerable and its completeness is checkable at construction; the rule receives the concrete
operation, so it can branch on a column type, a default, or anything else in the payload. A
helper wraps a constant verdict for the ops whose answer never varies, so the common case still
reads as a table.

**Pros**

- Satisfies both directions: enumerable per kind, dynamic per operation.
- Completeness is a construction-time error naming the missing operations, so a driver that adds
  an operation and forgets to classify it fails in its own unit test rather than in production.
- No context, no connection, no error — resolution is a pure function call.

**Cons**

- A rule is a function, so a set cannot be compared, printed, or serialized as a whole.
- Completeness is checked at run time by the constructor, not by the compiler; the exhaustiveness
  linter cannot see through a map literal.

### Question B: Where does the server version enter?

#### Option B1: A parameter on every query

`Resolve(op, serverVersion)`.

**Pros**

- Explicit at every call site; nothing is hidden in construction.
- One set answers for any server.

**Cons**

- Every caller must carry a version it has no other use for.
- A caller *can* pass a version that is not the connected server's, and the result is a confident
  verdict about a database nobody is talking to.

**Rejected because:** it makes the most dangerous possible mistake — classifying against the wrong
server — a type-correct program. This is the same defect [ADR 0011](0011-driver-contract.md) found
in the original driver contract, where a stateless driver taking a connection per call made
`pgDriver.Introspect(ctx, mysqlConn)` compile.

#### Option B2: Resolved lazily, inside the lookup

The set holds a connection and detects the version on first use.

**Pros**

- Nothing to thread through; the set is self-sufficient.

**Cons**

- Reintroduces I/O and an error into the resolution path, which is Question A2's problem arriving
  by a different door.

**Rejected because:** of A2's reason. A lazily impure function is still impure.

#### Option B3: Baked in at construction

The driver detects the server version when it opens the connection and builds a capability set
already specialised to that server. The set is obtained *from* the connection, so it cannot exist
without one, and there is no version parameter anywhere.

**Pros**

- "Per server version" is satisfied structurally rather than by discipline: there is no way to
  hold a set that is not bound to exactly one server.
- Resolution stays a pure function.
- Matches the contract [ADR 0011](0011-driver-contract.md) already recorded, where capabilities
  are a method on the session rather than on the driver.

**Cons**

- A set is not reusable across connections, and a long-lived process talking to several databases
  holds several sets. That is correct, and it is also more objects.

### Question C: How does an unresolved lock verdict compare against a threshold?

A configured ceiling gates migrations: a CI run fails when an operation's lock impact exceeds it.
Until lock classification lands, every verdict is unresolved, so the ordering of "unknown" decides
whether that gate means anything.

#### Option C1: Unknown ranks below the cheapest real verdict

**Pros**

- Nothing is blocked while the classification work is outstanding.

**Cons**

- The gate approves every operation, including the ones that would take production down.

**Rejected because:** it fails open. A gate that passes an unclassified table rewrite is worse
than no gate, because its presence is taken as evidence the migration was checked.

#### Option C2: Unknown ranks above the most disruptive real verdict

**Pros**

- Maximally cautious; nothing unclassified can ever pass.

**Cons**

- No ceiling accepts it, including the loosest one, so an operator who has deliberately decided to
  accept any lock cost still cannot proceed.

**Rejected because:** it removes the opt-out. A safety mechanism with no way to say "I accept this
risk" is one people disable entirely, and then the mechanism protects nothing.

#### Option C3: Unknown is exempt, and warns instead

**Pros**

- Visible without blocking.

**Cons**

- Advisory only.

**Rejected because:** ADR 0004 rejected exactly this reasoning when it declined to make lock
safety a lint rule — a warning that can be ignored is not a safety mechanism, and one that fires
on every operation until the classification work lands is ignored immediately.

#### Option C4: Unknown ranks equal to the most disruptive real verdict

An unresolved verdict is treated as a table rewrite for every comparison, while still displaying
as "unknown" wherever a verdict is shown. It is a legal verdict and never a legal ceiling.

**Pros**

- Satisfies the requirement that an undeterminable case reports the pessimistic verdict, without
  making drivers launder honesty into a specific claim they cannot support.
- Preserves the opt-out: the loosest ceiling accepts it, every stricter one refuses.
- Distinguishes "measured, and it rewrites the table" from "not yet classified" in output, which
  is exactly the distinction the classification work needs in order to find its own remaining
  work.

**Cons**

- Two verdicts share a rank, so the ordering is not a bijection and the enum cannot simply be
  indexed.
- Until classification lands, the default ceiling refuses every operation once the gate is wired.
  That is the correct direction, but it means the gate is unusable rather than merely noisy.

### Question D: Does the zero value carry meaning?

#### Option D1: The zero value is the safe verdict

Spell the constants so that the empty string means unsupported on one axis and unknown on the
other, so a driver that forgets to set a field gets the cautious answer for free. This is the
reasoning behind the transaction opt-out flag on a compiled statement, where the zero value is
deliberately the safe one.

**Pros**

- A forgotten field is safe rather than wrong.
- No validation needed to catch the omission.

**Cons**

- Config already distinguishes "the operator set no ceiling" from "the operator set one" by
  testing the field against the empty string. An empty-valued constant collapses those two states,
  so a defaulting pass could not tell an unconfigured ceiling from a configured one.
- A named string type decodes from YAML and JSON by direct assignment, so the word `unknown`
  arriving on the wire would produce a value that is not the constant. Preventing that means
  giving both types text marshalling for no reason other than to hide the zero value.
- Every other enumeration in the codebase — operation kinds, logical type kinds, lock strategies —
  uses non-empty constants and treats the empty string as invalid.

**Rejected because:** it buys safety that fail-closed comparison already provides, and pays for it
with two spellings of one value at every serialization boundary. The transaction flag is not a
precedent here: it is a boolean with no third state, where the only choice is which way round to
phrase it.

#### Option D2: Non-empty constants, and every comparison fails closed

The zero value of either axis is invalid. Nothing approves on it: an unrecognised lock impact
satisfies no ceiling, a verdict that fails its own validation resolves to a refusal, and a
capability set that was never built refuses every operation rather than answering for it.

**Pros**

- The safety property is a property of the comparisons, which are tested directly, rather than of
  a spelling that must be preserved.
- One spelling per value, everywhere.

**Cons**

- Requires validation to catch an omitted field, where D1 would have absorbed it silently.

### Question E: Which package owns the lock-impact vocabulary?

The four verdict names already exist in the configuration package, which needed them to validate
its ceiling setting before a capability package existed, with a note recording that ownership
would move here.

#### Option E1: Keep both copies

**Pros**

- Configuration stays free of dependencies on model packages.

**Cons**

- Two identical enumerations for one concept, which can drift, and a promise left outstanding.

**Rejected because:** the duplicate was explicitly temporary. Keeping it means every later reader
must discover which of the two is authoritative.

#### Option E2: The capability package owns it; configuration imports it

**Pros**

- One vocabulary. The ceiling an operator configures is the same type a driver reports.
- Configuration can validate against the *ceiling* set specifically, so `unknown` is rejected in a
  setting where it is meaningless.

**Cons**

- Configuration gains a dependency it did not have.

## Decision Outcome

**A3, B3, C4, D2, and E2.**

What that commits to:

- **A capability set is a map from operation kind to a rule, and a rule takes the whole
  operation.** The map is what makes the set enumerable and completeness checkable; the rule is
  what makes the verdict depend on a column type or a default rather than on a name. Constructing
  a set that does not answer for every operation is an error that names the ones it missed.
- **A set is obtained from a connection and is bound to that server for its lifetime.** There is
  no version parameter and no way to hold a set detached from the server it describes. Resolution
  performs no I/O and returns no error.
- **An unresolved lock verdict is displayed as unknown and compared as a table rewrite.** It is a
  verdict a driver may report and never a ceiling an operator may configure.
- **Every edge fails toward refusing.** An unrecognised lock impact satisfies no ceiling. A rule
  returning a verdict that contradicts itself — emulated with no strategy named, unsupported with
  no reason given — resolves to a refusal that names the driver and the contradiction rather than
  being passed along. A set that was never constructed refuses every operation.
- **The two axes are independent and stay independent.** A verdict carries both, and no
  combination of a support answer and a lock answer is disallowed except the one that is
  meaningless: an operation that cannot run has no lock cost to report.

## Consequences

### Positive

- The conformance requirement that every operation is either compilable or refused with a reason
  becomes mechanically checkable, because a set can be asked what it declared for each operation.
- A driver's capability declaration reads as a table for the operations whose answer is constant,
  and drops to a function only where the answer genuinely varies — so the exceptional cases are
  visible as exceptions.
- Adding a further axis later — whether an engine has transactional DDL, or the numeric limits it
  imposes on a logical type — is a field on a struct rather than a method on an interface, so it
  does not reopen any driver. This is ADR 0004's retrofit-cost argument applied to the model that
  argument created.
- Because resolution is pure and the server version is a construction parameter, the
  version-dependent cases can be tested exhaustively with no database: two sets built for two
  server versions, asked the same question, must disagree.

### Negative

- A set cannot be compared, printed, or serialized as a whole, because a rule is a function. The
  per-operation callback that serves capability answers to language SDKs asks about one operation
  at a time, which is unaffected; the published per-driver tables need a representative operation
  for each kind, which is a fixture the golden-file harness and the conformance suite must build
  regardless.
- Completeness is enforced by the constructor at run time, not by the compiler. A driver that
  never calls the constructor in a test would not discover a missing operation until it ran.
- Until lock classification lands, every driver reports unknown, so a configured ceiling of
  anything short of a table rewrite refuses every operation once the gate exists. The gate is
  therefore deliberately unusable rather than merely uninformative during that period.
- Configuration is no longer a leaf package. It now depends on the capability model to name the
  ceiling it validates.

### Neutral

- The capability package must never import the driver package. The driver contract names a
  capability set as a return type, so the dependency runs one way only, and the other direction is
  an import cycle. This is the same constraint [ADR 0011](0011-driver-contract.md) recorded for
  the history store, and it is not mechanically enforced: the import guard denies concrete driver
  packages, not the contract package they share.
- Two verdicts share a severity rank, so the ordering is a total preorder rather than a total
  order. Nothing needs a strict order — the only comparison is against a ceiling.

## Notes

The distinction that makes Question C tractable is that **"unknown" names the driver's confidence,
not a different severity.** Treating it as a fifth rank forces a choice between failing open and
removing the opt-out; treating it as a label attached to the pessimistic rank avoids both. If a
future decision has the same shape — a value that must be displayed distinctly but must not behave
distinctly — this is the precedent.

The rejected Option B1 is worth remembering for its own sake. It is the second time a contract in
this project was nearly written so that pairing a description with the wrong subject would compile;
[ADR 0011](0011-driver-contract.md) records the first. Both were caught by asking what the worst
type-correct program the signature permits would do.
