# 0009. A migration checksum is a scheme-versioned pair: normalized source and emitted IR

## Status

Accepted

## Date

2026-07-27

## Context

Applied migrations are immutable. A migration that has run against a database is a historical
fact, and editing its file afterwards means the recorded history no longer describes what the
database actually contains. The framework detects that by hashing each migration when it is
applied and comparing on every subsequent run. A mismatch is a hard error with a dedicated exit
code, and there is deliberately no flag that repairs it.

That design makes a false positive as expensive as a missed one. An operator facing a mismatch
they did not cause has no supported way out, so any hash that moves for a reason other than
someone editing the file is a fleet-wide incident rather than an inconvenience.

Two inputs are available to hash: the migration's source text, and the operations it emitted
when it ran. Choosing what to do with them raises four questions that have to be answered
together, because each constrains the others.

**What "normalized source" means.** Some byte differences are not edits. Git rewrites line
endings on checkout and on checkin, so one commit is CRLF on a Windows machine and LF on a Linux
CI runner. Other differences — a reindent, a trailing space, a byte order mark — look similar
but are not the same kind of thing.

**Whether the emitted IR can decide drift.** It cannot, and this is the finding that shapes
everything else. Migrations may branch on live query results, so planning a migration again
months later, against a database whose shape has moved on, can legitimately emit different
operations from the apply that was recorded. A mismatch in that half has three possible causes —
the file changed, the database changed, the SDK changed — and only the first is drift. Nor is
the half even recomputable in the ordinary case: doing so requires spawning a language runtime
and issuing live queries, which `migr status` on a machine with the binary but no toolchain
cannot do.

Worth stating plainly, because it revises an earlier assumption: [ADR
0003](0003-ir-versioning.md) justified hashing the IR as a way to make a modified migration
detectable. On inspection the source alone already achieves that. The IR half's value is
different in kind — it is a record of what one execution produced.

**How two inputs become one recorded value.** Concatenating them before hashing is ambiguous:
`("a", "bc")` and `("ab", "c")` produce the same byte stream, and the person who controls the
source controls both halves.

**Whether the recorded value describes itself.** A stored checksum outlives the binary that
wrote it. Every future language SDK must reproduce it bit for bit, and any change to how it is
computed changes every value at once.

## Decision Drivers

- **No false drift.** A hash that moves without the migration changing is an incident with no
  supported recovery.
- **Offline verification.** Deciding whether a migration was modified must not require a
  database, a language toolchain, or a subprocess.
- **Fewest degrees of freedom.** Every clause is something a future SDK must reimplement
  identically forever. Specification surface is the running cost.
- **A changed construction must be visible.** If the way a checksum is computed ever changes,
  the difference must be distinguishable from someone editing a file.
- **Reimplementability.** A second implementation should be derivable from prose, without
  reading the Go.

## Considered Options

### Question A: What normalization covers

#### Option A1: Only what version control can change without a visible diff

Rewrite `\r\n` and lone `\r` to `\n`. Nothing else.

**Pros**

- Fixes the one case where identical commits produce different bytes through no action of the
  author, on a mechanism (`core.autocrlf`, `text=auto`) that is on by default for many users.
- The criterion decides every future case with one sentence, rather than accumulating clauses.
- Two clauses is a specification a second SDK reproduces in three lines.

**Cons**

- The boundary has to be stated, because "normalized" invites the assumption that formatting is
  normalized too.

#### Option A2: Also strip a UTF-8 byte order mark

**Pros**

- A BOM cannot change the emitted IR — the Go scanner and the TypeScript scanner both discard it
  before the first token — so a BOM-only change moves the source digest while the IR digest
  proves nothing of substance changed.
- One person opening a file in an editor that adds a BOM otherwise causes a fleet-wide failure.

**Cons**

- Git never adds or removes a BOM, so a BOM appearing is a committed diff that someone can see
  and revert. It is a rewrite by the only test that distinguishes cases cleanly.

**Rejected because:** the criterion that decides A1 is "can version control change this
silently", and a BOM fails it. Adopting the alternative criterion — "can this change the emitted
IR" — would also excuse comment edits and formatting runs, which is precisely the leniency this
mechanism exists to refuse.

#### Option A3: Normalize nothing; hash raw bytes

**Pros**

- The smallest possible specification. No clause to reimplement, no boundary to explain.

**Cons**

