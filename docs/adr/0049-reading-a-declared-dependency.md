# 0049. A declared dependency is read once the runtimes are up, and a report names the order it has

## Status

Accepted

## Date

2026-09-28

## Context

[ADR 0018](0018-declared-dependencies.md) fixed the model for `Dependencies()`: a declaration is read
from a migration, an edge names a version, and resolution returns one total sequence — a stable
topological sort with the timestamp as the tiebreak. M4-T2 built that resolver, `migration.Order`, and
recorded under Negative that *"until one of those lands, the overlay has no producer"*, pointing at
M6-T7 and M7-T2 for the SDK halves.

Both landed. `sdk/go` reports a `Dependent`'s list, `sdk/typescript` reports a `dependencies`
property, `protocol.Discovered.Dependencies` carries them, and `docs/protocol.md` §4 states that the
engine *"sorts by version and overlays the declared dependencies itself"*. It did not.
`internal/cli`'s `discover` called `migration.Order(found, nil)`, so every declaration was read from
the migration, framed, written, read back, parsed — and dropped. M7-T10 came closest: `Session.Loaded`
made the `migration.discover` round trip to reconcile what each runtime loaded against what the engine
routed there, and threw the `dependencies` field away on the way past.

Everything under `internal/cli` had already been built to expect the overlay, and said so:
`history.Reconcile` keeps the caller's order because *"re-sorting here would show an order migr will
not follow and would discard the dependency overlay"*; `engine.Apply` runs the order it was given for
the same stated reason; `engine.Rollback` undoes by `(batch, ordinal)` **because** a declared edge may
point backwards in time; `validate.Options.Migrations` is documented as *"in the order a run would
perform them"*. Three layers were correct and the top of the stack passed `nil`.

So the plumbing is not the decision. Two things are.

**The order cannot be settled before a session exists.** `internal/cli` discovers the directory,
digests each file into `[]history.File`, and only then opens the `driver.Conn` that a runtime's
callbacks reach — while a declaration can only come from a runtime that has been started. Ordering is
therefore downstream of `connect`, which is downstream of everything that used to settle it.

**A report has no toolchain.** `migr status` starts no runtime by design
([ADR 0027](0027-read-only-reports.md)), and one of that record's stated consequences is that *"a
project whose language has no runtime yet can still ask for its status"*. Its rows are printed in the
order it was handed them, and its `Statuses` field claimed to be *"in the order migr up would run
them"* — a claim it could no longer make the moment `migr up` began resolving an overlay. Invariant #8
requires planning paths to be deterministic, and two commands printing two orders for one directory
and calling both the run's is exactly what that rule exists to prevent.

## Decision Drivers

- **One sequence per directory, per invocation.** `migr up`, `migr up --dry-run`, `migr plan` and
  `migr validate` must agree, and none of them may re-derive the tiebreak for itself.
- **A report must keep working with no toolchain installed.** That is ADR 0027's promise and the
  reason `docs/protocol.md` §4 rejects a computed graph in the same breath as a nondeterministic one.
- **Say what is not known rather than guessing.** [ADR 0040](0040-running-a-go-migration.md) already
  settled this shape once: `migr validate` reports a Go half it could not judge as **unchecked with a
  reason**, because reporting it as passing and refusing it outright are both lies.
- **No new persisted state.** Invariant #6's whole posture is that the database and the files are the
  only records, and that nothing is silently repaired from a cache.

## Considered Options

### A. Where the order is settled

- **A1: in `internal/cli`, after every session is started and before the digests are taken** ✔.
  `sources` already starts one session per language and already calls `Loaded`; it now returns what
  each declared alongside the source and the closer, and `attach` calls `migration.Order` and then
  `hashed` over the sequence that came back.
- A2: in `internal/engine`, from a new `Options.Declared`. **Rejected**: the engine is given
  `[]history.File`, whose fields are unexported, so it could not reorder them without a second
  ordering entry point in `internal/history` — and the declarations would still have to be collected
  by `internal/cli`, which is where the sessions and the files are already in one hand.
- A3: a second discovery phase that exists only to ask — start the runtimes, collect, stop them,
  order, start them again. **Rejected**: it pays twice for every handshake and every Go build to avoid
  moving two statements, and the second start could legitimately report something different.

Taking the digests after the sessions start is a real reordering of `open`, and it is required rather
than incidental: `history.File` deliberately exposes no version, so a pre-hashed slice cannot be
permuted from outside `internal/history`. Adding an accessor to permit that would invite exactly the
re-sorting `history/doc.go` warns against.

### B. Whether a declaration crosses a language boundary

- **B1: an edge names a version, whoever loaded it** ✔. Each session reports its own half and the run
  resolves one graph over the union. A `.ts` migration declaring a `.go` one is ordinary.
- B2: a runtime may only declare versions it loaded itself. **Rejected**: ADR 0018 chose the version
  as the key *because* it is the one identifier every layer already shares, and M7-T10 made one
  directory hold several languages. A rule that stopped at the language boundary would make the
  merged-branch case — the reason declarations exist — unexpressible in a mixed project.

