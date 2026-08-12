# 0027. A read-only report takes no lock

## Status

Accepted

## Date

2026-08-12

## Context

M4-T3 built the fold that resolves each migration to one of five states. M5-T2 built the CLI that
opens a database. M5-T3 is the first task whose commands only ever *read*: `migr status` prints what
the fold resolved, and `migr history` prints the log it folded.

Every value they need already exists — `history.Read` returns the log, `history.Reconcile` returns a
`Status` per version, and `Status` carries the run that decided the answer, so a table has the
batch, the instant, and a dirty run's `last_statement` with no second read. What the task decides is
therefore not what to compute but **what a report is allowed to do to the database it reports on**,
and what it may say about a database it has never touched.

Three things forced the question:

**M5-T2 built a read-only fold and put it behind the lock.** `executor.inspect` reads the history
without ensuring it and reports whether the table exists, which is nearly what `migr status` needs.
But it is a preview's fold: when the table is absent it substitutes `history.NewLog(nil)` — turning
"this database has no history table" into *every migration pending*, which is the right answer to
"what will run" and the wrong answer to "what is".

**`cli.open` builds things these commands must not need.** It calls `sourceFor`, which refuses
`migrations.language: go` until M6-T10 — so a Go project routed through it could not ask for its own
status, though a status compiles nothing and reads no migration body.

**A history row cannot name a file.** `history.Record` carries `Version` and `Name` and no `Ext`,
deliberately, since a run may be recorded for a migration whose file is now deleted.

## Decision Drivers

- [ADR 0019](0019-history-reconciliation.md): an absent history table yields **zero statuses**, not
  every migration pending. Under-reporting is the direction to fail in.
- `history/doc.go`: *"migr history is the command an operator runs to inspect exactly that table,
  and a store that refuses to read it has removed the tool they were reaching for."*
- `Read` takes a `Reader` — `List` alone — which states invariant #6 as a signature: reconciliation
  cannot write, cannot `Ensure`, and cannot repair.
- CLAUDE.md §5: exit code 3 is lock contention and exit code 4 is checksum drift. Both are things a
  *run* reports.

## Considered Options

### A. Whether `migr status` takes the migration lock

- **A1: no** ✔. One `List` is already one snapshot, so there is nothing torn to protect, and an
  unlocked status is the command an operator can run *while* a migration is running — which is
  precisely when they most want it.
- A2: yes, as a preview does ([ADR 0026](0026-dry-run-and-sql-sources.md), C). **Rejected**: it makes
  a read-only report block a deploy, and gives it an exit code 3 of its own. A status that waits
  `lock.timeout` and then fails, having printed nothing, is worse than a status that may be a moment
  stale.

The asymmetry with a preview is the point rather than an inconsistency. A preview answers *what will
run*, and an answer about a database someone else is changing is not an answer. A status answers
*what is*, and does not promise to act on it. `migr history` is the same and more so: a log of runs
that already happened cannot be made truer by locking.

### B. What a database with no history table resolves to

- **B1: zero statuses, printed as itself** ✔. `migr status` prints one sentence, plus how many
  migration files are on disk, and no rows at all. `migr history` prints one sentence.
- B2: N pendings, as a preview reports. **Rejected**, ADR 0019: read as pending, a fully migrated
  production database looks virgin. A preview may say it because it says so in the same breath and
  applies nothing either way; a status's whole output *is* the claim.
- B3: an empty table with the five column headings. **Rejected**: an empty table reads as "nothing to
  report", which is a different statement from "nothing has been reconciled here yet".

The file count is a fact about the directory rather than a state, and it is printed with no state
word anywhere near it, so nothing in that output can be read as "N pending".

### C. Where the fold lives

- **C1: `internal/cli` calls `history.Read` and `history.Reconcile` itself** ✔, through
  `session.log`, which narrows the store to `history.Reader` so no report can write or `Ensure`.