- Mixed-platform teams get drift on every migration, correctable only by getting every developer
  to configure git identically.

**Rejected because:** it converts a framework guarantee into operator discipline, for a case the
framework can settle exactly.

#### Option A4: Normalize aggressively — trim whitespace, canonicalize formatting

**Pros**

- A formatting-only change stops causing drift, which is arguably not a semantic change.

**Cons**

- "Semantic" is undecidable without parsing every supported language, and every parser is another
  cross-SDK specification that must agree exactly.

**Rejected because:** it answers a question nobody asked at the cost of the one property that
makes the mechanism trustworthy. A rewrite is a rewrite.

### Question B: One value or two halves

#### Option B1: Two digests, both recorded

**Pros**

- Drift is decided on the source half: read the file, normalize, hash once. No runtime, no
  database, no live queries, and no false positive from a migration that branched differently.
- The IR half keeps a job it can actually do — recording what one execution emitted, which is
  what a cross-SDK conformance suite compares and what proves a plan matches its apply.
- An error can say which half moved, so "you edited the file" and "the emitted operations
  differ" are distinguishable rather than two opaque strings.

**Cons**

- 135 characters recorded instead of 71.
- Two values invite the assumption that both are compared, when only one decides drift.

#### Option B2: One opaque composite over both inputs

**Pros**

- Half the length, and one rule: recompute, compare.

**Cons**

- Resolving whether a migration is modified requires recomputing the IR half, which requires a
  language runtime per migration and live database reads — and then still misreports a legitimate
  branch difference as drift.

**Rejected because:** it makes the cheap, exact check impossible in order to save 64 characters,
and the check it leaves behind reports failures the operator did not cause and cannot repair.

#### Option B3: Hash only the source; do not record the IR at all

**Pros**

- The smallest honest thing that detects modification, since the source digest is what decides
  drift regardless.

**Cons**

- Discards the record of what was actually emitted, which is the only evidence available months
  later about what a migration did, and the natural anchor for proving two SDKs emit identical
  IR for the same migration.

**Rejected because:** the IR half costs one hash of bytes already in memory, and it is
information that cannot be reconstructed after the fact. Rejected on value, not on cost.

### Question C: How the halves are combined

#### Option C1: Pair the two hex digests in one self-delimiting string

**Pros**

- Unambiguous by construction rather than by argument: fixed-width hex fields and a separator
  that cannot occur inside hex.
- Both halves are recoverable from a recorded value, which Question B requires.
- A second implementation needs SHA-256, hex, a replace, and a join — no byte concatenation, no
  domain-separation constant, and no chance of the most common reimplementation error in nested
  schemes, hashing the hex text of a digest where the raw bytes were meant.

**Cons**

- Longer than a single digest, and the format is now a parsing surface with its own rejection
  rules.

#### Option C2: Hash the concatenation of both inputs

**Pros**

- One digest, one hash call.

**Cons**

- Boundary-ambiguous: different input pairs produce identical byte streams.

**Rejected because:** ambiguity is free to eliminate and expensive to discover.

#### Option C3: Hash the two sub-digests together into a composite

**Pros**

- Unambiguous, since every input to the outer hash is fixed-width. Well-precedented in content
  addressing.

**Cons**

- Produces one opaque value, which is Option B2 by another route.

**Rejected because:** the construction is sound but delivers the shape Question B rejected. It
would be the right answer if a single 64-character value were a requirement.

#### Option C4: Keyed hashing (HMAC)

**Pros**

- Prevents an attacker from computing a valid checksum for altered content.

**Cons**

- There is nowhere to keep a key. It would have to live beside the checksums it protects, in a
  table that anyone able to alter history rows already controls.

**Rejected because:** it models a threat this mechanism does not address. Checksums detect
accidental modification; an attacker with write access to the history table defeats any scheme
whose secret is stored next to it.

### Question D: Whether the recorded value names its own scheme

#### Option D1: A scheme tag naming the whole construction (`migr1`)

**Pros**

- Versions everything that determines the value — algorithm, normalization rule, input set,
  composition — as one unit, which is the only granularity at which the question "can this build
  compare against this value" has an answer.
- Makes an upgrade path possible: a later build verifies old rows under the scheme they were
  written with and writes new rows under the current one, without ever rewriting a recorded
  value.
- An unrecognized scheme is a distinct, actionable error — upgrade the CLI — rather than a
  mismatch.