The merge cannot collide: routes are disjoint by extension, and M7-T10's two-directional
reconciliation has already refused a runtime claiming a migration routed elsewhere **before** anything
is merged.

### C. What `migr status` prints

- **C1: version order, named as such** ✔. `statusReport` carries `order` — `resolved` or `version` —
  and `unread_declarations`; the text output adds one line for `version`, saying that N migrations may
  declare a dependency only a runtime reports, that this report started none, and that `migr plan`
  prints the order a run will use. A project whose languages cannot declare anything a report must ask
  for is `resolved`, and its output is unchanged.
- C2: version order, silently. **Rejected**: it is C1 minus the one sentence that makes it true. A
  report claiming the run order while the history records a different one is how an operator learns to
  distrust the report.
- C3: cache the declarations where a report can read them — a digest-keyed file written by `up`,
  `plan` and `validate`. **Rejected**: a second record of what the migrations say, with its own
  invalidation rules, a cold-cache path that still needs C1's answer, and a CI run where `status` goes
  first and reads nothing. Invariant #6's posture is that the files and the database are the records.
- C4: `migr status` starts the runtimes. **Rejected**: it breaks ADR 0027's stated consequence, makes
  the command an operator reaches for *during a deploy* pay for a Go build, and makes a report fail on
  a machine where the toolchain is missing — for a column ordering.

`migr plan` is already the answer to "what order exactly": it starts the same runtimes a run does, so
it resolves the same overlay. That is why no `--resolve` flag was added to `status`.

### D. What an unresolvable graph does to `migr validate`

- **D1: it fails the command, exit 2** ✔, with `migration.GraphError`'s own message — every unknown
  edge and every cycle in one pass, each cycle rendered as its full path in filenames. The same
  refusal `up`, `down` and `plan` give.
- D2: report it inside the validation report as a problem per affected migration, and check every
  migration anyway. **Rejected for now**: the graph is a property of the directory rather than of a
  migration, `validate.Problem` has no shape for a project-level fault, and the "report everything at
  once" property ADR 0028 wanted is already kept — by the error itself.

## Decision Outcome

**A1, B1, C1, D1.**

`Session.Loaded` returns what a runtime loaded *and* what each migration declared, from the round trip
M7-T10 already pays for. `internal/cli`'s `sources` returns a `reading` — the source, the closer, and
the merged declarations — and `attach` settles the sequence with `migration.Order` before hashing it.
`migr validate` takes the same two lines between `offlineSource` and `validate.Run`, so the CI gate
reads the sequence it gates.

A cycle, and an edge naming a version nothing carries, are exit **2** wherever they surface: the
migrations are what is wrong, and an operator fixes them by editing a file.

`migr status` prints version order and says so. The basis is in `--json` as `order`, so a script reads
it rather than inferring it, and the one-line note is printed only for a directory holding migrations
whose declarations a report cannot read.

## Consequences

### Positive

- `Dependencies()` does something. The merged-branch case ADR 0018 was written for now runs, in every
  SDK, across languages, against a real server.
- One sequence per invocation: `up`, `--dry-run`, `plan` and `validate` all read what `attach`
  settled, and `migr status` is explicit about having a different question's answer.
- The declarations cost no extra round trip, no new file, and no protocol change.
- The runtime conformance suite now compares what each SDK *declares*, not only what it loads — it
  had ignored the field since M6-T9.

### Negative

- **`migr status` can print an order a run will not use**, and says so rather than resolving it. An
  operator who needs the exact sequence runs `migr plan`.
- **A cycle is only detectable after the runtimes start**, so `migr up` on a Go project builds the
  runner and shakes hands before refusing. There is no earlier moment that could know.
- **The source digests are taken after the sessions are up**, a moment later than before. Nothing
  depends on the old timing, and drift is decided against the bytes as they are at run time either
  way — but the reordering is real and is why `open` no longer calls `t.files`.
- **A `.sql` migration still declares nothing.** That format has no syntax for it, which ADR 0018
  left to whatever task defines one. It is also why C1's basis is conditional rather than constant: a
  `.sql`-only project's printed order is provably the run's.

### Neutral

- `migration.Order`'s errors were already built for this and needed no change: an edge naming nobody
  is dropped before the sort, every cycle is reported as a path, and everything wrong is reported at
  once.
- `engine.Rollback` is untouched. It undoes by `(batch, ordinal)` descending, so a run the overlay
  reordered reverses in *its own* reverse rather than in version order — which ADR 0022 chose by name
  and nothing could observe until now.

## Notes

Revisit D1 if a project accumulates enough declarations that a CI run wants the cycle and the compile
failures in one report. Revisit C1 if a declaration ever becomes readable without a runtime — a
marker in a `.sql` file, or a language whose declarations the engine could parse — in which case the
basis for such a project becomes `resolved` with no other change.