- C2: export `engine.Inspect`. **Rejected**: engine's fold is a preview's, and its substitution of a
  present-empty log is exactly what B1 refuses. Reusing it would mean reusing the wrong half, and
  passing a flag to switch the substitution off would put "what a database with no history table
  means" behind a bool.

`internal/engine` is orchestration — apply, rollback, preview. A status orchestrates nothing: two
calls into `internal/history`, which exports both for this reason.

### D. What each command may name a migration

- **D1: `migr status` renders a filename, `migr history` renders `version_name`** ✔.
- D2: both render a filename. **Rejected**: no row records an extension, so `history` would have to
  invent one from the project's current `migrations.language` — which may not be the language that
  migration was written in, and the file may not exist at all.

A `missing` or `dirty` status with no file carries a zero `Migration`, whose `Filename()` is the
string `"_"`. The renderer asks before it calls it, and falls back to the version — which is the only
name that version has.

### E. What a report exits with when it finds a problem

- **E1: always 0** ✔. The five states are this command's output; a report that printed them
  correctly did not fail.
- E2: 4 on drift, 1 on dirty, mirroring `migr up`. **Rejected**: one database holding a drifted *and*
  a dirty migration would have to pick a code, and exit code 4 stops meaning "a run refused because
  an applied migration was edited" — which is how operators learn to ignore it. `migr up` is already
  that gate, and M5-T5's `migr validate` is the one built for CI.

## Decision Outcome

**A1, B1, C1, D1, E1.**

`internal/cli`'s `open` split into three, so what a command needs is visible in which of them it
calls:

```go
func resolve(opts *options) (*target, error)                                   // config + Target
func (t *target) files(opts *options) ([]history.File, error)                  // discover + hash
func (t *target) connect(ctx context.Context, opts *options) (*session, error) // driver + Open
```

`migr up`, `migr down`, and `migr plan` call all three plus `source`. `migr status` calls
`resolve → files → connect`. `migr history` calls `resolve → connect`, and consequently prints a log
in a project whose migrations directory is missing or malformed — which is one of the situations an
operator opens it in.

A status row carries the run that decided it, nested and present exactly when there is one, and the
two source digests only where they disagree. A drifted row **reads `State` and renders both**; it
never compares them again ([ADR 0009](0009-migration-checksum.md)). The per-row note and the count
line are a total switch over `history.State` and a walk of `history.States()`, so M13-T14's sixth
state breaks the build here rather than rendering blank.

`migr history --json` carries the whole row — driver, runtime, engine version, IR version, checksum
— because this is the command an operator runs to inspect that table. The text table carries the
columns a person reads.

## Consequences

### Positive

- `migr status` and `migr history` can be run during a deploy, which is when they are most wanted.
- A database migr has never touched reports as itself in both commands, rather than as an empty
  table or as a directory's worth of pending migrations.
- A project whose language has no runtime yet can still ask for its status, since a report builds no
  source.
- `last_statement` became a value something renders rather than one only fixtures carry, which is
  what M9-T6 resumes from.

### Negative

- **A status can be stale.** Another run may commit between the `List` and the print. The alternative
  was blocking that run, and a report is not acted on by the tool that printed it.
- **A status opens a connection to report nothing new**, on a database with no history table. It
  cannot know that without asking.
- **`migr history` reads every row and truncates nothing.** `List` has no `LIMIT` — adding one is a
  store-contract change — so a long-lived database prints a long log. `| head` and `--json | jq`
  both work today; a `--limit` is a rendering cut that can be added when someone wants it.

### Neutral

- Two commands now render an instant, so `internal/cli` has a `stamp` constant beside
  `internal/engine`'s. They are deliberately not shared: one lays out a table and one lays out an
  error message, and neither package should import the other's formatting.
- `migr status` exits 0 over a drifted database while `migr up` exits 4 against the same one. That is
  the two commands answering different questions, and M5-T6 inherits it as a stated rule rather than
  as a gap.