**Cons**

- A reader cannot tell which hash function produced the value without consulting a specification.
- A fourth version line in a project that already has three.

#### Option D2: An algorithm tag (`sha256`)

**Pros**

- Immediately legible, and conventional in content-addressing formats.

**Cons**

- Names the least interesting variable. Changing the normalization rule, adding an input, or
  reordering the halves each keeps SHA-256 while changing every value.

**Rejected because:** a tag that stays true while the values become incomparable is worse than no
tag at all, since its only function is to invite a comparison that is no longer valid. Go's
module hashes use `h1:` for this reason and decline to name SHA-256 despite using it.

#### Option D3: Bare hex, no tag

**Pros**

- Shortest.

**Cons**

- A change in construction becomes indistinguishable from every operator simultaneously editing
  every migration.

**Rejected because:** it makes the one failure mode with no recovery path also undiagnosable.

## Decision Outcome

**A1, B1, C1, and D1.** A checksum is:

```
migr1:<64 lowercase hex of the normalized source>:<64 lowercase hex of the emitted IR>
```

where normalization rewrites `\r\n` and lone `\r` to `\n` and changes nothing else, and the
emitted IR is the bare operations array — the hashable unit [ADR
0007](0007-ir-document-envelope.md) established, with the version envelope outside it.

What that commits to:

- **Drift is decided on the source half.** The IR half is recorded, not compared, for the reason
  in Context.
- **Parsing accepts exactly one spelling.** Lowercase hex only, exact lengths, exact separator
  count. Hex decoders accept uppercase, so a lenient parser would put two spellings of one value
  into one column, where they compare unequal and read as drift forever after.
- **An unknown scheme and a malformed value are different errors.** One means upgrade the CLI,
  the other means this history row is corrupt.
- **Source attribution, the migration ID, and the direction stay out of the hashed bytes.** A
  call site is machine-specific; the other two are already named by the row that holds the
  checksum, and duplicating them would create fields that can disagree with their own container.

## Consequences

### Positive

- Deciding whether a migration was modified needs the file and nothing else — no database, no
  language toolchain, no subprocess.
- The one class of false drift the framework can eliminate outright, mixed line endings, is
  eliminated by the framework rather than by asking every developer to configure git.
- A recorded value carries enough information to say which half moved, so the eventual error
  message can distinguish an edited file from divergent emitted operations.
- A future change to the construction is a visible, named condition with an upgrade path, rather
  than universal silent drift.

### Negative

- **The IR encoding is now a compatibility surface, retroactively.** Reordering a struct field,
  adding a field without `omitempty`, or changing how a logical type renders now changes the
  checksum of every applied migration in every installation. Nothing in the IR package announces
  this, and it was not true before this decision.
- A checksum covers one file. A migration calling into a helper package whose body changed has an
  unchanged source digest. Closing that requires content-hashing the whole build.
- Two clauses of normalization is still two clauses, and both must be reproduced exactly by every
  future SDK. The known-answer vectors are the only thing that will catch a divergence, and only
  once a second SDK exists to run them against.
- Recording two digests where one would fit invites the assumption that both are compared.

### Neutral

- The value is one string in one column. The choice affects the column's documented contents and
  its width, not the history schema.
- This is a fourth independent version line, alongside the framework version, the IR version, and
  the protocol version. Nothing couples them, and a reader who assumes otherwise will make a
  wrong change.
- SHA-256 was the only primitive considered. It is in every relevant standard library, fixed by
  specification rather than by an implementation, and unencumbered by the weak-primitive
  objections that rule out MD5 and SHA-1. Nothing here turns on that choice.

## Notes

Two things are worth revisiting when their preconditions arrive.

Once a second SDK exists, its expression of the known-answer vectors becomes the real test of
whether this specification is reproducible from prose. Any clarification needed at that point is
a defect in this document, not in the SDK.

Separately, the emitted-IR half currently depends on encoder behavior that is not fully under the
framework's control. Go's JSON encoder escapes U+2028 and U+2029 unconditionally, independent of
the HTML-escaping setting the IR package disables, while JavaScript's `JSON.stringify` does not.
A check expression or view query containing one of those characters would therefore hash
differently in the Go and TypeScript SDKs. The vectors pin the current behavior so the difference
is documented rather than latent; closing it belongs with the second SDK, where it first becomes
observable.
