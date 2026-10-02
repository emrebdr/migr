# 0062. Resolving a dirty migration is an append, and a resume is an index into one list

## Status

Accepted

## Date

2026-10-02

## Context

[ADR 0024](0024-dirty-runs.md) made the dirty state producible and [ADR 0061](0061-refusing-a-dirty-database.md)
made it legible. Between them they also made it **final**: `up`, `down` and `plan` all refuse, and
nothing resolves it. `migr resolve` is the way out, and it is the only command that exists because
invariant #6 permits one — *"any repair path must be an explicit, separately-named command that says
exactly what it is changing."*

Three things were already in place, which is why this is narrow.

**The fold needs no new column.** `afterUp` returns a *fresh* `folded`, so an `Applied` row landing
after a dirty one clears `dirty` and sets `carried`; `afterDown` clears it and leaves the migration
pending. Resolution is therefore an **append**, which is the only shape invariant #6 allows — nothing
is edited and no recorded checksum is rewritten.

**The row to read is already a type.** `dirtyError` builds `*engine.DirtyStateError` from
`Status.Recorded`, and `recordedRun` is the shape `migr status` and `migr history` already render it
in. Whatever this command prints is a third rendering of one row rather than a fourth shape.

**The `outcome` column is `string(16)`**, not an enum and not check-constrained, so a fourth outcome
changes no history table on any engine.

Seven probes ran against MySQL 8.0 and MariaDB 11.4 before anything was written. Three mattered:

- **A `.sql` migration's IR digest is stable across two emits, and plan mode matches apply mode.** The
  whole resume path rests on that: if a second ask produced a different digest, resuming by index
  would be unavailable to every language.
- **An `Applied` row after a dirty one really does clear the state** on both servers, and the statement
  the stopped run never reached runs cleanly from its position.
- **A dirty row may name every statement.** ADR 0024 says a failure with no statement position records
  the running counter, so a commit or a history write that failed leaves a row with *nothing left to
  run* — which `Groups(statements[from:])` turns into an empty list and `checkRunnable` would refuse
  with `ErrNothingToRun`.

## Decision Drivers

- M9-T6's done-when: *"an explicit command resolves dirty state (resume from the recorded index, or
  mark resolved after manual intervention), and it prints precisely what it is about to change before
  doing it."*
- Invariant #6: applied migrations are immutable, no flag rewrites a recorded checksum, and a repair
  path says exactly what it is changing.
- [ADR 0009](0009-migration-checksum.md): a migration may branch on live query results, so the
  operations one run emitted cannot be reproduced on demand.
- ADR 0024: `LastStatement` is the last statement that **succeeded**, so an off-by-one replays a
  statement that already ran or skips one that never did.
- CLAUDE.md §11: favour the safer path — refuse the ambiguous operation.
- M5-T3's two reports: `migr status` and `migr history` exit 0 over a dirty database, and whatever this
  command prints reuses their shapes.

## Considered Options

### A. What a hand-resolution records

- **A1: a fourth outcome, `history.Resolved`** ✔, written by `Store.MarkResolved`, which records the
  operator's `--reason` in the `error` column. `Record.Validate`'s *"an error is required for a run
  that did not complete"* already covers it, so a resolution with no reason is refused by the contract
  rather than by a driver. A **resume** records `Applied`, because migr did run the statements and the
  dirty row above it records where it picked up.
- A2: an `Applied` row for both. **Rejected on merit.** Zero contract change, and the history then
  cannot tell work migr did from work somebody says they did — only the dirty row above it, by
  inference. A rollback over an asserted `applied` behaves differently from one over a real apply: the
  `down` runs statements undoing things that may not exist. Invariant #6's *"says exactly what it is
  changing"* is not satisfied by a row indistinguishable from an ordinary run.
- A3: two new outcomes, separating a resumed run from an asserted one. **Rejected**: a resumed run
  genuinely applied the migration, and the pair of rows — dirty at N, then applied — already says
  where it resumed. Two values would buy a distinction the log already draws.

### B. What refuses a resume

- **B1: both digests** ✔. A moved **source** digest is `*engine.DriftError` with `Resuming: true`, so
  `errors.Is(err, ErrDrifted)` gives exit 4 through `ExitCode` with no command-level tagging. An
  emitted **operation list** whose digest differs from the recorded IR half is `ErrNotResumable`.
- B2: the file only. **Rejected**: ADR 0009 is explicit that a migration may emit a different list, and
  then statement 4 of the new list is not the statement statement 4 named. The index is meaningless
  against a list that moved, and running the wrong statement is the one outcome this command must not
  produce.
- B3: neither, printing both digests for the operator to judge. **Rejected** as the ambiguous operation
  §11 says to refuse.

The cost is stated rather than hidden: **a migration that branches on live data is not resumable**, and
the refusal says so and points at the two asserted repairs.

### C. Which direction a resume carries on in

- **C1: the one the stopped run was going** ✔, from `Recorded.Direction`. A resumed `down` records a
  `down`/`Applied` row, which the fold already turns into `pending`, and it asks for the undo by the
  same path a rollback does — `undo`, which derives an inverse where none was authored, so the derived
  list is the same list and the IR digest check passes.
- C2: resume only an `up`. **Rejected**: it leaves the harder of the two halves to manual bookkeeping,
  which is the opposite of what M9's goal asks for.

### D. What the two asserted repairs reach

