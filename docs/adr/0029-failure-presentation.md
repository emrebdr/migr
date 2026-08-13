# 0029. A failure is a document too

## Status

Accepted

## Date

2026-08-13

## Context

M5 shipped ten commands, each of which classified its own failures as it landed. M5-T1 set the rule
they were meant to follow — *the argument is wrong → 2, the world is not ready → 1* — and recorded
it in `internal/cli/doc.go`. M5-T2 added the first cases, M5-T3 established that a report exits 0
having found a problem, and M5-T5 established that a validator exits 2 over the same project a
report exits 0 on. M5-T6 is the pass that holds the whole set against one rule instead of ten
habits.

Two things forced this ADR rather than a tidy-up.

**`cmd/migr` prints every failure and never sees `--json`.** It does
`fmt.Fprintf(os.Stderr, "migr: %v\n", err)` whatever the flags said. CLAUDE.md §5 promises that
`--json` switches *both* streams — JSON results on stdout, JSON logs on stderr — so
`migr up --json` against a drifted database put fourteen lines of English prose on a stream a
script is entitled to parse. Nothing in `cmd/migr` can fix that: `--json` is a cobra flag, parsed
inside `cli.Execute`.

**Two errors are paragraphs by design.** `engine.DriftError` renders the recorded and current
digests on their own lines under a heading, with three paragraphs of remediation; `engine.DirtyError`
renders how far a run got, what stopped it, and why the window exists. Both are right for a
terminal — comparing 64 hex characters is the message's whole job — and both are useless as a JSON
string. Every value in them is already an exported struct field.

Alongside that, the audit found four failures whose code disagreed with the rule `doc.go` states.

## Decision Drivers

- CLAUDE.md §5: `0` success · `1` runtime error · `2` validation failure · `3` lock contention ·
  `4` checksum drift · `5` lock-impact threshold exceeded — and *"`--json` switches **both**
  streams … so `migr status --json | jq` never finds a log line inside the document it is parsing."*
- `internal/cli/doc.go`: *"what separates them is whether the operator retypes something or changes
  something."*
- Invariant #9: *"Every user-facing error names the migration ID and the offending operation."*
- [ADR 0027](0027-read-only-reports.md), E: a report exits 0 having found a problem, because the
  five states are its output rather than its failure.
- [ADR 0024](0024-dirty-runs.md) and [ADR 0023](0023-batch-narrowing.md) both end with *"M5-T6 owns
  the mapping"*.
- `internal/cli/run.go` already writes a summary to stdout for a run that committed some migrations
  and then failed, so stdout is not free.

## Considered Options

### A. Where a `--json` failure is written

- **A1: one object, one line, on stderr** ✔. stdout keeps holding exactly one result document, and
  stderr stays parseable line by line beside the JSON logs already there. A caller reads the result
  with `| jq` and the failure with `2>&1 >/dev/null | jq`, and neither ever finds the other's
  content.
- A2: on stdout, beside the result. **Rejected on merit**: a run that applied two migrations and
  failed on the third prints its summary and then fails — that is `performDirection`'s whole design
  — so a failure on stdout makes two documents in one stream, and `jq` parses neither. Emitting it
  only when there was no result would make the stream's shape depend on how far the run got, which
  is worse than either fixed answer.
- A3: an indented document on stderr. **Rejected**: it is the same content as A1, but a multi-line
  object interleaved with line-delimited log records makes stderr unparseable exactly when logs are
  on, which is when someone is debugging.

### B. What shape it takes

- **B1: migr's own object** ✔ — `code`, `error`, `migration`, `operation`, `statement`, and a
  `drift` or `dirty` sub-document. The keys are this project's to keep stable, and `migration` and
  `operation` state invariant #9 as fields rather than as prose a script has to grep.
- B2: an slog `ERROR` record, through the logger the run already has. **Rejected on merit**, and it
  was close: it invents no new stream contract, since §5 already says stderr carries JSON logs, and
  it costs no code at all. But `time`, `level` and `msg` are slog's keys rather than migr's, a
  caller has to filter by level to find the answer, and the text rendering would become
  `level=ERROR msg=…` — losing the paragraph, which is the one thing a person at a terminal wants.

### C. Whether the failure document also carries the rendered paragraph

- **C1: fields only** ✔. The sub-documents carry every value the paragraph is built from.
- C2: fields plus a `detail` string holding the prose. **Rejected**: two renderings of one answer in
  one document, and the paragraph is precisely what this task exists to replace. The remediation it
  carries is in `docs/` and in the terminal rendering, both of which a person reads and a script
  does not.

### D. Which failures change code

- **D1: a flag, command name, or argument count cobra refuses → 2** ✔, where it was 1. Nothing is
  more literally *the argument is wrong*, and it is the most common failure the CLI has. `argsOf`
  wraps every `Args` check and `SetFlagErrorFunc` catches the flag parser.
