# 0018. A dependency is declared by a migration, never computed by one

## Status

Accepted

## Date

2026-08-02

## Context

A migration's filename carries a UTC timestamp, and that timestamp is its version and its default
order (CLAUDE.md §9). Timestamps are enough right up until two branches each add a migration and
merge: the one written second may carry the earlier timestamp, and running them in timestamp order
is then wrong. `Dependencies()` exists to say so — an explicit edge overlaid on the implicit order.

M4-T2 had to build that overlay, and found that **nothing supplies the edges and nothing says what
one is.** `Dependencies()` appears four times in the repository and every occurrence is prose:
`README.md`, CLAUDE.md §1 and §8, and `sdk/go/doc.go`. No Go type declares it. It is a method on
the SDK-side migration contract, which is M6-T7 for Go and M7-T2 for TypeScript — four milestones
after the task that has to order migrations.

Nor is there anywhere for it to live in what already exists. [ADR 0007](0007-ir-document-envelope.md)
put exactly two members in an IR document and `internal/ir`'s package doc refuses to add more:
"duplicating them here would create fields that can disagree with their own container."
`docs/ir.md` §2 says the same and then assigns dependency resolution to the engine without defining
an input for it. `migration.Migration` carries a version, a name, and an extension.

Two facts settle the shape of the answer.

**A `.sql` file has no methods.** M5's stated goal is "SQL-file migrations as the interim source
format until M6 lands the SDKs". If a declaration is something only a running language runtime can
produce, then for the whole of M5 no migration can declare anything.

**A computed dependency would make the order depend on the database.** ADR 0009 established that
migrations may branch on live query results — that is precisely why the IR half of a checksum
cannot decide drift. A `Dependencies()` free to do the same could return one graph during
`migr plan` and another during `migr up`, so the two would order the same migrations differently.
Invariant #8 requires planning paths to be deterministic, and a graph read out of a live database
cannot be.

## Decision Drivers

- **Determinism.** The sequence a migration runs in must not depend on when it was computed or
  against what.
- **The declaration must outlive the runtime.** SQL migrations, `migr status`, and `migr validate`
  all need the graph without spawning anything.
- **One identifier for machines, one for people.** These are different jobs and conflating them
  costs either stability or legibility.
- **A total order, not a graph.** The apply executor aborts a queue and `--step N` walks one; both
  need a sequence.

## Decision

**A declared dependency is read from a migration, never computed by it.** `Dependencies()` returns
a declaration — a fixed list, not the result of a query. Every SDK owes the same guarantee, and a
future non-SDK source (a header in a `.sql` file, most likely) is legitimate for the same reason.

**An edge names a version.** `20260727T153000` is the key discovery proves unique, the key a
history row joins on, and the key `migr down --to` names. It is also stable under renaming a
migration's name half, which a filename is not.

**Every message renders a filename.** A cycle printed as three fifteen-digit timestamps is the
least useful possible form of the one error the overlay exists to produce.

**The order is a stable topological sort with the version as the tiebreak.** Migrations that
nothing constrains relative to each other stay in timestamp order. The result is one total
sequence.

**A dependency may point backwards in time.** That is the merged-branch case, not a mistake.

**Resolution reports everything at once.** Every unknown dependency and every cycle, in one pass.
An edge naming a version nothing carries is dropped before the sort; dropping an edge can only
remove cycles, never create one, so every loop still found is genuine and the two reports do not
send an operator chasing each other.

`internal/migration` owns all of this and imports stdlib and nothing else, so `migr validate` and
`migr status` reach it without reaching through an executor.

## Considered Options

### Where a declaration comes from

#### Option A: A declaration read from the migration

**Pros**

- Works for `.sql` migrations, which have no functions to call, and therefore works during M5.
- `migr validate` and `migr status` resolve the graph with no toolchain and no database, which is
  the same property the source half of a checksum was given for the same reason (ADR 0009).
- The graph is a fact about the migration set, so it is the same one every time it is read.

**Cons**

- A future SDK author could still write `Dependencies()` to compute something. Nothing mechanically
  prevents it; this ADR is what makes it a stated violation rather than a discovery.

#### Option B: A value the runtime computes and returns over the protocol

**Pros**

- No new declaration syntax; `Dependencies()` is an ordinary method returning an ordinary slice.
- A migration could adapt its dependencies to what it finds, which sounds flexible.

**Cons**

- `.sql` migrations can never participate, and they are M5's entire source format.
- `migr status` and `migr validate` would need a language toolchain to answer "what order?".
- Plan and apply could disagree about the order, which is the failure ADR 0009 documents one layer
  down and declines to accept.

**Rejected because:** flexibility in the graph buys nothing anyone asked for and costs
determinism, which invariant #8 is not willing to trade.

### How an edge names its target

#### Option C: By version, rendered as a filename

**Pros**

- The version is unique by construction (discovery refuses two files claiming one) and is already
  the key for history and `--to`.
- Renaming a migration's name half leaves every edge pointing at it intact.
- The reader still sees `20260727T153000_add_users.sql` in every message.

**Cons**

- What an author writes is not what an author reads. A declaration is a bare timestamp.

#### Option D: By the full stem, `20260727T153000_add_users`

**Pros**

- Readable at both ends, and it is the migration ID `ir.OpError` already prints.

**Cons**

- Renaming the name half silently breaks every edge pointing at the migration.
- It is a third identifier alongside `Version` and `Filename`, and two of the three would be
  accepted in different places.

**Rejected because:** an identifier that a rename can break is the wrong thing to build a graph
out of, and the legibility it buys is available for free by rendering something else.

### What resolution returns

#### Option E: One ordered sequence

**Pros**

- The apply executor aborts "the remaining queue" and `--step N` counts through one; both already
  assume a sequence.
- The tiebreak rule is applied once, here, rather than re-derived by every consumer.

**Cons**

- A consumer wanting the graph itself — a visualizer, say — has to rebuild it.

#### Option F: The graph, for callers to traverse

**Rejected because:** every caller would repeat the tiebreak, and two that repeated it differently
would order the same migrations differently. The visualizer this would serve is M15-T7.

## Consequences

### Positive

- The order is a pure function of the migration set and its declarations, so `migr plan` and
  `migr up` cannot disagree about it.
- `migr status` and `migr validate` resolve it with no database and no language toolchain.
- SQL migrations can declare dependencies as soon as someone defines where to write one; nothing
  about the model has to change to allow it.
- A cycle is reported as a path, and every problem in a graph is reported in one pass.

### Negative

- **The declaration syntax is still undecided.** This ADR fixes the model and the neutral shape
  (`map[version][]version`) and leaves the spelling to M6-T7, M7-T2, and whatever task defines the
  SQL migration format. Until one of those lands, the overlay has no producer.
- An author writes a timestamp rather than a name, which is harder to write and harder to review
  than a filename would be.
- A migration whose dependencies genuinely differ per environment is not expressible. No such case
  is known, and the alternative costs determinism.

### Neutral

- Cycles are reported one per loop rather than every elementary cycle. Enumerating all of them is
  exponential, and one path per tangle is what an operator edits.
- `Dependencies()` keeps its name in the SDK contract even though the engine never calls a function
  by that name — it calls a protocol method that carries what one declared.

## Notes

Revisit if a second consumer needs the graph rather than the sequence. M15-T7's visualizer is the
likely one, and by then M11-T3's differ will have opinions about ordering too; the sequence is
derivable from the graph but not the reverse, so that change is additive.