- **D1: no runtime, no planner, and the recorded source digest** ✔. The row is built from
  `Status.Recorded` plus this session: `Runtime` and the **checksum** are copied from the run that went
  dirty, `Driver` and the instant come from this run. So an assertion starts no language runtime,
  compiles nothing, and works on a migration that no longer builds — which is exactly when an operator
  needs it. `newResolver` checks only what the repair needs, and `Options.checkRecording` is the
  narrower half of `Options.check`.
- D2: record the file's digest as it stands now. **Rejected**: it would launder a file edit into an
  applied state. Copying the recorded one settles the dirty state and reports the edit as `drifted` on
  the next `status` — the separate problem invariant #6 already has a code for.

### E. How "prints what it is about to change" is spelled

- **E1: one command, a required mode, and `--dry-run`** ✔. The acting invocation prints the same plan
  and then acts; `--dry-run` stops at the last step. One function with a `DryRun` field rather than a
  `Preview`/`Apply` pair, which is ADR 0026's *"a dry run and the run it describes cannot drift apart
  into two answers"* made structural.
- E2: a bare `migr resolve` as the report. **Rejected**: a flagless invocation that reports while a
  flagged one acts is magic no other command here has, and `--dry-run` already means this.
- E3: `migr cutover`'s `--confirm`. **Rejected**: `migr up` is more consequential and needs no
  confirmation, and the irreversible moment cutover is guarding does not exist here.

## Decision Outcome

**A1, B1, C1, D1, E1.**

```
migr resolve --resume                   # prints the statements it will run, then runs them
migr resolve --applied --reason "..."   # prints the row it will write, then writes it
migr resolve --rolled-back --reason "..."
migr resolve --resume --dry-run         # prints only
migr resolve                            # exit 2: which repair?
```

The skip is a **re-group rather than a trimmed group list**: `Groups(compiled.Statements()[from:])`
reuses `internal/engine/group.go` untouched, and `executor.from` is zero for every other run, so
`perform` sets `e.position = e.from` and a second failure records an **absolute** index. A resume that
stops again therefore goes dirty at the right place, and the next resume picks up from there.

`Pending() == 0` is the case probe 3 found: every statement had already run, so nothing is executed and
`Record` writes the `Applied` row the bookkeeping never got to. That is not an assertion about anything
— the schema change is complete and ADR 0024 says so.

What an operator sees, against a real MySQL server:

```
resolve — database "default", resume

  20260927T120000_add_people.sql
  stopped after statement 1 of the up run, 2026-10-02 15:58:42 UTC

  will run statements 2 through 3 of 3, then record the up run in batch 3

       2  raw
          ALTER TABLE m9t6_absent ADD COLUMN orphan int;
       3  raw
          CREATE INDEX m9t6_people_idx ON m9t6_people (id);

recorded as applied in batch 3. This migration is no longer between states.
```

`migr status` says `resolved by hand rather than run: <reason>` on a state an assertion settled, and
`migr history` writes the reason under the run. Both still exit 0.

## Consequences

### Positive

- **A real server is driven out of a dirty state.** MySQL 8.0 and MariaDB 11.4 both go dirty at
  statement 2 of 3, resume, and end with the catalog holding what all three statements were for. ADR
  0061 reached the first half of that; this is the second.
- The history never says migr applied something a human did, and the reason survives in the row —
  which is the only thing that will still say why a migration reads as settled once nobody remembers
  the incident.
- `migr up` succeeds against the resolved database, which is the whole point: the state was final
  before this command existed.
- A dirty **rollback** has a way forward too, by the same code path, and the fold needed no new rule.

### Negative

- **A migration that branches on live data cannot be resumed.** §B. The two asserted repairs are the
  path for those, and the refusal names them.
- **A resumed run's row looks like a full run.** Its `duration_ms` covers only the resumed part and no
  field says it started at statement 2 — the dirty row directly above it, same version, is what says
  so. A `statements` column (ADR 0061 §C2) would let the row carry it; it is still not added.
- **An older migr cannot read a database a newer one resolved.** `"resolved"` names no outcome it
  knows, so its store reports `history.ErrCorrupt` naming the column, the value, the table and the
  row — measured on both servers. That is the designed answer rather than a surprise, and it is exit 1
  rather than 4, because ADR 0029 keeps a corrupt row out of invariant #6's code.
- **Two dirty migrations are refused rather than settled one at a time.** No migr run can produce that
  — a dirty migration refuses both directions before anything else runs — so a database with two was
  not left that way by this tool, and settling one would be a guess at which.

### Neutral

- `migr resolve` against a database with **no history table** refuses rather than creating one: a
  command whose whole job is repair may not be the one that invents the record it repairs.
- A resolution that finds nothing between states exits **0**, as `migr up` does with nothing pending.
- `ErrDrifted`'s own text is now neutral between the two refusals — *"a recorded migration's file has
  been edited since the run that recorded it"* — because `--json` carries it as the one-line `error`
  and the old wording was false for a resume.

## Notes

A resume is for a **transient** cause: a lock timeout, a table another migration was meant to create,
a disk that filled. Where the cause is the migration's own SQL, fixing it means editing the file, and
then the source digest check refuses the resume and the asserted repairs are the way through. The
refusal says exactly that.

The live suite needed a history table per case for the reason M7-T8 recorded and ADR 0061 hit again:
`uniqueName(t)` is derived from the test's name, so the previous run's rows are still there.

M9-T7 inherits the CI matrix and the committed op-set round trip. Nothing here is waiting on it.