- **D2: `--step` and `--to` values that name no migration → 2** ✔ (`engine.ErrNoSteps`,
  `ErrUnknownVersion`, `ErrNoBatch`), where they were 1. Each is refused before the run acts on
  anything, and each is fixed by retyping a flag. ADR 0023 left this split here.
- **D3: a migrations directory that is not there, and a migration file that will not open → 1** ✔,
  where both were 2. `migr create` already exits 1 for the first, and the error's own remedy is
  `migr init` — creating a directory is changing the world, not retyping an argument. The second was
  never a separate decision: `target.files` tagged everything from hashing as 2, and hashing's only
  failure is `fs.ReadFile`.
- D4: `down --to <a version this database does not carry>` → 2. **Rejected**: the same command line
  succeeds against a database that does carry the version, so the argument is not wrong — the
  database is not where the operator thought it was. A typo and a genuinely unapplied migration are
  indistinguishable without asking the database, which is the definition of exit 1.

### E. Where the selector refusals are classified

- **E1: the command tags them** ✔, beside `errBothSelectors`, which `up`, `down` and `plan` already
  tag there. They come from flags those commands register.
- E2: two more sentinels in `ExitCode`. **Rejected**: a sentinel resolved there is one *every*
  command inherits, and `ExitCode`'s existing paragraph explains that the list is short on purpose —
  reporting an unrelated failure as exit 4 is how operators learn to ignore exit 4. The same logic
  applies downward: a list that grows one entry per engine sentinel stops being a boundary.

## Decision

A failure is written by `internal/cli`, on stderr, as one line: `migr: <the error>` by default and
one JSON object under `--json`. `cmd/migr` keeps exactly one print, for the one failure that
precedes the CLI — a driver registry that will not build, which happens before a flag is parsed.

The JSON object carries the exit code, a one-line message, the migration and operation the failure
came from, and — for the two errors that render paragraphs — a sub-document holding the values that
paragraph is built from.

The four re-mappings in D land as stated; `ErrNotApplied` stays at 1.

## Consequences

### Positive

- `migr up --json` is parseable on both streams for the first time, including when it fails.
- The exit codes now match the rule `doc.go` states, which had been true of the commands written
  after it and not of the ones written before.
- `TestEveryCommandEndsWithTheCodeSection5Documents` holds every command against §5 as one table,
  and `TestEveryCommandIsInTheExitCodeMatrix` fails the day an eleventh command is registered
  without saying which endings it reaches. `migr diff`, `migr seed` and `migr cutover` inherit that.
- Invariant #9 gained a mechanical check: `TestEveryFailureNamesTheMigrationItHappenedIn` walks
  eight ways a command can fail about a migration and asserts each names it.

### Negative

- **A script that read the human line has to change.** `migr … 2>&1 | grep '^migr: '` still works
  without `--json`, and finds nothing with it. There was no prior JSON failure to be compatible
  with, so this is a new contract rather than a broken one — but it is a contract now, and changing
  the keys later is a breaking change.
- **The root command needs both an `Args` check and a `RunE`.** Cobra applies its own
  unknown-command check only where `Args` is nil, and returns `flag.ErrHelp` before validating the
  arguments of a command that has nothing to run. Both are therefore load-bearing for `migr
  nonsense` to be refused rather than answered with help, and neither reads that way at the call
  site. `TestABareInvocationPrintsHelpAndSucceeds` and the `nonsense` case in
  `TestACommandLineNobodyCanTypeIsAValidationFailure` fail if either is removed.
- **A failure that is not about one migration carries no `migration` field**, even where its message
  names one. `engine.dirtyError` and `engine.missingError` are `fmt.Errorf` wrappings of a sentinel
  rather than types — deliberately, since ADR 0024 uses `errors.As` on `*DirtyError` to tell "is
  dirty" from "became dirty" — so there is nothing for `errors.As` to read. The message names the
  migration, which is what invariant #9 asks for; the field is absent.
- **`ExitLockImpact` still has no producer.** It is declared and documented and nothing returns it,
  because `--require-safe` and `safety.max_lock_impact` are M13-T6's.

### Neutral

- `history.CorruptError` gained a `Version`, so a row this build cannot decode names the migration
  it recorded a run of rather than only its batch and ordinal. The decoder already held the whole
  record; this is one field and one verb.
- `internal/cli` now names `*engine.DriftError`, `*engine.DirtyError`, `*engine.MigrationError`,
  `*plan.CompileError` and `*engine.StatementError` by type. That is not invariant #1: all five are
  core packages, and this package already imports every one of them.
- The text rendering is unchanged, prefix included. A person at a terminal sees exactly what they
  saw before.

## Notes

M13-T6 adds `--require-safe` and the first producer of exit code 5. It needs a row in the matrix
table and nothing else — `ExitCode` already resolves an explicit tag first.

M9-T6 resolves a dirty migration with a separately-named command. When it lands, the already-dirty
refusal becomes actionable and may be worth a type of its own, which would give it the `migration`
field the Negative section records as absent.
